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
	"github.com/monetr/permits/licenses"
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

	seen := make(map[string]struct{})
	var deps []model.Dependency
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}

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
	modPath := filepath.Join(filepath.Dir(p), "go.mod")
	data, err := os.ReadFile(modPath)
	if err != nil {
		return nil, fmt.Errorf("direct-only requires go.mod next to %s: %w", p, err)
	}

	mf, err := modfile.Parse(modPath, data, nil)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", modPath, err)
	}

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

func resolveProxies(override string) []string {
	v := override
	if v == "" {
		v = os.Getenv("GOPROXY")
	}
	if v == "" {
		v = "https://proxy.golang.org"
	}

	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' }) {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
			out = append(out, strings.TrimRight(p, "/"))
		}
	}

	return out
}

// repoURL derives the project URL from the module path. Module paths are host-rooted import paths
// (github.com/x/y), and vanity hosts redirect to the real repository, so prefixing a scheme is
// enough.
func repoURL(dep model.Dependency) string {
	return "https://" + dep.Name
}

// Fetch returns license artifacts, trying the local cache first and the module proxy as a
// fallback. An empty result with nil error means no license file.
func (f *Fetcher) Fetch(ctx context.Context, dep model.Dependency) ([]model.LicenseArtifact, error) {
	id := dep.Name + "@" + dep.Version
	f.mu.Lock()
	if cached, ok := f.cache[id]; ok {
		f.mu.Unlock()
		return cached, nil
	}
	f.mu.Unlock()

	escPath, err := module.EscapePath(dep.Name)
	if err != nil {
		return nil, fmt.Errorf("go module %s: %w", id, err)
	}

	escVer, err := module.EscapeVersion(dep.Version)
	if err != nil {
		return nil, fmt.Errorf("go module %s: %w", id, err)
	}

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
	if f.CacheDir == "" {
		return nil, nil
	}

	dir := filepath.Join(f.CacheDir, filepath.FromSlash(escPath)+"@"+escVer)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}

	var artifacts []model.LicenseArtifact
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() || !licenses.IsLicenseFile(p) {
			return nil
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		a := model.NewLicenseArtifact(dep, "", filepath.Base(p), "go-cache", data)
		a.Repository = repoURL(dep)
		artifacts = append(artifacts, a)

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

func (f *Fetcher) fromProxy(ctx context.Context, dep model.Dependency, escPath, escVer string) ([]model.LicenseArtifact, error) {
	var lastErr error
	for _, proxy := range f.Proxies {
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

		var artifacts []model.LicenseArtifact
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() || !licenses.IsLicenseFile(zf.Name) {
				continue
			}

			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}

			data, err := io.ReadAll(io.LimitReader(rc, model.MaxLicenseBytes))
			rc.Close()
			if err != nil {
				return nil, err
			}

			a := model.NewLicenseArtifact(dep, "", path.Base(zf.Name), "go-proxy", data)
			a.Repository = repoURL(dep)
			artifacts = append(artifacts, a)
		}

		if artifacts == nil {
			return []model.LicenseArtifact{}, nil
		}

		return artifacts, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("go proxy %s@%s: %w", dep.Name, dep.Version, lastErr)
	}

	return nil, fmt.Errorf("go proxy %s@%s: no usable proxy configured", dep.Name, dep.Version)
}
