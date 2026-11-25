// Package destblock refuses connections by the name of their destination, for the node's shared egress.
//
// A list of domain suffixes decides. A destination given by name is matched directly (List.Match). A destination
// given by IP address carries its name only in the first bytes the client sends: the SNI of a TLS ClientHello
// (WrapTCP) or of a QUIC Initial (QUICGuard). Nothing else is looked at: a stream that is not TLS, a hello without a
// name (ECH) and a stream that does not show its name within maxSniff bytes pass untouched.
package destblock

import (
	"net/netip"
	"strings"
)

// List is an immutable set of domain suffixes. A nil or empty list blocks nothing.
type List struct{ set map[string]struct{} }

// New builds a list from suffixes such as "max.ru" ("*.max.ru" and ".max.ru" mean the same). Case and a trailing dot are ignored.
func New(suffixes []string) *List {
	l := &List{set: make(map[string]struct{}, len(suffixes))}
	for _, s := range suffixes {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s, "*"), "."), ".")
		if s != "" {
			l.set[s] = struct{}{}
		}
	}
	return l
}

// Empty reports whether the list blocks nothing.
func (l *List) Empty() bool { return l == nil || len(l.set) == 0 }

// Match reports whether host is one of the suffixes or a subdomain of one: "max.ru" and "api.max.ru" match "max.ru",
// "3dmax.ru" and "max.ru.example.com" do not. An IP literal never matches.
func (l *List) Match(host string) bool {
	if l.Empty() {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return false
	}
	for {
		if _, ok := l.set[h]; ok {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			return false
		}
		h = h[i+1:]
	}
}
