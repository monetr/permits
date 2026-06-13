package licenses

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Link forms that appear in license/readme prose. This is deliberately not a markdown parser:
// narrow patterns over prose-style documents keep the rewrite predictable, and anything they fail
// to recognize is neutralized by the escaping pass instead of being trusted.
var (
	// [text](dest), ![alt](dest), with optional <dest> brackets and a "title"/'title'. The text
	// may contain one level of nested brackets, so badge links ([![alt](img)](dest)) and
	// already-defanged hosts ("x[dot]com") still match.
	inlineLinkRe = regexp.MustCompile(`!?\[((?:[^\[\]]|\[[^\[\]]*\])*)\]\(\s*<?([^()<>\s]*)>?(?:\s+"[^"]*"|\s+'[^']*')?\s*\)`)
	// Reference-style usages [text][label] and definition lines "[label]: dest".
	refLinkRe = regexp.MustCompile(`\[((?:[^\[\]]|\[[^\[\]]*\])+)\]\[[^\[\]]*\]`)
	refDefRe  = regexp.MustCompile(`(?m)^[ \t]{0,3}\[([^\[\]]+)\]:[ \t]*<?([^<>\s]+)>?(?:[ \t]+"[^"]*"|[ \t]+'[^']*')?[ \t]*$`)
	// <https://example.com/x>, <mailto:user@example.com>, and <user@example.com> autolinks.
	angleLinkRe = regexp.MustCompile(`<((?i:https?)://[^<>\s]+|(?i:mailto:)?[A-Za-z0-9._%+-]+@(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,})>`)
	// Bare URLs, emails, and www hosts, which most renderers auto-link. The email domain must end
	// in an alphabetic label so version specs like "lodash@4.17.21" are not mistaken for one.
	bareURLRe   = regexp.MustCompile(`\b(?i:https?)://[^\s<>"'\)\]]+`)
	bareEmailRe = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}\b`)
	bareWWWRe   = regexp.MustCompile(`(?i)\bwww\.[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+(?:/[^\s<>"'\)\]]*)?`)
)

// StripOptions configures [StripLinks].
type StripOptions struct {
	// Repository is the project URL relative link destinations resolve against
	// ("[guide](/x)" in a project at "https://github.com/acme/proj" becomes
	// "guide (github[dot]com/x)"). Empty means no base: a relative-only link
	// collapses to its bare path ("[foo](/bar)" becomes "/bar").
	Repository string
	// TrustedHosts are hosts, subdomains included, whose http(s) links and bare
	// URLs stay live instead of being censored, e.g. "github.com". Matching
	// uses the parsed hostname, so lookalikes ("github.com.evil.com") and
	// userinfo tricks ("https://github.com@evil.com") are not trusted. Trust
	// never extends to images, which render without a click, nor to
	// reference-style links, whose label indirection is not worth trusting.
	TrustedHosts []string
	// StripHTML removes HTML/JSX elements from the text entirely instead of
	// escaping them to visible plain text: tags are deleted (their inner text
	// stays), comments and script/style blocks vanish with their contents.
	// Fenced code blocks are not touched either way.
	StripHTML bool
}

// StripLinks rewrites a document into a form that is inert when rendered as markdown, MDX, or
// HTML. Markdown link syntax ([text](dest), [text][ref], <autolink>) is unwrapped, bare URLs, www
// hosts, and email addresses are censored in place by defanging the host ("https://github.com/foo"
// becomes "github[dot]com/foo"), and whatever markup survives that, raw HTML/JSX tags, MDX
// expressions and ESM, or unparseable link forms, is escaped (or, with
// [StripOptions.StripHTML], removed). Links to [StripOptions.TrustedHosts] are the exception and
// stay live. The rewrite is idempotent: re-running it with the same options is a no-op.
func StripLinks(text string, opts StripOptions) string {
	base := parseRepo(opts.Repository)

	// NUL delimits the placeholders that carry trusted links past the escaping pass, so real NUL
	// bytes (never legitimate in license text) must go first or they could forge one.
	s := strings.ReplaceAll(text, "\x00", "")

	var kept []string
	keep := func(link string) string {
		kept = append(kept, link)
		return "\x00" + strconv.Itoa(len(kept)-1) + "\x00"
	}

	// Nested constructs ([![alt](img)](dest)) are handled by recursing on the text capture, which
	// the regex cannot descend into on its own. Depth is bounded; anything deeper is left for the
	// escaping pass to neutralize.
	var rewriteInline func(in string, depth int) string
	rewriteInline = func(in string, depth int) string {
		if depth > 4 {
			return in
		}
		return inlineLinkRe.ReplaceAllStringFunc(in, func(m string) string {
			parts := inlineLinkRe.FindStringSubmatch(m)
			txt := rewriteInline(parts[1], depth+1)
			if !strings.HasPrefix(m, "!") {
				if u := trustedURL(parts[2], opts.TrustedHosts); u != "" {
					return keep("[" + escapeLinkText(txt) + "](" + u + ")")
				}
			}
			disp, external := destDisplay(parts[2], base)
			return unwrapLink(txt, disp, external)
		})
	}
	s = rewriteInline(s, 0)

	s = refLinkRe.ReplaceAllString(s, "$1")
	s = refDefRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := refDefRe.FindStringSubmatch(m)
		// "label: host[dot]tld/path" keeps the lookup table readable without being a definition
		// any more, which in turn demotes any usage this pass missed to literal text.
		if disp, _ := destDisplay(parts[2], base); disp != "" {
			return parts[1] + ": " + disp
		}
		return parts[1] + ":"
	})

	s = angleLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.Trim(m, "<>")
		if strings.Contains(inner, "://") {
			if u := trustedURL(inner, opts.TrustedHosts); u != "" {
				return keep("<" + u + ">")
			}
			return defangAbs(inner)
		}
		if len(inner) > 7 && strings.EqualFold(inner[:7], "mailto:") {
			inner = inner[7:]
		}
		return defangEmail(inner)
	})

	s = bareURLRe.ReplaceAllStringFunc(s, func(m string) string {
		core, tail := trimTrail(m)
		if trustedURL(core, opts.TrustedHosts) != "" {
			return m // left as written; renderers may auto-link it, which is the point
		}
		return defangAbs(core) + tail
	})
	s = bareEmailRe.ReplaceAllStringFunc(s, defangEmail)
	s = bareWWWRe.ReplaceAllStringFunc(s, func(m string) string {
		core, tail := trimTrail(m)
		if trustedURL("https://"+core, opts.TrustedHosts) != "" {
			return m
		}
		return defangAbs(core) + tail
	})

	s = walkOutsideFences(s, func(seg string) string {
		if opts.StripHTML {
			seg = removeHTML(seg)
		}
		return escapeSegment(seg)
	})

	// Reverse order: a trusted link nested inside another trusted link's text was tokenized
	// first, so its placeholder lives inside a later entry.
	for i := len(kept) - 1; i >= 0; i-- {
		s = strings.Replace(s, "\x00"+strconv.Itoa(i)+"\x00", kept[i], 1)
	}

	return s
}

