package release

import (
	"regexp"
	"strings"
)

const maxSourceFailureItems = 16

var sourceFailureCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

// SanitizeSourceFailures keeps bounded source failure entries with short, non-sensitive codes.
// It drops malformed entries individually so one bad item does not hide valid failure details.
func SanitizeSourceFailures(raw string) string {
	if raw == "" {
		return ""
	}
	var valid []string
	for count := 0; raw != "" && count < maxSourceFailureItems; count++ {
		item := raw
		if comma := strings.IndexByte(raw, ','); comma >= 0 {
			item, raw = raw[:comma], raw[comma+1:]
		} else {
			raw = ""
		}
		if len(item) > 27 { // cloudflare plus ':' and the 16-character code limit
			continue
		}
		kind, code, ok := strings.Cut(item, ":")
		if !ok || strings.Contains(code, ":") || !knownDownloadSource(kind) || !sourceFailureCodePattern.MatchString(code) {
			continue
		}
		valid = append(valid, item)
	}
	return strings.Join(valid, ",")
}

func knownDownloadSource(source string) bool {
	switch source {
	case "panel", "github", "peer", "cloudflare":
		return true
	default:
		return false
	}
}
