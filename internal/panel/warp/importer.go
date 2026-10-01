package warp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// maxImportText bounds each imported file; a wgcf profile is under 500 bytes.
const maxImportText = 16 << 10

// profile is what the panel needs from wgcf-profile.conf.
type profile struct {
	privateKey           string
	addressV4, addressV6 string // with prefix
	mtu                  int
	peerPublicKey        string
	endpointHost         string
	endpointPort         uint16
}

// account is wgcf-account.toml.
type account struct{ accessToken, deviceID, licenseKey, privateKey string }

// Parse errors name the field, never its value: a private key must not end up in an error text.

// parseProfile reads wgcf-profile.conf: [Interface] PrivateKey, Address (once per family, or comma separated),
// MTU; the first [Peer] PublicKey and Endpoint. Other keys (DNS, AllowedIPs, PersistentKeepalive) are ignored.
func parseProfile(text string) (profile, error) {
	if len(text) > maxImportText {
		return profile{}, errors.New("profile is too large")
	}
	p := profile{mtu: 1280}
	section, peers, endpoint := "", 0, ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			section = strings.ToLower(strings.Trim(line, "[] \t"))
			if section == "peer" {
				peers++
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch {
		case section == "interface" && k == "privatekey":
			p.privateKey = v
		case section == "interface" && k == "address":
			for _, a := range strings.Split(v, ",") {
				pf, err := parseAddress(strings.TrimSpace(a))
				if err != nil {
					return profile{}, errors.New("Address is not an IP address")
				}
				if pf.Addr().Is4() {
					p.addressV4 = pf.String()
				} else {
					p.addressV6 = pf.String()
				}
			}
		case section == "interface" && k == "mtu":
			n, err := strconv.Atoi(v)
			if err != nil || n < 576 || n > 1500 {
				return profile{}, errors.New("MTU must be 576-1500")
			}
			p.mtu = n
		case section == "peer" && peers == 1 && k == "publickey":
			p.peerPublicKey = v
		case section == "peer" && peers == 1 && k == "endpoint":
			endpoint = v
		}
	}
	switch {
	case !validKey(p.privateKey):
		return profile{}, errors.New("PrivateKey is missing or not a WireGuard key")
	case p.addressV4 == "":
		return profile{}, errors.New("Address has no IPv4 address")
	case !validKey(p.peerPublicKey):
		return profile{}, errors.New("[Peer] PublicKey is missing or not a WireGuard key")
	case endpoint == "":
		return profile{}, errors.New("[Peer] Endpoint is missing")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil { // no port: the WARP default
		host, port = strings.Trim(endpoint, "[]"), "2408"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host == "" {
		return profile{}, errors.New("[Peer] Endpoint is not host:port")
	}
	p.endpointHost, p.endpointPort = host, uint16(n)
	return p, nil
}

// parseAddress accepts "172.16.0.2/32" and a bare address (host prefix added).
func parseAddress(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// parseAccount reads wgcf-account.toml: flat key = 'value' lines (access_token, device_id, license_key, private_key).
func parseAccount(text string) (account, error) {
	if len(text) > maxImportText {
		return account{}, errors.New("account file is too large")
	}
	var a account
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == '[' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		switch strings.TrimSpace(k) {
		case "access_token":
			a.accessToken = v
		case "device_id":
			a.deviceID = v
		case "license_key":
			a.licenseKey = v
		case "private_key":
			a.privateKey = v
		}
	}
	if (a.accessToken == "") != (a.deviceID == "") {
		return account{}, errors.New("account file needs both access_token and device_id")
	}
	return a, nil
}

// resolver returns the addresses of a host name; the panel resolves an endpoint name once, at import.
type resolver func(ctx context.Context, host string) ([]netip.Addr, error)

func systemResolver(ctx context.Context, host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

// endpointV4 is the IPv4 literal of the profile's endpoint: the literal itself, or the first IPv4 answer for a name.
// An address that cannot be a WARP relay (unspecified, loopback, multicast) is refused.
func endpointV4(ctx context.Context, res resolver, host string) (string, error) {
	a, err := netip.ParseAddr(host)
	if err != nil {
		addrs, err := res(ctx, host)
		if err != nil {
			return "", errors.New("the endpoint name does not resolve")
		}
		for _, x := range addrs {
			if x.Unmap().Is4() {
				a, err = x.Unmap(), nil
				break
			}
		}
		if !a.IsValid() {
			return "", errors.New("the endpoint name has no IPv4 address")
		}
	}
	a = a.Unmap()
	switch {
	case !a.Is4():
		return "", errors.New("the endpoint needs an IPv4 address")
	case a.IsUnspecified() || a.IsLoopback() || a.IsMulticast():
		return "", fmt.Errorf("the endpoint address %s cannot be a WARP relay", a)
	}
	return a.String(), nil
}
