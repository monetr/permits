package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monetr/permits/model"
)

func TestBaseName(t *testing.T) {
	cases := []struct {
		spdx     []string
		fileName string
		want     string
	}{
		{[]string{"MIT"}, "LICENSE", "MIT"},
		{[]string{"Apache-2.0"}, "LICENSE.md", "Apache-2.0"},
		{nil, "LICENSE", "LICENSE"},
		{nil, "NOTICE", "NOTICE"},
		{nil, "LICENSE.txt", "LICENSE"},
		{[]string{"MIT", "Apache-2.0"}, "LICENSE.md", "LICENSE"}, // multi -> original, no .md.md
		{[]string{"MIT", "Apache-2.0"}, "LICENSE-MIT", "LICENSE-MIT"},
		{nil, "COPYING.rst", "COPYING"},
	}

	for _, c := range cases {
		got := baseName(model.LicenseArtifact{SPDX: c.spdx, FileName: c.fileName})
		if got != c.want {
			t.Errorf("baseName(spdx=%v file=%q) = %q, want %q", c.spdx, c.fileName, got, c.want)
		}
	}
}

func TestWriteExtraFrontmatter(t *testing.T) {
	dir := t.TempDir()
	dep := model.Dependency{Ecosystem: "npm", Name: "foo", Version: "1.0.0"}
	summary := model.Summary{Dependencies: []model.DepResult{{
		Dependency: dep,
		Status:     model.StatusResolved,
		Artifacts: []model.LicenseArtifact{
			{Dependency: dep, SPDX: []string{"MIT"}, FileName: "LICENSE", Text: "x"},
		},
	}}}

	// search is a bare boolean, title carries a colon that has to stay quoted by the caller.
	if err := Write(dir, summary,
		FrontmatterField{Key: "search", Value: "false"},
		FrontmatterField{Key: "title", Value: `"foo: a dep"`},
	); err != nil {
		t.Fatal(err)
	}

	md, err := os.ReadFile(filepath.Join(dir, "npm", "foo", "1.0.0", "MIT.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(md)

	// The extra fields land verbatim, after the standard ones, and inside the frontmatter block.
	if !strings.Contains(s, "\nsearch: false\n") {
		t.Errorf("expected verbatim boolean field:\n%s", s)
	}
	if !strings.Contains(s, "\ntitle: \"foo: a dep\"\n") {
		t.Errorf("expected verbatim quoted field:\n%s", s)
	}
	if i, j := strings.Index(s, "retrievedAt:"), strings.Index(s, "search: false"); i < 0 || j < i {
		t.Errorf("extra fields should follow the standard ones:\n%s", s)
	}
	if !strings.HasPrefix(s, "---\n") || strings.Count(s, "---\n") < 2 {
		t.Errorf("extra fields broke the frontmatter delimiters:\n%s", s)
	}
}

func TestWriteNoDoubleExtensionAndCollisions(t *testing.T) {
	dir := t.TempDir()
	dep := model.Dependency{Ecosystem: "npm", Name: "@scope/pkg", Version: "1.0.0"}
	summary := model.Summary{Dependencies: []model.DepResult{{
		Dependency: dep,
		Status:     model.StatusResolved,
		Artifacts: []model.LicenseArtifact{
			// Multi-SPDX + .md original -> must be LICENSE.md, not LICENSE.md.md.
			{Dependency: dep, SPDX: []string{"MIT", "Apache-2.0"}, FileName: "LICENSE.md", Text: "a"},
			// Same resulting stem -> collision suffix.
			{Dependency: dep, SPDX: []string{"MIT", "Apache-2.0"}, FileName: "LICENSE.txt", Text: "b"},
		},
	}}}

	if err := Write(dir, summary); err != nil {
		t.Fatal(err)
	}

	base := filepath.Join(dir, "npm", "@scope", "pkg", "1.0.0")
	for _, want := range []string{"LICENSE.md", "LICENSE-2.md"} {
		if _, err := os.Stat(filepath.Join(base, want)); err != nil {
			t.Errorf("expected %s: %v", want, err)
		}
	}

	if _, err := os.Stat(filepath.Join(base, "LICENSE.md.md")); err == nil {
		t.Error("LICENSE.md.md should not exist (double extension bug)")
	}
}