// trustedURL returns the canonical form of dest when it is an http(s) URL whose host is covered
// by trusted, and "" otherwise. Parentheses and angle brackets are %-encoded so the URL can never
// break out of the markdown syntax it is re-embedded in.
func trustedURL(dest string, trusted []string) string {
	if len(trusted) == 0 {
		return ""
	}

	u, err := url.Parse(strings.TrimSpace(dest))
	if err != nil {
		return ""
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "":
		u.Scheme = "https" // protocol-relative //host/path
	default:
		return ""
	}
	if u.Host == "" || !hostTrusted(u.Hostname(), trusted) {
		return ""
	}

	return urlBreakoutEscaper.Replace(u.String())
}

func hostTrusted(host string, trusted []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, t := range trusted {
		t = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(t), "."))
		if t != "" && (host == t || strings.HasSuffix(host, "."+t)) {
			return true
		}
	}

	return false
}

var urlBreakoutEscaper = strings.NewReplacer("(", "%28", ")", "%29", "<", "%3C", ">", "%3E")

// escapeLinkText escapes unescaped brackets inside a kept link's text, so attacker text like
// "a[x](b" cannot close the link early and shift its destination somewhere untrusted, and evens
// out a trailing backslash run that would otherwise escape the closing bracket. The escaping is
// parity-aware, which keeps it idempotent across runs.
func escapeLinkText(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 8)

	backslashes := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if (ch == '[' || ch == ']') && backslashes%2 == 0 {
			b.WriteByte('\\')
		}

		if ch == '\\' {
			backslashes++
		} else {
			backslashes = 0
		}
		b.WriteByte(ch)
	}
	if backslashes%2 == 1 {
		b.WriteByte('\\')
	}

	return b.String()
}

