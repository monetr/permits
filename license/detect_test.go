package license

import (
	"strings"
	"testing"
)

const mitText = `MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.`

func TestDetect(t *testing.T) {
	// The happy path: a verbatim MIT license must classify as exactly MIT.
	got := Detect(mitText)
	if len(got) != 1 || got[0] != "MIT" {
		t.Fatalf("Detect(MIT) = %v, want [MIT]", got)
	}

	// Empty input is not a license, so detection must come back empty rather than
	// guessing.
	if got := Detect(""); len(got) != 0 {
		t.Errorf("Detect(\"\") = %v, want []", got)
	}

	// Arbitrary prose must not be mistaken for a license; a false positive here
	// would attach a bogus SPDX id to a dependency.
	if got := Detect("this is not a license at all, just prose"); len(got) != 0 {
		t.Errorf("Detect(non-license) = %v, want []", got)
	}
}

func TestDetectDualLicense(t *testing.T) {
	// A single file containing both MIT and Apache-2.0 (the yaml.v3 shape). Both
	// identifiers must be reported because suppressing either one would
	// misrepresent the terms the dependency is actually offered under.
	dual := mitText + "\n\n" + apacheText

	got := Detect(dual)
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "MIT") || !strings.Contains(joined, "Apache-2.0") {
		t.Fatalf("Detect(dual) = %v, want both MIT and Apache-2.0", got)
	}
}

// eq reports whether two string slices are equal element for element. The
// detection results are order sensitive, so this deliberately does not sort
// before comparing.
func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestDetectFairSource(t *testing.T) {
	// FSL-1.1-MIT embeds the full MIT text as its "future license"; FSL must win
	// and MIT must be suppressed (the current grant is FSL, not MIT). Reporting
	// MIT here would let a consumer believe they had MIT rights today, which is
	// exactly the misread the fair-source detection exists to prevent.
	fslMIT := "Functional Source License, Version 1.1, MIT Future License\n\n" +
		"## Abbreviation\n\nFSL-1.1-MIT\n\n## Notice\n\nCopyright 2024 monetr\n\n" +
		"# MIT Future License\n\n" + mitText
	if got := Detect(fslMIT); !eq(got, []string{"FSL-1.1-MIT"}) {
		t.Errorf("Detect(FSL-1.1-MIT) = %v, want [FSL-1.1-MIT] (MIT must be suppressed)", got)
	}

	// The Apache-future variant is the same idea: the embedded Apache text must
	// not leak through past the FSL grant.
	fslAL := "Functional Source License, Version 1.1, Apache 2.0 Future License\n\nFSL-1.1-ALv2\n" + apacheText
	if got := Detect(fslAL); !eq(got, []string{"FSL-1.1-ALv2"}) {
		t.Errorf("Detect(FSL-1.1-ALv2) = %v, want [FSL-1.1-ALv2]", got)
	}

	// BUSL is recognized from its title plus the Licensor/Licensed Work header
	// that every Business Source License carries.
	busl := "Business Source License 1.1\n\nLicensor: monetr LLC\nLicensed Work: monetr\n"
	if got := Detect(busl); !eq(got, []string{"BUSL-1.1"}) {
		t.Errorf("Detect(BUSL) = %v, want [BUSL-1.1]", got)
	}

	// The Elastic License is likewise keyed off its title and acceptance clause.
	elastic := "Elastic License 2.0\n\nAcceptance ... By using the software, you agree ...\n"
	if got := Detect(elastic); !eq(got, []string{"Elastic-2.0"}) {
		t.Errorf("Detect(Elastic) = %v, want [Elastic-2.0]", got)
	}

	// Regression: a plain MIT (no FSL markers) must still classify as MIT. The
	// suppression above must only fire when the fair-source markers are present.
	if got := Detect(mitText); !eq(got, []string{"MIT"}) {
		t.Errorf("Detect(plain MIT) = %v, want [MIT]", got)
	}
}

const apacheText = `Apache License
Version 2.0, January 2004
http://www.apache.org/licenses/

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.`
