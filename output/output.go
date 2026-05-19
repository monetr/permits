// Package output writes a collection run to disk: a machine-readable summary.json plus one Markdown
// file per license artifact, laid out as <ecosystem>/<name>/<version>/<spdx-or-original>.md with
// YAML frontmatter followed by the verbatim license text.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monetr/permits/model"
)

// Write renders summary into dir, creating it if necessary.
func Write(dir string, summary model.Summary) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Track filenames already used within each dependency directory so that collisions (same
	// SPDX id, or repeated original names) get a numeric suffix instead of overwriting each other.
	used := make(map[string]bool)
	for i := range summary.Dependencies {
		for j := range summary.Dependencies[i].Artifacts {
			art := &summary.Dependencies[i].Artifacts[j]

			rel, err := writeArtifact(dir, *art, used)
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

// writeArtifact writes one Markdown file and returns its slash-separated path relative to dir (the
// directory that also holds summary.json).
func writeArtifact(dir string, a model.LicenseArtifact, used map[string]bool) (string, error) {
	depDir := filepath.Join(dir, segment(string(a.Ecosystem)), nameToPath(a.Name), segment(a.Version))
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		return "", err
	}

	path := uniquePath(depDir, baseName(a), used)

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", a.Name)
	fmt.Fprintf(&b, "version: %s\n", a.Version)
	fmt.Fprintf(&b, "ecosystem: %s\n", a.Ecosystem)
	if a.DeclaredLicense != "" {
		fmt.Fprintf(&b, "declaredLicense: %q\n", a.DeclaredLicense)
	}
	fmt.Fprintf(&b, "spdx: [%s]\n", strings.Join(quoteAll(a.SPDX), ", "))
	fmt.Fprintf(&b, "licenseFile: %q\n", a.FileName)
	fmt.Fprintf(&b, "source: %s\n", a.Source)
	fmt.Fprintf(&b, "sha256: %s\n", a.SHA256)
	fmt.Fprintf(&b, "retrievedAt: %s\n", a.RetrievedAt.Format("2006-01-02T15:04:05Z07:00"))
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
		out[i] = fmt.Sprintf("%q", s)
	}

	return out
}
