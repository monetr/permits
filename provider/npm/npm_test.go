package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jarcoal/httpmock"
	"github.com/monetr/permits/model"
)

func depSet(deps []model.Dependency) []string {
	var out []string
	for _, d := range deps {
		out = append(out, d.Name+"@"+d.Version)
	}
	sort.Strings(out)

	return out
}

func TestScannerLockfileVersions(t *testing.T) {
	want := []string{"@babel/core@7.0.0", "lodash@4.17.21", "react-dom@16.8.6"}

	for _, f := range []string{
		"../../testdata/pnpm-lock.v5.yaml",
		"../../testdata/pnpm-lock.v6.yaml",
		"../../testdata/pnpm-lock.v9.yaml",
	} {
		s := NewScanner()
		if !s.Detect("pnpm-lock.yaml") {
			t.Fatal("Detect should accept pnpm-lock.yaml")
		}

		deps, err := s.Scan(context.Background(), f)
		if err != nil {
			t.Fatalf("Scan(%s): %v", f, err)
		}

		got := depSet(deps)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v want %v", f, got, want)
		}
	}
}

func TestScannerDirect(t *testing.T) {
	// In every fixture lodash is the only direct dependency; @babel/core and
	// react-dom are transitive-only, and the v9 link: dep must be excluded.
	want := []string{"lodash@4.17.21"}

	for _, f := range []string{
		"../../testdata/pnpm-lock.v5.yaml",
		"../../testdata/pnpm-lock.v6.yaml",
		"../../testdata/pnpm-lock.v9.yaml",
	} {
		deps, err := NewScanner().ScanDirect(context.Background(), f)
		if err != nil {
			t.Fatalf("ScanDirect(%s): %v", f, err)
		}

		if got := depSet(deps); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v want %v", f, got, want)
		}
	}
}

func TestNormalizeKey(t *testing.T) {
	cases := []struct {
		key, name, version string
		major              int
		ok                 bool
	}{
		// v5: "/" separator, "_peer" suffix on the version.
		{"/lodash/4.17.21", "lodash", "4.17.21", 5, true},
		{"/@babel/core/7.0.0", "@babel/core", "7.0.0", 5, true},
		{"/react-dom/16.8.6_react@16.8.6", "react-dom", "16.8.6", 5, true},
		{"/@types/babel__core/7.1.0", "@types/babel__core", "7.1.0", 5, true},
		// v6: last-"@" separator, parenthesized peers.
		{"/lodash@4.17.21", "lodash", "4.17.21", 6, true},
		{"/@babel/core@7.0.0(react@18)", "@babel/core", "7.0.0", 6, true},
		// v9: same as v6, no leading slash.
		{"lodash@4.17.21", "lodash", "4.17.21", 9, true},
		{"@babel/core@7.0.0", "@babel/core", "7.0.0", 9, true},
		// Regression: "__" in the name must survive (was truncated to @types).
		{"@types/babel__core@7.1.0", "@types/babel__core", "7.1.0", 9, true},
		{"@types/babel__core@7.1.0(@babel/core@7.0.0)", "@types/babel__core", "7.1.0", 9, true},
		{"local-pkg@link:../local", "", "", 9, false},
		{"pkg@file:../x", "", "", 9, false},
		{"", "", "", 9, false},
	}
	for _, c := range cases {
		n, v, ok := normalizeKey(c.key, c.major)
		if ok != c.ok || n != c.name || v != c.version {
			t.Errorf("normalizeKey(%q, v%d) = (%q,%q,%v), want (%q,%q,%v)",
				c.key, c.major, n, v, ok, c.name, c.version, c.ok)
		}
	}
}

