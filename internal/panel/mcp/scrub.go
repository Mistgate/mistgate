//go:build !js

package mcp

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

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
		// Common API and service credentials. Keep the prefixes specific so IDs and ordinary prose remain readable.
		{regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{10,}`), `[key]`},
		{regexp.MustCompile(`\bsk-proj-[A-Za-z0-9_-]{10,}`), `[key]`},
		{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), `[key]`},
		{regexp.MustCompile(`\b(?:ghp|gho)_[A-Za-z0-9]{20,}`), `[key]`},
		{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), `[key]`},
		{regexp.MustCompile(`\b[0-9]{6,}:[A-Za-z0-9_-]{30,}`), `[token]`},
		{regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9-]{10,}`), `[key]`},
		{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), `[key]`},
		{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`), `[key]`},
		{regexp.MustCompile(`\bAGE-SECRET-KEY-1[0-9A-Z]{30,}`), `[key]`},
		{regexp.MustCompile(`\bBearer\s+[^\s` + stop + `]+`), `Bearer [key]`},
		{regexp.MustCompile(`eyJ[A-Za-z0-9_-]*\.eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]{8,}`), `[token]`},
		{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), `[private key]`},
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
	uuidShape     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b`)
	// keyShaped is a bare 43-character base64 value (a 32-byte key), optionally with its padding, between delimiters.
	keyShaped = regexp.MustCompile(`([^A-Za-z0-9+/_=-]|^)([A-Za-z0-9+/_-]{43}=?)([^A-Za-z0-9+/_=-]|$)`)
	longToken = regexp.MustCompile(`(^|[^A-Za-z0-9+/=_-])([A-Za-z0-9+/=_-]{32,})([^A-Za-z0-9+/=_-]|$)`)
)

func scrubWith(set []scrubber, s string) string {
	var uuids []string
	var markers []string
	s = uuidShape.ReplaceAllStringFunc(s, func(value string) string {
		marker := "\x00u" + strconv.Itoa(len(uuids)) + "\x00"
		uuids = append(uuids, value)
		markers = append(markers, marker)
		return marker
	})
	for _, sc := range set {
		s = sc.re.ReplaceAllString(s, sc.repl)
	}
	// the delimiters are consumed, so two values separated by one character need a second pass
	for i := 0; i < 2; i++ {
		s = keyShaped.ReplaceAllString(s, `${1}[key]${3}`)
	}
	for i := 0; i < 2; i++ {
		s = longToken.ReplaceAllStringFunc(s, func(match string) string {
			parts := longToken.FindStringSubmatch(match)
			if len(parts) != 4 || !highEntropy(parts[2]) {
				return match
			}
			return parts[1] + `[token]` + parts[3]
		})
	}
	for i, marker := range markers {
		s = strings.ReplaceAll(s, marker, uuids[i])
	}
	return s
}

func highEntropy(value string) bool {
	if len(value) < 32 {
		return false
	}
	var counts [256]int
	classes := 0
	var seen [4]bool // lowercase, uppercase, digits, URL-safe punctuation
	for i := 0; i < len(value); i++ {
		c := value[i]
		counts[c]++
		switch {
		case c >= 'a' && c <= 'z':
			seen[0] = true
		case c >= 'A' && c <= 'Z':
			seen[1] = true
		case c >= '0' && c <= '9':
			seen[2] = true
		default:
			seen[3] = true
		}
	}
	for _, ok := range seen {
		if ok {
			classes++
		}
	}
	if classes < 3 && !isHexKey(value) {
		return false
	}
	entropy := 0.0
	for _, count := range counts {
		if count == 0 {
			continue
		}
		p := float64(count) / float64(len(value))
		entropy -= p * math.Log2(p)
	}
	return entropy >= 3.5
}

func isHexKey(value string) bool {
	if len(value) < 32 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// scrub cleans a piece of text from data.
func scrub(s string) string { return scrubWith(textScrubbers, s) }

// scrubJSON is the last pass over a serialised result.
func scrubJSON(s string) string { return scrubWith(jsonScrubbers, s) }

// ScrubText applies the MCP result scrubbers to untrusted context the assistant sends to a provider.
func ScrubText(s string) string { return scrub(s) }

// ScrubJSON applies the final serialized-result scrubbers to assistant tool output.
func ScrubJSON(s string) string { return scrubJSON(s) }
