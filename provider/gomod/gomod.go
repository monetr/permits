// Package gomod implements the permits provider for the Go module ecosystem: a Scanner for go.sum
// and a Fetcher that reads raw license text from the local module cache, falling back to the Go
// module proxy when the module is not cached.
package gomod

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/monetr/permits/internal/httpx"
	"github.com/monetr/permits/license"
	"github.com/monetr/permits/model"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// Scanner parses go.sum files.
type Scanner struct{}

// NewScanner returns a go.sum scanner.
func NewScanner() *Scanner { return &Scanner{} }

// Ecosystem implements provider.Source.
func (s *Scanner) Ecosystem() model.Ecosystem { return model.EcosystemGo }

// Detect accepts files named go.sum.
func (s *Scanner) Detect(p string) bool {
	// Normalize Windows separators before taking the base name so a path like dir\go.sum is still
	// recognized regardless of the host OS.
	return strings.EqualFold(path.Base(strings.ReplaceAll(p, `\`, "/")), "go.sum")
}

// Scan parses go.sum into a deduplicated dependency set. go.sum already lists the full resolved
// (incl. transitive) module set; the "/go.mod" hash lines are folded into the same
// (module,version) entry.
func (s *Scanner) Scan(_ context.Context, p string) ([]model.Dependency, error) {
	file, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// go.sum lines can be long (hashes), so the scanner is given a generous buffer to avoid
	// bufio.ErrTooLong on large checksum files.
	seen := make(map[string]struct{})
	var deps []model.Dependency
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}

		// Each module appears twice in go.sum: once for the module zip and once for its go.mod.
		// Stripping the "/go.mod" suffix collapses both lines onto the same (module,version) key.
		mod := fields[0]
		ver := strings.TrimSuffix(fields[1], "/go.mod")
		id := mod + "@" + ver
		if _, dup := seen[id]; dup {
			continue
		}

		seen[id] = struct{}{}
		deps = append(deps, model.Dependency{
			Ecosystem: model.EcosystemGo,
			Name:      mod,
			Version:   ver,
		})
	}

	if err := sc.Err(); err != nil {
		return nil, err
	}

	return deps, nil
}

// ScanDirect parses only direct dependencies, implementing provider.DirectScanner. go.sum cannot
// distinguish direct from indirect, so it reads the sibling go.mod and returns its require
// directives that are not marked "// indirect".
func (s *Scanner) ScanDirect(_ context.Context, p string) ([]model.Dependency, error) {
	// Direct-vs-indirect information only lives in go.mod, so it must sit next to the go.sum we
	// were given.
	modPath := filepath.Join(filepath.Dir(p), "go.mod")
	data, err := os.ReadFile(modPath)
	if err != nil {
		return nil, fmt.Errorf("direct-only requires go.mod next to %s: %w", p, err)
	}

	mf, err := modfile.Parse(modPath, data, nil)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", modPath, err)
	}

	// Indirect requires are transitive dependencies; only the non-indirect entries are the
	// project's own direct dependencies.
	var deps []model.Dependency
	for _, r := range mf.Require {
		if r.Indirect {
			continue
		}

		deps = append(deps, model.Dependency{
			Ecosystem: model.EcosystemGo,
			Name:      r.Mod.Path,
			Version:   r.Mod.Version,
		})
	}

	return deps, nil
}

// Fetcher resolves license text from the module cache or the module proxy.
type Fetcher struct {
	// CacheDir overrides the module cache location (default: resolved from
	// GOMODCACHE/GOPATH/HOME).
	CacheDir string
	// Proxies overrides the proxy list (default: resolved from GOPROXY).
	Proxies []string

	client *httpx.Client

	mu    sync.Mutex
	cache map[string][]model.LicenseArtifact
}

// NewFetcher returns a [Fetcher]. proxyOverride, when non-empty, replaces the GOPROXY-derived list
// (comma or pipe separated).
func NewFetcher(cacheDir, proxyOverride string, timeout time.Duration) *Fetcher {
	// An empty cache dir means "discover it from the environment" rather than "no cache".
	if cacheDir == "" {
		cacheDir = resolveCacheDir()
	}

	return &Fetcher{
		CacheDir: cacheDir,
		Proxies:  resolveProxies(proxyOverride),
		client:   httpx.New(timeout),
		cache:    make(map[string][]model.LicenseArtifact),
	}
}

// Ecosystem implements provider.Source.
func (f *Fetcher) Ecosystem() model.Ecosystem { return model.EcosystemGo }

// resolveCacheDir mirrors the Go toolchain's own module cache resolution order: GOMODCACHE wins,
// then the first GOPATH entry, then $HOME/go.
func resolveCacheDir() string {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return v
	}

	if gp := os.Getenv("GOPATH"); gp != "" {
		return filepath.Join(strings.Split(gp, string(os.PathListSeparator))[0], "pkg", "mod")
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "go", "pkg", "mod")
	}

	return ""
}

// resolveProxies turns a GOPROXY-style list into concrete HTTP(S) base URLs. The override takes
// precedence over the environment, and proxy.golang.org is the final default.
func resolveProxies(override string) []string {
	v := override
	if v == "" {
		v = os.Getenv("GOPROXY")
	}
	if v == "" {
		v = "https://proxy.golang.org"
	}

	// GOPROXY entries are separated by commas or pipes; only direct HTTP(S) endpoints are usable
	// here, so sentinels like "direct" and "off" are filtered out implicitly.
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' }) {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
			out = append(out, strings.TrimRight(p, "/"))
		}
	}

	return out
}

// Fetch returns license artifacts, trying the local cache first and the module proxy as a
// fallback. An empty result with nil error means no license file.
func (f *Fetcher) Fetch(ctx context.Context, dep model.Dependency) ([]model.LicenseArtifact, error) {
	// The result cache is keyed by module@version so repeated dependencies (common across a large
	// dependency graph) are only resolved once.
	id := dep.Name + "@" + dep.Version
	f.mu.Lock()
	if cached, ok := f.cache[id]; ok {
		f.mu.Unlock()
		return cached, nil
	}
	f.mu.Unlock()

	// Module paths and versions must be escaped before they can be used as filesystem paths or
	// proxy URL segments (e.g. uppercase letters become !-prefixed).
	escPath, err := module.EscapePath(dep.Name)
	if err != nil {
		return nil, fmt.Errorf("go module %s: %w", id, err)
	}

	escVer, err := module.EscapeVersion(dep.Version)
	if err != nil {
		return nil, fmt.Errorf("go module %s: %w", id, err)
	}

	// Prefer the local module cache; only reach out to the network when the module is not present
	// locally.
	artifacts, err := f.fromCache(dep, escPath, escVer)
	if err != nil {
		return nil, err
	}

	if artifacts == nil {
		artifacts, err = f.fromProxy(ctx, dep, escPath, escVer)
		if err != nil {
			return nil, err
		}
	}

	// Normalize a nil slice to an empty one so callers can rely on a non-nil result and the cache
	// stores a definitive "no licenses" answer.
	if artifacts == nil {
		artifacts = []model.LicenseArtifact{}
	}

	f.mu.Lock()
	f.cache[id] = artifacts
	f.mu.Unlock()

	return artifacts, nil
}

// fromCache returns nil (not empty slice) when the module is not cached, so the caller knows to
// fall back to the proxy.
func (f *Fetcher) fromCache(dep model.Dependency, escPath, escVer string) ([]model.LicenseArtifact, error) {
	// With no cache directory there is nothing to read; signal "not cached" so Fetch falls back to
	// the proxy.
	if f.CacheDir == "" {
		return nil, nil
	}

	// A missing directory (or a non-directory at that path) likewise means the module is not in
	// the cache rather than an error condition.
	dir := filepath.Join(f.CacheDir, filepath.FromSlash(escPath)+"@"+escVer)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}

	// Walk the extracted module tree and collect every file that looks like a license document.
	var artifacts []model.LicenseArtifact
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() || !license.IsLicenseFile(p) {
			return nil
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		artifacts = append(artifacts,
			model.NewLicenseArtifact(dep, "", filepath.Base(p), "go-cache", data))

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("go cache %s@%s: %w", dep.Name, dep.Version, err)
	}

	if artifacts == nil {
		// Module is cached but has no license file: return empty (not nil) so we do not
		// redundantly hit the proxy.
		return []model.LicenseArtifact{}, nil
	}

	return artifacts, nil
}

// fromProxy downloads the module zip from each configured proxy in turn and extracts any license
// files. Proxy/transport failures fall through to the next proxy; only a successfully read zip
// produces a result.
func (f *Fetcher) fromProxy(ctx context.Context, dep model.Dependency, escPath, escVer string) ([]model.LicenseArtifact, error) {
	var lastErr error
	for _, proxy := range f.Proxies {
		// A fetch or zip-parse failure for one proxy should not abort the whole operation; remember
		// the error and try the next mirror.
		url := proxy + "/" + escPath + "/@v/" + escVer + ".zip"
		data, err := f.client.GetBytes(ctx, url)
		if err != nil {
			lastErr = err
			continue
		}

		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			lastErr = err
			continue
		}

		// Errors reading individual entries of a successfully opened zip are treated as hard
		// failures: the zip is corrupt rather than the proxy being unavailable.
		var artifacts []model.LicenseArtifact
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() || !license.IsLicenseFile(zf.Name) {
				continue
			}

			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}

			// Bound the read so a maliciously large entry cannot exhaust memory.
			data, err := io.ReadAll(io.LimitReader(rc, model.MaxLicenseBytes))
			rc.Close()
			if err != nil {
				return nil, err
			}

			artifacts = append(artifacts,
				model.NewLicenseArtifact(dep, "", path.Base(zf.Name), "go-proxy", data))
		}

		// A readable zip with no license file is a definitive answer; do not try further proxies.
		if artifacts == nil {
			return []model.LicenseArtifact{}, nil
		}

		return artifacts, nil
	}

	// Every proxy failed: surface the last transport error, or report that no proxy was usable in
	// the first place.
	if lastErr != nil {
		return nil, fmt.Errorf("go proxy %s@%s: %w", dep.Name, dep.Version, lastErr)
	}

	return nil, fmt.Errorf("go proxy %s@%s: no usable proxy configured", dep.Name, dep.Version)
}
