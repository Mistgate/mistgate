package dns

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Limits of a preset.
const (
	MaxServers         = 16  // main resolvers, and the servers of one split rule
	MaxSplitRules      = 64  // split rules per preset
	MaxSuffixesPerRule = 256 // domain suffixes in one split rule
	maxName            = 64
	maxDescription     = 512
)

// Kind is how a server is reached.
type Kind string

const (
	KindPlain Kind = "plain" // UDP/TCP DNS: "77.88.8.8" or "77.88.8.8:53"
	KindDoH   Kind = "doh"   // DNS over HTTPS: "https://dns.example.com/dns-query"
	KindDoT   Kind = "dot"   // DNS over TLS: "dns.example.com" or "94.140.14.14:853"
)

// Server is one resolver: a catalog variant (Variant set, see providers.go) or a custom address (Kind and Address).
type Server struct {
	Kind    Kind   `json:"kind,omitempty"`
	Address string `json:"address,omitempty"`
	Variant string `json:"variant,omitempty"`
}

// SplitRule sends domains ending in one of Suffixes to Servers instead of the preset's main ones.
type SplitRule struct {
	Suffixes []string `json:"suffixes"`
	Servers  []Server `json:"servers"`
}

// validationError marks a message meant for the admin (InvalidArgument), as opposed to a storage failure.
type validationError string

func (e validationError) Error() string { return string(e) }

func bad(format string, a ...any) error { return validationError(fmt.Sprintf(format, a...)) }

// cleanText trims and bounds a free-text field.
func cleanText(what, s string, max int, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", bad("%s is required", what)
	}
	if utf8.RuneCountInString(s) > max {
		return "", bad("%s is longer than %d characters", what, max)
	}
	// A description is two lines (Russian, English) in the built-ins, so newlines stay; other control
	// characters do not.
	if strings.ContainsFunc(s, func(r rune) bool { return (r < 0x20 && r != '\n') || r == 0x7f }) {
		return "", bad("%s has control characters", what)
	}
	return s, nil
}

// hostname returns the ASCII (punycode, lower case) form of a DNS host name.
func hostname(h string) (string, error) {
	h = strings.TrimSuffix(strings.TrimSpace(h), ".")
	if h == "" {
		return "", bad("empty host name")
	}
	a, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return "", bad("%q is not a valid host name", h)
	}
	if err := checkLabels(a); err != nil {
		return "", err
	}
	return a, nil
}

func checkLabels(a string) error {
	if a == "" || len(a) > 253 {
		return bad("host name length must be 1-253")
	}
	for _, l := range strings.Split(a, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return bad("%q is not a valid host name", a)
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return bad("%q is not a valid host name", a)
			}
		}
	}
	return nil
}

func port(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return bad("port must be 1-65535")
	}
	return nil
}

func usableIP(a netip.Addr) error {
	if a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() {
		return bad("%s is not a usable resolver address", a)
	}
	return nil
}

// normPlain accepts "ip" or "ip:port" ("[v6]:port" for IPv6 with a port) and returns the canonical form.
func normPlain(addr string) (string, error) {
	if a, err := netip.ParseAddr(addr); err == nil {
		return a.Unmap().String(), usableIP(a)
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return "", bad("%q is not an IP address or IP:port", addr)
	}
	if ap.Port() == 0 {
		return "", bad("port must be 1-65535")
	}
	if err := usableIP(ap.Addr()); err != nil {
		return "", err
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String(), nil
}

// normDoH accepts an https URL with a host name or an IP literal.
func normDoH(addr string) (string, error) {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", bad("%q is not an https URL", addr)
	}
	host := u.Hostname()
	if a, err := netip.ParseAddr(host); err == nil {
		if err := usableIP(a); err != nil {
			return "", err
		}
		host = a.String()
		if a.Is6() {
			host = "[" + host + "]"
		}
	} else if host, err = hostname(host); err != nil {
		return "", err
	}
	if p := u.Port(); p != "" {
		if err := port(p); err != nil {
			return "", err
		}
		host += ":" + p
	}
	out := url.URL{Scheme: "https", Host: host, Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}
	return out.String(), nil
}

