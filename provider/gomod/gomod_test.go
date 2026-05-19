package gomod

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jarcoal/httpmock"

	"github.com/monetr/permits/model"
)

func TestScannerDirect(t *testing.T) {
	dir := t.TempDir()
	gomod := `module example.com/app

go 1.23

require (
	github.com/direct/one v1.2.3
	golang.org/x/mod v0.17.0
)

require (
	github.com/indirect/dep v0.1.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), []byte("irrelevant\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps, err := NewScanner().ScanDirect(context.Background(), filepath.Join(dir, "go.sum"))
	if err != nil {
		t.Fatalf("ScanDirect: %v", err)
	}

	var got []string
	for _, d := range deps {
		got = append(got, d.Name+"@"+d.Version)
	}
	sort.Strings(got)

	want := []string{"github.com/direct/one@v1.2.3", "golang.org/x/mod@v0.17.0"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ScanDirect got %v, want %v (indirect must be excluded)", got, want)
	}

	// Missing go.mod must be a clear error, not a silent empty result.
	if _, err := NewScanner().ScanDirect(context.Background(), filepath.Join(t.TempDir(), "go.sum")); err == nil {
		t.Error("expected error when go.mod is absent")
	}
}

func TestScanner(t *testing.T) {
	s := NewScanner()

	if !s.Detect("path/to/go.sum") {
		t.Fatal("Detect should accept go.sum")
	}

	deps, err := s.Scan(context.Background(), "../../testdata/go.sum")
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, d := range deps {
		got = append(got, d.Name+"@"+d.Version)
	}
	sort.Strings(got)

	want := []string{
		"example.com/Mixed/Case@v1.0.0",
		"golang.org/x/mod@v0.17.0",
		"gopkg.in/yaml.v3@v3.0.1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestFetcherCacheHit(t *testing.T) {
	cache := t.TempDir()

	// example.com/Mixed/Case escapes to example.com/!mixed/!case.
	modDir := filepath.Join(cache, "example.com", "!mixed", "!case@v1.0.0")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "LICENSE"), []byte("BSD-3 verbatim"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := NewFetcher(cache, "https://proxy.invalid", time.Second)
	dep := model.Dependency{Ecosystem: model.EcosystemGo, Name: "example.com/Mixed/Case", Version: "v1.0.0"}

	arts, err := f.Fetch(context.Background(), dep)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(arts) != 1 || arts[0].Source != "go-cache" || arts[0].Text != "BSD-3 verbatim" {
		t.Fatalf("unexpected artifacts: %+v", arts)
	}
}

func makeModuleZip(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"golang.org/x/mod@v0.17.0/LICENSE": "x/mod BSD license text",
		"golang.org/x/mod@v0.17.0/go.mod":  "module golang.org/x/mod",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()

	return buf.Bytes()
}

func TestFetcherProxyFallback(t *testing.T) {
	zipData := makeModuleZip(t)

	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	const proxy = "https://proxy.test"
	const zipURL = proxy + "/golang.org/x/mod/@v/v0.17.0.zip"
	httpmock.RegisterResponder("GET", zipURL, httpmock.NewBytesResponder(200, zipData))
	httpmock.RegisterNoResponder(httpmock.NewStringResponder(404, "not found"))

	// Empty cache dir so the fetcher falls back to the proxy.
	f := NewFetcher(t.TempDir(), proxy, 5*time.Second)
	dep := model.Dependency{Ecosystem: model.EcosystemGo, Name: "golang.org/x/mod", Version: "v0.17.0"}

	arts, err := f.Fetch(context.Background(), dep)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(arts) != 1 || arts[0].Source != "go-proxy" || arts[0].FileName != "LICENSE" {
		t.Fatalf("unexpected artifacts: %+v", arts)
	}
	if arts[0].Text != "x/mod BSD license text" {
		t.Errorf("unexpected text: %q", arts[0].Text)
	}

	if n := httpmock.GetCallCountInfo()["GET "+zipURL]; n != 1 {
		t.Errorf("proxy called %d times, want 1", n)
	}
}
