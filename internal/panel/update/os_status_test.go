package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestGetOsUpdatesUsesStoredNodeSnapshots(t *testing.T) {
	e := newEnv(t)
	capable := e.addNode("de1", nodeOpts{caps: []string{osupdate.Capability}, online: 2})
	old := e.addNode("nl1", nodeOpts{caps: []string{"doctor/1"}, online: 1})
	now := e.clk.Now()
	status := &agentv1.OsStatus{MeasuredUnix: now.Unix(), DistroId: "ubuntu", DistroVersion: "24.04", Pending: 2, Security: 1,
		Notable: []string{"openssl 3.0.0 3.0.1 s"}, Apt: "ok", AutoUpdates: "on", BootIdHash: "0123456789abcdef"}
	body, err := protojson.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.PutOsStatus(e.ctx, capable, string(body), now, now); err != nil {
		t.Fatal(err)
	}
	e.s.osStatusMonitor = nil
	resp, err := (rpc{s: e.s}).GetOsUpdates(e.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.GetNodes()) != 2 {
		t.Fatalf("nodes = %+v", resp.Msg.GetNodes())
	}
	byID := map[string]*adminv1.NodeOs{}
	for _, node := range resp.Msg.GetNodes() {
		byID[node.GetNodeId()] = node
	}
	got := byID[capable]
	if got == nil || !got.GetConnected() || !got.GetSupported() || got.GetState() != adminv1.OsUpdateState_OS_UPDATE_STATE_UPDATES || got.GetOnlineUsers() != 2 || got.GetStatus().GetPending() != 2 {
		t.Fatalf("capable node = %+v", got)
	}
	if got.GetOnPanelServer() {
		t.Fatal("node was marked as the panel host without a panel snapshot")
	}
	got = byID[old]
	if got == nil || got.GetSupported() || got.GetState() != adminv1.OsUpdateState_OS_UPDATE_STATE_UNSUPPORTED {
		t.Fatalf("old agent node = %+v", got)
	}
	if resp.Msg.GetPanelHost() != nil {
		t.Fatalf("unexpected panel-host snapshot: %+v", resp.Msg.GetPanelHost())
	}
}

func TestGetOsUpdatesComparesPanelHostBootHashFromInProcessReader(t *testing.T) {
	e := newEnv(t)
	nodeID := e.addNode("de1", nodeOpts{caps: []string{osupdate.Capability}})
	root := t.TempDir()
	writePanelHostFixture(t, root, "/etc/os-release", "ID=ubuntu\nVERSION_ID=24.04\n")
	writePanelHostFixture(t, root, "/proc/sys/kernel/osrelease", "6.8.0-1-generic\n")
	writePanelHostFixture(t, root, "/proc/sys/kernel/random/boot_id", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\n")
	writePanelHostFixture(t, root, "/proc/locks", "")
	writePanelHostFixture(t, root, "/var/lib/dpkg/status", "ii\tbase-files\n")
	writePanelHostFixture(t, root, "/var/lib/apt/lists/deb.example_dists_suite_InRelease", "fixture")
	reader := &osupdate.Reader{Root: root, Now: e.clk.Now, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "apt-get":
			return nil, nil
		case "dpkg-query":
			if len(args) > 2 && args[2] == "unattended-upgrades" {
				return []byte("install ok installed\n"), nil
			}
			return []byte("ii\tbase-files\n"), nil
		case "linux-version":
			return []byte("6.8.0-1-generic\n"), nil
		case "apt-config":
			return []byte("L='1'; U='1'; R='false';\n"), nil
		case "systemctl":
			if args[0] == "is-enabled" {
				return []byte("enabled\nenabled\n"), nil
			}
			return []byte("active\nactive\n"), nil
		default:
			return nil, nil
		}
	}}
	monitor := osupdate.NewMonitor(reader)
	if _, _, didRead, err := monitor.Poll(e.ctx, false); err != nil || !didRead {
		t.Fatalf("panel-host read didRead=%v err=%v", didRead, err)
	}
	e.s.osStatusMonitor = monitor
	host := monitor.Snapshot()
	body, err := protojson.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	now := e.clk.Now()
	if err := e.st.PutOsStatus(e.ctx, nodeID, string(body), now, now); err != nil {
		t.Fatal(err)
	}
	resp, err := (rpc{s: e.s}).GetOsUpdates(e.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPanelHost() == nil || len(resp.Msg.GetNodes()) != 1 || !resp.Msg.GetNodes()[0].GetOnPanelServer() {
		t.Fatalf("panel host comparison = %+v", resp.Msg)
	}
}

