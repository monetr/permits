package npm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/monetr/permits/model"
)

func TestPackageLockScannerVersions(t *testing.T) {
	want := []string{"@babel/core@7.0.0", "lodash@4.17.21", "react-dom@16.8.6"}

	for _, f := range []string{
		"../../testdata/package-lock.v1.json",
		"../../testdata/package-lock.v2.json",
		"../../testdata/package-lock.v3.json",
	} {
		s := NewPackageLockScanner()
		if !s.Detect("package-lock.json") || !s.Detect("npm-shrinkwrap.json") {
			t.Fatal("Detect should accept package-lock.json and npm-shrinkwrap.json")
		}

		deps, err := s.Scan(context.Background(), f)
		if err != nil {
			t.Fatalf("Scan(%s): %v", f, err)
		}

		if got := depSet(deps); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v want %v", f, got, want)
		}
	}
}

func TestPackageLockScannerDirect(t *testing.T) {
	// lodash is the only direct dependency; @babel/core and react-dom are
	// transitive-only, and the non-registry local-pkg must be excluded.
	want := []string{"lodash@4.17.21"}

	for _, f := range []string{
		"../../testdata/package-lock.v1.json",
		"../../testdata/package-lock.v2.json",
		"../../testdata/package-lock.v3.json",
	} {
		deps, err := NewPackageLockScanner().ScanDirect(context.Background(), f)
		if err != nil {
			t.Fatalf("ScanDirect(%s): %v", f, err)
		}

		if got := depSet(deps); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v want %v", f, got, want)
		}
	}
}

func TestPackageLockDetect(t *testing.T) {
	s := NewPackageLockScanner()

	// Filename match, including a Windows-style path.
	for _, p := range []string{
		"package-lock.json", "npm-shrinkwrap.json",
		`C:\proj\Package-Lock.JSON`, "a/b/package-lock.json",
	} {
		if !s.Detect(p) {
			t.Errorf("Detect(%q) = false, want true", p)
		}
	}

	// A non-lockfile name and a non-existent .json must not match.
	if s.Detect("pnpm-lock.yaml") {
		t.Error("Detect(pnpm-lock.yaml) = true, want false")
	}
	if s.Detect("nope.json") {
		t.Error("Detect(missing .json) = true, want false")
	}

	// A differently named .json is accepted only if its content declares a
	// lockfileVersion (npm allows renamed lockfiles).
	dir := t.TempDir()
	withVer := filepath.Join(dir, "renamed.json")
	os.WriteFile(withVer, []byte(`{"lockfileVersion": 3, "packages": {}}`), 0o644)
	if !s.Detect(withVer) {
		t.Error("Detect(json with lockfileVersion) = false, want true")
	}

	without := filepath.Join(dir, "tsconfig.json")
	os.WriteFile(without, []byte(`{"compilerOptions": {}}`), 0o644)
	if s.Detect(without) {
		t.Error("Detect(unrelated json) = true, want false")
	}
}

func TestPackageLockScannerErrors(t *testing.T) {
	s := NewPackageLockScanner()

	if s.Ecosystem() != model.EcosystemNPM {
		t.Errorf("Ecosystem() = %q, want %q", s.Ecosystem(), model.EcosystemNPM)
	}

	// Missing file: both entry points surface the read error.
	if _, err := s.Scan(context.Background(), "../../testdata/does-not-exist.json"); err == nil {
		t.Error("Scan(missing) = nil error, want error")
	}
	if _, err := s.ScanDirect(context.Background(), "../../testdata/does-not-exist.json"); err == nil {
		t.Error("ScanDirect(missing) = nil error, want error")
	}

	// Malformed JSON: parse error is wrapped with the path.
	bad := filepath.Join(t.TempDir(), "package-lock.json")
	os.WriteFile(bad, []byte("{not json"), 0o644)
	if _, err := s.Scan(context.Background(), bad); err == nil {
		t.Error("Scan(malformed) = nil error, want error")
	}
	if _, err := s.ScanDirect(context.Background(), bad); err == nil {
		t.Error("ScanDirect(malformed) = nil error, want error")
	}
}

func TestPackageLockName(t *testing.T) {
	cases := []struct{ key, name string }{
		{"node_modules/lodash", "lodash"},
		{"node_modules/@babel/core", "@babel/core"},
		{"node_modules/lodash/node_modules/react-dom", "react-dom"},
		{"node_modules/a/node_modules/@scope/b", "@scope/b"},
		{"", ""},
		{"../local", ""},
		{"packages/app", ""},
	}
	for _, c := range cases {
		if got := packageLockName(c.key); got != c.name {
			t.Errorf("packageLockName(%q) = %q, want %q", c.key, got, c.name)
		}
	}
}
