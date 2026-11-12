//go:build !js

// Package clientdebug takes the logs a person's app sends for support (client API: POST <base>/debug, client_debug.proto of the
// plan "logs for support"). It reads the delivery strictly, cleans every text the person's machine wrote, and hands the lines to the
// store, which keeps them as rows of debug_capture_line under the bundle's id. It is the VPS edition's: the Cloudflare edition
// takes no logs, and none of this is in its wasm.
package clientdebug

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// minSecret is the shortest secret name worth replacing: a shorter one would also eat ordinary words.
const minSecret = 6

// fileWins are the endings that are a top-level domain and also the extension of a file an app writes about (zip, mov, Mistgate.app,
// tool.py, run.sh, x.pl, main.rs, README.md, lib.so, x.cc, doc.ps, x.ml): a name that ends in one is taken for a file and left. The
// cost is a site in one of these domains (a Polish or a Serbian one) that a log names; the app must not log destinations at all (the
// contract), and the other top-level domains that people also use as extensions (io, dev, ai, sh as a site ...) stay hosts: a name
// ending in io or dev or ai is a host.
var fileWins = []string{"zip", "mov", "app", "py", "sh", "pl", "rs", "md", "so", "cc", "ps", "ml"}

var (
	// password=hunter2, auth: x, short_id=ab12, cookie=... : the value of a field that is a secret by its name
	secretField = regexp.MustCompile(`(?i)\b(password|passwd|pwd|auth|obfs-password|short_id|sid|psk|pin|cookie|ssid)\s*[=:]\s*[^\s,;]+`)
	bearer      = regexp.MustCompile(`(?i)\b(bearer)(?:\s*:\s*|\s+)[^\s,;"]+`)
	basicAuth   = regexp.MustCompile(`(?i)\b(authorization\s*[=:]\s*"?basic)\s+[^\s,;"]+`)
	// vless%3A%2F%2F…: a share link that went through percent-encoding (a query string, a log of a URL)
	encodedLink = regexp.MustCompile(`(?i)\b(?:vless|vmess|hysteria2|hysteria|hy2|ss|ssr|trojan|tuic|wireguard|awg|socks5?|https?)%3A%2F%2F\S+`)
	// 32 hex digits without dashes: a UUID or a HWID
	hex32 = regexp.MustCompile(`(?i)\b[0-9a-f]{32}\b`)
)

// secretWords are the names secretField looks for; a text with none of them is not run through it.
var secretWords = []string{"password", "passwd", "pwd", "auth", "short_id", "sid", "psk", "pin", "cookie"}

// Cleaner cleans the text of a person's log. Every text of a line (the message and the attribute values) goes through Text.
type Cleaner struct{ secrets []string }

// NewCleaner takes the panel's secret names (the agent SNI, the secret path prefixes); names shorter than minSecret are ignored.
func NewCleaner(secrets []string) *Cleaner {
	var keep []string
	for _, s := range secrets {
		if s = strings.Trim(s, "/ "); len(s) >= minSecret {
			keep = append(keep, s)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return len(keep[i]) > len(keep[j]) }) // the longest first
	return &Cleaner{secrets: keep}
}

// Secrets replaces the panel's secret names in a text that is checked against a shape otherwise (the source of a line, the app's name).
func (c *Cleaner) Secrets(s string) string {
	for _, secret := range c.secrets {
		s = logscrub.ReplaceFold(s, secret, "<secret>")
	}
	return s
}

