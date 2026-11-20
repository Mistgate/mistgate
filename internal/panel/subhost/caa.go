//go:build !js

package subhost

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

const caaTTL = time.Minute

type caaEntry struct {
	record, status string
	at             time.Time
}

// caa is the CAA record that limits certificates for the domain to this panel's Let's Encrypt account, and whether DNS has it:
// account_pending (no account yet: the record has no accounturi), present, absent or unknown (the lookup failed).
func (s *Service) caa(ctx context.Context, domain string) (record, status string) {
	now := s.cfg.Now()
	s.caaMu.Lock()
	if e, ok := s.caaCache[domain]; ok && now.Sub(e.at) < caaTTL {
		s.caaMu.Unlock()
		return e.record, e.status
	}
	s.caaMu.Unlock()

	uri := s.accountURI(ctx)
	value := "letsencrypt.org; validationmethods=tls-alpn-01"
	if uri != "" {
		value = "letsencrypt.org; accounturi=" + uri + "; validationmethods=tls-alpn-01"
	}
	record = domain + `. CAA 0 issue "` + value + `"`
	switch issue, found, err := s.nearestCAA(ctx, domain); {
	case uri == "":
		status = "account_pending"
	case err != nil:
		status = "unknown"
	case found && containsCAA(issue, value):
		status = "present"
	default:
		status = "absent"
	}
	s.caaMu.Lock()
	s.caaCache[domain] = caaEntry{record: record, status: status, at: now}
	s.caaMu.Unlock()
	return record, status
}

func containsCAA(values []string, want string) bool {
	norm := func(v string) string { return strings.ReplaceAll(strings.ToLower(v), " ", "") }
	for _, v := range values {
		if norm(v) == norm(want) {
			return true
		}
	}
	return false
}

// nearestCAA walks from the domain up to the nearest name that has CAA records (what a CA checks).
func (s *Service) nearestCAA(ctx context.Context, domain string) ([]string, bool, error) {
	for name := domain; strings.Contains(name, "."); {
		issue, found, err := s.cfg.CAA(ctx, name)
		if err != nil || found {
			return issue, found, err
		}
		_, name, _ = strings.Cut(name, ".")
	}
	return nil, false, nil
}

// accountURI is the URI of the autocert account, "" while there is none (no certificate was ever asked for).
func (s *Service) accountURI(ctx context.Context) string {
	s.caaMu.Lock()
	uri := s.account
	s.caaMu.Unlock()
	if uri != "" || s.cfg.ACMEDir == "" {
		return uri
	}
	data, err := autocert.DirCache(s.cfg.ACMEDir).Get(ctx, "acme_account+key")
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return ""
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	acct, err := (&acme.Client{Key: key, DirectoryURL: acme.LetsEncryptURL}).GetReg(cctx, "")
	if err != nil || acct.URI == "" {
		return ""
	}
	s.caaMu.Lock()
	s.account = acct.URI
	s.caaMu.Unlock()
	return acct.URI
}

// lookupCAA asks the system's resolver for the "issue" CAA values of name. found says whether name has CAA records at all.
func lookupCAA(ctx context.Context, name string) ([]string, bool, error) {
	conf, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil, false, err
	}
	if len(conf.Servers) == 0 {
		return nil, false, errors.New("no DNS server")
	}
	c := &dns.Client{Timeout: 3 * time.Second}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeCAA)
	m.RecursionDesired = true
	r, _, err := c.ExchangeContext(ctx, m, net.JoinHostPort(conf.Servers[0], conf.Port))
	if err != nil {
		return nil, false, err
	}
	if r.Rcode != dns.RcodeSuccess && r.Rcode != dns.RcodeNameError {
		return nil, false, errors.New(dns.RcodeToString[r.Rcode])
	}
	var issue []string
	found := false
	for _, rr := range r.Answer {
		if c, ok := rr.(*dns.CAA); ok {
			found = true
			if c.Tag == "issue" {
				issue = append(issue, c.Value)
			}
		}
	}
	return issue, found, nil
}