// normDoT accepts "host", "host:port", "ip" or "ip:port".
func normDoT(addr string) (string, error) {
	if strings.ContainsAny(addr, "/?# @") {
		return "", bad("%q is not host or host:port", addr)
	}
	if a, err := netip.ParseAddr(addr); err == nil {
		return a.Unmap().String(), usableIP(a)
	}
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		if ap.Port() == 0 {
			return "", bad("port must be 1-65535")
		}
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String(), usableIP(ap.Addr())
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil { // no port
		h, p = addr, ""
	}
	if h, err = hostname(h); err != nil {
		return "", err
	}
	if p == "" {
		return h, nil
	}
	if err := port(p); err != nil {
		return "", err
	}
	return h + ":" + p, nil
}

// NormalizeServer validates a server and returns it in canonical form.
func NormalizeServer(s Server) (Server, error) {
	if id := strings.TrimSpace(s.Variant); id != "" {
		if _, _, ok := LookupVariant(id); !ok {
			return Server{}, bad("%q is not a provider variant of the catalog", id)
		}
		return Server{Variant: id}, nil
	}
	addr := strings.TrimSpace(s.Address)
	var err error
	switch s.Kind {
	case KindPlain:
		addr, err = normPlain(addr)
	case KindDoH:
		addr, err = normDoH(addr)
	case KindDoT:
		addr, err = normDoT(addr)
	default:
		return Server{}, bad("unknown server kind")
	}
	if err != nil {
		return Server{}, err
	}
	return Server{Kind: s.Kind, Address: addr}, nil
}

// normServers validates a server list (1..MaxServers after dropping exact duplicates).
func normServers(what string, in []Server) ([]Server, error) {
	out := make([]Server, 0, len(in))
	seen := map[Server]bool{}
	for _, s := range in {
		n, err := NormalizeServer(s)
		if err != nil {
			return nil, bad("%s: %v", what, err)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, bad("%s: add at least one server", what)
	}
	if len(out) > MaxServers {
		return nil, bad("%s: at most %d servers", what, MaxServers)
	}
	return out, nil
}

// NormalizeSuffix validates a domain suffix: ".ru", "gosuslugi.ru", ".рф", "*.example.com". The result is
// lower case punycode; a leading dot is kept when given (".ru" and "ru" both match the domain and its
// subdomains, at a label boundary).
func NormalizeSuffix(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "*")
	dot := strings.HasPrefix(s, ".")
	s = strings.TrimPrefix(s, ".")
	a, err := hostname(s)
	if err != nil {
		return "", bad("suffix %q: %v", s, err)
	}
	if dot {
		return "." + a, nil
	}
	return a, nil
}

func suffixBody(s string) string { return strings.TrimPrefix(s, ".") }

func normSplit(in []SplitRule) ([]SplitRule, error) {
	if len(in) > MaxSplitRules {
		return nil, bad("at most %d split rules", MaxSplitRules)
	}
	out := make([]SplitRule, 0, len(in))
	for i, r := range in {
		what := fmt.Sprintf("split rule %d", i+1)
		var sfx []string
		seen := map[string]bool{}
		for _, s := range r.Suffixes {
			n, err := NormalizeSuffix(s)
			if err != nil {
				return nil, bad("%s: %v", what, err)
			}
			if b := suffixBody(n); !seen[b] {
				seen[b] = true
				sfx = append(sfx, n)
			}
		}
		if len(sfx) == 0 {
			return nil, bad("%s: add at least one domain suffix", what)
		}
		if len(sfx) > MaxSuffixesPerRule {
			return nil, bad("%s: at most %d suffixes", what, MaxSuffixesPerRule)
		}
		srv, err := normServers(what, r.Servers)
		if err != nil {
			return nil, err
		}
		out = append(out, SplitRule{Suffixes: sfx, Servers: srv})
	}
	return out, nil
}

// NormalizeTransport validates a preferred transport; "" means plain.
func NormalizeTransport(k Kind) (Kind, error) {
	switch k {
	case "":
		return KindPlain, nil
	case KindPlain, KindDoT, KindDoH:
		return k, nil
	}
	return "", bad("unknown transport")
}

// Clean validates and normalises the editable fields of a preset.
func Clean(name, description string, servers []Server, split []SplitRule) (n, d string, srv []Server, sp []SplitRule, err error) {
	if n, err = cleanText("name", name, maxName, true); err != nil {
		return
	}
	if d, err = cleanText("description", description, maxDescription, false); err != nil {
		return
	}
	if srv, err = normServers("servers", servers); err != nil {
		return
	}
	sp, err = normSplit(split)
	return
}
