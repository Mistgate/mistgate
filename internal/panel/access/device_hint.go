package access

import (
	"strings"
	"unicode"
	"unicode/utf8"
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

// cleanHeader drops control characters, trims and cuts to max runes.
func cleanHeader(s string, max int) string {
	if s == "" {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

// clientOf is the app of a User-Agent: its first token, name and version only ("Happ/3.4.2"; Happ 6 sends
// "Happ/6.0.0/ios/2609301456616", whose platform and build the device row has or does not need), at most 40 characters.
func clientOf(ua string) string {
	ua = strings.TrimSpace(ua)
	if i := strings.IndexAny(ua, " \t"); i >= 0 {
		ua = ua[:i]
	}
	if name, rest, ok := strings.Cut(ua, "/"); ok {
		version, _, _ := strings.Cut(rest, "/")
		ua = name + "/" + version
	}
	return cleanHeader(ua, 40)
}
