// Package permits gathers the verbatim license text of every dependency a
// project resolves. It scans lockfiles (pnpm-lock.yaml, go.sum) and retrieves
// raw license files from the npm registry and the Go module cache/proxy.
//
// The library is usable on its own: build a [Collector] and call
// [Collector.Collect]. The command in ./cmd/permits is a thin wrapper around
// this package. New dependency ecosystems are added by registering a
// [provider.Scanner] and [provider.Fetcher] in a [*provider.Registry] — no
// changes to the collector.
package permits

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/monetr/permits/license"
	"github.com/monetr/permits/model"
	"github.com/monetr/permits/provider"
	"github.com/monetr/permits/provider/gomod"
	"github.com/monetr/permits/provider/npm"
)

// Options configures a collection run and the default providers.
type Options struct {
	// Concurrency is the number of dependencies fetched in parallel (min 1).
	Concurrency int
	// Timeout is the per-request timeout for network fetches.
	Timeout time.Duration
	// Strict, when true, makes the run report failure if any dependency yields
	// no license. The collector still returns full results; callers decide.
	Strict bool
	// DirectOnly restricts scanning to direct (top-level) dependencies,
	// excluding transitive ones. Requires each matched scanner to implement
	// [provider.DirectScanner]; otherwise [Collector.Collect] returns an
	// error.
	DirectOnly bool
	// NpmRegistry overrides the npm registry base URL.
	NpmRegistry string
	// NodeModulesDirs are local node_modules roots checked before the npm
	// registry (cache-first, like the Go module cache).
	NodeModulesDirs []string
	// GoProxy overrides the GOPROXY list.
	GoProxy string
	// GoCacheDir overrides the Go module cache location.
	GoCacheDir string
	// Logf, if set, receives progress messages.
	Logf func(format string, args ...any)
}

// withDefaults returns a copy of the options with every unset or invalid field replaced by a sane
// default. Callers may pass a zero-value [Options] and still get a working configuration; the
// collector always normalizes through here so the rest of the package never has to guard against
// zero values.
func (o Options) withDefaults() Options {
	if o.Concurrency < 1 {
		o.Concurrency = 8
	}

	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}

	// A nil logger would panic the first time a provider reports progress, so substitute a no-op
	// sink rather than make every call site nil-check.
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}

	return o
}

// DefaultRegistry returns a registry with the built-in npm and Go providers
// wired up using opts.
func DefaultRegistry(opts Options) *provider.Registry {
	opts = opts.withDefaults()

	// Wire the two ecosystems we ship out of the box. Each provider gets only the configuration it
	// needs; additional ecosystems are added by the caller registering more scanner/fetcher pairs
	// on the returned registry.
	r := provider.NewRegistry()
	r.Register(npm.NewScanner(), npm.NewFetcher(opts.NpmRegistry, opts.NodeModulesDirs, opts.Timeout))
	r.Register(gomod.NewScanner(), gomod.NewFetcher(opts.GoCacheDir, opts.GoProxy, opts.Timeout))

	return r
}

// Collector orchestrates scanning and fetching against a registry.
type Collector struct {
	reg  *provider.Registry
	opts Options
}

// NewCollector returns a [Collector] backed by reg. Register additional
// providers in reg before calling to support more ecosystems.
func NewCollector(reg *provider.Registry, opts Options) *Collector {
	return &Collector{reg: reg, opts: opts.withDefaults()}
}

