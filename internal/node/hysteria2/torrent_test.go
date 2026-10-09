package hysteria2

import (
	"testing"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/torrentguard"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestHysteriaRequestAttributionRejectsAmbiguousDestination(t *testing.T) {
	owner := func(id string) *credState {
		state := &credState{id: id}
		state.owner.Store(&credIdentity{userID: id})
		return state
	}
	newInbound := func() *inbound {
		e := &eng{}
		e.torrentEnabled.Store(true)
		in := &inbound{e: e}
		in.idx.Store(&index{byID: map[string]*credState{
			"cred-a": owner("user-a"),
			"cred-b": owner("user-b"),
		}})
		return in
	}

	t.Run("unique", func(t *testing.T) {
		in := newInbound()
		in.TCPRequest(nil, "cred-a", "Example.COM:443")
		if got := in.takeRequestUserID("example.com:443"); got != "user-a" {
			t.Fatalf("user id = %q, want user-a", got)
		}
	})
	t.Run("ambiguous concurrent users", func(t *testing.T) {
		in := newInbound()
		in.TCPRequest(nil, "cred-a", "example.com:443")
		in.TCPRequest(nil, "cred-b", "example.com:443")
		if got := in.takeRequestUserID("example.com:443"); got != "" {
			t.Fatalf("ambiguous user id = %q, want no attribution", got)
		}
		if got := in.takeRequestUserID("example.com:443"); got != "" {
			t.Fatalf("second ambiguous user id = %q, want no attribution", got)
		}
	})
}

func TestTorrentPortKeepsOnlyTheNumber(t *testing.T) {
	for addr, want := range map[string]string{
		"198.51.100.3:6881":  "6881",
		"[2001:db8::1]:6969": "6969",
		"tracker.example:80": "80",
		"198.51.100.3":       "",
		"198.51.100.3:http":  "",
		"198.51.100.3:0":     "",
		"198.51.100.3:70000": "",
		"":                   "",
	} {
		if got := torrentPort(addr); got != want {
			t.Errorf("torrentPort(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestReportTorrentCarriesEvidenceAndPortNeverAddress(t *testing.T) {
	var got engine.Event
	e := &eng{}
	e.env.Event = func(ev engine.Event) { got = ev }
	in := &inbound{e: e, spec: plugin.InboundSpec{ID: "hy-1"}}
	in.reportTorrent(torrentguard.ProtocolBitTorrentTracker, torrentguard.EvidenceTrackerConnect, "udp", "usr_a", torrentPort("198.51.100.9:6969"))
	want := map[string]string{"protocol": "udp", "torrent_protocol": "bittorrent_tracker", "evidence": "tracker_connect", "dst_port": "6969", "user_id": "usr_a"}
	if got.Code != "torrent_attempt" || len(got.Params) != len(want) {
		t.Fatalf("event = %+v", got)
	}
	for k, v := range want {
		if got.Params[k] != v {
			t.Errorf("param %s = %q, want %q", k, got.Params[k], v)
		}
	}
}
