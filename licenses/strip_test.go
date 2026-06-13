package licenses

import "testing"

func TestStripLinks(t *testing.T) {
	const repo = "https://github.com/acme/proj"

	cases := []struct {
		name, in, repo, want string
		trusted              []string
		stripHTML            bool
	}{
		{
			name: "inline absolute link",
			in:   "See the [MIT License](https://opensource.org/licenses/MIT).",
			want: "See the MIT License (opensource[dot]org/licenses/MIT).",
		},
		{
			name: "root-relative link resolves against the repository host",
			in:   "Read [the guide](/foo).",
			repo: repo,
			want: "Read the guide (github[dot]com/foo).",
		},
		{
			name: "plain-relative link resolves under the repository",
			in:   "See [CHANGELOG](CHANGELOG.md).",
			repo: repo,
			want: "See CHANGELOG (github[dot]com/acme/proj/CHANGELOG.md).",
		},
		{
			name: "relative link without a repository collapses to the bare path",
			in:   "Read [the guide](/foo).",
			want: "Read /foo.",
		},
		{
			name: "non-rooted relative link without a repository keeps the path too",
			in:   "Read [the guide](docs/guide.md).",
			want: "Read docs/guide.md.",
		},
		{
			name: "fragment link keeps just the text",
			in:   "Jump to [definitions](#definitions).",
			want: "Jump to definitions.",
		},
		{
			name: "image",
			in:   "![logo](https://example.com/logo.png)",
			want: "logo (example[dot]com/logo.png)",
		},
		{
			name: "nested badge link",
			in:   "[![CI](https://ci.dev/badge.svg)](https://ci.dev/runs)",
			want: "CI (ci[dot]dev/badge.svg) (ci[dot]dev/runs)",
		},
		{
			name: "link with title",
			in:   `[docs](https://example.com/docs "The Docs")`,
			want: "docs (example[dot]com/docs)",
		},
		{
			name: "text matching the destination collapses",
			in:   "[github.com/acme/proj](https://github.com/acme/proj)",
			want: "github[dot]com/acme/proj",
		},
		{
			name: "mailto link",
			in:   "Questions? [Email legal](mailto:legal@acme.io).",
			want: "Questions? Email legal (legal@acme[dot]io).",
		},
		{
			name: "reference-style link and definition",
			in:   "Under the [MIT license][mit].\n\n[mit]: https://opensource.org/licenses/MIT\n",
			want: "Under the MIT license.\n\nmit: opensource[dot]org/licenses/MIT\n",
		},
		{
			name: "reference definition with relative destination",
			in:   "[coc]: /CODE_OF_CONDUCT.md\n",
			repo: repo,
			want: "coc: github[dot]com/CODE_OF_CONDUCT.md\n",
		},
		{
			name: "angle autolink",
			in:   "Obtain a copy at <https://www.apache.org/licenses/LICENSE-2.0>.",
			want: "Obtain a copy at www[dot]apache[dot]org/licenses/LICENSE-2.0.",
		},
		{
			name: "mailto autolink",
			in:   "<mailto:legal@acme.io>",
			want: "legal@acme[dot]io",
		},
		{
			name: "angle email keeps attribution readable",
			in:   "Copyright (c) Jane Doe <jane@example.com>",
			want: "Copyright (c) Jane Doe jane@example[dot]com",
		},
		{
			name: "bare url keeps trailing punctuation",
			in:   "obtain one at http://mozilla.org/MPL/2.0/.",
			want: "obtain one at mozilla[dot]org/MPL/2.0/.",
		},
		{
			name: "bare url with query and fragment",
			in:   "https://example.com/a?b=c#d",
			want: "example[dot]com/a?b=c#d",
		},
		{
			name: "bare www host",
			in:   "see www.apache.org/licenses for details",
			want: "see www[dot]apache[dot]org/licenses for details",
		},
		{
			name: "bare email",
			in:   "contact jane.doe@example.co.uk for details",
			want: "contact jane.doe@example[dot]co[dot]uk for details",
		},
		{
			name: "version specifiers are not emails",
			in:   "bundled from lodash@4.17.21",
			want: "bundled from lodash@4.17.21",
		},
		{
			name: "import paths and filenames untouched",
			in:   "The package golang.org/x/mod ships node.js bindings in index.test.js.",
			want: "The package golang.org/x/mod ships node.js bindings in index.test.js.",
		},
		{
			name: "plain license prose untouched",
			in:   "Permission is hereby granted, free of charge, to any person (the \"Software\").",
			want: "Permission is hereby granted, free of charge, to any person (the \"Software\").",
		},
		{
			name: "repository without scheme still resolves",
			in:   "[guide](/foo)",
			repo: "github.com/acme/proj",
			want: "guide (github[dot]com/foo)",
		},

		// Raw HTML, MDX, and unparseable link forms.
		{
			name: "raw img tag is escaped and its source defanged",
			in:   `<img src="https://evil.com/x.png">`,
			want: `\<img src="evil[dot]com/x.png">`,
		},
		{
			name: "raw anchor tag is escaped",
			in:   `Click <a href="https://evil.com">here</a>.`,
			want: `Click \<a href="evil[dot]com">here\</a>.`,
		},
		{
			name: "mdx expression braces are escaped",
			in:   "total {alert(1)} end",
			want: `total \{alert(1)\} end`,
		},
		{
			name: "license prose braces are escaped but render the same",
			in:   "{name license(s), version(s), and exceptions}",
			want: `\{name license(s), version(s), and exceptions\}`,
		},
		{
			name: "less-than in prose",
			in:   "valid for x < 10",
			want: `valid for x \< 10`,
		},
		{
			name: "line-leading import cannot become mdx esm",
			in:   `import Evil from "https://evil.com/mod.js"`,
			want: `&#105;mport Evil from "evil[dot]com/mod.js"`,
		},
		{
			name: "line-leading export cannot become mdx esm",
			in:   "export const x = 1",
			want: "&#101;xport const x = 1",
		},
		{
			name: "mid-sentence import is left alone",
			in:   "may not import or export the Software",
			want: "may not import or export the Software",
		},
		{
			name: "unrecognized link form is defused",
			in:   "[x](javascript:alert(1))",
			want: `[x]\(javascript:alert(1))`,
		},
		{
			name: "escaped backslash before a tag does not hide it",
			in:   `\\<img src=x>`,
			want: `\\\<img src=x>`,
		},
		{
			name: "fenced code is left verbatim",
			in:   "```\n<img src=x> {y}\n```\nafter <b>\n",
			want: "```\n<img src=x> {y}\n```\nafter \\<b>\n",
		},
		{
			name: "short fence does not close a longer one",
			in:   "````\n```\n<img>\n````\n<img>\n",
			want: "````\n```\n<img>\n````\n\\<img>\n",
		},
		{
			name: "backtick-fence with backtick info is not a fence",
			in:   "``` x ` y\n<img src=x>\n",
			want: "``` x ` y\n\\<img src=x>\n",
		},

		// Trusted hosts.
		{
			name:    "trusted inline link stays live",
			in:      "[the repo](https://github.com/acme/proj)",
			trusted: []string{"github.com"},
			want:    "[the repo](https://github.com/acme/proj)",
		},
		{
			name:    "trusted subdomain autolink stays live",
			in:      "<https://gist.github.com/abc>",
			trusted: []string{"github.com"},
			want:    "<https://gist.github.com/abc>",
		},
		{
			name:    "trusted bare url stays live with its punctuation",
			in:      "hosted at https://github.com/acme/proj.",
			trusted: []string{"github.com"},
			want:    "hosted at https://github.com/acme/proj.",
		},
		{
			name:    "untrusted links are still censored alongside trusted ones",
			in:      "[a](https://github.com/x) and [b](https://evil.com/y)",
			trusted: []string{"github.com"},
			want:    "[a](https://github.com/x) and b (evil[dot]com/y)",
		},
		{
			name:    "trusted image is still stripped",
			in:      "![logo](https://github.com/logo.png)",
			trusted: []string{"github.com"},
			want:    "logo (github[dot]com/logo.png)",
		},
		{
			name:    "lookalike host is not trusted",
			in:      "https://github.com.evil.com/x",
			trusted: []string{"github.com"},
			want:    "github[dot]com[dot]evil[dot]com/x",
		},
		{
			name:    "userinfo cannot spoof a trusted host",
			in:      "[x](https://github.com@evil.com/p)",
			trusted: []string{"github.com"},
			want:    "x (evil[dot]com/p)",
		},
		{
			name:    "brackets in trusted link text cannot shift the destination",
			in:      "[a[x](b](https://github.com/y)",
			trusted: []string{"github.com"},
			want:    `[a\[x\](b](https://github.com/y)`,
		},

		// StripHTML mode.
		{
			name:      "strip-html removes an img element",
			in:        `before <img src="https://evil.com/x.png"> after`,
			stripHTML: true,
			want:      "before  after",
		},
		{
			name:      "strip-html removes anchor tags but keeps their text",
			in:        `Click <a href="https://evil.com">here</a>.`,
			stripHTML: true,
			want:      "Click here.",
		},
		{
			name:      "strip-html removes script elements with their contents",
			in:        `a <script>fetch("https://evil.com")</script> b`,
			stripHTML: true,
			want:      "a  b",
		},
		{
			name:      "strip-html removes comments",
			in:        "x <!-- hidden --> y",
			stripHTML: true,
			want:      "x  y",
		},
		{
			name:      "strip-html leaves fenced code alone",
			in:        "```\n<b>code</b>\n```\n",
			stripHTML: true,
			want:      "```\n<b>code</b>\n```\n",
		},
		{
			name:      "strip-html escapes an unterminated tag",
			in:        `dangling <img src="x`,
			stripHTML: true,
			want:      `dangling \<img src="x`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := StripOptions{Repository: c.repo, TrustedHosts: c.trusted, StripHTML: c.stripHTML}

			got := StripLinks(c.in, opts)
			if got != c.want {
				t.Errorf("StripLinks(%q, %+v)\n got: %q\nwant: %q", c.in, opts, got, c.want)
			}

			// Stripping must be idempotent so re-processing already-censored text is harmless.
			if again := StripLinks(got, opts); again != got {
				t.Errorf("not idempotent:\nonce:  %q\ntwice: %q", got, again)
			}
		})
	}
}
