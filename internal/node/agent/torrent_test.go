package agent

import (
	"context"
	"strconv"
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

func TestEngineUTPSynNeedsRepeatedDetectionsAcrossPorts(t *testing.T) {
	a := &Agent{out: newOutbox(32, 1<<20), torrentEvents: map[string]time.Time{}}
	send := func(inbound, user, port string) {
		a.engineEvent(engine.Event{Code: "torrent_attempt", InboundID: inbound, Warning: true, Params: map[string]string{
			"user_id": user, "protocol": "udp", "torrent_protocol": "bittorrent_utp", "evidence": "utp_syn", "dst_port": port,
		}})
	}
	for _, port := range []string{"6881", "6882", "6881", "6883"} {
		send("in-1", "user-1", port)
	}
	for _, port := range []string{"6881", "6882", "6881", "6882", "6881"} {
		send("in-3", "user-1", port)
	}
	send("in-2", "user-1", "6884")
	send("in-1", "user-2", "6884")
	if got := len(a.out.after(0)); got != 0 {
		t.Fatalf("weak uTP events before five detections for one inbound/user = %d, want 0", got)
	}
	send("in-1", "user-1", "6881")
	if got := len(a.out.after(0)); got != 1 {
		t.Fatalf("weak uTP events after five detections across three ports = %d, want 1", got)
	}
	send("in-1", "user-1", "6882")
	if got := len(a.out.after(0)); got != 1 {
		t.Fatalf("weak uTP event bypassed the five-minute throttle: queued %d", got)
	}
	a.offset.Add(301)
	send("in-1", "user-1", "6883")
	if got := len(a.out.after(0)); got != 2 {
		t.Fatalf("qualified weak uTP event after the throttle = %d, want 2", got)
	}
	a.offset.Add(601)
	for _, port := range []string{"6881", "6882", "6881", "6883"} {
		send("in-1", "user-1", port)
	}
	if got := len(a.out.after(0)); got != 2 {
		t.Fatalf("expired detections qualified the weak-evidence window: queued %d", got)
	}
	send("in-1", "user-1", "6881")
	if got := len(a.out.after(0)); got != 3 {
		t.Fatalf("fresh qualified weak uTP event = %d, want 3", got)
	}
}

func TestEngineUTPSynStateStaysBounded(t *testing.T) {
	a := &Agent{out: newOutbox(32, 1<<20), torrentEvents: map[string]time.Time{}}
	for i := 0; i < 4097; i++ {
		a.engineEvent(engine.Event{Code: "torrent_attempt", InboundID: strconv.Itoa(i), Params: map[string]string{
			"user_id": "user-1", "evidence": "utp_syn", "dst_port": "6881",
		}})
	}
	if len(a.torrentUTPSyn) != 4096 {
		t.Fatalf("uTP evidence states = %d, want the 4096-entry bound", len(a.torrentUTPSyn))
	}
	if queued := a.out.after(0); len(queued) != 0 {
		t.Fatalf("unqualified weak events were queued: %d", len(queued))
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
