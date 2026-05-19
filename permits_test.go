package permits_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	permits "github.com/monetr/permits"
	"github.com/monetr/permits/model"
	"github.com/monetr/permits/output"
	"github.com/monetr/permits/provider"
)

// fakeProvider is a custom ecosystem registered without touching the npm or go
// providers, proving that the registry extension point works end to end for an
// arbitrary third-party ecosystem.
type fakeProvider struct{ scanned string }

func (f *fakeProvider) Ecosystem() model.Ecosystem { return model.Ecosystem("fake") }
func (f *fakeProvider) Detect(p string) bool       { return strings.HasSuffix(p, ".fake") }

func (f *fakeProvider) Scan(_ context.Context, p string) ([]model.Dependency, error) {
	return []model.Dependency{
		{Ecosystem: "fake", Name: "foo", Version: "1.0.0"},
		{Ecosystem: "fake", Name: "bar", Version: "2.0.0"},
		{Ecosystem: "fake", Name: "foo", Version: "1.0.0"}, // duplicate
	}, nil
}

func (f *fakeProvider) Fetch(_ context.Context, dep model.Dependency) ([]model.LicenseArtifact, error) {
	// "bar" deliberately returns no artifacts so the collector has to record a
	// NoLicenseFound result alongside the resolved "foo".
	if dep.Name == "bar" {
		return nil, nil // no license found
	}

	return []model.LicenseArtifact{{
		Dependency: dep,
		FileName:   "LICENSE",
		Source:     "fake",
		SHA256:     "deadbeef",
		Text:       "fake license text with ``` fence inside",
	}}, nil
}

func TestCollectorWithCustomEcosystem(t *testing.T) {
	// Arrange: drop a manifest the fakeProvider will claim via its ".fake"
	// suffix, then register that provider on a fresh registry so nothing else
	// can interfere with the scan.
	dir := t.TempDir()
	in := filepath.Join(dir, "deps.fake")
	if err := os.WriteFile(in, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := provider.NewRegistry()
	fp := &fakeProvider{}
	reg.Register(fp, fp)

	// Act: collect against the manifest using a real Collector so the dedupe,
	// concurrency, and stats bookkeeping all run for real.
	c := permits.NewCollector(reg, permits.Options{Concurrency: 2, Timeout: time.Second})
	summary, arts, err := c.Collect(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}

	// Scan returns "foo" twice and "bar" once; the duplicate "foo" must collapse
	// so the total is two, and only "foo" resolves to an artifact while "bar"
	// counts as NoLicenseFound.
	if summary.Stats.Total != 2 {
		t.Fatalf("Total = %d, want 2 (deduped)", summary.Stats.Total)
	}
	if summary.Stats.Resolved != 1 || summary.Stats.NoLicenseFound != 1 {
		t.Fatalf("stats = %+v", summary.Stats)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(arts))
	}

	// Write the summary to disk so we can assert on the rendered output tree, not
	// just the in-memory model.
	out := filepath.Join(dir, "out")
	if err := output.Write(out, summary); err != nil {
		t.Fatal(err)
	}

	md, err := os.ReadFile(filepath.Join(out, "fake", "foo", "1.0.0", "LICENSE.md"))
	if err != nil {
		t.Fatal(err)
	}

	// The frontmatter must carry the dependency identity straight from the
	// artifact the provider returned.
	s := string(md)
	if !strings.Contains(s, "name: foo") || !strings.Contains(s, "source: fake") {
		t.Errorf("frontmatter missing fields:\n%s", s)
	}

	// Non-license text classifies to nothing -> empty SPDX array.
	if !strings.Contains(s, "spdx: []") {
		t.Errorf("expected empty spdx array in frontmatter:\n%s", s)
	}

	// The body must survive verbatim even though it contains a Markdown code
	// fence, which is why the writer must not wrap it in its own fence.
	if !strings.Contains(s, "fake license text with ``` fence inside") {
		t.Errorf("license body missing:\n%s", s)
	}

	// License text is emitted verbatim after the frontmatter, not fenced.
	if !strings.Contains(s, "---\n\nfake license text") {
		t.Errorf("expected verbatim text right after frontmatter:\n%s", s)
	}

	sj, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err != nil {
		t.Fatalf("summary.json not written: %v", err)
	}

	// The relative path must point at the file that was actually written.
	if !strings.Contains(string(sj), `"path": "fake/foo/1.0.0/LICENSE.md"`) {
		t.Errorf("summary.json missing relative path:\n%s", sj)
	}
}
