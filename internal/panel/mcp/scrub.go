package mcp

import "regexp"

// scrub is the last wall: every string an agent can see is run through it, after the projection has
// chosen what to show. It catches what a projection forgot, or what an attacker put into a name or a note to be carried out
// in a result: panel secrets, share links, key material and anything shaped like a credential in a URL.
//
// It runs twice: on each piece of text from data as it enters a view (clean), where a blank ends a word, and once more on
// the serialised result, where a quote or a backslash does. The two differ only in the characters that end a match.

type scrubber struct {
	re   *regexp.Regexp
	repl string
}

func scrubbers(stop string) []scrubber {
	// stop is a character class body of the characters that end a link or a URL; blanks always do
	return []scrubber{
		{regexp.MustCompile(`tk1_[A-Za-z0-9_-]{16,}`), `[token]`},
		{regexp.MustCompile(`cf_[A-Za-z0-9_-]{16,}`), `[confirm]`},
		// share links and tunnel configs
		{regexp.MustCompile(`(?i)\b(?:vless|vmess|trojan|ss|ssr|hysteria2|hysteria|hy2|tuic|wireguard|awg|socks5?)://[^\s` + stop + `]+`), `[link]`},
		{regexp.MustCompile(`(?i)\[(?:Interface|Peer)\]`), `[config]`},
		// key material by name
		{regexp.MustCompile(`(?i)(private[_ -]?key|preshared[_ -]?key|secret[_ -]?key)\W{0,8}[A-Za-z0-9+/=_-]{16,}`), `$1 [key]`},
		// http(s) URLs: a query string, and a path that carries a credential (a segment of 20+ URL-safe characters: the
		// secret prefix, a subscription token) are replaced; the host stays.
		{regexp.MustCompile(`(https?://[^\s` + stop + `/?#]+(?:/[^\s` + stop + `?#]*)?)[?#][^\s` + stop + `]*`), `${1}?[redacted]`},
		{regexp.MustCompile(`(https?://[^\s` + stop + `/?#]+)/(?:[^\s` + stop + `?#/]*/)*[A-Za-z0-9_.~-]{20,}[^\s` + stop + `]*`), `${1}/[redacted]`},
	}
}

var (
	textScrubbers = scrubbers(``)
	jsonScrubbers = scrubbers(`"\\`)
	// keyShaped is a bare 43-character base64 value (a 32-byte key), optionally with its padding, between delimiters.
	keyShaped = regexp.MustCompile(`([^A-Za-z0-9+/_=-]|^)([A-Za-z0-9+/_-]{43}=?)([^A-Za-z0-9+/_=-]|$)`)
)

func scrubWith(set []scrubber, s string) string {
	for _, sc := range set {
		s = sc.re.ReplaceAllString(s, sc.repl)
	}
	// the delimiters are consumed, so two values separated by one character need a second pass
	for i := 0; i < 2; i++ {
		s = keyShaped.ReplaceAllString(s, `${1}[key]${3}`)
	}
	return s
}

// scrub cleans a piece of text from data.
func scrub(s string) string { return scrubWith(textScrubbers, s) }

// scrubJSON is the last pass over a serialised result.
func scrubJSON(s string) string { return scrubWith(jsonScrubbers, s) }
