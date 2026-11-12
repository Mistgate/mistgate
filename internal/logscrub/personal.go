package logscrub

import (
	"regexp"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// The cleaners below are for text a person's own app wrote (the logs it sends for support): what names the person or their
// machine, not what names a server.

const (
	// MAC replaces a hardware address; Email an e-mail address; User the account name in a home directory; Computer the name of a
	// Windows machine that Windows made up.
	MAC      = "<mac>"
	Email    = "<email>"
	User     = "<user>"
	Computer = "<computer>"
)

var (
	// aa:bb:cc:dd:ee:ff, AA-BB-CC-DD-EE-FF (also the 8-byte form) and aabb.ccdd.eeff
	macPattern = regexp.MustCompile(`(?i)(?:[0-9a-f]{2}[:-]){5,7}[0-9a-f]{2}|[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}`)
	// 0a1b2c3d4e5f next to a word that says it is one
	macWord = regexp.MustCompile(`(?i)\b((?:(?:mac|bssid|hwaddr)\W{0,3}|physical address[\s.:]{0,30}))[0-9a-f]{12}\b`)
	email   = regexp.MustCompile(`[\p{L}\p{N}._%+-]+@[\p{L}\p{N}-]+(?:\.[\p{L}\p{N}-]+)+`)
	// C:\Users\Name\, C:/Users/Name, \\?\C:\Users\Name and \\SERVER\Users\Name\: the name is a segment that may hold blanks and an
	// apostrophe when a separator ends it; without one it runs to a quote, a bracket, a comma, a colon or the end of the line.
	winHome = regexp.MustCompile(`(?i)((?:[a-z]:|\\\\[^\\/\s"'<>|:*?]+)[\\/]+(?:[a-z]\$[\\/]+)?users[\\/]+)(?:([^\\/"<>|:*?\r\n]+)([\\/])|(?:[^"'<>|:*?\\/,)\r\n]|'\pL)+)`)
	// /home/name/ and /Users/name/ (also behind /mnt/c, a container's mount, ...)
	unixHome = regexp.MustCompile(`(/(?:home|Users)/)(?:([^/"<>|:*?\r\n]+)(/)|[^\s/"'<>|:*?,)]+)`)
	// DESKTOP-7F3K2QJ and LAPTOP-7F3K2QJ: the names Windows gives a machine whose owner did not choose one
	computerName = regexp.MustCompile(`(?i)\b(?:DESKTOP|LAPTOP)-[A-Z]*[0-9][A-Z0-9]{2,7}\b`)
	// a DNS name with a letter top-level label, in any script. The character after it must not go on as a name (it is taken with
	// the match and put back), and a name may hold underscores (_dmarc.example.org).
	dnsName = regexp.MustCompile(`((?:[\p{L}\p{N}_](?:[\p{L}\p{N}_-]{0,61}[\p{L}\p{N}_])?\.)+(?:xn--[a-zA-Z0-9-]{1,59}|\p{L}{2,63}))(?:$|[^\p{L}\p{N}_])`)
	// the same for text with no byte above 0x7f, where the classes are small and the match is several times faster
	dnsNameASCII = regexp.MustCompile(`((?:[A-Za-z0-9_](?:[A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?\.)+(?:xn--[a-zA-Z0-9-]{1,59}|[A-Za-z]{2,63}))(?:$|[^A-Za-z0-9_])`)
)

func alnum(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// MACs replaces every hardware address by MAC. A run of hex digits that merely contains the shape (a longer word) stays, and the
// search goes on inside it: "mac:aa:bb:cc:dd:ee:ff" begins with "ac:aa:bb:…", which is no address, and the address follows.
func MACs(s string) string {
	if len(s) < 12 {
		return s
	}
	if lower := strings.ToLower(s); strings.Contains(lower, "mac") || strings.Contains(lower, "bssid") || strings.Contains(lower, "hwaddr") || strings.Contains(lower, "physical address") {
		s = macWords(s)
	}
	// an address has five separators (or two dots): most text has fewer, and is not worth the pattern
	if strings.Count(s, ":") < 5 && strings.Count(s, "-") < 5 && strings.Count(s, ".") < 2 {
		return s
	}
	var b strings.Builder
	for pos, changed := 0, false; ; {
		m := macPattern.FindStringIndex(s[pos:])
		if m == nil {
			if !changed {
				return s
			}
			b.WriteString(s[pos:])
			return b.String()
		}
		start, end := pos+m[0], pos+m[1]
		if end < len(s) && alnum(s[end]) && end-start > 17 {
			end -= 3 // "aa:bb:cc:dd:ee:ff:3f2b…": the last pair began a word, so the address is the pairs before it
		}
		if start > 0 && alnum(s[start-1]) || end < len(s) && alnum(s[end]) {
			b.WriteString(s[pos : start+1])
			pos = start + 1
			changed = true
			continue
		}
		b.WriteString(s[pos:start])
		b.WriteString(MAC)
		pos, changed = end, true
	}
}

// macWords is the address without separators next to the word that says it is one. The word is not the placeholder's own (<mac>).
func macWords(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range macWord.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > 0 && s[m[0]-1] == '<' {
			continue
		}
		b.WriteString(s[last:m[3]])
		b.WriteString(MAC)
		last = m[1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// Emails replaces every e-mail address (in any script) by Email.
func Emails(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return email.ReplaceAllString(s, Email)
}

// HomePaths replaces the account name in a home directory of Windows (C:\Users\Name\..., also on a share), Linux (/home/name/...)
// and macOS (/Users/name/...) by User, keeps the rest of the path, and the made-up name of a Windows machine by Computer.
func HomePaths(s string) string {
	if !strings.ContainsAny(s, `\/-`) {
		return s
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "users") || strings.Contains(lower, "/home/") {
		s = winHome.ReplaceAllString(s, "${1}"+User+"${3}")
		s = unixHome.ReplaceAllString(s, "${1}"+User+"${3}")
	}
	if strings.Contains(lower, "desktop-") || strings.Contains(lower, "laptop-") {
		s = computerName.ReplaceAllString(s, Computer)
	}
	return s
}

// realTLD says whether the last label of a name is a top-level domain of ICANN's (not an ending of the public suffix list's
// private section, and not a file extension that no registry knows).
func realTLD(label string) bool {
	label = strings.ToLower(label)
	for i := 0; i < len(label); i++ {
		if label[i] >= 0x80 {
			ascii, err := idna.Lookup.ToASCII(label)
			if err != nil {
				return false
			}
			label = ascii
			break
		}
	}
	_, icann := publicsuffix.PublicSuffix(label)
	return icann
}

// HostsKnownTLD replaces every DNS name whose last label is a real top-level domain by Host, in any script. A name that ends in
// anything else is left: System.Net.Http.HttpRequestException, settings.ini and app.exe.config are no destinations. fileWins (lower
// case) lists the endings that are a top-level domain and also a common file extension (zip, py, sh ...): a name ending in one of them
// is taken for a file and left. Everything else that is a top-level domain is a host, even if it is also an extension.
func HostsKnownTLD(s string, fileWins ...string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	re := dnsNameASCII
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			re = dnsName
			break
		}
	}
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		name := s[m[2]:m[3]]
		tld := strings.ToLower(name[strings.LastIndexByte(name, '.')+1:])
		if !realTLD(tld) || contains(fileWins, tld) {
			continue
		}
		b.WriteString(s[last:m[2]])
		b.WriteString(Host)
		last = m[3]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ReplaceFold replaces every occurrence of needle in s with with, ignoring the case of ASCII letters (a secret name is a DNS
// name or a path prefix, and a log may spell it either way). Only ASCII letters fold, so offsets in the folded copy are those of
// the original. An empty needle replaces nothing.
func ReplaceFold(s, needle, with string) string {
	if needle == "" {
		return s
	}
	lower := asciiLower(needle)
	var b strings.Builder
	for {
		i := strings.Index(asciiLower(s), lower)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(with)
		s = s[i+len(needle):]
	}
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