func TestRepositoryURL(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{map[string]any{"type": "git", "url": "git+https://github.com/lodash/lodash.git"}, "https://github.com/lodash/lodash"},
		{"git://github.com/jonschlinkert/is-number.git", "https://github.com/jonschlinkert/is-number"},
		{"git+ssh://git@github.com/acme/proj.git", "https://github.com/acme/proj"},
		{"git@github.com:acme/proj.git", "https://github.com/acme/proj"},
		{"github:babel/babel", "https://github.com/babel/babel"},
		{"gitlab:group/proj", "https://gitlab.com/group/proj"},
		{"bitbucket:team/proj", "https://bitbucket.org/team/proj"},
		{"gist:11081aaa281", "https://gist.github.com/11081aaa281"},
		{"acme/proj", "https://github.com/acme/proj"},
		{"https://example.org/repo/", "https://example.org/repo"},
		{map[string]any{"url": ""}, ""},
		{"not a url", ""},
		{nil, ""},
		{42, ""},
	}
	for _, c := range cases {
		if got := repositoryURL(c.in); got != c.want {
			t.Errorf("repositoryURL(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: "package/" + name,
			Mode: 0o644,
			Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()

	return buf.Bytes()
}

func TestFetcherNodeModulesFirst(t *testing.T) {
	root := t.TempDir()

	// pnpm virtual store layout for a scoped package.
	pnpmPkg := filepath.Join(root, ".pnpm", "@babel+core@7.0.0", "node_modules", "@babel", "core")
	if err := os.MkdirAll(pnpmPkg, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(pnpmPkg, "package.json"), []byte(`{"version":"7.0.0","license":"MIT","repository":"github:babel/babel"}`), 0o644)
	os.WriteFile(filepath.Join(pnpmPkg, "LICENSE"), []byte("local babel license"), 0o644)

	// flat layout for an unscoped package.
	flatPkg := filepath.Join(root, "lodash")
	if err := os.MkdirAll(flatPkg, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(flatPkg, "package.json"), []byte(`{"version":"4.17.21"}`), 0o644)
	os.WriteFile(filepath.Join(flatPkg, "LICENSE"), []byte("local lodash license"), 0o644)

	// The only registered responder is the lodash metadata endpoint returning
	// 404. Locally-resolvable deps must NOT call it; the version-mismatch case
	// must fall back to it. Call count then proves both behaviours exactly.
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	const metaURL = "https://registry.test/lodash"
	httpmock.RegisterResponder("GET", metaURL, httpmock.NewStringResponder(404, "not found"))
	httpmock.RegisterNoResponder(func(req *http.Request) (*http.Response, error) {
		t.Errorf("unexpected network call to %s", req.URL)
		return httpmock.NewStringResponse(500, "unexpected"), nil
	})

	f := NewFetcher("https://registry.test", []string{root}, time.Second)

	babel := model.Dependency{Ecosystem: model.EcosystemNPM, Name: "@babel/core", Version: "7.0.0"}
	arts, err := f.Fetch(context.Background(), babel)
	if err != nil {
		t.Fatalf("Fetch(@babel/core): %v", err)
	}
	if len(arts) != 1 || arts[0].Source != "npm-node-modules" ||
		arts[0].Text != "local babel license" || arts[0].DeclaredLicense != "MIT" {
		t.Fatalf("unexpected @babel/core artifacts: %+v", arts)
	}
	if arts[0].Repository != "https://github.com/babel/babel" {
		t.Errorf("Repository = %q, want shorthand expanded to github URL", arts[0].Repository)
	}

	lodash := model.Dependency{Ecosystem: model.EcosystemNPM, Name: "lodash", Version: "4.17.21"}
	arts, err = f.Fetch(context.Background(), lodash)
	if err != nil {
		t.Fatalf("Fetch(lodash): %v", err)
	}
	if len(arts) != 1 || arts[0].Source != "npm-node-modules" || arts[0].Text != "local lodash license" {
		t.Fatalf("unexpected lodash artifacts: %+v", arts)
	}

	// Version mismatch in flat layout must fall through (and here, fail on the
	// dead registry) rather than wrongly using the local copy.
	wrong := model.Dependency{Ecosystem: model.EcosystemNPM, Name: "lodash", Version: "9.9.9"}
	if _, err := f.Fetch(context.Background(), wrong); err == nil {
		t.Fatal("expected fallback to registry for version mismatch")
	}

	// Exactly one network call total: the version-mismatch fallback. The two
	// locally-resolved deps must not have hit the registry.
	if n := httpmock.GetCallCountInfo()["GET "+metaURL]; n != 1 {
		t.Errorf("registry called %d times, want exactly 1 (mismatch fallback only)", n)
	}
}

func TestFetcher(t *testing.T) {
	tgz := makeTarball(t, map[string]string{
		"LICENSE":   "MIT License\n\nverbatim text",
		"README.md": "not a license",
	})

	const registry = "https://registry.test"
	const tarballURL = registry + "/lodash/-/lodash-4.17.21.tgz"

	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder("GET", registry+"/lodash", httpmock.NewJsonResponderOrPanic(200, map[string]any{
		"versions": map[string]any{
			"4.17.21": map[string]any{
				"license":    "MIT",
				"repository": map[string]any{"type": "git", "url": "git+https://github.com/lodash/lodash.git"},
				"dist":       map[string]any{"tarball": tarballURL},
			},
		},
	}))
	httpmock.RegisterResponder("GET", tarballURL, httpmock.NewBytesResponder(200, tgz))

	f := NewFetcher(registry, nil, 5*time.Second)
	dep := model.Dependency{Ecosystem: model.EcosystemNPM, Name: "lodash", Version: "4.17.21"}

	arts, err := f.Fetch(context.Background(), dep)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(arts))
	}

	a := arts[0]
	if a.FileName != "LICENSE" || a.DeclaredLicense != "MIT" || a.Source != "npm-tarball" {
		t.Errorf("unexpected artifact: %+v", a)
	}
	if a.Repository != "https://github.com/lodash/lodash" {
		t.Errorf("Repository = %q, want normalized github URL", a.Repository)
	}
	if a.Text != "MIT License\n\nverbatim text" {
		t.Errorf("unexpected text: %q", a.Text)
	}
	if a.SHA256 == "" {
		t.Error("missing sha256")
	}

	// Second call must be served from the in-memory cache: reset all responders
	// so any real HTTP attempt would fail, then Fetch again.
	httpmock.Reset()
	if _, err := f.Fetch(context.Background(), dep); err != nil {
		t.Errorf("cached Fetch failed (should not hit network): %v", err)
	}
}
