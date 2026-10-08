package agent

import (
	"context"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/torrentguard"
	"github.com/mistgate/mistgate/internal/plugin"
	"net/netip"
)

func TestEngineTorrentEventsAreRateLimitedPerUserAndInbound(t *testing.T) {
	a := &Agent{out: newOutbox(32, 1<<20), torrentEvents: map[string]time.Time{}}
	first := engine.Event{
		Code: "torrent_attempt", InboundID: "in-1", Warning: true,
		Params: map[string]string{"user_id": "user-1", "protocol": "tcp", "destination": "198.51.100.1:6881"},
	}
	a.engineEvent(first)
	first.Params["destination"] = "198.51.100.2:6881"
	a.engineEvent(first) // A new destination by the same user is still one recent alert.
	second := first
	second.Params = map[string]string{"user_id": "user-2", "protocol": "udp", "destination": "198.51.100.1:6881"}
	a.engineEvent(second)
	second.InboundID = "in-2"
	a.engineEvent(second)

	queued := a.out.after(0)
	if len(queued) != 3 {
		t.Fatalf("queued events = %d, want one per inbound and user", len(queued))
	}
	for _, msg := range queued {
		event := msg.GetEvent()
		if event == nil || event.Code != "torrent_attempt" || event.Severity != pb.Severity_SEVERITY_WARNING {
			t.Fatalf("unexpected queued event: %+v", msg)
		}
		// Who, never where to: an engine's destination or client address does not leave the node.
		if _, ok := event.Params["destination"]; ok || event.Params["user_id"] == "" || event.Params["protocol"] == "" {
			t.Fatalf("event params = %v", event.Params)
		}
	}

	// Without a known user every detection of an inbound shares one key, whatever address or destination it came from.
	b := &Agent{out: newOutbox(32, 1<<20), torrentEvents: map[string]time.Time{}}
	for _, ip := range []string{"10.66.4.2", "10.66.4.3"} {
		b.engineEvent(engine.Event{Code: "torrent_attempt", InboundID: "in-1", Warning: true,
			Params: map[string]string{"protocol": "udp", "client_ip": ip, "destination": "198.51.100.1:6881"}})
	}
	if queued := b.out.after(0); len(queued) != 1 || len(queued[0].GetEvent().Params) != 1 {
		t.Fatalf("unattributed events = %v", queued)
	}
}

func TestEngineTorrentEventKeepsEvidenceAndPortButNoAddress(t *testing.T) {
	a := &Agent{out: newOutbox(32, 1<<20), torrentEvents: map[string]time.Time{}}
	a.engineEvent(engine.Event{Code: "torrent_attempt", InboundID: "in-1", Warning: true, Params: map[string]string{
		"user_id": "user-1", "protocol": "udp", "torrent_protocol": "bittorrent_tracker",
		"evidence": "tracker_connect", "dst_port": "6969", "destination": "198.51.100.1:6969", "client_ip": "10.66.4.2",
	}})
	queued := a.out.after(0)
	if len(queued) != 1 {
		t.Fatalf("queued events = %d", len(queued))
	}
	params := queued[0].GetEvent().Params
	if params["evidence"] != "tracker_connect" || params["dst_port"] != "6969" || len(params) != 5 {
		t.Fatalf("event params = %v", params)
	}
}

type torrentGuardTestHost struct {
	*tunHost
	ifaces []string
	cb     func(hostctl.TorrentDetection)
}

func (h *torrentGuardTestHost) SetTorrentGuard(_ context.Context, ifaces []string, cb func(hostctl.TorrentDetection)) error {
	h.ifaces = append([]string(nil), ifaces...)
	h.cb = cb
	return nil
}

func TestSyncTorrentGuardScopesAWGAndResolvesClient(t *testing.T) {
	for _, tc := range []struct {
		name       string
		creds      map[string]plugin.UserCred
		wantUserID string
	}{
		{
			name: "unique client address",
			creds: map[string]plugin.UserCred{
				"cred-1": {CredID: "cred-1", UserID: "user-1", Data: []byte(`{"allowed_ips":["10.66.4.2/32"]}`)},
			},
			wantUserID: "user-1",
		},
		{
			name: "ambiguous client address is not attributed",
			creds: map[string]plugin.UserCred{
				"cred-1": {CredID: "cred-1", UserID: "user-1", Data: []byte(`{"allowed_ips":["10.66.4.2/32"]}`)},
				"cred-2": {CredID: "cred-2", UserID: "user-2", Data: []byte(`{"allowed_ips":["10.66.4.2/32"]}`)},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &tunHost{fakeHost: &fakeHost{}, ord: &order{}}
			host := &torrentGuardTestHost{tunHost: base}
			a := &Agent{
				host: host, out: newOutbox(16, 1<<20), torrentEvents: map[string]time.Time{},
			}
			a.settings.Store(&pb.NodeSettings{TorrentBlockerEnabled: true})
			next := &model{inbounds: map[string]*inbound{
				"awg-1": {spec: plugin.InboundSpec{
					ID: "awg-1", Protocol: awg.Protocol, Enabled: true,
					Listen: plugin.Listen{Network: "udp", Port: 51820},
					Tunnel: plugin.Tunnel{AddrV4: netip.MustParsePrefix("10.66.4.1/24"), MTU: 1280},
				}, creds: tc.creds},
			}}
			a.syncTorrentGuard(context.Background(), next, []hostctl.Tunnel{{Iface: "mgawg51820"}}, true)
			if len(host.ifaces) != 1 || host.ifaces[0] != "mgawg51820" {
				t.Fatalf("guard interfaces = %v", host.ifaces)
			}
			if host.cb == nil {
				t.Fatal("guard callback was not installed")
			}
			host.cb(hostctl.TorrentDetection{
				TunnelIface: "mgawg51820", TunnelIP: netip.MustParseAddr("10.66.4.2"),
				L4Protocol: "tcp", Signature: torrentguard.ProtocolBitTorrentTCP,
				Evidence: torrentguard.EvidenceTCPHandshake, DstPort: 6881,
			})
			queued := a.out.after(0)
			if len(queued) != 1 {
				t.Fatalf("queued events = %d, want 1", len(queued))
			}
			event := queued[0].GetEvent()
			if event.InboundId != "awg-1" || event.Params["protocol"] != "tcp" || event.Params["torrent_protocol"] != string(torrentguard.ProtocolBitTorrentTCP) {
				t.Fatalf("event identity = inbound %q params %v", event.InboundId, event.Params)
			}
			if event.Params["evidence"] != "tcp_handshake" || event.Params["dst_port"] != "6881" {
				t.Fatalf("event evidence = %q, port = %q", event.Params["evidence"], event.Params["dst_port"])
			}
			// The client's tunnel address only finds the user; it never leaves the node.
			for _, k := range []string{"client_ip", "destination"} {
				if _, ok := event.Params[k]; ok {
					t.Fatalf("event carries %s: %v", k, event.Params)
				}
			}
			if got := event.Params["user_id"]; got != tc.wantUserID {
				t.Fatalf("user_id = %q, want %q", got, tc.wantUserID)
			}
		})
	}
}
