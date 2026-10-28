//go:build linux

package hostctl

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// relayHost is testHost plus a fake /proc/sys for forwarding and a runner that answers the two looks at the FORWARD policy.
func relayHost(t *testing.T) (*linuxHost, *[]call, *string, *string) {
	t.Helper()
	h, calls := testHost(t)
	h.rly.addrs = func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("198.51.100.1")}, nil }
	ruleset, iptables := new(string), new(string)
	base := h.run
	h.run = func(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "nft" && joined == "list ruleset":
			*calls = append(*calls, call{stdin, name, joined})
			return []byte(*ruleset), nil
		case name == "iptables" && joined == "-S FORWARD":
			*calls = append(*calls, call{stdin, name, joined})
			return []byte(*iptables), nil
		}
		return base(ctx, stdin, name, args...)
	}
	p := filepath.Join(h.procSys, "net/ipv4/ip_forward")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return h, calls, ruleset, iptables
}

func nftScripts(calls []call) []string {
	var out []string
	for _, c := range calls {
		if c.name == "nft" && c.args == "-f -" {
			out = append(out, c.stdin)
		}
	}
	return out
}

const (
	dockerRuleset = `table ip filter {
	chain FORWARD {
		type filter hook forward priority filter; policy drop;
	}
}
`
	ownRuleset = `table inet mistgate_awg {
	chain isolate {
		type filter hook forward priority filter; policy drop;
	}
}
table inet mistgate_relay {
	chain pass {
		type filter hook forward priority filter; policy drop;
	}
}
table ip nat {
	chain PREROUTING {
		type nat hook prerouting priority dstnat; policy accept;
	}
}
`
)

func oneForward() []Forward { return []Forward{fwd("rly_a", "tcp", 8443, "192.0.2.10", 443)} }

func TestSetRelaysInstallsOnceAndEnablesForwarding(t *testing.T) {
	h, calls, _, _ := relayHost(t)
	ctx := context.Background()
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatal(err)
	}
	if got := nftScripts(*calls); len(got) != 1 || !strings.Contains(got[0], "table inet mistgate_relay {") || !strings.Contains(got[0], "dnat ip to 192.0.2.10:443") {
		t.Fatalf("scripts: %q", got)
	}
	if readSys(t, h, "net/ipv4/ip_forward") != "1" {
		t.Error("IPv4 forwarding is not on")
	}
	// The same set again: nothing is sent, the counters keep counting.
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatal(err)
	}
	if got := nftScripts(*calls); len(got) != 1 {
		t.Errorf("a second identical call reached nft: %d scripts", len(got))
	}
	// Another set replaces the table as a whole.
	if err := h.SetRelays(ctx, append(oneForward(), fwd("rly_b", "udp", 9443, "198.51.100.20", 8443))); err != nil {
		t.Fatal(err)
	}
	if got := nftScripts(*calls); len(got) != 2 || !strings.Contains(got[1], "rly_b") || !strings.Contains(got[1], "delete table inet mistgate_relay") {
		t.Errorf("scripts: %q", got)
	}
	// No forwards: the table is deleted and a further empty call does nothing.
	if err := h.SetRelays(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got := nftScripts(*calls)
	if len(got) != 3 || strings.Contains(got[2], "table inet mistgate_relay {") {
		t.Fatalf("scripts: %q", got)
	}
	if err := h.SetRelays(ctx, nil); err != nil || len(nftScripts(*calls)) != 3 {
		t.Errorf("empty again: %v, %d scripts", err, len(nftScripts(*calls)))
	}
}

func TestSetRelaysLeavesForwardingAloneWithoutForwards(t *testing.T) {
	h, _, _, _ := relayHost(t)
	if err := h.SetRelays(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if readSys(t, h, "net/ipv4/ip_forward") != "0" {
		t.Error("a node without forwards had its forwarding switched on")
	}
}

func TestSetRelaysFailsClosedOnDropPolicy(t *testing.T) {
	for name, set := range map[string]func(ruleset, iptables *string){
		"docker, nft":   func(r, _ *string) { *r = dockerRuleset },
		"ufw, iptables": func(_, i *string) { *i = "-P FORWARD DROP\n-A FORWARD -j ufw-before-forward\n" },
	} {
		t.Run(name, func(t *testing.T) {
			h, calls, ruleset, iptables := relayHost(t)
			set(ruleset, iptables)
			err := h.SetRelays(context.Background(), oneForward())
			if !errors.Is(err, ErrForwardDrop) {
				t.Fatalf("err = %v, want ErrForwardDrop", err)
			}
			for _, s := range nftScripts(*calls) {
				if strings.Contains(s, "table inet mistgate_relay {") {
					t.Fatalf("a table was installed behind a dropping FORWARD chain:\n%s", s)
				}
			}
			if readSys(t, h, "net/ipv4/ip_forward") != "0" {
				t.Error("forwarding was switched on for nothing")
			}
		})
	}
}

func TestSetRelaysRemovesItsTableWhenDockerAppears(t *testing.T) {
	h, calls, ruleset, _ := relayHost(t)
	now := time.Unix(1700000000, 0)
	h.rly.now = func() time.Time { return now }
	ctx := context.Background()
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatal(err)
	}
	// Docker arrives. Within the recheck period nothing changes; after it the forward fails closed and the table goes.
	*ruleset = dockerRuleset
	now = now.Add(time.Minute)
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatalf("within the recheck period: %v", err)
	}
	now = now.Add(relayRecheck)
	if err := h.SetRelays(ctx, oneForward()); !errors.Is(err, ErrForwardDrop) {
		t.Fatalf("after the recheck period: %v", err)
	}
	got := nftScripts(*calls)
	if last := got[len(got)-1]; strings.Contains(last, "table inet mistgate_relay {") || !strings.Contains(last, "delete table inet mistgate_relay") {
		t.Fatalf("the table is still there:\n%s", last)
	}
	// Docker goes: the forward comes back.
	*ruleset = ""
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatal(err)
	}
	got = nftScripts(*calls)
	if !strings.Contains(got[len(got)-1], "table inet mistgate_relay {") {
		t.Error("the forward did not come back")
	}
}

