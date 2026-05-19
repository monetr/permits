package output

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/monetr/permits/model"
)

func TestBaseName(t *testing.T) {
	// baseName decides the on-disk stem for a license file. When exactly one SPDX
	// identifier was detected we prefer that identifier so the output tree reads
	// nicely (MIT, Apache-2.0, ...). When the detection is ambiguous (zero or more
	// than one SPDX id) we fall back to the original file name with any text
	// extension stripped, which is what keeps us from producing names like
	// "LICENSE.md.md" later on.
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
	// Two artifacts on the same dependency that both resolve to the "LICENSE"
	// stem exercise the two trickiest bits of Write at once: the multi-SPDX
	// ".md" original must not turn into "LICENSE.md.md", and the second artifact
	// that collides on the same name must get a numeric suffix instead of
	// silently overwriting the first.
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

	// Both the original and the collision-suffixed file must exist side by side.
	base := filepath.Join(dir, "npm", "@scope", "pkg", "1.0.0")
	for _, want := range []string{"LICENSE.md", "LICENSE-2.md"} {
		if _, err := os.Stat(filepath.Join(base, want)); err != nil {
			t.Errorf("expected %s: %v", want, err)
		}
	}

	// The double-extension name is the specific regression this test guards.
	if _, err := os.Stat(filepath.Join(base, "LICENSE.md.md")); err == nil {
		t.Error("LICENSE.md.md should not exist (double extension bug)")
	}
}
