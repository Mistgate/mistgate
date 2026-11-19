package sniscan

import (
	"crypto/x509"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// What comes from a neighbour is a certificate, a few DNS answers and the head of one HTTP response. Everything here
// reads that and keeps only names that are plain host names and flags; nothing else of it leaves this package.

// validHost reports whether s is a plain host name: lower-case ASCII letters, digits and hyphens in labels of 1-63
// bytes, at least two labels, 253 bytes at most, not an address and no mask. A name in a certificate that is not one
// (a mask, an address, Unicode, a single label) is not a candidate.
func validHost(s string) bool {
	if len(s) < 3 || len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return false
	}
	labels := 0
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '.' {
			c := s[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
			continue
		}
		l := s[start:i]
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		labels++
		start = i + 1
	}
	// A name whose last label is all digits is a (malformed) address, not a site.
	last := s[strings.LastIndexByte(s, '.')+1:]
	if strings.Trim(last, "0123456789") == "" {
		return false
	}
	return labels >= 2
}

// normHost lower-cases a name of a certificate and drops the final dot.
func normHost(s string) string { return strings.ToLower(strings.TrimSuffix(s, ".")) }

// registrable is the registrable domain of a host name (example.co.uk for www.example.co.uk); the name itself when the
// public suffix list has none for it.
func registrable(host string) string {
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// maxSAN bounds how many names of one certificate are looked at; a certificate with more is a hosting one.
const maxSAN = 100

// certNames reads a leaf certificate: the exact host names it is issued for (no mask, at most MaxNames, in the order of
// the certificate) and whether it is the certificate of one site, that is, its names, masks included, belong to at most
// two registrable domains.
func certNames(c *x509.Certificate) (names []string, single bool) {
	domains := map[string]bool{}
	seen := map[string]bool{}
	many := len(c.DNSNames) > maxSAN
	for i, raw := range c.DNSNames {
		if i == maxSAN {
			break
		}
		n := normHost(raw)
		bare := strings.TrimPrefix(n, "*.")
		if !validHost(bare) {
			continue
		}
		domains[registrable(bare)] = true
		if n == bare && !seen[n] && len(names) < MaxNames {
			seen[n] = true
			names = append(names, n)
		}
	}
	return names, !many && len(domains) <= 2
}

// offsite reports whether the Location of a redirect leads to another registrable domain than the site's own name. A
// relative reference, the same host and a subdomain of it stay on the site; what cannot be read counts as away.
func offsite(location, name string) bool {
	u, err := url.Parse(strings.TrimSpace(location))
	if err != nil {
		return true
	}
	host := normHost(u.Hostname())
	if host == "" {
		return u.Scheme != "" || strings.HasPrefix(strings.TrimSpace(location), "//") // "mailto:..." is not this site
	}
	return registrable(host) != registrable(name)
}
