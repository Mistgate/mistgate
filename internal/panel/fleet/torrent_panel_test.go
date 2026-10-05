package fleet

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestTorrentBlockerSettingRequiresCapabilityAndPropagates(t *testing.T) {
	x, a := newL3Env(t)
	service := nodeService{x.f}
	set := func(enabled bool) error {
		_, err := service.UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{
			NodeId: a.nodeID, TorrentBlockerEnabled: &enabled,
		}))
		return err
	}

	if err := set(true); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Fatalf("enable without torrentguard/1: %v", err)
	}
	// Disabling stays allowed after an agent loses the capability.
	if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{TorrentBlockerEnabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if err := set(false); err != nil {
		t.Fatalf("disable without torrentguard/1: %v", err)
	}

	n, err := x.st.Node(x.ctx, a.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	oldAgentSettings := nodeSettings(n, nil)
	guardSettings := nodeSettings(n, []string{capTorrentGuard})
	if oldAgentSettings.TorrentBlockerEnabled || guardSettings.TorrentBlockerEnabled {
		t.Fatal("a disabled setting should be false for every agent")
	}
	if settingsSig(oldAgentSettings) != settingsSig(guardSettings) {
		t.Fatal("adding the disabled capability changed the legacy settings signature")
	}
	enabledNode := n
	enabledNode.TorrentBlockerEnabled = true
	if nodeSettings(enabledNode, nil).TorrentBlockerEnabled {
		t.Fatal("an old agent was sent the torrent blocker setting")
	}
	if settingsSig(guardSettings) == settingsSig(nodeSettings(enabledNode, []string{capTorrentGuard})) {
		t.Fatal("enabling the blocker did not change the settings signature")
	}

	c, _, initial := connectCaps(a, "torrentguard", capTorrentGuard)
	if initial.Settings == nil || initial.Settings.TorrentBlockerEnabled {
		t.Fatalf("initial torrent blocker setting = %v", initial.Settings)
	}
	getNode := func() *adminv1.Node {
		t.Helper()
		resp, err := service.GetNode(x.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Node
	}
	if got := getNode(); !got.TorrentBlockerSupported || got.TorrentBlockerEnabled {
		t.Fatalf("node capability/settings: supported=%v enabled=%v", got.TorrentBlockerSupported, got.TorrentBlockerEnabled)
	}

	if err := set(true); err != nil {
		t.Fatal(err)
	}
	delta := c.desired()
	if delta.Settings == nil || !delta.Settings.TorrentBlockerEnabled || len(delta.Inbounds) != 0 {
		t.Fatalf("torrent blocker setting delta = %v", delta)
	}
	if got := getNode(); !got.TorrentBlockerSupported || !got.TorrentBlockerEnabled {
		t.Fatalf("updated node capability/settings: supported=%v enabled=%v", got.TorrentBlockerSupported, got.TorrentBlockerEnabled)
	}
}

func TestTorrentAttemptEventResolvesDisplayName(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID) // includes usr_alice
	c, _, _ := connectFull(a, "torrent-event")
	c.send(1, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Severity:  agentv1.Severity_SEVERITY_WARNING,
		Code:      "torrent_attempt",
		InboundId: "inb_1",
		TimeUnix:  time.Now().Unix(),
		Params: map[string]string{
			"protocol": "fakehy", "torrent_protocol": "bittorrent", "destination": "203.0.113.8:51413",
			"client_ip": "203.0.113.9", "user_id": "usr_alice", "user_name": "untrusted display name", "password": "never-store",
		},
	}}})
	c.sync(2)

	rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: a.nodeID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Code != "torrent_attempt" {
			continue
		}
		if row.Params["user_id"] != "usr_alice" || row.Params["user_name"] != "alice" {
			t.Fatalf("event identity was not resolved from the user row: %+v", row.Params)
		}
		if row.Params["protocol"] != "fakehy" || row.Params["torrent_protocol"] != "bittorrent" || row.InboundID != "inb_1" {
			t.Fatalf("event details were lost: %+v", row)
		}
		for _, k := range []string{"password", "client_ip", "destination"} {
			if _, exists := row.Params[k]; exists {
				t.Fatalf("%s was persisted: %+v", k, row.Params)
			}
		}
		return
	}
	t.Fatal("torrent_attempt event was not stored")
}
