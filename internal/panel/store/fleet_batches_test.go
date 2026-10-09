//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fleetBatchFixture struct {
	nodeID, userID, deviceID, inboundID, credID string
	now                                         time.Time
}

func newFleetBatchFixture(t *testing.T, s *Store, prefix string) fleetBatchFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	nodeID := "nod_" + prefix
	if _, err := s.CreateEnrollment(ctx, &NodeRow{ID: nodeID, Name: prefix, Address: "example.com"}, "", []byte(prefix+"-token"), "adm_test", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	profileID := "prf_" + prefix
	if err := s.Access().CreateProfile(ctx, AccessProfile{ID: profileID, Protocol: "awg", Name: prefix, SettingsJSON: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	inboundID := "inb_" + prefix
	if err := s.Access().CreateInbound(ctx, AccessInbound{ID: inboundID, ProfileID: profileID, NodeID: nodeID, Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	groupID := "grp_" + prefix
	if err := s.Access().CreateGroup(ctx, AccessGroup{ID: groupID, Name: prefix, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	userID := "usr_" + prefix
	if _, err := s.W.ExecContext(ctx, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, prefix, groupID, now.Unix(), []byte(prefix+"-sub"), []byte{1}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	deviceID := "dev_" + prefix
	if _, err := s.W.ExecContext(ctx, `INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at)
		VALUES (?, ?, ?, 0, 0, ?)`, deviceID, userID, []byte(prefix+"-hwid"), now.Unix()); err != nil {
		t.Fatal(err)
	}
	credID := "crd_" + prefix
	if _, err := s.W.ExecContext(ctx, `INSERT INTO device_credential
		(id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
		VALUES (?, ?, ?, 'awg', ?, '{}', ?, ?, 0)`, credID, deviceID, userID, []byte{1}, now.Unix(), profileID); err != nil {
		t.Fatal(err)
	}
	return fleetBatchFixture{nodeID: nodeID, userID: userID, deviceID: deviceID, inboundID: inboundID, credID: credID, now: now}
}

func TestIngestStatsConcurrentSeqAndNewInstance(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	f := newFleetBatchFixture(t, s, "stats_seq")
	const workers = 12
	start := make(chan struct{})
	results := make([]FleetStatsOut, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = s.IngestStats(ctx, FleetStatsIn{
				NodeID: f.nodeID, Instance: "instance-a", Seq: 1, Now: f.now, HourStart: f.now.Unix() / 3600 * 3600,
				Traffic: []FleetTraffic{{CredID: f.credID, InboundID: f.inboundID, Up: 100, Down: 50}},
			})
		}(i)
	}
	close(start)
	wg.Wait()
	duplicates := 0
	applied := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("concurrent ingest %d: %v", i, errs[i])
		}
		if results[i].Duplicate {
			duplicates++
		} else {
			applied++
		}
	}
	if applied != 1 || duplicates != workers-1 {
		t.Fatalf("concurrent results: applied=%d duplicates=%d, want 1 and %d", applied, duplicates, workers-1)
	}
	if got := queryInt(t, s, `SELECT used_bytes FROM user WHERE id = ?`, f.userID); got != 150 {
		t.Fatalf("used_bytes after concurrent seq = %d, want 150", got)
	}
	if got := queryInt(t, s, `SELECT last_seq FROM node WHERE id = ?`, f.nodeID); got != 1 {
		t.Fatalf("last_seq after concurrent seq = %d, want 1", got)
	}
	var up, down int64
	if err := s.R.QueryRowContext(ctx, `SELECT bytes_up, bytes_down FROM traffic_bucket WHERE user_id = ? AND node_id = ? AND protocol = 'awg'`, f.userID, f.nodeID).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if up != 100 || down != 50 {
		t.Fatalf("traffic bucket after concurrent seq = %d/%d, want 100/50", up, down)
	}

	out, err := s.IngestStats(ctx, FleetStatsIn{
		NodeID: f.nodeID, Instance: "instance-b", Seq: 1, Now: f.now.Add(time.Minute), HourStart: f.now.Unix() / 3600 * 3600,
		Traffic: []FleetTraffic{{CredID: f.credID, InboundID: f.inboundID, Up: 100, Down: 50}},
	})
	if err != nil || out.Duplicate {
		t.Fatalf("new instance seq 1 = %+v, %v; want applied", out, err)
	}
	if got := queryInt(t, s, `SELECT used_bytes FROM user WHERE id = ?`, f.userID); got != 300 {
		t.Fatalf("used_bytes after new instance = %d, want 300", got)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT bytes_up, bytes_down FROM traffic_bucket WHERE user_id = ? AND node_id = ? AND protocol = 'awg'`, f.userID, f.nodeID).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if up != 200 || down != 100 {
		t.Fatalf("traffic bucket after new instance = %d/%d, want 200/100", up, down)
	}
	if got := queryInt(t, s, `SELECT last_seq FROM node WHERE id = ? AND agent_instance_id = 'instance-b'`, f.nodeID); got != 1 {
		t.Fatalf("new instance last_seq = %d, want 1", got)
	}
}

func TestIngestStatsCommitsCertAndAWGTouchOnce(t *testing.T) {
	s := openTemp(t)
	f := newFleetBatchFixture(t, s, "stats_touch")
	ctx := context.Background()
	var groupID, profileID string
	if err := s.R.QueryRowContext(ctx, `SELECT group_id FROM user WHERE id = ?`, f.userID).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT profile_id FROM inbound WHERE id = ?`, f.inboundID).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
		VALUES ('usr_stats_touch_2', 'stats_touch_second', ?, ?, ?, ?, ?)`, groupID, f.now.Unix(), []byte("stats-touch-second"), []byte{1}, f.now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at)
		VALUES ('dev_stats_touch_2', 'usr_stats_touch_2', ?, 0, 0, ?)`, []byte("stats-touch-hwid"), f.now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO device_credential
		(id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
		VALUES ('crd_stats_touch_2', 'dev_stats_touch_2', 'usr_stats_touch_2', 'awg', ?, '{}', ?, ?, 0)`,
		[]byte{1}, f.now.Unix(), profileID); err != nil {
		t.Fatal(err)
	}
	connectedAt := f.now.Add(-time.Minute)
	in := FleetStatsIn{
		NodeID: f.nodeID, Instance: "instance-a", Seq: 1, Now: f.now, HourStart: f.now.Unix() / 3600 * 3600,
		Traffic: []FleetTraffic{
			{CredID: f.credID, InboundID: f.inboundID, Up: 40, Down: 60},
			{CredID: "crd_stats_touch_2", InboundID: f.inboundID, Up: 1, Down: 2},
		},
		Sessions: []FleetSessionRef{
			{CredID: f.credID, InboundID: f.inboundID, ConnectedAt: connectedAt},
			{CredID: f.credID, InboundID: f.inboundID, ConnectedAt: connectedAt.Add(30 * time.Second)},
		},
		Certs: []FleetInboundCert{{InboundID: f.inboundID, Pin: "first-pin", NotAfter: f.now.Add(24 * time.Hour)}},
	}
	out, err := s.IngestStats(context.Background(), in)
	if err != nil || out.Duplicate {
		t.Fatalf("first stats ingest = %+v, %v; want applied", out, err)
	}
	if ref, ok := out.Refs[f.credID]; !ok || ref.DeviceID != f.deviceID || ref.Protocol != "awg" {
		t.Fatalf("resolved refs = %+v; want AWG device %q", out.Refs, f.deviceID)
	}
	if len(out.Users) != 2 || out.Users[0] > out.Users[1] {
		t.Fatalf("stats users = %v, want sorted user IDs", out.Users)
	}
	if got := queryInt(t, s, `SELECT last_seen_at FROM device WHERE id = ?`, f.deviceID); got != connectedAt.Add(30*time.Second).Unix() {
		t.Fatalf("device last_seen_at = %d, want %d", got, connectedAt.Add(30*time.Second).Unix())
	}
	var pin string
	if err := s.R.QueryRowContext(context.Background(), `SELECT cert_pin_sha256 FROM inbound WHERE id = ?`, f.inboundID).Scan(&pin); err != nil {
		t.Fatal(err)
	}
	if pin != "first-pin" {
		t.Fatalf("inbound certificate pin = %q, want first-pin", pin)
	}

	in.Sessions[0].ConnectedAt = f.now.Add(time.Hour)
	in.Certs[0].Pin = "duplicate-pin"
	out, err = s.IngestStats(context.Background(), in)
	if err != nil || !out.Duplicate {
		t.Fatalf("duplicate stats ingest = %+v, %v; want Duplicate", out, err)
	}
	if got := queryInt(t, s, `SELECT last_seen_at FROM device WHERE id = ?`, f.deviceID); got != connectedAt.Add(30*time.Second).Unix() {
		t.Fatalf("duplicate changed device last_seen_at to %d, want %d", got, connectedAt.Add(30*time.Second).Unix())
	}
	if err := s.R.QueryRowContext(context.Background(), `SELECT cert_pin_sha256 FROM inbound WHERE id = ?`, f.inboundID).Scan(&pin); err != nil {
		t.Fatal(err)
	}
	if pin != "first-pin" {
		t.Fatalf("duplicate changed inbound certificate pin to %q", pin)
	}
}

func TestIngestStatsLiveBuilderIsPureAcrossGuardRetry(t *testing.T) {
	s := openTemp(t)
	f := newFleetBatchFixture(t, s, "stats_live_retry")
	ctx := context.Background()
	if _, _, err := s.NodeHello(ctx, f.nodeID, 4, HelloInfo{Instance: "instance-retry"}, f.now); err != nil {
		t.Fatal(err)
	}
	in := FleetStatsIn{
		NodeID: f.nodeID, Instance: "instance-retry", Session: 4, Seq: 100, Now: f.now, HourStart: f.now.Unix() / 3600 * 3600,
		Traffic:  []FleetTraffic{{CredID: f.credID, InboundID: f.inboundID, Up: 4, Down: 5}},
		Sessions: []FleetSessionRef{{CredID: f.credID, InboundID: f.inboundID, ConnectedAt: f.now.Add(-time.Minute)}},
		Certs:    []FleetInboundCert{{InboundID: f.inboundID, Pin: "retry-pin", NotAfter: f.now.Add(time.Hour)}},
	}
	trafficBefore := append([]FleetTraffic(nil), in.Traffic...)
	sessionsBefore := append([]FleetSessionRef(nil), in.Sessions...)
	certsBefore := append([]FleetInboundCert(nil), in.Certs...)
	var calls int
	var forcedRetryErr error
	in.Live = func(refs map[string]FleetCredRef) FleetLive {
		calls++
		if calls == 1 {
			_, forcedRetryErr = s.W.ExecContext(ctx, `UPDATE node SET last_seq = 50 WHERE id = ?`, f.nodeID)
		}
		if _, ok := refs[f.credID]; !ok {
			t.Errorf("Live builder retry %d did not receive the resolved credential", calls)
		}
		return FleetLive{Apply: true, SampleAt: f.now.Unix(), UsersJSON: `{"usr_retry":1699999940}`,
			LiveJSON: fmt.Sprintf(`{"result":%d}`, calls)}
	}

	out, err := s.IngestStats(ctx, in)
	if err != nil || out.Duplicate {
		t.Fatalf("IngestStats() = %+v, %v", out, err)
	}
	if forcedRetryErr != nil {
		t.Fatalf("forcing the guarded retry: %v", forcedRetryErr)
	}
	if calls != 2 {
		t.Fatalf("Live builder called %d times, want two guarded attempts", calls)
	}
	rows, err := s.NodeLive(ctx, f.now, f.nodeID, true)
	if err != nil || len(rows) != 1 || rows[0].Session != 4 || !strings.Contains(rows[0].LiveJSON, `"result":2`) {
		t.Fatalf("node_live after retry = %+v, %v; want the second builder result", rows, err)
	}
	if !reflect.DeepEqual(in.Traffic, trafficBefore) || !reflect.DeepEqual(in.Sessions, sessionsBefore) || !reflect.DeepEqual(in.Certs, certsBefore) {
		t.Fatalf("IngestStats mutated its inputs: traffic=%+v sessions=%+v certs=%+v", in.Traffic, in.Sessions, in.Certs)
	}
	if got := queryInt(t, s, `SELECT used_bytes FROM user WHERE id = ?`, f.userID); got != 9 {
		t.Fatalf("used_bytes after retry = %d, want 9", got)
	}
}

func TestIngestStatsKeepsTrafficWhenLiveProjectionIsSkipped(t *testing.T) {
	s := openTemp(t)
	f := newFleetBatchFixture(t, s, "stats_live_skip")
	ctx := context.Background()
	if _, _, err := s.NodeHello(ctx, f.nodeID, 1, HelloInfo{Instance: "instance-skip"}, f.now); err != nil {
		t.Fatal(err)
	}
	first, err := s.IngestStats(ctx, FleetStatsIn{NodeID: f.nodeID, Instance: "instance-skip", Session: 1, Seq: 1, Now: f.now,
		HourStart: f.now.Unix() / 3600 * 3600, Traffic: []FleetTraffic{{CredID: f.credID, InboundID: f.inboundID, Up: 2, Down: 3}},
		Live: func(map[string]FleetCredRef) FleetLive {
			return FleetLive{Apply: true, UsersJSON: `{"usr_previous":1}`, LiveJSON: `{"marker":"previous"}`}
		},
	})
	if err != nil || first.Duplicate {
		t.Fatalf("first IngestStats() = %+v, %v", first, err)
	}
	second, err := s.IngestStats(ctx, FleetStatsIn{NodeID: f.nodeID, Instance: "instance-skip", Session: 1, Seq: 2, Now: f.now.Add(time.Second),
		HourStart: f.now.Unix() / 3600 * 3600, Traffic: []FleetTraffic{{CredID: f.credID, InboundID: f.inboundID, Up: 4, Down: 5}},
		Live: func(map[string]FleetCredRef) FleetLive {
			return FleetLive{UsersJSON: `{"usr_skipped":1}`, LiveJSON: `{"marker":"skipped"}`}
		},
	})
	if err != nil || second.Duplicate {
		t.Fatalf("second IngestStats() = %+v, %v", second, err)
	}
	rows, err := s.NodeLive(ctx, f.now.Add(time.Second), f.nodeID, true)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].LiveJSON, `"marker":"previous"`) {
		t.Fatalf("skipped projection changed node_live: %+v, %v", rows, err)
	}
	if got := queryInt(t, s, `SELECT used_bytes FROM user WHERE id = ?`, f.userID); got != 14 {
		t.Fatalf("used_bytes after skipped projection = %d, want 14", got)
	}
}

func TestFleetStatsWriteBatchStatementCount(t *testing.T) {
	const users = 300
	const sessions = 600
	const certificates = 8
	now := time.Unix(1_700_000_000, 0).UTC()
	in := FleetStatsIn{NodeID: "nod_statement_count", Instance: "instance", Seq: 1, Now: now, HourStart: now.Unix() / 3600 * 3600}
	buckets := make([]fleetStatsBucketWrite, 0, users)
	userWrites := make([]fleetStatsUserWrite, 0, users)
	touches := make([]fleetDeviceTouchWrite, 0, users)
	for i := 0; i < users; i++ {
		id := fmt.Sprintf("%03d", i)
		buckets = append(buckets, fleetStatsBucketWrite{UserID: "usr_" + id, NodeID: in.NodeID, Protocol: "awg",
			HourStart: in.HourStart, BytesUp: 1, BytesDown: 2})
		userWrites = append(userWrites, fleetStatsUserWrite{UserID: "usr_" + id, Bytes: 3})
		touches = append(touches, fleetDeviceTouchWrite{DeviceID: "dev_" + id, LastSeenAt: now.Unix() - 1, NotAfter: now.Unix() - 1})
	}
	hours := []fleetStatsHourWrite{{NodeID: in.NodeID, Protocol: "awg", HourStart: in.HourStart, BytesUp: users, BytesDown: users * 2,
		PeakUsers: users, PeakDevices: users}}
	certs := make([]fleetInboundCertWrite, 0, certificates)
	for i := 0; i < certificates; i++ {
		certs = append(certs, fleetInboundCertWrite{InboundID: fmt.Sprintf("inb_%03d", i), Pin: "pin", NotAfter: now.Add(time.Hour).Unix()})
	}
	stmts, err := fleetStatsWriteStmts(in, in.Instance, 0, buckets, hours, userWrites, certs, touches, FleetLive{})
	if err != nil {
		t.Fatal(err)
	}
	before := 1 + len(buckets) + len(hours) + len(userWrites) + sessions + len(certs) + 1
	if before != 1211 {
		t.Fatalf("legacy batch statement count = %d, want 1211", before)
	}
	if len(stmts) != 8 {
		t.Fatalf("IngestStats statements for %d users, %d sessions, and %d certificates: before=%d after=%d, want 8 after",
			users, sessions, certificates, before, len(stmts))
	}
	t.Logf("IngestStats statements for %d users, %d sessions, and %d certificates: before=%d after=%d",
		users, sessions, certificates, before, len(stmts))
}

func TestNodeAppliedWriteBatchStatementCount(t *testing.T) {
	for _, count := range []int{0, 1, 50} {
		in := make([]InboundApplied, count)
		for i := range in {
			in[i] = InboundApplied{ID: fmt.Sprintf("inb_%03d", i), State: "active"}
		}
		stmts, err := nodeAppliedStmts("nod_statement_count", 1, false, 1, "hash", in, time.Unix(1_700_000_000, 0).UTC())
		if err != nil {
			t.Fatalf("NodeApplied statements for %d inbounds: %v", count, err)
		}
		if len(stmts) != 3 {
			t.Errorf("NodeApplied statements with %d inbounds = %d, want 3", count, len(stmts))
		}
		if !strings.Contains(stmts[2].Query, "json_each(?1)") || len(stmts[2].Args) != 3 {
			t.Errorf("NodeApplied inbound statement does not use one bounded JSON input: query=%q args=%d", stmts[2].Query, len(stmts[2].Args))
		}
	}
}

func TestNodeAppliedWritesInboundResultsAndKeepsGuards(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	f := newFleetBatchFixture(t, s, "node_applied")
	other := newFleetBatchFixture(t, s, "node_applied_other")
	if _, _, err := s.NodeHello(ctx, f.nodeID, 42, HelloInfo{Instance: "instance"}, f.now); err != nil {
		t.Fatal(err)
	}

	inboundIDs := []string{f.inboundID}
	for i := 1; i < 50; i++ {
		profileID := fmt.Sprintf("prf_apply_%03d", i)
		inboundID := fmt.Sprintf("inb_apply_%03d", i)
		if err := s.Access().CreateProfile(ctx, AccessProfile{ID: profileID, Protocol: "awg", Name: profileID, SettingsJSON: "{}", CreatedAt: f.now}); err != nil {
			t.Fatal(err)
		}
		if err := s.Access().CreateInbound(ctx, AccessInbound{ID: inboundID, ProfileID: profileID, NodeID: f.nodeID, Enabled: true, CreatedAt: f.now}); err != nil {
			t.Fatal(err)
		}
		inboundIDs = append(inboundIDs, inboundID)
	}

	const disabledID = "inb_apply_disabled"
	const disabledProfileID = "prf_apply_disabled"
	if err := s.Access().CreateProfile(ctx, AccessProfile{ID: disabledProfileID, Protocol: "awg", Name: disabledProfileID, SettingsJSON: "{}", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Access().CreateInbound(ctx, AccessInbound{ID: disabledID, ProfileID: disabledProfileID, NodeID: f.nodeID, Enabled: false, CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}

	readInbound := func(id string) (state, lastError, specHash, pin string, notAfter, updatedAt int64) {
		t.Helper()
		if err := s.R.QueryRowContext(ctx, `SELECT state, last_error, applied_spec_hash, cert_pin_sha256, cert_not_after, updated_at
			FROM inbound WHERE id = ?`, id).Scan(&state, &lastError, &specHash, &pin, &notAfter, &updatedAt); err != nil {
			t.Fatalf("read inbound %q: %v", id, err)
		}
		return
	}
	readNode := func() (int64, string) {
		t.Helper()
		var rev int64
		var hash string
		if err := s.R.QueryRowContext(ctx, `SELECT applied_revision, applied_hash FROM node WHERE id = ?`, f.nodeID).Scan(&rev, &hash); err != nil {
			t.Fatal(err)
		}
		return rev, hash
	}

	if err := s.NodeApplied(ctx, f.nodeID, 42, true, 1, "empty", nil, f.now); err != nil {
		t.Fatalf("NodeApplied with no inbounds: %v", err)
	}
	if rev, hash := readNode(); rev != 1 || hash != "empty" {
		t.Fatalf("node after empty result = (%d, %q), want (1, empty)", rev, hash)
	}
	if got := queryInt(t, s, `SELECT drift FROM node_live WHERE node_id = ?`, f.nodeID); got != 1 {
		t.Fatalf("node_live drift after current-session result = %d, want 1", got)
	}

	if err := s.NodeApplied(ctx, f.nodeID, 42, true, 2, "one", []InboundApplied{{ID: f.inboundID, State: "active", Error: "single error",
		SpecHash: "single spec", CertPin: "single pin"}}, f.now.Add(time.Second)); err != nil {
		t.Fatalf("NodeApplied with one inbound: %v", err)
	}
	if state, lastError, specHash, pin, notAfter, updatedAt := readInbound(f.inboundID); state != "active" || lastError != "single error" ||
		specHash != "single spec" || pin != "single pin" || notAfter != 0 || updatedAt != f.now.Add(time.Second).Unix() {
		t.Fatalf("single inbound result = (%q, %q, %q, %q, %d, %d)", state, lastError, specHash, pin, notAfter, updatedAt)
	}

	const appliedAt = 1_700_000_002
	applyTime := time.Unix(appliedAt, 0).UTC()
	many := make([]InboundApplied, 0, 54)
	want := make(map[string]InboundApplied, len(inboundIDs))
	for i, id := range inboundIDs {
		state := "active"
		if i%2 == 0 {
			state = "failed"
		}
		result := InboundApplied{ID: id, State: state, Error: fmt.Sprintf("error_%03d", i), SpecHash: fmt.Sprintf("spec_%03d", i),
			CertPin: fmt.Sprintf("pin_%03d", i), CertNotAfter: f.now.Add(time.Duration(i+1) * time.Hour)}
		many = append(many, result)
		want[id] = result
	}
	many = append(many,
		InboundApplied{ID: disabledID, State: "failed", Error: "disabled", SpecHash: "disabled", CertPin: "disabled", CertNotAfter: f.now.Add(time.Hour)},
		InboundApplied{ID: other.inboundID, State: "failed", Error: "foreign", SpecHash: "foreign", CertPin: "foreign", CertNotAfter: f.now.Add(time.Hour)},
		InboundApplied{ID: f.inboundID, State: "active", Error: "first duplicate", SpecHash: "first duplicate", CertPin: "first duplicate", CertNotAfter: f.now.Add(2 * time.Hour)},
		InboundApplied{ID: f.inboundID, State: "failed", Error: "last duplicate", SpecHash: "last duplicate", CertPin: "last duplicate", CertNotAfter: f.now.Add(3 * time.Hour)},
	)
	want[f.inboundID] = many[len(many)-1]
	if err := s.NodeApplied(ctx, f.nodeID, 42, false, 3, "many", many, applyTime); err != nil {
		t.Fatalf("NodeApplied with many inbounds: %v", err)
	}
	if rev, hash := readNode(); rev != 3 || hash != "many" {
		t.Fatalf("node after many results = (%d, %q), want (3, many)", rev, hash)
	}
	for _, id := range inboundIDs {
		expected := want[id]
		state, lastError, specHash, pin, notAfter, updatedAt := readInbound(id)
		if state != expected.State || lastError != expected.Error || specHash != expected.SpecHash || pin != expected.CertPin ||
			notAfter != fleetUnix(expected.CertNotAfter) || updatedAt != appliedAt {
			t.Errorf("inbound %q = (%q, %q, %q, %q, %d, %d), want (%q, %q, %q, %q, %d, %d)", id,
				state, lastError, specHash, pin, notAfter, updatedAt, expected.State, expected.Error, expected.SpecHash,
				expected.CertPin, fleetUnix(expected.CertNotAfter), appliedAt)
		}
	}
	for _, id := range []string{disabledID, other.inboundID} {
		state, lastError, specHash, pin, notAfter, updatedAt := readInbound(id)
		if state != "pending" || lastError != "" || specHash != "" || pin != "" || notAfter != 0 || updatedAt != f.now.Unix() {
			t.Errorf("guarded inbound %q changed: (%q, %q, %q, %q, %d, %d)", id, state, lastError, specHash, pin, notAfter, updatedAt)
		}
	}

	if err := s.NodeApplied(ctx, f.nodeID, 41, true, 4, "foreign-session", nil, applyTime.Add(time.Second)); err != nil {
		t.Fatalf("NodeApplied from a foreign session: %v", err)
	}
	if got := queryInt(t, s, `SELECT drift FROM node_live WHERE node_id = ?`, f.nodeID); got != 0 {
		t.Fatalf("foreign-session NodeApplied changed drift to %d, want 0", got)
	}
}

func TestNodeAppliedBoundsInboundJSON(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := s.NodeApplied(ctx, "nod_missing", 1, false, 1, "", make([]InboundApplied, maxNodeAppliedInbounds+1), now); err == nil {
		t.Fatal("NodeApplied accepted more than the inbound count limit")
	}
	if err := s.NodeApplied(ctx, "nod_missing", 1, false, 1, "", []InboundApplied{{ID: strings.Repeat("x", maxNodeAppliedJSONBytes)}}, now); err == nil {
		t.Fatal("NodeApplied accepted a JSON parameter larger than the byte limit")
	}
}

func TestEnrollmentBatchReplayAndFleetErrors(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	f := newFleetBatchFixture(t, s, "enroll_batch")
	if err := s.InsertCA(ctx, CARow{ID: "cas_batch", CertPEM: "ca", Fingerprint: "fingerprint", KeyEnc: []byte{1},
		NotBefore: f.now.Add(-time.Hour), NotAfter: f.now.AddDate(1, 0, 0)}, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEnrollment(ctx, &NodeRow{ID: "nod_name_collision", Name: "enroll_batch", Address: "example.com"}, "", []byte("other-token"), "adm_test", f.now, f.now.Add(time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateEnrollment(taken name) = %v, want ErrConflict", err)
	}

	const workers = 8
	start := make(chan struct{})
	results := make([]EnrollResult, workers)
	errs := make([]error, workers)
	var issues atomic.Int32
	var wg sync.WaitGroup
	issue := func(nodeID string) (CertRow, error) {
		serial := issues.Add(1)
		return CertRow{Serial: fmt.Sprintf("serial-%d", serial), NodeID: nodeID, CAID: "cas_batch", PEM: "certificate",
			NotBefore: f.now, NotAfter: f.now.Add(24 * time.Hour), IssuedAt: f.now}, nil
	}
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = s.Enroll(ctx, []byte("enroll_batch-token"), []byte("same-key"), f.now, issue)
		}(i)
	}
	close(start)
	wg.Wait()
	replays := 0
	serial := ""
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("concurrent Enroll %d: %v", i, errs[i])
		}
		if results[i].Replay {
			replays++
		}
		if serial == "" {
			serial = results[i].Cert.Serial
		} else if results[i].Cert.Serial != serial {
			t.Fatalf("concurrent Enroll serials differ: %q and %q", serial, results[i].Cert.Serial)
		}
	}
	if replays != workers-1 {
		t.Fatalf("concurrent Enroll replays = %d, want %d", replays, workers-1)
	}
	if got := queryInt(t, s, `SELECT count(*) FROM node_cert WHERE node_id = ?`, f.nodeID); got != 1 {
		t.Fatalf("certificates after concurrent Enroll = %d, want 1", got)
	}
	replay, err := s.Enroll(ctx, []byte("enroll_batch-token"), []byte("same-key"), f.now, issue)
	if err != nil || !replay.Replay || replay.Cert.Serial != serial {
		t.Fatalf("same-key Enroll replay = %+v, %v", replay, err)
	}
	if _, err := s.Enroll(ctx, []byte("enroll_batch-token"), []byte("different-key"), f.now, issue); !errors.Is(err, ErrEnrollToken) {
		t.Fatalf("different-key Enroll = %v, want ErrEnrollToken", err)
	}

	if _, _, err := s.NodeHello(ctx, "nod_missing", 1, HelloInfo{Instance: "instance"}, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NodeHello(missing) = %v, want ErrNotFound", err)
	}
	retired, err := s.CreateEnrollment(ctx, &NodeRow{ID: "nod_retired_batch", Name: "retired_batch", Address: "example.com"}, "", []byte("retired-token"), "adm_test", f.now, f.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enroll(ctx, []byte("retired-token"), []byte("retired-key"), f.now, issue); err != nil {
		t.Fatalf("Enroll before retirement: %v", err)
	}
	if err := s.RetireNode(ctx, retired.ID, f.now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NodeHello(ctx, retired.ID, 1, HelloInfo{Instance: "instance"}, f.now); !errors.Is(err, ErrNodeRetired) {
		t.Fatalf("NodeHello(retired) = %v, want ErrNodeRetired", err)
	}
	if err := s.RetireNode(ctx, retired.ID, f.now); !errors.Is(err, ErrNodeRetired) {
		t.Fatalf("RetireNode(retired) = %v, want ErrNodeRetired", err)
	}
	if err := s.RetireNode(ctx, "nod_missing", f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RetireNode(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Enroll(ctx, []byte("retired-token"), []byte("key"), f.now, issue); !errors.Is(err, ErrNodeRetired) {
		t.Fatalf("Enroll(retired) = %v, want ErrNodeRetired", err)
	}
}

func queryInt(t *testing.T, s *Store, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if err := s.R.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
