package license

import "testing"

func TestIsLicenseFile(t *testing.T) {
	// IsLicenseFile is the gate that decides whether an archive entry is worth
	// reading as a license. It has to recognize the usual license/notice file
	// names regardless of casing, directory prefix, common text extensions, or a
	// Windows-style path separator. Just as importantly it must reject source
	// files that merely happen to have "license" in the name, otherwise we would
	// classify tooling like license-checker.js as a real license.
	cases := []struct {
		path string
		want bool
	}{
		// Names we expect to be treated as license/notice files.
		{"LICENSE", true},
		{"package/LICENSE", true},
		{"LICENSE.md", true},
		{"LICENSE.txt", true},
		{"license", true},
		{"LICENCE", true},
		{"COPYING", true},
		{"COPYRIGHT", true},
		{"UNLICENSE", true},
		{"NOTICE", true},
		{"NOTICE.md", true},
		{"LICENSE-MIT", true},
		{"LICENSE-APACHE", true},
		{"LICENSE_BSD", true},
		{"license.rst", true},
		{`pkg\LICENSE`, true},

		// Names that look license-ish but are really source or docs, so they must
		// be rejected.
		{"license-checker.js", false},
		{"LICENSE.go", false},
		{"licenseUtil.ts", false},
		{"README.md", false},
		{"src/main.go", false},
		{"", false},
		{"licenser", false},
	}

	for _, c := range cases {
		if got := IsLicenseFile(c.path); got != c.want {
			t.Errorf("IsLicenseFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
