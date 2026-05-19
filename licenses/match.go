// Package licenses provides ecosystem-agnostic detection of license files by filename. It is
// intentionally conservative: it matches the conventional license/notice filenames and ignores
// source files that merely contain the word "license" (e.g. license-checker.js).
package licenses

import (
	"path"
	"regexp"
	"strings"
)

// baseRe matches a license-ish base name: LICENSE/LICENCE, COPYING, COPYRIGHT,
// UNLICENSE, NOTICE, optionally suffixed (e.g. LICENSE-MIT, LICENSE.APACHE).
var baseRe = regexp.MustCompile(`^(licen[sc]e|copying|copyright|unlicense|notice)([._-].+)?$`)

// allowedExt are the extensions a license file may carry. An empty extension
// (no dot) is also allowed.
var allowedExt = map[string]struct{}{
	"":     {},
	".md":  {},
	".txt": {},
	".rst": {},
}

// IsLicenseFile reports whether the given path's base name looks like a license or notice file.
func IsLicenseFile(p string) bool {
	base := strings.ToLower(path.Base(filepathToSlash(p)))
	if base == "" {
		return false
	}

	ext := path.Ext(base)
	if _, ok := allowedExt[ext]; !ok {
		return false
	}

	stem := strings.TrimSuffix(base, ext)

	return baseRe.MatchString(stem)
}

// filepathToSlash normalizes both OS and archive separators to '/'.
func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, `\`, "/")
}
