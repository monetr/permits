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
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	os.Exit(run())
}

func run() int {
	var pnpmLocks, goSums, nodeModules, trustHosts stringSlice
	flag.Var(&pnpmLocks, "pnpm-lock", "path to a pnpm-lock.yaml (repeatable)")
	flag.Var(&goSums, "go-sum", "path to a go.sum (repeatable)")
	flag.Var(&nodeModules, "node-modules", "node_modules root to check before the npm registry (repeatable); defaults to each pnpm-lock's sibling node_modules")
	flag.Var(&trustHosts, "trust-host", "with -strip-links, host whose links stay live, subdomains included, e.g. github.com (repeatable)")
	out := flag.String("out", "./licenses", "output directory")
	concurrency := flag.Int("concurrency", 8, "parallel fetch workers")
	goproxy := flag.String("goproxy", "", "override GOPROXY list")
	npmRegistry := flag.String("npm-registry", "", "override npm registry base URL")
	timeout := flag.Duration("timeout", 30*time.Second, "per-request timeout")
	direct := flag.Bool("direct", false, "only resolve direct (top-level) dependencies, excluding transitive")
	stripLinks := flag.Bool("strip-links", false, "censor links in license text (github.com/foo -> github[dot]com/foo) and neutralize raw HTML; relative links resolve against the package repository when known")
	stripHTML := flag.Bool("strip-html", false, "with -strip-links, remove HTML elements from license text entirely instead of escaping them")
	strict := flag.Bool("strict", false, "exit non-zero if any dependency yields no license")
	verbose := flag.Bool("v", false, "verbose progress logging")
	flag.Parse()

	files := append([]string(pnpmLocks), []string(goSums)...)
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "permits: at least one -pnpm-lock or -go-sum is required")
		flag.Usage()
		return 2
	}

	// Default each pnpm-lock's sibling node_modules as a local source.
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

	opts := permits.Options{
		Concurrency:     *concurrency,
		Timeout:         *timeout,
		Strict:          *strict,
		DirectOnly:      *direct,
		StripLinks:      *stripLinks,
		TrustedHosts:    trustHosts,
		StripHTML:       *stripHTML,
		NpmRegistry:     *npmRegistry,
		NodeModulesDirs: nmDirs,
		GoProxy:         *goproxy,
	}
	if *verbose {
		logger := log.New(os.Stderr, "permits: ", 0)
		opts.Logf = logger.Printf
	}

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

	if st.Failed > 0 || (*strict && st.NoLicenseFound > 0) {
		return 1
	}

	return 0
}
