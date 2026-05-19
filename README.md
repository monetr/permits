# permits

`permits` gathers the **verbatim license text** of every dependency a project
actually resolves, and classifies each file's **SPDX license identifier(s)**
(via [`google/licensecheck`](https://github.com/google/licensecheck) — a single
file may yield several, e.g. a dual MIT/Apache-2.0 `LICENSE`). The "fair
source" family that licensecheck lacks (`FSL-1.1-MIT`, `FSL-1.1-ALv2`,
`BUSL-1.1`, `Elastic-2.0`) is detected too, and is authoritative for the file
(the future-license text embedded in FSL is not treated as the current grant).
Text is always captured verbatim even when the license cannot be classified.
It scans lockfiles and pulls the raw license files straight from where the
packages are published:

| Ecosystem | Scanned file    | License source                                          |
|-----------|-----------------|---------------------------------------------------------|
| npm       | `pnpm-lock.yaml`| local `node_modules`, then the npm registry tarball     |
| Go        | `go.sum`        | local Go module cache, then the Go module proxy         |

Both ecosystems are **local-first**: if the dependency is already installed
(an npm package in `node_modules` — including the pnpm `.pnpm` virtual store —
or a Go module in the cache) and contains a license file, that copy is used and
the network is not contacted. A package present locally with no license file is
treated as authoritative ("no license"), not retried remotely.

By default it scans the **full resolved set** (including transitive
dependencies); pass `-direct` to restrict to direct/top-level dependencies
(pnpm `importers`/root deps and `go.mod` non-`// indirect` requires). It emits
a machine-readable `summary.json` plus one Markdown file per
dependency-and-license-file, each with YAML frontmatter followed by the
verbatim text.

## Install / build

```sh
make build      # produces ./permits
# or
go build -o permits ./cmd/permits
```

## CLI usage

```sh
permits \
  -pnpm-lock ./pnpm-lock.yaml \
  -go-sum ./go.sum \
  -out ./licenses
```

Flags (stdlib `flag`):

| Flag            | Default      | Meaning                                            |
|-----------------|--------------|----------------------------------------------------|
| `-pnpm-lock`    | —            | path to a `pnpm-lock.yaml` (repeatable)            |
| `-go-sum`       | —            | path to a `go.sum` (repeatable)                    |
| `-out`          | `./licenses` | output directory                                   |
| `-concurrency`  | `8`          | parallel fetch workers                             |
| `-goproxy`      | env GOPROXY  | override Go proxy list                             |
| `-npm-registry` | npmjs.org    | override npm registry base URL                     |
| `-node-modules` | lock sibling | node_modules root checked before the registry (repeatable) |
| `-timeout`      | `30s`        | per-request timeout                                |
| `-direct`       | `false`      | only resolve direct (top-level) deps, excluding transitive |
| `-strict`       | `false`      | exit non-zero if any dependency yields no license  |
| `-v`            | `false`      | verbose progress logging                           |

Exit codes: `0` all good · `1` failures (or `-strict` with missing licenses) ·
`2` usage/IO error.

### Output layout

```
licenses/
  summary.json
  npm/@monetr/notify/1.0.4/EPL-2.0.md
  npm/react/19.2.6/MIT.md
  go/golang.org/x/mod/v0.17.0/BSD-3-Clause.md
  go/gopkg.in/yaml.v3/v3.0.1/Apache-2.0.md   # NOTICE -> single SPDX
  go/gopkg.in/yaml.v3/v3.0.1/LICENSE.md      # dual MIT/Apache -> original name
```

Layout is `<ecosystem>/<name>/<version>/<file>.md`. Scoped npm names
(`@scope/pkg`) and Go module paths (`host.com/x/y`) nest as real directories.
The file stem is the detected **SPDX id** when exactly one is found, otherwise
the original in-package filename (so dual-license or unclassifiable files keep
`LICENSE.md` / `NOTICE.md`); same-name collisions get a `-2`, `-3` suffix. Path
segments containing `.`/`..`/separators are sanitized to `_`. Each Markdown
file:

```
---
name: react
version: 18.2.0
ecosystem: npm
declaredLicense: "MIT"
spdx: ["MIT"]
licenseFile: "LICENSE"
source: npm-tarball
sha256: <hex>
retrievedAt: 2026-05-18T00:00:00Z
---

<verbatim license text, emitted as-is after the frontmatter>
```

## Library usage

The CLI is just a thin consumer; the library is the primary surface.

```go
import (
	permits "github.com/monetr/permits"
	"github.com/monetr/permits/output"
)

opts := permits.Options{Concurrency: 8}
c := permits.NewCollector(permits.DefaultRegistry(opts), opts)

summary, artifacts, err := c.Collect(ctx, "pnpm-lock.yaml", "go.sum")
// summary/artifacts are usable directly; writing files is optional:
_ = output.Write("./licenses", summary)
```

`artifacts` is the flat list of every `model.LicenseArtifact` (including the raw
`Text`), so the library can be embedded without ever touching the filesystem.

## Adding a new ecosystem

Implement `provider.Scanner` + `provider.Fetcher` and register them — no
collector changes:

```go
reg := permits.DefaultRegistry(opts) // or provider.NewRegistry()
reg.Register(myCargoScanner, myCargoFetcher)
c := permits.NewCollector(reg, opts)
```

A `Scanner` turns a lockfile into `[]model.Dependency`; a `Fetcher` turns one
`model.Dependency` into `[]model.LicenseArtifact`. See `provider/npm` and
`provider/gomod` for reference implementations.

## Development

```sh
make test       # go test ./...
make lint       # go vet + gofmt check
```
