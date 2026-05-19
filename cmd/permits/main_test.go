package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// binPath builds the CLI once for the test binary's lifetime.
var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "permits-cli")
	if err != nil {
		panic(err)
	}

	binPath = filepath.Join(dir, "permits")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build failed: " + string(out))
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()

	cmd := exec.Command(binPath, args...)
	out, err := cmd.CombinedOutput()

	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}

	return string(out), code
}

func TestCLINoArgsExits2(t *testing.T) {
	out, code := runCLI(t)

	if code != 2 {
		t.Fatalf("exit = %d, want 2; output:\n%s", code, out)
	}
	if !strings.Contains(out, "at least one -pnpm-lock or -go-sum is required") {
		t.Errorf("missing usage message:\n%s", out)
	}
}

// writeLockProject creates a pnpm project whose only dependency is resolvable
// from a local pnpm virtual store, so no network is needed.
func writeLockProject(t *testing.T) string {
	t.Helper()

	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "pnpm-lock.yaml"), []byte(
		"lockfileVersion: '9.0'\npackages:\n  demo@1.0.0:\n    resolution: {integrity: sha512-x}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgDir := filepath.Join(proj, "node_modules", ".pnpm", "demo@1.0.0", "node_modules", "demo")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version":"1.0.0","license":"MIT"}`), 0o644)
	os.WriteFile(filepath.Join(pkgDir, "LICENSE"), []byte("MIT-ish demo license"), 0o644)

	return proj
}

func TestCLIResolvesFromSiblingNodeModules(t *testing.T) {
	proj := writeLockProject(t)
	outDir := filepath.Join(proj, "out")

	// Dead registry proves the sibling node_modules was used (no network).
	out, code := runCLI(t,
		"-pnpm-lock", filepath.Join(proj, "pnpm-lock.yaml"),
		"-npm-registry", "http://127.0.0.1:9",
		"-out", outDir,
		"-timeout", "3s",
	)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, out)
	}

	md, err := os.ReadFile(filepath.Join(outDir, "npm", "demo", "1.0.0", "LICENSE.md"))
	if err != nil {
		t.Fatalf("license md not written: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "source: npm-node-modules") || !strings.Contains(s, "MIT-ish demo license") {
		t.Errorf("unexpected license output:\n%s", s)
	}

	if _, err := os.Stat(filepath.Join(outDir, "summary.json")); err != nil {
		t.Errorf("summary.json not written: %v", err)
	}
}

func TestCLIFailureExits1(t *testing.T) {
	proj := t.TempDir()

	// Lockfile references a package that is NOT in node_modules, and the
	// registry is unreachable -> the dependency fails -> exit 1.
	os.WriteFile(filepath.Join(proj, "pnpm-lock.yaml"), []byte(
		"lockfileVersion: '9.0'\npackages:\n  missing-pkg@9.9.9:\n    resolution: {integrity: sha512-x}\n",
	), 0o644)

	out, code := runCLI(t,
		"-pnpm-lock", filepath.Join(proj, "pnpm-lock.yaml"),
		"-npm-registry", "http://127.0.0.1:9",
		"-out", filepath.Join(proj, "out"),
		"-timeout", "2s",
	)

	if code != 1 {
		t.Fatalf("exit = %d, want 1; output:\n%s", code, out)
	}
	if !strings.Contains(out, "1 failed") {
		t.Errorf("expected failure summary line:\n%s", out)
	}
}