// unwrapLink renders a stripped link from its text and the destination's display form, collapsing
// the two when they say the same thing so "[github.com/x](https://github.com/x)" does not come out
// as "github[dot]com/x (github[dot]com/x)". A non-external display is an unresolvable bare path,
// which replaces the link outright.
func unwrapLink(text, disp string, external bool) string {
	text = strings.TrimSpace(text)
	switch {
	case disp == "":
		return text
	case !external, text == "", sameTarget(text, disp):
		return disp
	default:
		return text + " (" + disp + ")"
	}
}

func sameTarget(text, disp string) bool {
	norm := func(s string) string {
		if i := strings.Index(s, "://"); i >= 0 {
			s = s[i+3:]
		}
		s = strings.ReplaceAll(s, "[dot]", ".")
		return strings.TrimSuffix(s, "/")
	}

	return strings.EqualFold(norm(text), norm(disp))
}

// destDisplay turns a link destination into its display form. external reports whether the
// destination points somewhere real (an absolute or repo-resolved URL, an email); a relative path
// with no repo to resolve against comes back verbatim but not external, since it is only
// meaningful as a path. An empty display means there is nothing worth keeping (fragments, opaque
// schemes).
func destDisplay(dest string, base *url.URL) (disp string, external bool) {
	dest = strings.TrimSpace(dest)
	if dest == "" || strings.HasPrefix(dest, "#") {
		return "", false
	}

	if len(dest) > 7 && strings.EqualFold(dest[:7], "mailto:") {
		return defangEmail(dest[7:]), true
	}

	u, err := url.Parse(dest)
	if err != nil {
		return "", false
	}

	switch {
	case u.Host == "" && u.Scheme != "":
		// Opaque destinations (data:, tel:) carry nothing readable.
		return "", false
	case u.Host == "":
		if base == nil {
			return dest, false
		}
		u = base.ResolveReference(u)
	case u.Scheme == "":
		u.Scheme = "https" // protocol-relative //host/path
	}

	return defangURL(u), true
}

// parseRepo prepares the repository URL as a resolution base, or nil when unusable.
func parseRepo(repo string) *url.URL {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil
	}

	if !strings.Contains(repo, "://") {
		repo = "https://" + repo
	}

	u, err := url.Parse(repo)
	if err != nil || u.Host == "" {
		return nil
	}

	// A trailing slash makes plain-relative destinations resolve under the project instead of
	// replacing its last path segment. Root-relative ones (/foo) replace the path either way.
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}

	return u
}

func defangURL(u *url.URL) string {
	var b strings.Builder
	b.WriteString(strings.ReplaceAll(u.Host, ".", "[dot]"))
	b.WriteString(u.EscapedPath())
	if u.RawQuery != "" {
		b.WriteString("?" + u.RawQuery)
	}
	if u.Fragment != "" {
		b.WriteString("#" + u.Fragment)
	}

	return b.String()
}

func defangAbs(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return defangURL(u)
	}

	// Schemeless (www.foo.com/x) or unparseable: defang everything up to the first slash.
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	host, rest, found := strings.Cut(s, "/")
	host = strings.ReplaceAll(host, ".", "[dot]")
	if found {
		host += "/" + rest
	}

	return host
}

// trimTrail splits a bare match from the sentence punctuation the regex swallowed, so "see
// http://x.com/y." keeps its full stop.
func trimTrail(m string) (core, tail string) {
	core = strings.TrimRight(m, ".,;:!?")

	return core, m[len(core):]
}

// defangEmail censors only the domain; the local part has no effect on auto-linking.
func defangEmail(addr string) string {
	local, domain, ok := strings.Cut(addr, "@")
	if !ok {
		return strings.ReplaceAll(addr, ".", "[dot]")
	}

	return local + "@" + strings.ReplaceAll(domain, ".", "[dot]")
}

// HTML/JSX element forms removed by [StripOptions.StripHTML]. Script and style lose their
// contents too; for every other element only the tags go, keeping inner text. Anything these miss
// (an unterminated tag, a comment without its close) is still escaped by escapeSegment.
var (
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlScriptRe  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</\s*script\s*>`)
	htmlStyleRe   = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</\s*style\s*>`)
	htmlMiscRe    = regexp.MustCompile(`(?s)<![^>]*>|<\?[^>]*>`)
	// A tag name is required and attributes must follow whitespace, so autolink leftovers like
	// "<https://x>" (":" cannot continue a tag) are not eaten.
	htmlTagRe = regexp.MustCompile(`(?s)</?[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*)?/?>`)
)

