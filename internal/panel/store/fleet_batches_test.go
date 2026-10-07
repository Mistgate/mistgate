//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
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
	stmts, err := fleetStatsWriteStmts(in, in.Instance, 0, buckets, hours, userWrites, certs, touches)
	if err != nil {
		t.Fatal(err)
	}
	before := 1 + len(buckets) + len(hours) + len(userWrites) + sessions + len(certs) + 1
	if before != 1211 {
		t.Fatalf("legacy batch statement count = %d, want 1211", before)
	}
	if len(stmts) != 7 {
		t.Fatalf("IngestStats statements for %d users, %d sessions, and %d certificates: before=%d after=%d, want 7 after",
			users, sessions, certificates, before, len(stmts))
	}
	t.Logf("IngestStats statements for %d users, %d sessions, and %d certificates: before=%d after=%d",
		users, sessions, certificates, before, len(stmts))
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

	if _, _, err := s.NodeHello(ctx, "nod_missing", HelloInfo{Instance: "instance"}, f.now); !errors.Is(err, ErrNotFound) {
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
	if _, _, err := s.NodeHello(ctx, retired.ID, HelloInfo{Instance: "instance"}, f.now); !errors.Is(err, ErrNodeRetired) {
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
