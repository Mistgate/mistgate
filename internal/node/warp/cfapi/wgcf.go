package cfapi

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

var defaultPorts = []uint16{2408, 500, 1701, 4500}

// ParseWgcf builds an Account from the text of wgcf-profile.conf (required) and, when given, wgcf-account.toml
// (adds the device id, the access token and the license key, which enable Fetch and Delete; the client id, and so
// the "reserved" bytes, are then read with Fetch). An endpoint that is a host name stays in EndpointHost with
// empty EndpointV4/V6: the caller resolves it once (ResolveEndpoint) and stores the literal. Errors never contain
// key material.
func ParseWgcf(profileConf, accountTOML string) (Account, error) {
	a, err := parseProfile(profileConf)
	if err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(accountTOML) == "" {
		return a, nil
	}
	kv, err := parseTOML(accountTOML)
	if err != nil {
		return Account{}, err
	}
	if k := kv["private_key"]; k != "" && k != a.PrivateKey.Reveal() {
		return Account{}, errors.New("wgcf-account.toml belongs to another profile (private keys differ)")
	}
	a.ID, a.Token, a.License = kv["device_id"], Secret(kv["access_token"]), Secret(kv["license_key"])
	if a.ID != "" {
		if err := checkID(a.ID); err != nil {
			return Account{}, errors.New("wgcf-account.toml: bad device_id")
		}
	}
	return a, nil
}

func parseProfile(conf string) (Account, error) {
	var (
		a       Account
		section string
		peers   int
		port    uint16
	)
	a.MTU = 1280
	sc := bufio.NewScanner(strings.NewReader(conf))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if section == "peer" {
				peers++
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return Account{}, fmt.Errorf("profile line %d: not a key = value pair", n)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch {
		case section == "interface" && k == "privatekey":
			a.PrivateKey = Secret(v)
		case section == "interface" && k == "address":
			for _, s := range strings.Split(v, ",") {
				pf, err := netip.ParsePrefix(strings.TrimSpace(s))
				if err != nil {
					return Account{}, fmt.Errorf("profile line %d: bad address", n)
				}
				if pf.Addr().Unmap().Is4() {
					a.AddressV4 = pf.String()
				} else {
					a.AddressV6 = pf.String()
				}
			}
		case section == "interface" && k == "mtu":
			m, err := strconv.Atoi(v)
			if err != nil || m < 576 || m > 1500 {
				return Account{}, fmt.Errorf("profile line %d: bad MTU", n)
			}
			a.MTU = uint16(m)
		case section == "peer" && k == "publickey":
			a.PeerPublicKey = v
		case section == "peer" && k == "endpoint":
			a.EndpointHost = v
			host, ps, err := splitHostPort(v)
			if err != nil {
				return Account{}, fmt.Errorf("profile line %d: bad endpoint", n)
			}
			p, err := strconv.Atoi(ps)
			if err != nil || p < 1 || p > 65535 {
				return Account{}, fmt.Errorf("profile line %d: bad endpoint port", n)
			}
			port = uint16(p)
			if ip, err := netip.ParseAddr(host); err == nil {
				ip = ip.Unmap()
				if ip.Is4() {
					a.EndpointV4 = ip.String()
				} else {
					a.EndpointV6 = ip.String()
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Account{}, errors.New("profile is not readable text")
	}
	switch {
	case peers != 1:
		return Account{}, fmt.Errorf("profile has %d [Peer] sections, want 1", peers)
	case !validKey(a.PrivateKey.Reveal()):
		return Account{}, errors.New("profile has no valid PrivateKey")
	case !validKey(a.PeerPublicKey):
		return Account{}, errors.New("profile has no valid peer PublicKey")
	case a.AddressV4 == "":
		return Account{}, errors.New("profile has no IPv4 Address")
	case port == 0:
		return Account{}, errors.New("profile has no Endpoint")
	}
	a.Ports = uniquePorts(append([]uint16{port}, defaultPorts...))
	return a, nil
}

func validKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

func uniquePorts(in []uint16) []uint16 {
	seen := map[uint16]bool{}
	var out []uint16
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func splitHostPort(s string) (host, port string, err error) {
	if strings.HasPrefix(s, "[") {
		i := strings.Index(s, "]:")
		if i < 0 {
			return "", "", errors.New("bad endpoint")
		}
		return s[1:i], s[i+2:], nil
	}
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || strings.Count(s, ":") != 1 {
		return "", "", errors.New("bad endpoint")
	}
	return s[:i], s[i+1:], nil
}

// parseTOML reads the flat "key = 'value'" lines wgcf-account.toml consists of.
func parseTOML(s string) (map[string]string, error) {
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("account line %d: not a key = value pair", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
			v = v[1 : len(v)-1]
		}
		kv[strings.TrimSpace(k)] = v
	}
	return kv, nil
}

// ResolveEndpoint turns a host-name endpoint into IP literals once, through lookup (the panel's resolver): the IPv4
// address is taken, and the IPv6 one only when withV6 is set (new accounts often have no working IPv6). A profile
// whose endpoint already was a literal is left alone.
func (a *Account) ResolveEndpoint(ctx context.Context, lookup func(ctx context.Context, host string) ([]netip.Addr, error), withV6 bool) error {
	if a.EndpointV4 != "" || a.EndpointV6 != "" {
		return nil
	}
	host, _, err := splitHostPort(a.EndpointHost)
	if err != nil {
		return err
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve endpoint: %w", err)
	}
	for _, ip := range addrs {
		ip = ip.Unmap()
		switch {
		case ip.Is4() && a.EndpointV4 == "":
			a.EndpointV4 = ip.String()
		case ip.Is6() && withV6 && a.EndpointV6 == "":
			a.EndpointV6 = ip.String()
		}
	}
	if a.EndpointV4 == "" && a.EndpointV6 == "" {
		return errors.New("resolve endpoint: no usable address")
	}
	return nil
}
