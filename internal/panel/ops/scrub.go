//go:build !js

package ops

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/mcp"
)

// minSecret is the shortest secret name worth replacing: a shorter one would also eat ordinary words.
const minSecret = 6

// allowedAttrs are the log attribute keys panel_logs and the export return. Anything else (a user, an admin, an address, a path
// on disk, a token) stays in the ring and goes no further. A key is added here on purpose, with a look at what its values hold.
var allowedAttrs = map[string]bool{
	"code": true, "request_id": true, "component": true, "err": true, "error": true, "error_code": true,
	"node": true, "node_id": true, "inbound": true, "relay": true, "profile": true, "protocol": true, "provider": true,
	"job_id": true, "reason": true, "status": true, "state": true, "count": true, "rows": true, "version": true, "rollout": true,
	"action": true, "source": true, "scope": true, "revision": true, "retry_in": true, "failures": true, "method": true,
	"path": true, "ms": true, "op": true, "kind": true, "listener": true, "unit": true, "built": true, "preset": true,
	"seq": true, "from": true, "to": true,
}

// Scrubber cleans text that leaves the panel. Its order matters: secret names first (they may sit inside an address-like
// string), then addresses, then the MCP scrubbers for credential-shaped text.
type Scrubber struct{ secrets []string }

// NewScrubber takes the panel's secret names (SNI, path prefixes); names shorter than minSecret are ignored.
func NewScrubber(secrets []string) *Scrubber {
	var keep []string
	for _, s := range secrets {
		if s = strings.Trim(s, "/ "); len(s) >= minSecret {
			keep = append(keep, s)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return len(keep[i]) > len(keep[j]) }) // the longest first
	return &Scrubber{secrets: keep}
}

// Text cleans one piece of text.
func (c *Scrubber) Text(s string) string {
	for _, secret := range c.secrets {
		s = replaceFold(s, secret, "<secret>")
	}
	return mcp.ScrubText(logscrub.Addrs(s))
}

// Value cleans an attribute value, which may also name a destination: DNS names go too. The result is cut to n bytes.
func (c *Scrubber) Value(s string, n int) string {
	return logscrub.Clip(logscrub.Hosts(c.Text(s)), n)
}

// Line cleans one serialised JSON line (an export row): the same passes, with the JSON-aware credential scrubbers last.
func (c *Scrubber) Line(s string) string {
	for _, secret := range c.secrets {
		s = replaceFold(s, secret, "<secret>")
	}
	return mcp.ScrubJSON(logscrub.Addrs(s))
}

// replaceFold replaces every ASCII-case-insensitive occurrence of needle. Only ASCII letters fold, so offsets in the folded
// copy are those of the original.
func replaceFold(s, needle, with string) string {
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

func jsonObject(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}