// Collect scans every input file into a deduplicated dependency set, fetches
// license artifacts concurrently, and returns the run [model.Summary] together
// with the flat list of every artifact. A per-dependency failure does not abort
// the run; it is recorded in the [model.Summary].
func (c *Collector) Collect(ctx context.Context, files ...string) (model.Summary, []model.LicenseArtifact, error) {
	// Resolve every input file into a single deduplicated dependency set first. A scan error is
	// fatal because we cannot meaningfully report licenses for a set we failed to build.
	deps, err := c.scan(ctx, files)
	if err != nil {
		return model.Summary{}, nil, err
	}

	// Fetch the dependencies concurrently. The semaphore bounds in-flight fetches to the
	// configured concurrency, and each result is written into its own slot so the goroutines
	// never contend on a shared accumulator.
	results := make([]model.DepResult, len(deps))
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.opts.Concurrency)

	for i, dep := range deps {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, dep model.Dependency) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = c.fetchOne(ctx, dep)
		}(i, dep)
	}

	wg.Wait()

	// Assemble the run summary. We tally every dependency by its terminal status and flatten the
	// per-dependency artifacts into the single list callers consume; a per-dependency failure was
	// already captured above and never aborts the run.
	summary := model.Summary{
		GeneratedAt:  time.Now().UTC(),
		Dependencies: results,
	}

	var all []model.LicenseArtifact
	for _, r := range results {
		summary.Stats.Total++
		switch r.Status {
		case model.StatusResolved:
			summary.Stats.Resolved++
		case model.StatusNoLicenseFound:
			summary.Stats.NoLicenseFound++
		case model.StatusFailed:
			summary.Stats.Failed++
		}
		all = append(all, r.Artifacts...)
	}

	return summary, all, nil
}

// scan walks every input file, dispatches it to the scanner registered for its shape, and merges
// the discovered dependencies into a single deduplicated set keyed by [model.Dependency.Key]. A
// file with no matching scanner is skipped rather than treated as an error so callers can pass a
// heterogeneous mix of lockfiles.
func (c *Collector) scan(ctx context.Context, files []string) ([]model.Dependency, error) {
	seen := make(map[string]struct{})

	var deps []model.Dependency
	for _, f := range files {
		sc, ok := c.reg.ScannerFor(f)
		if !ok {
			c.opts.Logf("skipping %s: no scanner matched", f)
			continue
		}

		// In direct-only mode the scanner must be able to distinguish top-level dependencies from
		// transitive ones; not every scanner can, so fail loudly rather than silently returning
		// the full transitive set.
		var (
			found []model.Dependency
			err   error
		)
		if c.opts.DirectOnly {
			ds, ok := sc.(provider.DirectScanner)
			if !ok {
				return nil, fmt.Errorf("direct-only mode requested but %s (%T) cannot distinguish direct dependencies", f, sc)
			}
			found, err = ds.ScanDirect(ctx, f)
		} else {
			found, err = sc.Scan(ctx, f)
		}
		if err != nil {
			return nil, err
		}

		c.opts.Logf("scanned %s: %d dependencies (directOnly=%v)", f, len(found), c.opts.DirectOnly)

		// Deduplicate across files: the same dependency frequently appears in more than one
		// lockfile, and we only want to fetch and report its license once.
		for _, d := range found {
			if _, dup := seen[d.Key()]; dup {
				continue
			}
			seen[d.Key()] = struct{}{}
			deps = append(deps, d)
		}
	}

	return deps, nil
}

// fetchOne resolves the license artifacts for a single dependency and returns its terminal
// [model.DepResult]. Every failure path is folded into the result rather than returned as an
// error so that one bad dependency cannot abort the wider concurrent run in [Collector.Collect].
func (c *Collector) fetchOne(ctx context.Context, dep model.Dependency) model.DepResult {
	res := model.DepResult{Dependency: dep}

	// There must be a fetcher registered for the dependency's ecosystem; without one we cannot
	// retrieve anything, so record the failure and move on.
	fetcher, err := c.reg.FetcherFor(dep.Ecosystem)
	if err != nil {
		res.Status = model.StatusFailed
		res.Error = err.Error()
		return res
	}

	// Bound the network fetch by the configured per-request timeout so a single slow registry or
	// proxy cannot stall the whole run.
	fctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	arts, err := fetcher.Fetch(fctx, dep)
	if err != nil {
		c.opts.Logf("fetch %s: %v", dep.Key(), err)
		res.Status = model.StatusFailed
		res.Error = err.Error()
		return res
	}

	// A successful fetch that yields nothing is distinct from a failure: the dependency exists,
	// it simply ships no recoverable license file.
	if len(arts) == 0 {
		res.Status = model.StatusNoLicenseFound
		return res
	}

	// Classify SPDX centrally so every provider, the summary, and the returned artifacts stay
	// consistent.
	for i := range arts {
		arts[i].SPDX = license.Detect(arts[i].Text)
	}

	res.Status = model.StatusResolved
	res.Artifacts = arts
	c.opts.Logf("resolved %s: %d license file(s)", dep.Key(), len(arts))

	return res
}
