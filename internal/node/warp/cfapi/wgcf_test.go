package cfapi

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

const (
	testPriv = "gCAG3i7yU6l0oIq9+qX0l4V6cVQwIp5N0k3oJb0k3Fs=" // 32 bytes of nothing in particular
	testPub  = fakePeerKey
)

func profile(endpoint string) string {
	return "[Interface]\nPrivateKey = " + testPriv + "\nAddress = 172.16.0.2/32, 2001:db8:110::2/128\n" +
		"DNS = 1.1.1.1, 1.0.0.1\nMTU = 1280\n[Peer]\nPublicKey = " + testPub + "\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = " + endpoint + "\nPersistentKeepalive = 25\n"
}

const accountTOML = "access_token = 'tok-secret-0001'\ndevice_id = 'dev-0001'\nlicense_key = 'lic-secret-0002'\nprivate_key = '" + testPriv + "'\n"

func TestParseWgcfHostnameEndpoint(t *testing.T) {
	a, err := ParseWgcf(profile("engage.example.com:2408"), accountTOML)
	if err != nil {
		t.Fatal(err)
	}
	if a.PrivateKey.Reveal() != testPriv || a.PeerPublicKey != testPub || a.AddressV4 != "172.16.0.2/32" || a.AddressV6 != "2001:db8:110::2/128" || a.MTU != 1280 {
		t.Fatalf("%+v", a)
	}
	if a.ID != "dev-0001" || a.Token.Reveal() != "tok-secret-0001" || a.License.Reveal() != "lic-secret-0002" {
		t.Fatalf("account: %+v", a)
	}
	if a.EndpointV4 != "" || a.EndpointHost != "engage.example.com:2408" || fmt.Sprint(a.Ports) != "[2408 500 1701 4500]" {
		t.Fatalf("endpoint: %+v", a)
	}
	// the host name is resolved once by the caller; v6 only on request
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "engage.example.com" {
			return nil, errors.New("unexpected host " + host)
		}
		return []netip.Addr{netip.MustParseAddr("2001:db8::a29f:c001"), netip.MustParseAddr("198.51.100.7")}, nil
	}
	b := a
	if err := b.ResolveEndpoint(context.Background(), lookup, false); err != nil || b.EndpointV4 != "198.51.100.7" || b.EndpointV6 != "" {
		t.Fatalf("%+v %v", b, err)
	}
	c := a
	if err := c.ResolveEndpoint(context.Background(), lookup, true); err != nil || c.EndpointV6 != "2001:db8::a29f:c001" {
		t.Fatalf("%+v %v", c, err)
	}
	// already a literal: nothing to resolve
	if err := b.ResolveEndpoint(context.Background(), func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("called") }, true); err != nil {
		t.Fatal(err)
	}
	d := a
	if err := d.ResolveEndpoint(context.Background(), func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("nxdomain") }, false); err == nil {
		t.Fatal("resolution failure ignored")
	}
}

func TestParseWgcfLiteralEndpointWithoutToken(t *testing.T) {
	a, err := ParseWgcf(profile("198.51.100.7:500"), "")
	if err != nil {
		t.Fatal(err)
	}
	if a.EndpointV4 != "198.51.100.7" || fmt.Sprint(a.Ports) != "[500 2408 1701 4500]" || a.Token != "" || a.ID != "" {
		t.Fatalf("%+v", a)
	}
	a, err = ParseWgcf(profile("[2001:db8::1]:2408"), "")
	if err != nil || a.EndpointV6 != "2001:db8::1" || a.EndpointV4 != "" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestParseWgcfTomlQuotesAndComments(t *testing.T) {
	toml := "# wgcf\naccess_token = \"t1\"\ndevice_id = 'd1'\n\nlicense_key='l1'\n"
	a, err := ParseWgcf(profile("198.51.100.7:2408"), toml)
	if err != nil || a.Token.Reveal() != "t1" || a.ID != "d1" || a.License.Reveal() != "l1" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestParseWgcfRejects(t *testing.T) {
	good := profile("198.51.100.7:2408")
	bad := map[string]string{
		"empty":           "",
		"no peer":         strings.Split(good, "[Peer]")[0],
		"two peers":       good + "[Peer]\nPublicKey = " + testPub + "\nEndpoint = 198.51.100.8:2408\n",
		"bad private key": strings.Replace(good, testPriv, "AAAA", 1),
		"bad peer key":    strings.Replace(good, "PublicKey = "+testPub, "PublicKey = zz", 1),
		"no v4":           strings.Replace(good, "172.16.0.2/32, ", "", 1),
		"bad endpoint":    strings.Replace(good, "198.51.100.7:2408", "198.51.100.7", 1),
		"port 0":          strings.Replace(good, "198.51.100.7:2408", "198.51.100.7:0", 1),
		"bad mtu":         strings.Replace(good, "MTU = 1280", "MTU = 12", 1),
		"not ini":         "hello world\n",
	}
	for name, conf := range bad {
		_, err := ParseWgcf(conf, "")
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), testPriv) {
			t.Errorf("%s: error leaks the private key", name)
		}
	}
	// an account file of another profile
	other := strings.Replace(accountTOML, testPriv, "b3RoZXJrZXlvdGhlcmtleW90aGVya2V5b3RoZXI=", 1)
	if _, err := ParseWgcf(good, other); err == nil || strings.Contains(err.Error(), "b3RoZXJrZXl") {
		t.Fatalf("mismatching account accepted or leaked: %v", err)
	}
	if _, err := ParseWgcf(good, "access_token = 'x'\ndevice_id = 'a/b'\n"); err == nil {
		t.Fatal("bad device id accepted")
	}
	if _, err := ParseWgcf(good, "garbage line\n"); err == nil {
		t.Fatal("garbage toml accepted")
	}
}