func removeHTML(s string) string {
	s = htmlCommentRe.ReplaceAllString(s, "")
	s = htmlScriptRe.ReplaceAllString(s, "")
	s = htmlStyleRe.ReplaceAllString(s, "")
	s = htmlMiscRe.ReplaceAllString(s, "")

	return htmlTagRe.ReplaceAllString(s, "")
}

// esmLineRe finds a line-leading import/export keyword, which MDX would otherwise parse (and
// execute) as an ESM statement.
var esmLineRe = regexp.MustCompile(`(?m)^ {0,3}(import|export)\b`)

// escapeSegment escapes the syntax that stays active after the link rewriting, so a document
// rendered as markdown, or as MDX where raw tags are JSX and braces are evaluated JavaScript,
// cannot smuggle in a link, an image, or code:
//
//   - "<" stops raw HTML/JSX tags, "{"/"}" stop MDX expressions. Backslash escapes render as the
//     bare character in both CommonMark and MDX.
//   - "](" stops any inline-link form the rewrites did not recognize, such as destinations with
//     parentheses ("[x](javascript:alert(1))").
//   - A line-leading import/export keyword gets its first letter HTML-entity-escaped, which
//     renders identically but can never start an MDX ESM block.
func escapeSegment(seg string) string {
	seg = esmLineRe.ReplaceAllStringFunc(seg, func(m string) string {
		kw := len(m) - len("import") // "import" and "export" are the same length
		return m[:kw] + "&#" + strconv.Itoa(int(m[kw])) + ";" + m[kw+1:]
	})

	var b strings.Builder
	b.Grow(len(seg) + 64)

	backslashes := 0 // length of the backslash run before the current character
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		switch {
		case ch == '<' || ch == '{' || ch == '}':
			// An odd run means the character is already escaped; escaping again would instead
			// escape the backslash and re-activate it.
			if backslashes%2 == 0 {
				b.WriteByte('\\')
			}
		case ch == '(' && i > 0 && seg[i-1] == ']':
			b.WriteByte('\\')
		}

		if ch == '\\' {
			backslashes++
		} else {
			backslashes = 0
		}
		b.WriteByte(ch)
	}

	return b.String()
}

// walkOutsideFences applies f to the runs of text between fenced code blocks and returns the
// reassembled document. Fenced blocks pass through untouched: they are inert in markdown and MDX
// alike, and rewrites inside them would display verbatim. The fence tracking errs toward
// processing, only an unambiguous CommonMark fence is skipped.
func walkOutsideFences(s string, f func(string) string) string {
	var b, seg strings.Builder
	flush := func() {
		if seg.Len() > 0 {
			b.WriteString(f(seg.String()))
			seg.Reset()
		}
	}

	fence := "" // the opening marker of the fenced block we are inside
	for line := range strings.Lines(s) {
		switch {
		case fence != "":
			b.WriteString(line)
			if fenceCloses(fence, line) {
				fence = ""
			}
		default:
			if open := fenceOpen(line); open != "" {
				flush()
				fence = open
				b.WriteString(line)
				continue
			}
			seg.WriteString(line)
		}
	}
	flush()

	return b.String()
}

// fenceOpen reports the fence marker ("```", "~~~~", ...) when line opens a CommonMark fenced
// code block, or "" otherwise. Anything ambiguous is not a fence: skipping rewrites inside a
// region the renderer does not treat as code would leave it active.
func fenceOpen(line string) string {
	t := strings.TrimRight(line, "\r\n")

	indent := 0
	for indent < len(t) && indent < 4 && t[indent] == ' ' {
		indent++
	}
	if indent > 3 {
		return ""
	}

	t = t[indent:]
	if len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return ""
	}

	c := t[0]
	n := 0
	for n < len(t) && t[n] == c {
		n++
	}
	if n < 3 {
		return ""
	}

	// CommonMark: an info string on a backtick fence cannot contain backticks (that would be an
	// inline code span instead).
	if c == '`' && strings.IndexByte(t[n:], '`') >= 0 {
		return ""
	}

	return strings.Repeat(string(c), n)
}

// fenceCloses reports whether line closes the fence opened by open. It accepts a superset of
// CommonMark's closing rule (any indentation), which only ever ends the skipped region early and
// processes more, never less.
func fenceCloses(open, line string) bool {
	t := strings.TrimSpace(line)
	if len(t) < len(open) {
		return false
	}

	for i := 0; i < len(t); i++ {
		if t[i] != open[0] {
			return false
		}
	}

	return true
}
