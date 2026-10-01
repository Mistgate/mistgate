package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// instance is this installation's reachability and WebAuthn configuration, written by
// `mistgate setup` into the setting table and read by `mistgate serve`.
//
// Admin reachability:
//
//	(a) AdminHost set:    https://<admin-host>/           on the main listener
//	(b) neither set:      <public-url>/<AdminPrefix>      on the main listener
//	(c) AdminListen set:  a separate listener, prefix "/"
type instance struct {
	PublicURL   string   // the decoy site's URL, e.g. https://example.com
	AdminHost   string   // mode (a)
	AdminPrefix string   // "/" + 24 base32 chars + "/" in mode (b), "/" otherwise
	AdminListen string   // mode (c)
	RPID        string   // WebAuthn relying party id
	RPOrigins   []string // allowed browser origins; the first one is the admin URL's origin
	AgentSNI    string   // secret TLS server name that selects the agent endpoint, e.g. q3m8x2kd7w4ht9pa.example.com
	SubPrefix   string   // secret path prefix of the public subscription endpoint, "/" + 24 chars + "/"
}

var errNotConfigured = errors.New("not configured: run `mistgate setup` first")

// adminURL is the address the owner opens in a browser.
func (in instance) adminURL() string { return in.RPOrigins[0] + in.AdminPrefix }

// muxPrefix is the prefix for httpserver.Config: empty unless the admin lives under a secret path.
func (in instance) muxPrefix() string {
	if in.AdminPrefix == "/" {
		return ""
	}
	return in.AdminPrefix
}

type setupOpts struct {
	publicURL, adminHost, adminListen, rpID, rpOrigins string
}

func newInstance(o setupOpts) (instance, error) {
	in := instance{AdminPrefix: "/", AdminListen: o.adminListen, AdminHost: strings.ToLower(o.adminHost)}
	if o.adminListen != "" && o.adminHost != "" {
		return in, errors.New("--admin-listen and --admin-host are mutually exclusive: the admin is reached one way only")
	}
	in.SubPrefix = newSecretPrefix()

	var pub *url.URL
	if o.publicURL != "" {
		var err error
		if pub, err = url.Parse(strings.TrimRight(o.publicURL, "/")); err != nil || (pub.Scheme != "http" && pub.Scheme != "https") || pub.Hostname() == "" {
			return in, fmt.Errorf("--public-url %q must be http(s)://host[:port]", o.publicURL)
		}
		in.PublicURL = pub.String()
	}

	switch {
	case o.adminListen != "":
		_, port, err := net.SplitHostPort(o.adminListen)
		if err != nil {
			return in, fmt.Errorf("--admin-listen %q: %w", o.adminListen, err)
		}
		in.RPID = "localhost"
		in.RPOrigins = []string{"http://localhost:" + port}
	case in.AdminHost != "":
		scheme, port := "https", ""
		if pub != nil {
			scheme, port = pub.Scheme, pub.Port()
		}
		origin := scheme + "://" + in.AdminHost
		if port != "" {
			origin += ":" + port
		}
		in.RPID, in.RPOrigins = in.AdminHost, []string{origin}
	default:
		if pub == nil {
			return in, errors.New("--public-url is required (or --admin-host, or --admin-listen)")
		}
		in.AdminPrefix = newSecretPrefix()
		in.RPID, in.RPOrigins = pub.Hostname(), []string{pub.Scheme + "://" + pub.Host}
	}

	in.AgentSNI = newAgentSNI(in.sniDomain())
	if o.rpID != "" {
		in.RPID = o.rpID
	}
	if o.rpOrigins != "" {
		in.RPOrigins = splitList(o.rpOrigins)
		if len(in.RPOrigins) == 0 {
			return in, errors.New("--rp-origins is empty")
		}
	}
	return in, nil
}

// newSecretPrefix is "/" + 24 random base32 characters (120 bits) + "/".
func newSecretPrefix() string {
	var raw [15]byte
	rand.Read(raw[:])
	return "/" + strings.ToLower(base32.StdEncoding.EncodeToString(raw[:])) + "/"
}

