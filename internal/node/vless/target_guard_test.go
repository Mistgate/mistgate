package vless

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

// fakeNet replaces the resolver and the node's addresses for one test: nothing here touches the network.
func fakeNet(t *testing.T, names map[string][]string, own ...string) {
	t.Helper()
	oldLookup, oldIfaces := lookupTargetAddrs, interfaceAddrs
	t.Cleanup(func() { lookupTargetAddrs, interfaceAddrs = oldLookup, oldIfaces })
	lookupTargetAddrs = func(_ context.Context, host string) ([]netip.Addr, error) {
		list, ok := names[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range list {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
	interfaceAddrs = func() ([]netip.Addr, error) {
		var out []netip.Addr
		for _, s := range own {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func TestCheckTargetPublic(t *testing.T) {
	fakeNet(t, map[string][]string{
		"shop.example.com":    {"192.0.2.10", "2001:db8::10"},
		"inner.example.com":   {"10.1.2.3"},
		"loop.example.com":    {"127.0.0.1"},
		"mixed.example.com":   {"192.0.2.10", "192.168.0.5"}, // one such address is enough: the dial may pick it
		"cgnat.example.com":   {"100.64.1.1"},
		"link.example.com":    {"169.254.169.254"},
		"v6loop.example.com":  {"::1"},
		"ula.example.com":     {"fd00::1"},
		"mapped.example.com":  {"::ffff:127.0.0.1"},
		"anyaddr.example.com": {"0.0.0.0"},
		"self.example.com":    {"192.0.2.77"}, // a public address that is the node's own
	}, "192.0.2.77", "fe80::1%eth0")
	for _, tc := range []struct {
		target string
		local  bool
	}{
		{"shop.example.com:443", false},
		{"192.0.2.10:443", false},
		{"unknown.example.com:443", false}, // not resolved: the probe says "unreachable"
		{"inner.example.com:443", true},
		{"loop.example.com:8443", true},
		{"mixed.example.com:443", true},
		{"cgnat.example.com:443", true},
		{"link.example.com:443", true},
		{"v6loop.example.com:443", true},
		{"ula.example.com:443", true},
		{"mapped.example.com:443", true},
		{"anyaddr.example.com:443", true},
		{"self.example.com:443", true},
		{"127.0.0.1:443", true},
		{"[::1]:443", true},
		{"192.0.2.77:443", true},
		{"10.0.0.1:443", true},
	} {
		err := checkTargetPublic(context.Background(), tc.target)
		if tc.local != (err != nil) || err != nil && err != errTargetLocal {
			t.Errorf("%s: err = %v, want local = %v", tc.target, err, tc.local)
		}
	}
}

// A target set on the node that leads to this machine or a private network keeps the inbound from starting, with a code the panel
// words; the same address as the target of a profile (a site next to the node) still works.
func TestNodeTargetThatIsLocalDoesNotStart(t *testing.T) {
	fakeNet(t, map[string][]string{"inner.example.com": {"10.1.2.3"}})
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "xhttp")
	spec.Settings = []byte(strings.Replace(string(spec.Settings), f.targetAddr, "inner.example.com:443", 1))
	spec.Settings = []byte(strings.Replace(string(spec.Settings), `"localhost"`, `"inner.example.com"`, 1))
	spec.TLS = plugin.TLS{ServerName: "inner.example.com"}
	if _, err := f.engine.Apply(context.Background(), spec, nil); err == nil {
		t.Fatal("Apply started an inbound whose target set on the node is a private address")
	}
	health := f.engine.Health()
	if len(health) != 1 || health[0].State != plugin.RunFailed || health[0].Detail != "reality_target: local" {
		t.Fatalf("health = %+v", health)
	}

	// the same inbound with the target in the profile (no name in the TLS block) starts, as before
	local := f.spec("node-b", "xhttp")
	if _, err := f.engine.Apply(context.Background(), local, nil); err != nil {
		t.Fatal(fmt.Errorf("a profile's target next to the node: %w", err))
	}
}
