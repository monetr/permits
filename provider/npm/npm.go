// Package npm implements the permits provider for the npm ecosystem: a Scanner
// for pnpm-lock.yaml (lockfileVersion 5, 6 and 9) and a Fetcher that resolves
// raw license text from the package tarball published to the npm registry.
package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monetr/permits/internal/httpx"
	"github.com/monetr/permits/license"
	"github.com/monetr/permits/model"
	"gopkg.in/yaml.v3"
)

const defaultRegistry = "https://registry.npmjs.org"

// Scanner parses pnpm-lock.yaml files.
type Scanner struct{}

// NewScanner returns a pnpm-lock.yaml scanner.
func NewScanner() *Scanner { return &Scanner{} }

// Ecosystem implements provider.Source.
func (s *Scanner) Ecosystem() model.Ecosystem { return model.EcosystemNPM }

// Detect accepts files named like a pnpm lockfile, or any YAML file whose
// content declares a pnpm "lockfileVersion".
func (s *Scanner) Detect(p string) bool {
	base := strings.ToLower(path.Base(strings.ReplaceAll(p, `\`, "/")))
	if strings.Contains(base, "pnpm-lock") {
		return true
	}
	if !strings.HasSuffix(base, ".yaml") && !strings.HasSuffix(base, ".yml") {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 256)
	n, _ := f.Read(head)
	return strings.Contains(string(head[:n]), "lockfileVersion")
}

// lockfile is a permissive view of pnpm-lock.yaml. Only the keys of the
// packages/snapshots maps are needed, so their values decode into struct{}
// rather than retaining the whole subtree. LockfileVersion is needed because
// peer-dependency encoding differs by version (v5 uses "_peer", v6/v9 use
// "(peer)") and v5 names contain "__" that must not be confused with "_peer".
type lockfile struct {
	LockfileVersion any                 `yaml:"lockfileVersion"`
	Packages        map[string]struct{} `yaml:"packages"`
	Snapshots       map[string]struct{} `yaml:"snapshots"`

	// Direct dependencies. v9 nests them under importers (one per workspace);
	// v5/v6 list them at the lockfile root.
	Importers            map[string]importerDeps `yaml:"importers"`
	Dependencies         map[string]directRef    `yaml:"dependencies"`
	DevDependencies      map[string]directRef    `yaml:"devDependencies"`
	OptionalDependencies map[string]directRef    `yaml:"optionalDependencies"`
}

type importerDeps struct {
	Dependencies         map[string]directRef `yaml:"dependencies"`
	DevDependencies      map[string]directRef `yaml:"devDependencies"`
	OptionalDependencies map[string]directRef `yaml:"optionalDependencies"`
}

// directRef is a direct-dependency value, which is either a bare version string
// (v5: `lodash: 4.17.21`) or an object (v6/v9: `{specifier, version}`).
type directRef struct{ Version string }

func (d *directRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		d.Version = n.Value
		return nil
	}
	var obj struct {
		Version string `yaml:"version"`
	}
	if err := n.Decode(&obj); err != nil {
		return err
	}
	d.Version = obj.Version
	return nil
}

// majorVersion extracts the integer major from a lockfileVersion that may be a
// YAML number (5.4) or string ('6.0', '9.0'). Defaults to 9 (modern) when
// absent or unparseable.
func majorVersion(v any) int {
	s := strings.TrimSpace(fmt.Sprint(v))
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return 9
}

func loadLockfile(p string) (lockfile, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return lockfile{}, err
	}
	var lf lockfile
	if err := yaml.Unmarshal(raw, &lf); err != nil {
		return lockfile{}, fmt.Errorf("parsing %s: %w", p, err)
	}
	return lf, nil
}

// depCollector accumulates unique, valid npm dependencies. It rejects empty and
// non-registry specifiers (link:/file:/workspace:/git/url contain ':').
type depCollector struct {
	seen map[string]struct{}
	deps []model.Dependency
}

func newDepCollector() *depCollector {
	return &depCollector{seen: make(map[string]struct{})}
}

func (c *depCollector) add(name, version string) {
	if name == "" || version == "" || strings.ContainsAny(version, ":") {
		return
	}
	id := name + "@" + version
	if _, dup := c.seen[id]; dup {
		return
	}
	c.seen[id] = struct{}{}
	c.deps = append(c.deps, model.Dependency{
		Ecosystem: model.EcosystemNPM,
		Name:      name,
		Version:   version,
	})
}

// Scan parses the full resolved dependency set (incl. transitive).
func (s *Scanner) Scan(_ context.Context, p string) ([]model.Dependency, error) {
	lf, err := loadLockfile(p)
	if err != nil {
		return nil, err
	}
	major := majorVersion(lf.LockfileVersion)
	c := newDepCollector()
	for _, keys := range []map[string]struct{}{lf.Packages, lf.Snapshots} {
		for k := range keys {
			if name, version, ok := normalizeKey(k, major); ok {
				c.add(name, version)
			}
		}
	}
	return c.deps, nil
}

// ScanDirect parses only direct (top-level) dependencies, implementing
// provider.DirectScanner. It reads pnpm's importers (v9) and the lockfile-root
// dependency maps (v5/v6); transitive packages/snapshots are ignored.
func (s *Scanner) ScanDirect(_ context.Context, p string) ([]model.Dependency, error) {
	lf, err := loadLockfile(p)
	if err != nil {
		return nil, err
	}
	c := newDepCollector()
	addAll := func(m map[string]directRef) {
		for name, ref := range m {
			version := ref.Version
			if i := strings.IndexByte(version, '('); i >= 0 {
				version = version[:i] // strip v6/v9 peer suffix
			}
			c.add(name, strings.TrimSpace(version))
		}
	}
	for _, imp := range lf.Importers { // v9 (one entry per workspace)
		addAll(imp.Dependencies)
		addAll(imp.DevDependencies)
		addAll(imp.OptionalDependencies)
	}
	// v5/v6 keep direct deps at the lockfile root.
	addAll(lf.Dependencies)
	addAll(lf.DevDependencies)
	addAll(lf.OptionalDependencies)
	return c.deps, nil
}

// normalizeKey converts a pnpm package/snapshot key into (name, version).
// Peer-dependency encoding is version-specific:
//
//	v5:  /lodash/4.17.21   /@babel/core/7.0.0   /react-dom/16.8.6_react@16.8.6
//	v6:  /lodash@4.17.21   /@babel/core@7.0.0(react@18)
//	v9:  lodash@4.17.21    @babel/core@7.0.0    @types/babel__core@7.1.0(...)
//
// In v5 the name/version separator is "/" and the peer suffix is "_peer"; the
// name itself may contain "__" (e.g. @types/babel__core), so the peer suffix is
// only stripped from the version (last path segment), never the whole key. In
// v6/v9 the separator is the last "@" and peers are parenthesized; "_" is never
// a separator there. Non-registry specifiers (link:/file:/git/url) are
// rejected.
func normalizeKey(key string, major int) (name, version string, ok bool) {
	k := strings.TrimSpace(key)
	k = strings.TrimPrefix(k, "/")
	if k == "" {
		return "", "", false
	}

	if major <= 5 {
		// v5: split on the last "/"; peer suffix "_peer" rides on the version.
		slash := strings.LastIndexByte(k, '/')
		if slash <= 0 {
			return "", "", false
		}
		name, version = k[:slash], k[slash+1:]
		if i := strings.IndexByte(version, '_'); i >= 0 {
			version = version[:i]
		}
	} else {
		// v6/v9: parenthesized peers; split on the last "@".
		if i := strings.IndexByte(k, '('); i >= 0 {
			k = k[:i]
		}
		at := strings.LastIndexByte(k, '@')
		if at <= 0 {
			return "", "", false
		}
		name, version = k[:at], k[at+1:]
	}

	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" || version == "" {
		return "", "", false
	}
	// Reject non-registry specifiers (link:, file:, workspace:, http(s):, git:).
	if strings.ContainsAny(version, ":") {
		return "", "", false
	}
	return name, version, true
}

// Fetcher resolves license text from a local node_modules tree when available,
// falling back to the npm registry tarball.
type Fetcher struct {
	Registry string
	// ModulesDirs are node_modules roots checked before the registry. Both the
	// pnpm virtual store (<root>/.pnpm/<pkg>@<ver>/node_modules/<name>) and the
	// flat layout (<root>/<name>) are supported.
	ModulesDirs []string
	client      *httpx.Client

	mu    sync.Mutex
	cache map[string][]model.LicenseArtifact
}

// NewFetcher returns a [Fetcher]. registry defaults to the public npm registry;
// modulesDirs are local node_modules roots checked before the registry;
// timeout is the per-request timeout.
func NewFetcher(registry string, modulesDirs []string, timeout time.Duration) *Fetcher {
	if registry == "" {
		registry = defaultRegistry
	}
	return &Fetcher{
		Registry:    strings.TrimRight(registry, "/"),
		ModulesDirs: modulesDirs,
		client:      httpx.New(timeout),
		cache:       make(map[string][]model.LicenseArtifact),
	}
}

// Ecosystem implements provider.Source.
func (f *Fetcher) Ecosystem() model.Ecosystem { return model.EcosystemNPM }

type registryDoc struct {
	Versions map[string]struct {
		License  any `json:"license"`
		Licenses any `json:"licenses"`
		Dist     struct {
			Tarball string `json:"tarball"`
		} `json:"dist"`
	} `json:"versions"`
}

// Fetch downloads the package metadata and tarball and extracts every license
// file. An empty result with nil error means no license file was present.
func (f *Fetcher) Fetch(ctx context.Context, dep model.Dependency) ([]model.LicenseArtifact, error) {
	id := dep.Name + "@" + dep.Version
	f.mu.Lock()
	if cached, ok := f.cache[id]; ok {
		f.mu.Unlock()
		return cached, nil
	}
	f.mu.Unlock()

	// node_modules first: if the package is installed locally and contains a
	// license file, use it and skip the network entirely.
	if local := f.fromNodeModules(dep); local != nil {
		f.mu.Lock()
		f.cache[id] = local
		f.mu.Unlock()
		return local, nil
	}

	metaURL := f.Registry + "/" + escapePackageName(dep.Name)
	body, err := f.client.GetBytes(ctx, metaURL)
	if err != nil {
		return nil, fmt.Errorf("npm metadata %s: %w", id, err)
	}
	var doc registryDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("npm metadata %s: %w", id, err)
	}
	v, ok := doc.Versions[dep.Version]
	if !ok {
		return nil, fmt.Errorf("npm metadata %s: version not found", id)
	}
	if v.Dist.Tarball == "" {
		return nil, fmt.Errorf("npm metadata %s: no tarball", id)
	}

	tgz, err := f.client.GetBytes(ctx, v.Dist.Tarball)
	if err != nil {
		return nil, fmt.Errorf("npm tarball %s: %w", id, err)
	}
	declared := declaredLicense(v.License, v.Licenses)
	artifacts, err := extractTarball(tgz, dep, declared)
	if err != nil {
		return nil, fmt.Errorf("npm tarball %s: %w", id, err)
	}

	f.mu.Lock()
	f.cache[id] = artifacts
	f.mu.Unlock()
	return artifacts, nil
}

// escapePackageName encodes a package name for a registry URL path, encoding
// the scope separator (@scope/name -> @scope%2Fname).
func escapePackageName(name string) string {
	if strings.HasPrefix(name, "@") {
		return strings.ReplaceAll(name, "/", "%2F")
	}
	return url.PathEscape(name)
}

// fromNodeModules looks for the installed package directory across the
// configured node_modules roots and, if it contains a license file, returns the
// artifacts read from disk. It returns nil (not an empty slice) when the
// package is not found locally, so the caller falls back to the registry. A
// locally installed package with no license file yields an empty, non-nil
// slice (authoritative "no license"), matching the Go cache behaviour.
func (f *Fetcher) fromNodeModules(dep model.Dependency) []model.LicenseArtifact {
	for _, root := range f.ModulesDirs {
		if dir := packageDir(root, dep); dir != "" {
			arts := f.readPackageDir(dir, dep)
			if arts != nil {
				return arts
			}
		}
	}
	return nil
}

// packageDir returns the installed package directory for dep under a
// node_modules root, checking the pnpm virtual store first, then the flat
// layout. It returns "" if no directory exists.
func packageDir(root string, dep model.Dependency) string {
	// pnpm virtual store: node_modules/.pnpm/<name+scope>@<ver>*/node_modules/<name>
	escaped := strings.ReplaceAll(dep.Name, "/", "+")
	pattern := filepath.Join(root, ".pnpm", escaped+"@"+dep.Version+"*", "node_modules", filepath.FromSlash(dep.Name))
	if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.IsDir() {
				return m
			}
		}
	}
	// Flat layout: node_modules/<name> — only trust it if the version matches.
	flat := filepath.Join(root, filepath.FromSlash(dep.Name))
	if info, err := os.Stat(flat); err == nil && info.IsDir() {
		if v := readPackageJSONVersion(flat); v == "" || v == dep.Version {
			return flat
		}
	}
	return ""
}

// readPackageDir reads every license file in dir. Returns a non-nil (possibly
// empty) slice when dir exists, so a present-but-unlicensed local install is
// authoritative.
func (f *Fetcher) readPackageDir(dir string, dep model.Dependency) []model.LicenseArtifact {
	declared := readPackageJSONLicense(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	artifacts := []model.LicenseArtifact{}
	for _, e := range entries {
		if e.IsDir() || !license.IsLicenseFile(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		artifacts = append(artifacts,
			model.NewLicenseArtifact(dep, declared, e.Name(), "npm-node-modules", data))
	}
	return artifacts
}

type packageJSON struct {
	Version  string `json:"version"`
	License  any    `json:"license"`
	Licenses any    `json:"licenses"`
}

func parsePackageJSON(dir string) (packageJSON, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return packageJSON{}, false
	}
	var pj packageJSON
	if json.Unmarshal(data, &pj) != nil {
		return packageJSON{}, false
	}
	return pj, true
}

func readPackageJSONVersion(dir string) string {
	pj, _ := parsePackageJSON(dir)
	return pj.Version
}

func readPackageJSONLicense(dir string) string {
	pj, ok := parsePackageJSON(dir)
	if !ok {
		return ""
	}
	return declaredLicense(pj.License, pj.Licenses)
}

func extractTarball(tgz []byte, dep model.Dependency, declared string) ([]model.LicenseArtifact, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var artifacts []model.LicenseArtifact
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// npm tarballs nest everything under "package/".
		rel := strings.TrimPrefix(path.Clean(hdr.Name), "package/")
		if !license.IsLicenseFile(rel) {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, model.MaxLicenseBytes))
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts,
			model.NewLicenseArtifact(dep, declared, path.Base(rel), "npm-tarball", data))
	}
	return artifacts, nil
}

// declaredLicense extracts an SPDX-ish string from the npm "license" (string or
// {type}) or legacy "licenses" ([]{type}) fields.
func declaredLicense(lic, lics any) string {
	switch v := lic.(type) {
	case string:
		return v
	case map[string]any:
		if t, ok := v["type"].(string); ok {
			return t
		}
	}
	if arr, ok := lics.([]any); ok {
		var parts []string
		for _, e := range arr {
			if m, ok := e.(map[string]any); ok {
				if t, ok := m["type"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, " OR ")
	}
	return ""
}
