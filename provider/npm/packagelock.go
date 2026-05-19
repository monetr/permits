package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/monetr/permits/model"
)

// PackageLockScanner parses npm's package-lock.json (and the identically
// formatted npm-shrinkwrap.json), lockfileVersion 1, 2 and 3.
type PackageLockScanner struct{}

// NewPackageLockScanner returns a package-lock.json scanner.
func NewPackageLockScanner() *PackageLockScanner { return &PackageLockScanner{} }

// Ecosystem implements provider.Source.
func (s *PackageLockScanner) Ecosystem() model.Ecosystem { return model.EcosystemNPM }

// Detect accepts files named like an npm lockfile, or any JSON file whose content declares a
// "lockfileVersion".
func (s *PackageLockScanner) Detect(p string) bool {
	base := strings.ToLower(path.Base(strings.ReplaceAll(p, `\`, "/")))
	if base == "package-lock.json" || base == "npm-shrinkwrap.json" {
		return true
	}

	if !strings.HasSuffix(base, ".json") {
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

// packageLock is a permissive view of package-lock.json. v2/v3 carry the authoritative "packages"
// map keyed by install path; v1 (and the legacy compatibility block v2 still writes) instead nests
// the resolved tree under "dependencies".
type packageLock struct {
	Packages     map[string]packageLockEntry `json:"packages"`
	Dependencies map[string]packageLockDep   `json:"dependencies"`
}

// packageLockEntry is a "packages" value. The root project is keyed by "" and lists its declared
// direct deps; every other entry is keyed by install path (node_modules/foo,
// node_modules/foo/node_modules/bar) and carries the resolved version. "link" marks a workspace
// symlink whose real entry lives elsewhere, so it has no usable registry version.
type packageLockEntry struct {
	Version              string            `json:"version"`
	Link                 bool              `json:"link"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

// packageLockDep is a v1 "dependencies" value: a resolved version plus its own nested dependency
// tree (npm's per-package node_modules layout).
type packageLockDep struct {
	Version      string                    `json:"version"`
	Dependencies map[string]packageLockDep `json:"dependencies"`
}

func loadPackageLock(p string) (packageLock, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return packageLock{}, err
	}

	var pl packageLock
	if err := json.Unmarshal(raw, &pl); err != nil {
		return packageLock{}, fmt.Errorf("parsing %s: %w", p, err)
	}

	return pl, nil
}

// packageLockName extracts the package name from a "packages" install-path key. The name is the
// path after the last "node_modules/" segment (so nested duplicates resolve to the same name); a
// key without that segment is the root project or a workspace folder and yields "".
func packageLockName(key string) string {
	const sep = "node_modules/"
	if i := strings.LastIndex(key, sep); i >= 0 {
		return key[i+len(sep):]
	}

	return ""
}

// Scan parses the full resolved dependency set (incl. transitive).
func (s *PackageLockScanner) Scan(_ context.Context, p string) ([]model.Dependency, error) {
	pl, err := loadPackageLock(p)
	if err != nil {
		return nil, err
	}

	c := newDepCollector()

	if len(pl.Packages) > 0 {
		for key, entry := range pl.Packages {
			if entry.Link {
				continue
			}

			c.add(packageLockName(key), entry.Version)
		}

		return c.deps, nil
	}

	// v1: walk the nested dependency tree.
	var walk func(map[string]packageLockDep)
	walk = func(m map[string]packageLockDep) {
		for name, dep := range m {
			c.add(name, dep.Version)
			walk(dep.Dependencies)
		}
	}
	walk(pl.Dependencies)

	return c.deps, nil
}

// ScanDirect parses only direct (top-level) dependencies, implementing provider.DirectScanner.
// v2/v3 record the root project's declared deps under the "" package entry; their versions are
// resolved by looking up the top-level node_modules entry. v1 does not store the root manifest, so
// the best available approximation is the top-level "dependencies" keys (npm's hoisted tree, which
// for a flat project equals the direct set).
func (s *PackageLockScanner) ScanDirect(_ context.Context, p string) ([]model.Dependency, error) {
	pl, err := loadPackageLock(p)
	if err != nil {
		return nil, err
	}

	c := newDepCollector()

	if len(pl.Packages) > 0 {
		root := pl.Packages[""]
		for _, m := range []map[string]string{
			root.Dependencies, root.DevDependencies, root.OptionalDependencies,
		} {
			for name := range m {
				entry, ok := pl.Packages["node_modules/"+name]
				if !ok || entry.Link {
					continue
				}

				c.add(name, entry.Version)
			}
		}

		return c.deps, nil
	}

	for name, dep := range pl.Dependencies {
		c.add(name, dep.Version)
	}

	return c.deps, nil
}