// newAgentSNI is the secret TLS server name of the agent endpoint: 16 random base32 characters (80 bits) as a
// subdomain of domain, e.g. q3m8x2kd7w4ht9pa.example.com.
//
// Every agent sends it in the clear in its ClientHello, so it is what a watcher on the path sees. A name under
// the reserved .invalid TLD (what earlier versions generated) is a unique, instantly recognisable marker; a
// random subdomain of the panel's own domain looks like any of the throw-away host names that CDNs and tunnel
// services hand out, and like the secret admin subdomain of mode (a). Nothing resolves it: agents dial the
// panel by address and only put the name into the TLS handshake. The name stays secret, so a prober without it
// reaches the decoy site.
func newAgentSNI(domain string) string {
	var raw [10]byte
	rand.Read(raw[:])
	return strings.ToLower(base32.StdEncoding.EncodeToString(raw[:])) + "." + domain
}

// sniDomain picks the suffix of the agent SNI: the decoy site's host name, else the admin host, else a generic
// ".com" (an IP-only or local installation has no domain of its own to borrow).
func (in instance) sniDomain() string {
	for _, h := range []string{hostOf(in.PublicURL), in.AdminHost} {
		if h != "" && h != "localhost" && !strings.HasSuffix(h, ".localhost") && net.ParseIP(strings.Trim(h, "[]")) == nil && strings.Contains(h, ".") {
			return h
		}
	}
	return "com"
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.TrimRight(p, "/"))
		}
	}
	return out
}

// devInstance is the fixed configuration of `serve --dev` (nothing is stored).
func devInstance() instance {
	return instance{
		AdminPrefix: "/",
		AdminListen: "127.0.0.1:8081",
		RPID:        "localhost",
		RPOrigins:   []string{"http://localhost:8081", "http://localhost:5173"},
	}
}

func (in instance) settings() map[string]string {
	return map[string]string{
		"public_url":   in.PublicURL,
		"admin_host":   in.AdminHost,
		"admin_prefix": in.AdminPrefix,
		"admin_listen": in.AdminListen,
		"rp_id":        in.RPID,
		"rp_origins":   strings.Join(in.RPOrigins, ","),
		"agent_sni":    in.AgentSNI,
		"sub_prefix":   in.SubPrefix,
	}
}

func loadInstance(ctx context.Context, st *store.Store) (instance, error) {
	get := func(k string) (string, error) { return st.Setting(ctx, k) }
	var in instance
	var err error
	if in.RPID, err = get("rp_id"); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return in, errNotConfigured
		}
		return in, err
	}
	origins, err := get("rp_origins")
	if err != nil {
		return in, err
	}
	if in.RPOrigins = splitList(origins); len(in.RPOrigins) == 0 {
		return in, errors.New("setting rp_origins is empty")
	}
	for k, dst := range map[string]*string{
		"public_url": &in.PublicURL, "admin_host": &in.AdminHost,
		"admin_prefix": &in.AdminPrefix, "admin_listen": &in.AdminListen,
	} {
		if *dst, err = get(k); err != nil {
			return in, err
		}
	}
	if in.AdminListen != "" && (in.AdminHost != "" || in.AdminPrefix != "/") {
		return in, errors.New("stored settings combine a separate admin listener with an admin host or prefix; delete the data dir and run `mistgate setup` again")
	}
	return in, ensureSecrets(ctx, st, &in)
}

// ensureSecrets reads the agent SNI and the subscription prefix, generating and storing
// them once for installations made before those existed (and for --dev, which stores
// nothing else).
func ensureSecrets(ctx context.Context, st *store.Store, in *instance) error {
	fill := map[string]string{}
	var err error
	if in.AgentSNI, err = st.Setting(ctx, "agent_sni"); errors.Is(err, store.ErrNotFound) {
		in.AgentSNI = newAgentSNI(in.sniDomain())
		fill["agent_sni"] = in.AgentSNI
	} else if err != nil {
		return err
	}
	if in.SubPrefix, err = st.Setting(ctx, "sub_prefix"); errors.Is(err, store.ErrNotFound) {
		in.SubPrefix = newSecretPrefix()
		fill["sub_prefix"] = in.SubPrefix
	} else if err != nil {
		return err
	}
	if len(fill) == 0 {
		return nil
	}
	return st.SetSettings(ctx, fill)
}
