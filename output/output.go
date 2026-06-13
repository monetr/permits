// Package output writes a collection run to disk: a machine-readable summary.json plus one Markdown
// file per license artifact, laid out as <ecosystem>/<name>/<version>/<spdx-or-original>.md with
// YAML frontmatter followed by the verbatim license text.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monetr/permits/model"
)

// FrontmatterField is an extra line injected into every artifact's YAML frontmatter, after the
// standard fields. The value is written verbatim, so the caller owns its YAML type: a Value of
// "false" lands as the boolean search: false, while a quoted `"false"` stays a string. This is how
// callers tag the generated docs for whatever renders them, e.g. a "search: false" that tells a
// docs site to skip indexing.
type FrontmatterField struct {
	Key   string
	Value string
}

// Write renders summary into dir, creating it if necessary. Any extra fields are appended to every
// artifact's frontmatter block in the order given.
func Write(dir string, summary model.Summary, extra ...FrontmatterField) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Order summary.json the same way the on-disk tree lists: by the path segments
	// (<ecosystem>/<name>/<version>) and then artifact filename. Sorting before the write
	// loop also makes the collision suffixes (-2, -3) deterministic in folder order.
	sortByFolder(summary.Dependencies)

	// Track filenames already used within each dependency directory so that collisions (same
	// SPDX id, or repeated original names) get a numeric suffix instead of overwriting each other.
	used := make(map[string]bool)
	for i := range summary.Dependencies {
		for j := range summary.Dependencies[i].Artifacts {
			art := &summary.Dependencies[i].Artifacts[j]

			rel, err := writeArtifact(dir, *art, used, extra)
			if err != nil {
				return err
			}

			art.Path = rel // recorded in summary.json below
		}
	}

	f, err := os.Create(filepath.Join(dir, "summary.json"))
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")

	return enc.Encode(summary)
}

// sortByFolder orders deps, and the artifacts within each dep, by the path the tree on disk
// uses: <ecosystem>/<name>/<version> then the artifact's file stem. nameToPath is compared
// (not the raw name) so scoped/Go-module names sort by their real directory segments.
func sortByFolder(deps []model.DepResult) {
	sort.SliceStable(deps, func(i, j int) bool {
		a, b := deps[i].Dependency, deps[j].Dependency
		if a.Ecosystem != b.Ecosystem {
			return a.Ecosystem < b.Ecosystem
		}
		if pa, pb := nameToPath(a.Name), nameToPath(b.Name); pa != pb {
			return pa < pb
		}
		return a.Version < b.Version
	})

	for i := range deps {
		arts := deps[i].Artifacts
		sort.SliceStable(arts, func(x, y int) bool {
			return baseName(arts[x]) < baseName(arts[y])
		})
	}
}

// writeArtifact writes one Markdown file and returns its slash-separated path relative to dir (the
// directory that also holds summary.json).
func writeArtifact(dir string, a model.LicenseArtifact, used map[string]bool, extra []FrontmatterField) (string, error) {
	depDir := filepath.Join(dir, segment(string(a.Ecosystem)), nameToPath(a.Name), segment(a.Version))
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		return "", err
	}

	path := uniquePath(depDir, baseName(a), used)

	var b strings.Builder
	// Every value is written as a quoted scalar. A bare version like "1.0" or the RFC3339
	// retrievedAt would otherwise be parsed as a float or a timestamp instead of the string we
	// mean, and a stray colon or quote in a package name or license id would break the document.
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", yamlString(a.Name))
	fmt.Fprintf(&b, "version: %s\n", yamlString(a.Version))
	fmt.Fprintf(&b, "ecosystem: %s\n", yamlString(string(a.Ecosystem)))
	if a.DeclaredLicense != "" {
		fmt.Fprintf(&b, "declaredLicense: %s\n", yamlString(a.DeclaredLicense))
	}
	fmt.Fprintf(&b, "spdx: [%s]\n", strings.Join(quoteAll(a.SPDX), ", "))
	fmt.Fprintf(&b, "licenseFile: %s\n", yamlString(a.FileName))
	fmt.Fprintf(&b, "source: %s\n", yamlString(a.Source))
	fmt.Fprintf(&b, "sha256: %s\n", yamlString(a.SHA256))
	fmt.Fprintf(&b, "retrievedAt: %s\n", yamlString(a.RetrievedAt.Format("2006-01-02T15:04:05Z07:00")))
	// Caller-supplied lines go last and pass through verbatim; we do not quote the value here so a
	// "search: false" stays a boolean rather than turning into the string "false".
	for _, fm := range extra {
		fmt.Fprintf(&b, "%s: %s\n", fm.Key, fm.Value)
	}
	b.WriteString("---\n\n")

	b.WriteString(a.Text)
	if !strings.HasSuffix(a.Text, "\n") {
		b.WriteByte('\n')
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}

	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return "", err
	}

	return filepath.ToSlash(rel), nil
}

// baseName is the file stem (".md" is added later): the SPDX id when exactly one was detected,
// otherwise the original in-package filename with any license-ish extension stripped so we don't
// produce e.g. "LICENSE.md.md".
func baseName(a model.LicenseArtifact) string {
	if len(a.SPDX) == 1 {
		return segment(a.SPDX[0])
	}

	return segment(stripLicenseExt(a.FileName))
}

// stripLicenseExt removes a trailing documentation extension. It only strips a known set (not
// [filepath.Ext]) so SPDX-ish names like "Apache-2.0" or "BSD-3-Clause" keep their ".0"/"Clause"
// tail.
func stripLicenseExt(name string) string {
	for _, ext := range []string{".markdown", ".md", ".txt", ".rst"} {
		if len(name) > len(ext) && strings.EqualFold(name[len(name)-len(ext):], ext) {
			return name[:len(name)-len(ext)]
		}
	}

	return name
}

// uniquePath joins stem+".md" under depDir, appending -2, -3, ... if that file was already written
// this run.
func uniquePath(depDir, stem string, used map[string]bool) string {
	for i := 1; ; i++ {
		name := stem
		if i > 1 {
			name = fmt.Sprintf("%s-%d", stem, i)
		}

		p := filepath.Join(depDir, name+".md")
		if !used[p] {
			used[p] = true
			return p
		}
	}
}

// nameToPath turns a package name into nested path segments, preserving the natural hierarchy of
// scoped npm names (@scope/pkg) and Go module paths (host.com/x/y) while keeping each segment
// filesystem-safe.
func nameToPath(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = segment(p)
	}

	return filepath.Join(parts...)
}

// segment makes a single path component filesystem-safe and prevents traversal.
func segment(s string) string {
	switch s {
	case "", ".", "..":
		return "_"
	}
	return strings.NewReplacer("/", "_", `\`, "_", string(filepath.Separator), "_").Replace(s)
}

// quoteAll YAML-quotes each element so the flow sequence is always valid.
func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = yamlString(s)
	}

	return out
}

// yamlString renders s as a double-quoted YAML scalar. JSON's string encoding is a strict subset
// of YAML's flow scalar syntax, so going through encoding/json gives us correct escaping of quotes,
// backslashes, and control characters for free, and the result is always valid YAML. HTML escaping
// is turned off so the "<", ">", and "&" that show up in license urls and prose stay readable
// instead of getting rewritten into their numeric escapes.
func yamlString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// Encode tacks on a trailing newline we do not want in the middle of a line.
	_ = enc.Encode(s)

	return strings.TrimSuffix(b.String(), "\n")
}
