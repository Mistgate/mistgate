package bridge

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

// An exit with a local dial reaches its in-process target, and learns each person's address when the entry passes it; an
// entry that does not pass it (an agent's bridge) never tells.
func TestLocalDialAndClientAddress(t *testing.T) {
	pki := newTestPKI(t)
	seen := make(chan netip.AddrPort, 4)
	localDial := func(_ context.Context, target plugin.BridgeTarget, client netip.AddrPort) (net.Conn, error) {
		seen <- client
		mine, theirs := net.Pipe()
		go func() {
			defer theirs.Close()
			_, _ = io.Copy(theirs, theirs)
		}()
		return mine, nil
	}
	entry, dialer, server, _ := newBridgeFixtureConfigured(t, 1, pki, 0, nil, func(d *Dialer) { d.SetLocalDial(localDial) })
	host, port := server.Listener.Addr().(*net.TCPAddr).IP.String(), uint16(server.Listener.Addr().(*net.TCPAddr).Port)
	spec := DialSpec{Dial: plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: host, Port: port, SNI: "msk1.example", PinSHA256: pki.pin,
		Path: "/private-bridge-path", Pool: 2, Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: LocalTargetNetwork}}}}
	dialer.Sync(context.Background(), []DialSpec{spec})
	waitFor(t, 10*time.Second, func() bool { return entry.Connected("nod_de1") }, "dialer did not connect")

	person := netip.MustParseAddrPort("[2001:db8::7]:40000")
	through := func() netip.AddrPort {
		t.Helper()
		mine, accepted := net.Pipe()
		go func() {
			_ = entry.Attach(context.Background(), "nod_de1", Client{Conn: accepted, RelayID: "rly_a", Addr: person})
		}()
		defer mine.Close()
		_ = mine.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := mine.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 4)
		if _, err := io.ReadFull(mine, got); err != nil || string(got) != "ping" {
			t.Fatalf("echo %q, %v", got, err)
		}
		select {
		case c := <-seen:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("the local dial was not used")
		}
		return netip.AddrPort{}
	}
	if got := through(); got.IsValid() {
		t.Fatalf("an agent's bridge told the exit %v", got)
	}
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}, Roots: pki.roots, PassClient: true}); err != nil {
		t.Fatal(err)
	}
	if got := through(); got != person {
		t.Fatalf("the exit learnt %v, want %v", got, person)
	}
}

// A dialer without a local dial refuses LocalTargetNetwork targets rather than dialling a port 0.
func TestLocalTargetNeedsALocalDial(t *testing.T) {
	d := plugin.BridgeDial{EntryNodeID: "nod_host", Host: "203.0.113.5", Port: 443, SNI: "de1.example.com", Path: "/p",
		Targets: []plugin.BridgeTarget{{RelayID: "sub", Network: LocalTargetNetwork}}}
	if _, code := normalizeDial(d, false); code != "target_not_local" {
		t.Fatalf("without a local dial: %q", code)
	}
	if _, code := normalizeDial(d, true); code != "" {
		t.Fatalf("with a local dial: %q", code)
	}
}
