package license

import "testing"

func TestIsLicenseFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
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
