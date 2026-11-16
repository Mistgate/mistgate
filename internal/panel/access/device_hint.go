package access

import (
	"strings"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// DeviceHint is what a subscription request says about its device: the X-HWID, X-Device-OS, X-Ver-OS and X-Device-Model
// headers and the User-Agent, as sent. The HWID is already canonical (CanonHWID); the rest is cleaned only when a device
// is created or touched (meta), never on the path of a request that has nothing to write.
type DeviceHint struct {
	HWID                        string // canonical; "" = the request reports none (or an invalid one)
	OS, OSVersion, Model, Agent string
}

// NewDeviceHint reads the headers of a request into a hint.
func NewDeviceHint(hwid, osName, osVersion, model, userAgent string) DeviceHint {
	return DeviceHint{HWID: CanonHWID(hwid), OS: osName, OSVersion: osVersion, Model: model, Agent: userAgent}
}

// CanonHWID is the identity of a device in an X-HWID header: [A-Za-z0-9=-]{10,64} after trimming (the rule of Remnawave, so
// the same apps work), lower-cased and without dashes, so the alternative HWID of Happ on a desktop and the upper-case UUID
// of INCY name the same device. "" for anything else, and for a value that is nothing but dashes. A byte loop, no regexp:
// it runs on every request.
func CanonHWID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 10 || len(s) > 64 {
		return ""
	}
	var buf [64]byte
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-':
			continue
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '=':
		default:
			return ""
		}
		buf[n] = c
		n++
	}
	if n < 10 { // dashes are formatting; what is left is the identity, and 9 characters are no identity
		return ""
	}
	return string(buf[:n])
}

// deviceMeta is what a hint stores on its device.
type deviceMeta struct{ Platform, Model, OSVersion, Client string }

// meta cleans the hint for the device row. A device that reports a HWID is a platform even when it does not say which
// ("other"), so that it never looks like the shared place; the shared device gets one only when the request names it.
func (h DeviceHint) meta() deviceMeta {
	m := deviceMeta{Platform: platformOf(h.OS), Model: cleanHeader(h.Model, 64), OSVersion: cleanHeader(h.OSVersion, 64), Client: clientOf(h.Agent)}
	if m.Platform == "" && h.HWID != "" {
		m.Platform = "other"
	}
	return m
}

// platformOf maps an X-Device-OS to one of devicePlatforms; "" for nothing.
func platformOf(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "ios") || strings.Contains(s, "iphone") || strings.Contains(s, "ipad"):
		return "ios"
	case strings.Contains(s, "android"):
		return "android"
	case strings.Contains(s, "windows"):
		return "windows"
	case strings.Contains(s, "mac") || strings.Contains(s, "darwin"):
		return "macos"
	case strings.Contains(s, "linux"):
		return "linux"
	}
	return "other"
}

// cleanHeader removes control and format characters before storing device metadata.
func cleanHeader(s string, max int) string {
	return store.CleanDisplayText(s, max)
}

// clientOf is the app of a User-Agent: its first token, name and version only ("Happ/3.4.2"; Happ 6 sends
// "Happ/6.0.0/ios/2609301456616", whose platform and build the device row has or does not need), at most 40 characters.
func clientOf(ua string) string {
	fields := strings.Fields(ua) // any Unicode space, a no-break one too
	// kl!ck asks as its core ("mihomo/1.19.33 klick/0.5.1") so every panel serves it the mihomo format; its own token names it.
	for _, tok := range fields {
		if name, version, ok := strings.Cut(tok, "/"); ok && strings.EqualFold(name, "klick") && versionLike(version) {
			return "kl!ck/" + version
		}
	}
	if len(fields) == 0 {
		return ""
	}
	// the characters of an app name and version only: a User-Agent cannot carry a sentence into the device row or an agent's view
	ua = cleanHeader(fields[0], 200) // control characters go, as in every header
	for i := 0; i < len(ua); i++ {
		if c := ua[i]; !(asciiAlnum(c) || strings.IndexByte("._+-/!", c) >= 0) {
			ua = ua[:i]
			break
		}
	}
	if name, rest, ok := strings.Cut(ua, "/"); ok {
		version, _, _ := strings.Cut(rest, "/")
		ua = name + "/" + version
	}
	return cleanHeader(ua, 40)
}

// versionLike: "0.5.1", "1.2.3-beta.1": a digit first, then letters, digits, dots, dashes and pluses, at most 24.
func versionLike(v string) bool {
	if v == "" || len(v) > 24 || v[0] < '0' || v[0] > '9' {
		return false
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; !(asciiAlnum(c) || c == '.' || c == '-' || c == '+') {
			return false
		}
	}
	return true
}

func asciiAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