func writePanelHostFixture(t *testing.T, root, path, contents string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(path, "/")))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOsUpdateStateDoesNotTreatReadFailuresAsUpToDate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status *agentv1.OsStatus
		want   adminv1.OsUpdateState
	}{
		{"locked", &agentv1.OsStatus{DistroId: "ubuntu", Apt: "locked"}, adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN},
		{"simulation failure", &agentv1.OsStatus{DistroId: "ubuntu", Apt: "ok", Error: "read_failed"}, adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN},
		{"up to date", &agentv1.OsStatus{DistroId: "ubuntu", Apt: "ok"}, adminv1.OsUpdateState_OS_UPDATE_STATE_UP_TO_DATE},
		{"not apt", &agentv1.OsStatus{DistroId: "ubuntu", Error: "not_apt"}, adminv1.OsUpdateState_OS_UPDATE_STATE_UNSUPPORTED},
		// os-release could not be read: the host is unknown, not unsupported.
		{"os-release unreadable", &agentv1.OsStatus{Apt: "unknown", Error: "read_failed"}, adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN},
		// Debian testing and sid have no VERSION_ID; the state comes from the rest of the snapshot.
		{"debian without version", &agentv1.OsStatus{DistroId: "debian", Apt: "ok", Pending: 1}, adminv1.OsUpdateState_OS_UPDATE_STATE_UPDATES},
		{"other distribution", &agentv1.OsStatus{DistroId: "fedora", Apt: "ok"}, adminv1.OsUpdateState_OS_UPDATE_STATE_UNSUPPORTED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.status.MeasuredUnix = osStatusNow
			if got := osUpdateState(true, true, tc.status, osStatusNow); got != tc.want {
				t.Fatalf("osUpdateState = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestOsUpdatesEditionAndPanelHostRootGates(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		edge, root, reader, want bool
	}{
		{"root VPS", false, true, true, true},
		{"non-root VPS", false, false, true, false},
		{"VPS without reader", false, true, false, false},
		{"edge", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReadPanelHostStatus(tc.edge, tc.root, tc.reader); got != tc.want {
				t.Fatalf("shouldReadPanelHostStatus = %v, want %v", got, tc.want)
			}
		})
	}
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	_, err := (rpc{e.s}).GetOsUpdates(e.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "unavailable_in_edition: feature=os_updates") {
		t.Fatalf("edge GetOsUpdates = %v, want os_updates unavailable", err)
	}
}

const osStatusNow = 1794000000

// A connected node whose snapshot stopped arriving is not in the state it last reported. The limit is
// max(12 h, 3 x the agent's full-read interval); offline and unsupported keep their own answers.
func TestOsUpdateStateReportsStaleSnapshotsAsUnknown(t *testing.T) {
	limit := int64(osStatusStaleAfter / time.Second)
	if want := int64(18 * 3600); limit != want {
		t.Fatalf("stale limit = %d s, want %d s (3 x the %s full read)", limit, want, osupdate.FullReadInterval)
	}
	reboot := func(measured int64) *agentv1.OsStatus {
		return &agentv1.OsStatus{MeasuredUnix: measured, DistroId: "ubuntu", Apt: "ok", RebootRequired: true}
	}
	for _, tc := range []struct {
		name      string
		capable   bool
		connected bool
		age       int64
		want      adminv1.OsUpdateState
	}{
		{"fresh", true, true, 3600, adminv1.OsUpdateState_OS_UPDATE_STATE_REBOOT_REQUIRED},
		{"at the limit", true, true, limit, adminv1.OsUpdateState_OS_UPDATE_STATE_REBOOT_REQUIRED},
		{"past the limit", true, true, limit + 1, adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN},
		{"a week old", true, true, 7 * 24 * 3600, adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN},
		{"stale but offline", true, false, limit + 1, adminv1.OsUpdateState_OS_UPDATE_STATE_OFFLINE},
		{"stale and unsupported", false, true, limit + 1, adminv1.OsUpdateState_OS_UPDATE_STATE_UNSUPPORTED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := osUpdateState(tc.capable, tc.connected, reboot(osStatusNow-tc.age), osStatusNow); got != tc.want {
				t.Fatalf("osUpdateState = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestGetOsUpdatesMarksAStaleStoredSnapshotUnknown(t *testing.T) {
	e := newEnv(t)
	fresh := e.addNode("de1", nodeOpts{caps: []string{osupdate.Capability}})
	stale := e.addNode("nl1", nodeOpts{caps: []string{osupdate.Capability}})
	now := e.clk.Now()
	put := func(nodeID string, measured time.Time) {
		t.Helper()
		body, err := protojson.Marshal(&agentv1.OsStatus{MeasuredUnix: measured.Unix(), DistroId: "ubuntu", DistroVersion: "24.04", Pending: 2,
			Apt: "ok", AutoUpdates: "on"})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.PutOsStatus(e.ctx, nodeID, string(body), measured, now); err != nil {
			t.Fatal(err)
		}
	}
	put(fresh, now.Add(-time.Hour))
	put(stale, now.Add(-20*time.Hour))
	e.s.osStatusMonitor = nil
	resp, err := (rpc{s: e.s}).GetOsUpdates(e.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]adminv1.OsUpdateState{}
	for _, node := range resp.Msg.GetNodes() {
		state[node.GetNodeId()] = node.GetState()
	}
	if state[fresh] != adminv1.OsUpdateState_OS_UPDATE_STATE_UPDATES || state[stale] != adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN {
		t.Fatalf("states = %v, want fresh=UPDATES stale=UNKNOWN", state)
	}
}
