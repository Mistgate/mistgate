//go:build js && wasm

package store

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"syscall/js"
	"testing"
	"time"
)

// Batch shapes on D1 (design/cloudflare-edition/AGENT-LINK.md §7.7): how many statements each hot store call sends,
// and how large its biggest bound value is. The fake D1 counts statements per batch and the bytes of every bound
// value; real D1 refuses a string or BLOB over 2,000,000 bytes, so the biggest value is the number that matters.

const d1MaxValueBytes = 2_000_000

type d1Shape struct {
	Calls, Statements int
	BatchSizes        []int
	MaxValueBytes     int
	TotalBoundBytes   int
	DBMillis          float64
}

func measureD1(binding js.Value, label string, run func()) d1Shape {
	binding.Call("__beginQueryCount", label)
	run()
	c := binding.Call("__endQueryCount")
	sizes := make([]int, c.Get("batchSizes").Length())
	for i := range sizes {
		sizes[i] = c.Get("batchSizes").Index(i).Int()
	}
	return d1Shape{
		Calls: c.Get("sequentialQueries").Int(), Statements: c.Get("prepareExecutions").Int(), BatchSizes: sizes,
		MaxValueBytes: c.Get("maxBoundBytes").Int(), TotalBoundBytes: c.Get("totalBoundBytes").Int(),
		DBMillis: c.Get("dbMillis").Float(),
	}
}

func (s d1Shape) String() string {
	return fmt.Sprintf("%d D1 calls, %d statements, batches %v, largest bound value %d bytes (%.2f MB), %d bytes bound in all",
		s.Calls, s.Statements, s.BatchSizes, s.MaxValueBytes, float64(s.MaxValueBytes)/1e6, s.TotalBoundBytes)
}

type shapeFixture struct {
	st            *Store
	nodeID        string
	enrolledID    string
	now           time.Time
	inbound, prof string
}

