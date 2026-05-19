// Package provider defines the extension point of the permits library. A new
// dependency ecosystem is added by implementing [Scanner] (parse a lockfile
// into dependencies) and [Fetcher] (retrieve raw license artifacts for a
// dependency), then registering them in a [Registry]. The collector depends
// only on these interfaces and never on a concrete ecosystem, so third parties
// can add ecosystems without forking.
package provider

import (
	"context"
	"fmt"

	"github.com/monetr/permits/model"
)

// Source identifies which ecosystem a provider component serves.
type Source interface {
	Ecosystem() model.Ecosystem
}

// Scanner parses a lockfile/manifest into the set of resolved dependencies.
type Scanner interface {
	Source
	// Detect reports whether this scanner handles the given file. It may
	// inspect the filename and/or peek at the file contents.
	Detect(path string) bool
	// Scan parses the file into the resolved dependency set.
	Scan(ctx context.Context, path string) ([]model.Dependency, error)
}

// DirectScanner is an optional capability: a [Scanner] that can also restrict
// results to the project's direct (top-level) dependencies, excluding
// transitive ones. Providers implement it when the distinction is recoverable
// (pnpm importers / top-level deps, go.mod non-indirect requires). The
// collector checks for this interface only when direct-only mode is requested.
type DirectScanner interface {
	Scanner
	ScanDirect(ctx context.Context, path string) ([]model.Dependency, error)
}

// Fetcher retrieves the raw license artifacts for a single dependency.
type Fetcher interface {
	Source
	// Fetch returns every license artifact found for dep. Returning an empty
	// slice with a nil error means "fetched, but no license file present".
	Fetch(ctx context.Context, dep model.Dependency) ([]model.LicenseArtifact, error)
}

// Registry maps an ecosystem to its [Scanner] and [Fetcher].
type Registry struct {
	scanners []Scanner
	fetchers map[model.Ecosystem]Fetcher
}

// NewRegistry returns an empty [Registry].
func NewRegistry() *Registry {
	return &Registry{fetchers: make(map[model.Ecosystem]Fetcher)}
}

// Register adds a provider's [Scanner] and [Fetcher]. Either may be nil if a
// provider only supplies one half, though both are required for that ecosystem
// to be usable end to end. A later registration for the same ecosystem replaces
// the previous [Fetcher].
func (r *Registry) Register(s Scanner, f Fetcher) {
	if s != nil {
		r.scanners = append(r.scanners, s)
	}
	if f != nil {
		r.fetchers[f.Ecosystem()] = f
	}
}

// ScannerFor returns the first registered scanner whose [Scanner.Detect]
// accepts path.
func (r *Registry) ScannerFor(path string) (Scanner, bool) {
	for _, s := range r.scanners {
		if s.Detect(path) {
			return s, true
		}
	}
	return nil, false
}

// FetcherFor returns the fetcher registered for the given ecosystem.
func (r *Registry) FetcherFor(eco model.Ecosystem) (Fetcher, error) {
	f, ok := r.fetchers[eco]
	if !ok {
		return nil, fmt.Errorf("no fetcher registered for ecosystem %q", eco)
	}
	return f, nil
}
