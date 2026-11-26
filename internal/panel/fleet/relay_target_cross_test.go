package fleet

import (
	"cmp"
	"net"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	accesssvc "github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The fleet module makes a relay check and names its target, the health module writes a monitor under the target's key, and the
// access module shows the relay and drops the check and the monitor on any mismatch of the two. All three must compute the same
// target for the same exit, or a healthy relay silently loses its evidence.
func TestRelayTargetAgreesAcrossFleetHealthAndAccess(t *testing.T) {
	for _, tc := range []struct {
		name, address    string
		override, public uint16
		port             uint16 // the destination port the entry forwards to
	}{
		{"port_override", addrExit, 24443, 0, 24443},
		{"public_port", addrExit, 19443, 443, 443},
		{"ipv6_exit", "2001:db8::10", 0, 0, 8443},
		{"ipv6_exit_with_port_override", "2001:db8::10", 24443, 0, 24443},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCheckEnv(t, "entry", "exit", "s1")
			x.exec(`UPDATE node SET address = ? WHERE id = ?`, tc.address, x.exit.nodeID)
			x.exec(`UPDATE inbound SET port_override = NULLIF(?, 0), public_port = ?, tls_server_name_override = 'front.example.com' WHERE id = 'inb_vless'`, tc.override, tc.public)
			x.tcpRelay(true)

			// the real check, as the owner's check of the relay runs it
			res := x.start("rly_t")
			tcpRun(x.ec, "ours", tc.port)
			tcpRun(x.c1, "ours", 7443)
			check := result(t, res)
			if want := net.JoinHostPort(tc.address, strconv.Itoa(int(tc.port))); check.To != want {
				t.Fatalf("the check's target = %q, want %q", check.To, want)
			}

			// the monitor, written under the key the health round computes for the same exit inbound
			link, err := x.st.RelayLink(x.ctx, "rly_t")
			if err != nil {
				t.Fatal(err)
			}
			exit, err := x.st.Node(x.ctx, x.exit.nodeID)
			if err != nil {
				t.Fatal(err)
			}
			row, err := x.st.FleetInbound(x.ctx, "inb_vless")
			if err != nil {
				t.Fatal(err)
			}
			spec, err := x.f.buildSpec(exit, row)
			if err != nil {
				t.Fatal(err)
			}
			if want := cmp.Or(tc.override, uint16(8443)); spec.Listen.Port != want {
				t.Fatalf("the exit's server port = %d, want %d", spec.Listen.Port, want)
			}
			_, key := link.Target(row, exit.Address, spec.Listen.Port)
			if err := x.st.SetRelayMonitorCheck(x.ctx, "rly_t", store.RelayMonitorCheck{AtUnix: time.Now().Unix(), OK: true, TargetKey: key}); err != nil {
				t.Fatal(err)
			}

			acc, err := accesssvc.New(x.st, x.v, x.f.reg, x.f, nil, accesssvc.Config{})
			if err != nil {
				t.Fatal(err)
			}
			list, err := acc.ListRelays(x.ctx, connect.NewRequest(&adminv1.ListRelaysRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			var got *adminv1.Relay
			for _, r := range list.Msg.Relays {
				if r.Id == "rly_t" {
					got = r
				}
			}
			if got == nil {
				t.Fatalf("relay missing from the list: %+v", list.Msg.Relays)
			}
			if got.Check == nil || got.Monitor == nil || got.ExitPort != uint32(tc.port) {
				t.Fatalf("access dropped the evidence of a relay whose target did not change: check=%v monitor=%v exit_port=%d (want %d)",
					got.Check, got.Monitor, got.ExitPort, tc.port)
			}
		})
	}
}