func TestForwardDropIgnoresOurOwnTables(t *testing.T) {
	h, _, ruleset, _ := relayHost(t)
	*ruleset = ownRuleset
	if why, drops := h.forwardDrop(context.Background()); drops {
		t.Errorf("our own tables are not a reason: %s", why)
	}
	*ruleset = ownRuleset + dockerRuleset
	if why, drops := h.forwardDrop(context.Background()); !drops || !strings.Contains(why, "filter") {
		t.Errorf("a foreign drop was not found: %q %v", why, drops)
	}
}

func TestSetRelaysForwardingFailureFailsClosed(t *testing.T) {
	h, calls, _, _ := relayHost(t)
	// A /proc/sys that cannot be written (a container): here the file is a directory, which fails for root too.
	p := filepath.Join(h.procSys, "net/ipv4/ip_forward")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	err := h.SetRelays(context.Background(), oneForward())
	if err == nil || !strings.HasPrefix(err.Error(), "forwarding: ") {
		t.Fatalf("err = %v", err)
	}
	for _, s := range nftScripts(*calls) {
		if strings.Contains(s, "table inet mistgate_relay {") {
			t.Fatalf("a table was installed without forwarding:\n%s", s)
		}
	}
}

func TestSetRelaysRefusesWhatCannotBeRendered(t *testing.T) {
	h, calls, _, _ := relayHost(t)
	err := h.SetRelays(context.Background(), []Forward{fwd("rly_a", "tcp", 8443, "10.0.0.1", 443)})
	if err == nil || len(nftScripts(*calls)) != 0 {
		t.Errorf("a private target reached nft: %v", err)
	}
}

func TestCleanupDeletesTheRelayTable(t *testing.T) {
	h, calls, _, _ := relayHost(t)
	ctx := context.Background()
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatal(err)
	}
	before := len(nftScripts(*calls))
	if err := h.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	for _, s := range nftScripts(*calls)[before:] {
		if strings.Contains(s, "delete table inet mistgate_relay") && !strings.Contains(s, "table inet mistgate_relay {") {
			deleted = true
		}
	}
	if !deleted {
		t.Error("Cleanup did not delete the relay table")
	}
	// What was installed is forgotten: the same set is installed again, not skipped.
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatal(err)
	}
	if got := nftScripts(*calls); !strings.Contains(got[len(got)-1], "table inet mistgate_relay {") {
		t.Error("the set was not installed again after Cleanup")
	}
}

// The table matches the node's own addresses. When they change the table is replaced at once, not after the five-minute look;
// when they cannot be read, or there is none, nothing is installed and a table that was there goes.
func TestSetRelaysFollowsTheAddressesOfTheNode(t *testing.T) {
	h, calls, _, _ := relayHost(t)
	addrs := []netip.Addr{netip.MustParseAddr("198.51.100.1")}
	var readErr error
	h.rly.addrs = func() ([]netip.Addr, error) { return addrs, readErr }
	ctx := context.Background()
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatal(err)
	}
	if got := nftScripts(*calls); len(got) != 1 || !strings.Contains(got[0], "elements = { 198.51.100.1 }") || !strings.Contains(got[0], "ip daddr @local4 tcp dport 8443") {
		t.Fatalf("scripts: %q", got)
	}
	if err := h.SetRelays(ctx, oneForward()); err != nil || len(nftScripts(*calls)) != 1 {
		t.Fatalf("the same addresses reinstalled the table: %v", err)
	}
	addrs = []netip.Addr{netip.MustParseAddr("198.51.100.1"), netip.MustParseAddr("192.0.2.5")}
	if err := h.SetRelays(ctx, oneForward()); err != nil {
		t.Fatal(err)
	}
	if got := nftScripts(*calls); len(got) != 2 || !strings.Contains(got[1], "elements = { 192.0.2.5, 198.51.100.1 }") {
		t.Fatalf("a new address did not reach the table: %q", got)
	}

	for name, fail := range map[string]func(){
		"unreadable": func() { readErr = errors.New("netlink refused") },
		"none":       func() { readErr, addrs = nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			readErr, addrs = nil, []netip.Addr{netip.MustParseAddr("198.51.100.1")}
			if err := h.SetRelays(ctx, oneForward()); err != nil {
				t.Fatal(err)
			}
			before := len(nftScripts(*calls))
			fail()
			if err := h.SetRelays(ctx, oneForward()); err == nil {
				t.Fatal("installed without knowing the addresses of the node")
			}
			got := nftScripts(*calls)
			if len(got) != before+1 || strings.Contains(got[len(got)-1], "table inet mistgate_relay {") || !strings.Contains(got[len(got)-1], "delete table inet mistgate_relay") {
				t.Fatalf("the old table is still there: %q", got[before:])
			}
		})
	}
}