func newShapeFixture(t *testing.T) shapeFixture {
	t.Helper()
	st := openD1Store(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := st.InsertCA(ctx, CARow{ID: "cas_shape", CertPEM: "ca", Fingerprint: "fingerprint", KeyEnc: []byte{1},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0)}, now); err != nil {
		t.Fatal(err)
	}
	node, err := st.CreateEnrollment(ctx, &NodeRow{ID: "nod_shape", Name: "node-a", Address: "node-a.example.com"}, "", []byte("shape-token"), "adm_test", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateProfile(ctx, AccessProfile{ID: "prf_shape", Protocol: "awg", Name: "shape", SettingsJSON: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateInbound(ctx, AccessInbound{ID: "inb_shape", ProfileID: "prf_shape", NodeID: node.ID, Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateGroup(ctx, AccessGroup{ID: "grp_shape", Name: "shape", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	enrolled, err := st.CreateEnrollment(ctx, &NodeRow{ID: "nod_shape2", Name: "node-b", Address: "node-b.example.com"}, "", []byte("shape-token-2"), "adm_test", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enroll(ctx, []byte("shape-token-2"), []byte("shape-key"), now, func(nodeID string) (CertRow, error) {
		return CertRow{Serial: "serial-shape", NodeID: nodeID, CAID: "cas_shape", PEM: "certificate", NotBefore: now,
			NotAfter: now.Add(24 * time.Hour), IssuedAt: now}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateInbound(ctx, AccessInbound{ID: "inb_shape2", ProfileID: "prf_shape", NodeID: enrolled.ID, Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return shapeFixture{st: st, nodeID: node.ID, enrolledID: enrolled.ID, now: now, inbound: "inb_shape", prof: "prf_shape"}
}

// seedUsers inserts n users with one AWG device and one credential each, in three statements.
func (f shapeFixture) seedUsers(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?1)
		 INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
		 SELECT 'usr_' || printf('%026d', i), 'user-' || i, 'grp_shape', ?2, CAST('h' || i AS BLOB), x'01', ?2 FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?1)
		 INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at)
		 SELECT 'dev_' || printf('%026d', i), 'usr_' || printf('%026d', i), CAST('w' || i AS BLOB), 0, 0, ?2 FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?1)
		 INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
		 SELECT 'crd_' || printf('%026d', i), 'dev_' || printf('%026d', i), 'usr_' || printf('%026d', i), 'awg', x'01', '{}', ?2, 'prf_shape', 0 FROM n`,
	} {
		if _, err := f.st.W.ExecContext(ctx, q, n, f.now.Unix()); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func (f shapeFixture) statsIn(users, sessions int, live FleetLive) FleetStatsIn {
	traffic := make([]FleetTraffic, users)
	for i := range traffic {
		traffic[i] = FleetTraffic{CredID: fmt.Sprintf("crd_%026d", i+1), InboundID: f.inbound, Up: 40, Down: 60}
	}
	refs := make([]FleetSessionRef, sessions)
	for i := range refs {
		refs[i] = FleetSessionRef{CredID: fmt.Sprintf("crd_%026d", i+1), InboundID: f.inbound, ConnectedAt: f.now.Add(-time.Minute)}
	}
	return FleetStatsIn{NodeID: f.nodeID, Instance: "shape-instance", Session: 1, Seq: 1, Now: f.now, HourStart: f.now.Unix() / 3600 * 3600,
		Traffic: traffic, Sessions: refs,
		Certs: []FleetInboundCert{{InboundID: f.inbound, Pin: "shape-pin", NotAfter: f.now.Add(24 * time.Hour)}},
		Live:  func(map[string]FleetCredRef) FleetLive { return live }}
}

func memSys() float64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return float64(m.Sys) / (1 << 20)
}

func TestD1BatchShapes(t *testing.T) {
	f := newShapeFixture(t)
	ctx := context.Background()
	binding := js.Global().Get("__d1")
	f.seedUsers(t, 3)
	if _, _, err := f.st.NodeHello(ctx, f.nodeID, 1, HelloInfo{AgentVersion: "test", Instance: "shape-instance"}, f.now); err != nil {
		t.Fatal(err)
	}
	live := FleetLive{Apply: true, SampleAt: f.now.Unix(), UsersJSON: `{"usr_00000000000000000000000001":1699999940}`, LiveJSON: `{"interval_end_unix":1700000000}`}
	var err error
	hello := measureD1(binding, "NodeHello", func() {
		_, _, err = f.st.NodeHello(ctx, f.nodeID, 2, HelloInfo{AgentVersion: "test", Instance: "shape-instance"}, f.now)
	})
	if err != nil {
		t.Fatal(err)
	}
	stats := measureD1(binding, "IngestStats", func() { _, err = f.st.IngestStats(ctx, f.statsIn(3, 2, live)) })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.NodeHello(ctx, f.enrolledID, 1, HelloInfo{AgentVersion: "test", Instance: "shape-instance-b"}, f.now); err != nil {
		t.Fatal(err)
	}
	applied := measureD1(binding, "NodeApplied", func() {
		err = f.st.NodeApplied(ctx, f.enrolledID, 1, false, 1, "hash", []InboundApplied{{ID: "inb_shape2", State: "active", SpecHash: "spec"}}, f.now)
	})
	if err != nil {
		t.Fatal(err)
	}
	renewed := CertRow{Serial: "serial-shape-new", NodeID: f.enrolledID, CAID: "cas_shape", PEM: "renewed", NotBefore: f.now, NotAfter: f.now.Add(48 * time.Hour), IssuedAt: f.now}
	renew := measureD1(binding, "RenewCert", func() { err = f.st.RenewCert(ctx, renewed, "serial-shape", f.now, time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]d1Shape{"NodeHello": hello, "IngestStats (3 users)": stats, "NodeApplied": applied, "RenewCert": renew} {
		t.Logf("%-22s %s", name, s)
	}
	// The shapes the edge design depends on (one batch per hot call; IngestStats a read batch and a write batch).
	for name, c := range map[string]struct {
		got  d1Shape
		want []int
	}{
		"NodeHello": {hello, []int{5}}, "IngestStats": {stats, []int{4, 8}}, "NodeApplied": {applied, []int{3}}, "RenewCert": {renew, []int{4}},
	} {
		if fmt.Sprint(c.got.BatchSizes) != fmt.Sprint(c.want) {
			t.Errorf("%s batches = %v, want %v", name, c.got.BatchSizes, c.want)
		}
	}
}

// TestD1LargeIngestStats: a node of the size the project plans for (5,000 users) must keep every bound value well under
// D1's 2,000,000-byte string limit; the 65,536-user batch the stats guard still admits is measured below.
func TestD1LargeIngestStats(t *testing.T) {
	f := newShapeFixture(t)
	ctx := context.Background()
	const users, sessions = 5_000, 500
	f.seedUsers(t, users)
	if _, _, err := f.st.NodeHello(ctx, f.nodeID, 1, HelloInfo{AgentVersion: "test", Instance: "shape-instance"}, f.now); err != nil {
		t.Fatal(err)
	}
	var err error
	before := memSys()
	shape := measureD1(js.Global().Get("__d1"), "IngestStats 5000", func() {
		_, err = f.st.IngestStats(ctx, f.statsIn(users, sessions, FleetLive{Apply: true, SampleAt: f.now.Unix()}))
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("IngestStats with %d users and %d sessions: %s", users, sessions, shape)
	t.Logf("Go memory from the OS %.0f MB -> %.0f MB", before, memSys())
	if shape.MaxValueBytes >= d1MaxValueBytes/2 {
		t.Errorf("largest bound value is %d bytes with %d users: no headroom under D1's %d", shape.MaxValueBytes, users, d1MaxValueBytes)
	}
}

// TestD1LargestIngestStats is the worst case the stats guard admits: 65,536 deltas (maxStatsDeltas in fleet) from as many
// users, 2,000 open sessions, and a live projection at its 1.5 MB valve. It only records numbers (see §7.7: the batch is
// larger than D1 accepts); run it with GOMEASURE=1, it takes a few seconds.
func TestD1LargestIngestStats(t *testing.T) {
	if testing.Short() || js.Global().Get("process").Get("env").Get("GOMEASURE").IsUndefined() {
		t.Skip("set GOMEASURE=1 to run the 65,536-user batch")
	}
	f := newShapeFixture(t)
	ctx := context.Background()
	binding := js.Global().Get("__d1")
	const users, sessions = 65_536, 2_000
	f.seedUsers(t, users)
	if _, _, err := f.st.NodeHello(ctx, f.nodeID, 1, HelloInfo{AgentVersion: "test", Instance: "shape-instance"}, f.now); err != nil {
		t.Fatal(err)
	}
	userMap := make([]string, sessions)
	for i := range userMap {
		userMap[i] = fmt.Sprintf(`"usr_%026d":1699999940`, i+1)
	}
	usersJSON := "{" + strings.Join(userMap, ",") + "}"
	live := FleetLive{Apply: true, SampleAt: f.now.Unix(), UsersJSON: usersJSON,
		LiveJSON: `{"pad":"` + strings.Repeat("x", 1_500_000-len(usersJSON)-len(`{"pad":""}`)) + `"}`}
	before := memSys()
	var err error
	start := time.Now()
	var out FleetStatsOut
	shape := measureD1(binding, "IngestStats largest", func() { out, err = f.st.IngestStats(ctx, f.statsIn(users, sessions, live)) })
	elapsed := time.Since(start)
	if err != nil || out.Duplicate {
		t.Fatalf("IngestStats = %+v, %v", out, err)
	}
	t.Logf("largest IngestStats (%d users, %d sessions, live %d bytes): %s", users, sessions, len(live.UsersJSON)+len(live.LiveJSON), shape)
	t.Logf("wall %v of which in the fake SQLite %.0f ms; Go memory from the OS %.0f MB -> %.0f MB (wasm memory never shrinks)", elapsed.Round(time.Millisecond), shape.DBMillis, before, memSys())
	// the biggest value is the bucket list, linear in the users: where D1's limit is crossed
	perUser := float64(shape.MaxValueBytes) / users
	t.Logf("largest value %.0f bytes per user: D1's %d-byte limit is reached at about %.0f users per batch", perUser, d1MaxValueBytes, d1MaxValueBytes/perUser)
}
