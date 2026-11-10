//go:build !js

package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"

	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/access"
)

// scrub is the last wall: every string an agent can see is run through it, after the projection has
// chosen what to show. It catches what a projection forgot, or what an attacker put into a name or a note to be carried out
// in a result: panel secrets, share links, key material and anything shaped like a credential in a URL.
//
// Result JSON is walked token by token so scrubbing leaves key order and number text intact.

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
	uuidShape     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b`)
	// keyShaped is a bare 43-character base64 value (a 32-byte key), optionally with its padding, between delimiters.
	keyShaped = regexp.MustCompile(`([^A-Za-z0-9+/_=-]|^)([A-Za-z0-9+/_-]{43}=?)([^A-Za-z0-9+/_=-]|$)`)
	longToken = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)
)

func scrubWith(set []scrubber, s string) string {
	// UUIDs are not panel identifiers, so redact them before generic token and URL patterns can consume them.
	s = uuidShape.ReplaceAllString(s, `[uuid]`)
	for _, sc := range set {
		s = sc.re.ReplaceAllString(s, sc.repl)
	}
	// the delimiters are consumed, so two values separated by one character need a second pass
	for i := 0; i < 2; i++ {
		s = keyShaped.ReplaceAllString(s, `${1}[key]${3}`)
	}
	var out strings.Builder
	last := 0
	for _, span := range longToken.FindAllStringIndex(s, -1) {
		// Delimiters stay outside each match so a low-entropy run cannot consume the next run's boundary.
		start, end := span[0], span[1]
		if !longTokenBoundary(s, start, true) || !longTokenBoundary(s, end, false) || !highEntropy(s[start:end]) {
			continue
		}
		out.WriteString(s[last:start])
		out.WriteString(`[token]`)
		last = end
	}
	if last != 0 {
		out.WriteString(s[last:])
		s = out.String()
	}
	return s
}

func longTokenBoundary(s string, at int, left bool) bool {
	if left && at == 0 || !left && at == len(s) {
		return true
	}
	c := s[at]
	if left {
		c = s[at-1]
	}
	return !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.ContainsRune("+/=_-", rune(c)))
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

// scrubJSON rewrites string values while retaining object order and the original spelling of JSON numbers.
func scrubJSON(s string) string {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var out bytes.Buffer
	if err := scrubJSONToken(&out, d, false, ""); err != nil {
		return `[invalid JSON tool result]`
	}
	if _, err := d.Token(); err != io.EOF {
		return `[invalid JSON tool result]`
	}
	return out.String()
}

func scrubJSONToken(out *bytes.Buffer, d *json.Decoder, redact bool, cursorName string) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			out.WriteByte('{')
			first := true
			for d.More() {
				if !first {
					out.WriteByte(',')
				}
				first = false
				nameToken, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return io.ErrUnexpectedEOF
				}
				if err := writeJSONString(out, name); err != nil {
					return err
				}
				out.WriteByte(':')
				if err := scrubJSONToken(out, d, redact || secretKeyName(name), name); err != nil {
					return err
				}
			}
			close, err := d.Token()
			if err != nil || close != json.Delim('}') {
				return io.ErrUnexpectedEOF
			}
			out.WriteByte('}')
		case '[':
			out.WriteByte('[')
			first := true
			for d.More() {
				if !first {
					out.WriteByte(',')
				}
				first = false
				if err := scrubJSONToken(out, d, redact, ""); err != nil {
					return err
				}
			}
			close, err := d.Token()
			if err != nil || close != json.Delim(']') {
				return io.ErrUnexpectedEOF
			}
			out.WriteByte(']')
		default:
			return io.ErrUnexpectedEOF
		}
	case string:
		if redact {
			if value != profileSecretMask {
				value = `[key]`
			}
		} else if validCursor(cursorName, value) {
			return writeJSONString(out, value)
		} else {
			value = scrub(value)
		}
		return writeJSONString(out, value)
	case json.Number:
		out.WriteString(value.String())
	case bool:
		if value {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return io.ErrUnexpectedEOF
	}
	return nil
}

func writeJSONString(out *bytes.Buffer, value string) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	out.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	return nil
}

func validCursor(name, value string) bool {
	switch name {
	case "next_page_token":
		return access.ValidUserListPageToken(value)
	case "next_cursor":
		return tlCursorShape.MatchString(value)
	default:
		return false
	}
}

func secretKeyName(name string) bool {
	var normalized strings.Builder
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			continue
		}
		normalized.WriteRune(unicode.ToLower(r))
	}
	key := normalized.String()
	return strings.Contains(key, "privatekey") || strings.Contains(key, "secretkey") ||
		strings.Contains(key, "presharedkey") || key == "psk" || key == "password"
}

// ScrubText applies the MCP result scrubbers to untrusted context the assistant sends to a provider.
func ScrubText(s string) string { return scrub(s) }

// ScrubJSON scrubs string values in a JSON result while retaining object order and number text.
func ScrubJSON(s string) string { return scrubJSON(s) }

func scrubDebugCaptureAttrs(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]string)
	for _, key := range logscrub.JournalAttrs {
		if value, ok := attrs[key]; ok {
			out[key] = value
		}
	}
	return out
}
