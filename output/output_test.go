package output

import (
	"os"
	"path/filepath"
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
