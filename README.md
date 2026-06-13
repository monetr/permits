# permits

[![CI](https://github.com/monetr/permits/actions/workflows/ci.yml/badge.svg)](https://github.com/monetr/permits/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/monetr/permits/branch/main/graph/badge.svg)](https://codecov.io/gh/monetr/permits)
[![Go Reference](https://pkg.go.dev/badge/github.com/monetr/permits.svg)](https://pkg.go.dev/github.com/monetr/permits)

permits collects the full license text of every dependency your project
resolves. Instead of trusting the `license` field in a manifest, it fetches the
actual `LICENSE` and `NOTICE` files from wherever the package is published and
works out the SPDX identifier from the file contents.

It supports npm and Go. Almost all of the behavior lives in the library; the
CLI is a small wrapper around it.

Every file is stored exactly as published. If the text doesn't match a known
license it's still saved, just without an SPDX id. Detection uses
[`google/licensecheck`](https://github.com/google/licensecheck) and runs on the
file contents, so one file can produce several ids (a combined MIT/Apache-2.0
`LICENSE`, for example).

permits also recognizes the source-available licenses licensecheck doesn't
cover: `FSL-1.1-MIT`, `FSL-1.1-ALv2`, `BUSL-1.1`, and `Elastic-2.0`. For an FSL
file it reports the current grant, not the future license embedded in the text.

It looks on disk before going to the network:

| Ecosystem | Scanned file     | License source                                      |
|-----------|------------------|-----------------------------------------------------|
| npm       | `pnpm-lock.yaml` | local `node_modules`, then the npm registry tarball |
| Go        | `go.sum`         | local module cache, then the Go module proxy        |

If a package is already installed (npm in `node_modules`, including pnpm's
`.pnpm` store, or Go in the module cache), that copy is used and the network is
skipped. A package that exists locally but has no license file is taken at face
value: permits records "no license" rather than falling back to the registry.

By default it scans the whole dependency graph, transitive dependencies
included. `-direct` limits it to your direct dependencies: the pnpm
`importers`/root deps, and `go.mod` requires not marked `// indirect`.

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
| `-strip-links`  | `false`      | censor links and neutralize raw HTML in the license text   |
| `-trust-host`   | —            | host whose links stay live with `-strip-links` (repeatable)|
| `-strip-html`   | `false`      | with `-strip-links`, delete HTML elements instead          |
| `-strict`       | `false`      | exit non-zero if any dependency yields no license          |
| `-v`            | `false`      | verbose progress logging                                   |

Exit codes: `0` on success, `1` if a dependency failed (or `-strict` and
something had no license), `2` on a usage or I/O error.

### Sanitizing for rendering

`-strip-links` is the one option that edits the stored text. It exists so the
collected licenses can be published on a site that renders markdown (or MDX)
to HTML without a package author being able to smuggle in live links, images,
or markup. The text is rewritten so it renders as plain prose:

- Markdown link syntax is unwrapped and the host censored:
  `[foo](https://foo.com/x)` becomes `foo (foo[dot]com/x)`. Bare URLs, `www.`
  hosts, and email addresses are censored the same way, since most renderers
  auto-link them.
- Relative links resolve against the package's repository when it's known
  (npm `repository` metadata, the Go module path), so `[guide](/x)` becomes
  `guide (github[dot]com/x)`. Without a repository the link collapses to its
  bare path, just `/x`.
- Raw HTML/JSX (`<img>`, `<a href>`), MDX expressions (`{...}`), line-leading
  `import`/`export`, and any link form the rewriter doesn't recognize are
  escaped so they display as text instead of doing anything. `-strip-html`
  deletes HTML elements outright instead: tags go away (their inner text
  stays), comments and script/style blocks vanish entirely. Fenced code blocks
  are left alone either way.
- `-trust-host github.com` (repeatable) lets links to hosts you trust through
  unchanged, subdomains included. Trust is decided by the parsed hostname, so
  `github.com.evil.com` or `https://github.com@evil.com` don't qualify, and it
  never applies to images, which render without a click.

SPDX detection still runs on the original text, and the recorded `sha256`
covers the rewritten text.

### Output

permits writes a `summary.json` and one Markdown file per license file:

```
licenses/
  summary.json
  npm/@monetr/notify/1.0.4/EPL-2.0.md
  npm/react/19.2.6/MIT.md
  go/golang.org/x/mod/v0.17.0/BSD-3-Clause.md
  go/gopkg.in/yaml.v3/v3.0.1/Apache-2.0.md   # NOTICE -> single SPDX
  go/gopkg.in/yaml.v3/v3.0.1/LICENSE.md      # dual MIT/Apache -> original name
```

Paths are `<ecosystem>/<name>/<version>/<file>.md`. Scoped npm names
(`@scope/pkg`) and Go module paths (`host.com/x/y`) become nested directories.
The filename is the SPDX id when exactly one is detected; otherwise it's the
original in-package filename, so dual-licensed or unrecognized files keep their
`LICENSE.md` or `NOTICE.md`. Repeated names get a `-2`, `-3` suffix, and `.`,
`..`, or path separators inside a segment become `_`.

Each file is YAML frontmatter followed by the original license text:

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

The CLI just calls the library:

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

`artifacts` is a flat slice of `model.LicenseArtifact` including the raw `Text`,
so you can use permits without writing anything to disk.

### Adding an ecosystem

Implement `provider.Scanner` and `provider.Fetcher` and register them; the
collector doesn't change:

```go
reg := permits.DefaultRegistry(opts) // or provider.NewRegistry()
reg.Register(myCargoScanner, myCargoFetcher)
c := permits.NewCollector(reg, opts)
```

A `Scanner` parses a lockfile into `[]model.Dependency`; a `Fetcher` turns one
`model.Dependency` into `[]model.LicenseArtifact`. `provider/npm` and
`provider/gomod` are working examples to copy.

## Development

```sh
make test       # go test ./...
make lint       # go vet + gofmt check
```
