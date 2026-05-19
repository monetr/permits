# permits

[![CI](https://github.com/monetr/permits/actions/workflows/ci.yml/badge.svg)](https://github.com/monetr/permits/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/monetr/permits/branch/main/graph/badge.svg)](https://codecov.io/gh/monetr/permits)
[![Go Reference](https://pkg.go.dev/badge/github.com/monetr/permits.svg)](https://pkg.go.dev/github.com/monetr/permits)

**Collect the real license text of every dependency you actually ship — not a guess from a metadata field.**

Most license tools read a `license` string out of a manifest and call it a day. `permits` goes to where
each package is actually published, pulls the **verbatim `LICENSE` / `NOTICE` files**, and works out the
**SPDX identifier(s)** from the text itself. That's the difference between "the manifest claims MIT" and
"here is the exact copyright notice you're obligated to redistribute."

It handles npm and Go today, and it's a library first — the CLI is a thin wrapper around it.

## Why it's different

- **Verbatim text, always.** Every license file is captured exactly as published. Even when the text
  can't be classified you still get the bytes — nothing is silently dropped.
- **SPDX from the source, not the label.** Classification comes from the file contents via
  [`google/licensecheck`](https://github.com/google/licensecheck), so one file can legitimately resolve
  to several IDs — a dual MIT/Apache-2.0 `LICENSE`, for instance.
- **Fair-source aware.** The source-available family `licensecheck` doesn't know — `FSL-1.1-MIT`,
  `FSL-1.1-ALv2`, `BUSL-1.1`, `Elastic-2.0` — is detected and treated as authoritative. An FSL file
  embeds its *future* license; permits won't mistake that for the grant you have today.
- **Local-first.** If a dependency is already on disk, permits reads it there and never touches the
  network.

| Ecosystem | Scanned file     | License source                                      |
|-----------|------------------|-----------------------------------------------------|
| npm       | `pnpm-lock.yaml` | local `node_modules`, then the npm registry tarball |
| Go        | `go.sum`         | local module cache, then the Go module proxy        |

"Local-first" is literal. A package found locally (npm under `node_modules`, including pnpm's `.pnpm`
store; Go in the module cache) is used as-is. If that local copy has no license file, permits trusts
that answer and records "no license" rather than second-guessing it over the network.

By default permits walks the **full resolved graph**, transitive dependencies and all. Pass `-direct`
to keep only your top-level dependencies (pnpm `importers`/root deps, and `go.mod` requires that aren't
marked `// indirect`).

## Install

```sh
make build      # produces ./permits
# or
go build -o permits ./cmd/permits
```

## CLI

```sh
permits \
  -pnpm-lock ./pnpm-lock.yaml \
  -go-sum ./go.sum \
  -out ./licenses
```

| Flag            | Default      | Meaning                                                    |
|-----------------|--------------|------------------------------------------------------------|
| `-pnpm-lock`    | —            | path to a `pnpm-lock.yaml` (repeatable)                    |
| `-go-sum`       | —            | path to a `go.sum` (repeatable)                            |
| `-out`          | `./licenses` | output directory                                           |
| `-concurrency`  | `8`          | parallel fetch workers                                     |
| `-goproxy`      | env GOPROXY  | override Go proxy list                                     |
| `-npm-registry` | npmjs.org    | override npm registry base URL                             |
| `-node-modules` | lock sibling | node_modules root checked before the registry (repeatable) |
| `-timeout`      | `30s`        | per-request timeout                                        |
| `-direct`       | `false`      | only resolve direct (top-level) deps, excluding transitive |
| `-strict`       | `false`      | exit non-zero if any dependency yields no license          |
| `-v`            | `false`      | verbose progress logging                                   |

Exit codes: **`0`** clean · **`1`** failures (or `-strict` with missing licenses) · **`2`** usage / I/O error.

### Output

permits writes a machine-readable `summary.json` plus one Markdown file per dependency-and-license-file:

```
licenses/
  summary.json
  npm/@monetr/notify/1.0.4/EPL-2.0.md
  npm/react/19.2.6/MIT.md
  go/golang.org/x/mod/v0.17.0/BSD-3-Clause.md
  go/gopkg.in/yaml.v3/v3.0.1/Apache-2.0.md   # NOTICE -> single SPDX
  go/gopkg.in/yaml.v3/v3.0.1/LICENSE.md      # dual MIT/Apache -> original name
```

The shape is `<ecosystem>/<name>/<version>/<file>.md`. Scoped npm names (`@scope/pkg`) and Go module
paths (`host.com/x/y`) nest as real directories. The filename is the detected **SPDX id** when there's
exactly one; otherwise it falls back to the original in-package name, so dual-licensed or
unclassifiable files keep their `LICENSE.md` / `NOTICE.md`. Name collisions get a `-2`, `-3` suffix,
and any `.` / `..` / path separators inside a segment are flattened to `_`.

Each Markdown file is YAML frontmatter followed by the untouched license text:

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

<verbatim license text, emitted exactly as published>
```

## Library

The CLI is just a thin consumer — the library is the real surface.

```go
import (
	permits "github.com/monetr/permits"
	"github.com/monetr/permits/output"
)

opts := permits.Options{Concurrency: 8}
c := permits.NewCollector(permits.DefaultRegistry(opts), opts)

summary, artifacts, err := c.Collect(ctx, "pnpm-lock.yaml", "go.sum")
// summary and artifacts are ready to use; writing files is optional.
_ = output.Write("./licenses", summary)
```

`artifacts` is a flat list of every `model.LicenseArtifact`, raw `Text` included, so you can embed
permits without ever touching the filesystem.

### Adding an ecosystem

Implement `provider.Scanner` and `provider.Fetcher`, register the pair, and you're done — the
collector never changes:

```go
reg := permits.DefaultRegistry(opts) // or provider.NewRegistry()
reg.Register(myCargoScanner, myCargoFetcher)
c := permits.NewCollector(reg, opts)
```

A `Scanner` turns a lockfile into `[]model.Dependency`; a `Fetcher` turns one `model.Dependency` into
`[]model.LicenseArtifact`. `provider/npm` and `provider/gomod` are the reference implementations to
copy from.

## Development

```sh
make test       # go test ./...
make lint       # go vet + gofmt check
```