// Text cleans one text. The order matters:
//  1. text in another script is normalized (NFKC: full-width letters and look-alike hyphens become the ASCII ones that the steps
//     below look for); control characters (C0, C1) and line and paragraph separators become blanks; format characters (bidirectional controls,
//     zero width) and private-use characters are removed, so nothing hides from the steps below or reorders what the owner reads;
//  2. the panel's secret names become <secret> (they may sit inside an address-like string);
//  3. IP addresses become <ip> or <ip>:port;
//  4. hardware addresses, e-mail addresses, the account name in a home directory and the made-up name of a Windows machine;
//  5. secrets by the name of their field (password=…, short_id=…), Bearer and Basic values, share links in percent-encoding,
//     32 hex digits (UUID, HWID);
//  6. credential-shaped text: share links, JWTs, PEM blocks, API keys, query strings, UUIDs, base64 keys, long tokens;
//  7. DNS names with a real top-level domain become <host>; file names and type names stay (state.json, Mistgate.exe,
//     System.Net.Http.HttpRequestException).
func (c *Cleaner) Text(s string) string {
	out := c.pass(plain(s), true)
	if out == s {
		return out
	}
	// A step that cuts text out leaves its neighbours side by side, and they can be an address, a name or a field now
	// ("example.com1.2.3.4" is "example.com<ip>", "4vless%3A…" is "<ip>vless%3A…"). The cheap steps run once more over what the first
	// pass changed; the credential scrubber, which costs nearly all of the time, does not.
	return c.pass(out, false)
}

// pass is steps 2 to 7; the credential scrubbers (step 6) only when full says so.
func (c *Cleaner) pass(s string, full bool) string {
	s = logscrub.Addrs(c.Secrets(s))
	s = logscrub.HomePaths(logscrub.Emails(logscrub.MACs(s)))
	// the patterns below need a word or a mark that most text does not have: look for it first (a pattern run over every line of
	// 4 MiB is seconds)
	lower := strings.ToLower(s)
	for _, word := range secretWords {
		if strings.Contains(lower, word) {
			s = secretField.ReplaceAllString(s, "$1=[key]")
			break
		}
	}
	if strings.Contains(lower, "bearer") {
		s = bearer.ReplaceAllString(s, "$1 [key]")
	}
	if strings.Contains(lower, "basic") {
		s = basicAuth.ReplaceAllString(s, "$1 [key]")
	}
	if strings.Contains(lower, "%3a%2f%2f") {
		s = encodedLink.ReplaceAllString(s, "[link]")
	}
	s = hex32.ReplaceAllString(s, "[token]")
	if full {
		s = logscrub.Addrs(mcp.ScrubText(s))
	}
	return logscrub.Addrs(logscrub.HostsKnownTLD(s, fileWins...))
}

// plain is step 1.
func plain(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			s = norm.NFKC.String(s) // full-width letters, a non-breaking hyphen ...: the secret name and the scheme are found as written in ASCII
			break
		}
	}
	clean := true
	for _, r := range s {
		if odd(r) {
			clean = false
			break
		}
	}
	if clean && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError || unicode.IsControl(r) || r == ' ' || r == ' ':
			b.WriteByte(' ')
		case unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func odd(r rune) bool {
	return r == utf8.RuneError || unicode.IsControl(r) || r == ' ' || r == ' ' || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r)
}

// tokenByte says whether a byte can be part of a key, a token or a base64 value.
func tokenByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' || c == '_' || c == '-'
}

// cut is s cut to at most n bytes before it is cleaned: at the last character that cannot be part of a token (a blank, a quote, a
// colon, a backslash, a bracket ...) when there is one, so that the end of a long token is not left for the cleaners to miss (a key
// cut in half no longer looks like a key). A text that fits is returned as it is.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = logscrub.Clip(s, n)
	for i := len(s) - 1; i > 0; i-- {
		if s[i]&0xC0 != 0x80 && !tokenByte(s[i]) { // the start of a character that is no token character
			return s[:i]
		}
	}
	return s
}

// NoteRunes is the longest note of a request.
const NoteRunes = store.ClientDebugNoteRunes

// Note is the owner's note of a request as the person gets it: control and invisible formatting characters removed or turned to
// blanks (the same step 1 as the logs), the ends trimmed, at most NoteRunes characters. It is plain text; the app shows it as such.
func Note(s string) string {
	return strings.TrimSpace(logscrub.ClipRunes(strings.TrimSpace(plain(s)), NoteRunes))
}
