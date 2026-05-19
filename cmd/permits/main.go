// Command permits scans pnpm-lock.yaml and go.sum files and gathers the
// verbatim license text of every resolved dependency from the npm registry and
// the Go module cache/proxy. It is a thin wrapper around the permits library.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	permits "github.com/monetr/permits"
	"github.com/monetr/permits/output"
)

// stringSlice is a repeatable string flag (-pnpm-lock a -pnpm-lock b).
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }

// Set appends another value, allowing the same flag to be supplied more than once instead of
// overwriting the previous occurrence.
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	os.Exit(run())
}

// run holds the real entrypoint so that it can return an exit code instead of calling os.Exit
// directly; this keeps deferred cleanup working and makes the flow testable.
func run() int {
	// Define the command-line surface. The lockfile and node_modules flags are repeatable so a
	// single invocation can cover a polyglot repository.
	var pnpmLocks, goSums, nodeModules stringSlice
	flag.Var(&pnpmLocks, "pnpm-lock", "path to a pnpm-lock.yaml (repeatable)")
	flag.Var(&goSums, "go-sum", "path to a go.sum (repeatable)")
	flag.Var(&nodeModules, "node-modules", "node_modules root to check before the npm registry (repeatable); defaults to each pnpm-lock's sibling node_modules")
	out := flag.String("out", "./licenses", "output directory")
	concurrency := flag.Int("concurrency", 8, "parallel fetch workers")
	goproxy := flag.String("goproxy", "", "override GOPROXY list")
	npmRegistry := flag.String("npm-registry", "", "override npm registry base URL")
	timeout := flag.Duration("timeout", 30*time.Second, "per-request timeout")
	direct := flag.Bool("direct", false, "only resolve direct (top-level) dependencies, excluding transitive")
	strict := flag.Bool("strict", false, "exit non-zero if any dependency yields no license")
	verbose := flag.Bool("v", false, "verbose progress logging")
	flag.Parse()

	// At least one lockfile is mandatory; with nothing to scan there is no work to do, so fail
	// fast with usage rather than producing an empty result.
	files := append([]string(pnpmLocks), []string(goSums)...)
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "permits: at least one -pnpm-lock or -go-sum is required")
		flag.Usage()
		return 2
	}

	// Default each pnpm-lock's sibling node_modules as a local source. Resolving from an existing
	// install is faster and more reliable than the registry, and the seen set keeps shared
	// node_modules roots from being probed twice.
	nmDirs := []string(nodeModules)
	if len(nmDirs) == 0 {
		seen := map[string]struct{}{}
		for _, lf := range pnpmLocks {
			d := filepath.Join(filepath.Dir(lf), "node_modules")
			if _, ok := seen[d]; ok {
				continue
			}
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				seen[d] = struct{}{}
				nmDirs = append(nmDirs, d)
			}
		}
	}

	// Translate the parsed flags into library options. Verbose logging is opt-in and routed to
	// stderr so it never contaminates the machine-readable summary on stdout.
	opts := permits.Options{
		Concurrency:     *concurrency,
		Timeout:         *timeout,
		Strict:          *strict,
		DirectOnly:      *direct,
		NpmRegistry:     *npmRegistry,
		NodeModulesDirs: nmDirs,
		GoProxy:         *goproxy,
	}
	if *verbose {
		logger := log.New(os.Stderr, "permits: ", 0)
		opts.Logf = logger.Printf
	}

	// Run the collection and persist the result. Both the collection and the write are fatal: a
	// failure here means we have nothing trustworthy to report.
	c := permits.NewCollector(permits.DefaultRegistry(opts), opts)
	summary, _, err := c.Collect(context.Background(), files...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "permits: %v\n", err)
		return 2
	}

	if err := output.Write(*out, summary); err != nil {
		fmt.Fprintf(os.Stderr, "permits: writing output: %v\n", err)
		return 2
	}

	st := summary.Stats
	fmt.Printf("permits: %d deps, %d resolved, %d no-license, %d failed -> %s\n",
		st.Total, st.Resolved, st.NoLicenseFound, st.Failed, *out)

	// A hard failure always exits non-zero; missing licenses only do so under -strict, letting CI
	// choose whether an unresolved license should break the build.
	if st.Failed > 0 || (*strict && st.NoLicenseFound > 0) {
		return 1
	}

	return 0
}
