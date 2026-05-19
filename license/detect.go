package license

import (
	"sort"
	"strings"

	"github.com/google/licensecheck"
)

// Detect classifies raw license text and returns the distinct SPDX license identifiers it
// recognizes, sorted. A single file may yield multiple IDs (e.g. a dual MIT/Apache-2.0 LICENSE). It
// returns an empty slice when nothing is recognized; callers should treat that as "unknown", not
// "unlicensed".
//
// The "fair source" family (FSL, BUSL, Elastic) is detected first and is authoritative for the
// file: google/licensecheck does not know these, and its partial matches on the embedded *future*
// license (e.g. the MIT text inside FSL-1.1-MIT) would otherwise mislabel the current grant.
func Detect(text string) []string {
	if text == "" {
		return []string{}
	}

	// The fair-source family is checked first and short-circuits the rest of detection. These
	// licenses embed the text of a *future* permissive license, so letting licensecheck run would
	// produce partial matches against that embedded grant and mislabel the file.
	if fs := fairSource(text); len(fs) > 0 {
		return fs
	}

	// Hand the text to google/licensecheck and collect every distinct, non-empty SPDX identifier it
	// reports. A single file can legitimately yield more than one (e.g. a dual-licensed LICENSE).
	cov := licensecheck.Scan([]byte(text))
	set := make(map[string]struct{}, len(cov.Match))
	for _, m := range cov.Match {
		if m.ID != "" {
			set[m.ID] = struct{}{}
		}
	}

	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)

	return out
}

// fairSource recognizes the "fair source" / source-available license family that licensecheck's
// builtin set lacks. Detection is anchored on these licenses' stable title lines and self-declared
// abbreviations, matched against whitespace-normalized text so wrapping does not matter.
func fairSource(text string) []string {
	// Collapse all runs of whitespace into single spaces and lower-case the result so that the
	// substring checks below are immune to line wrapping and capitalization differences.
	n := strings.ToLower(strings.Join(strings.Fields(text), " "))
	set := map[string]struct{}{}

	// The Functional Source License names the permissive license it converts into in a "<X> Future
	// License" line. We anchor on either the self-declared abbreviation or the stable title line
	// paired with that future-license phrase.
	hasFSLTitle := strings.Contains(n, "functional source license")
	switch {
	case strings.Contains(n, "fsl-1.1-mit"),
		hasFSLTitle && strings.Contains(n, "mit future license"):
		set["FSL-1.1-MIT"] = struct{}{}
	case strings.Contains(n, "fsl-1.1-alv2"),
		hasFSLTitle && strings.Contains(n, "apache 2.0 future license"):
		set["FSL-1.1-ALv2"] = struct{}{}
	}

	// The Business Source License and the Elastic License are independent of the FSL above, so they
	// are matched separately and may co-occur with it in unusual multi-license files.
	if strings.Contains(n, "business source license 1.1") {
		set["BUSL-1.1"] = struct{}{}
	}
	if strings.Contains(n, "elastic license 2.0") {
		set["Elastic-2.0"] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)

	return out
}
