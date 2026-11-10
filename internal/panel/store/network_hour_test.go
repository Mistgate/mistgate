//go:build !js

package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIngestStatsNetworksFiltersAndAggregates(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	f := newFleetBatchFixture(t, st, "asn_stats")
	profileID, inboundID := "prf_asn_vless", "inb_asn_vless"
	if err := st.Access().CreateProfile(ctx, AccessProfile{ID: profileID, Protocol: "vless", Name: "de1", SettingsJSON: "{}", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateInbound(ctx, AccessInbound{ID: inboundID, ProfileID: profileID, NodeID: f.nodeID, Enabled: true, CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO device_credential
		(id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
		VALUES ('crd_asn_vless', ?, ?, 'vless', ?, '{}', ?, ?, 0)`, f.deviceID, f.userID, []byte("asn-vless"), f.now.Unix(), profileID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.NodeHello(ctx, f.nodeID, 1, HelloInfo{AgentVersion: "test", Instance: "asn-instance"}, f.now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNetworkMode(ctx, 2); err != nil {
		t.Fatal(err)
	}
	hour := f.now.Unix() / 3600 * 3600
	for seq, counters := range []FleetNetwork{
		{InboundID: f.inboundID, ASN: 64496, Attempts: 5, Unfinished: 2, Connects: 3, Broken: 1, People: 3},
		{InboundID: f.inboundID, ASN: 64496, Attempts: 7, Unfinished: 1, Connects: 4, Broken: 2, People: 2},
	} {
		personRows := []FleetPersonNetwork{
			{CredID: f.credID, InboundID: f.inboundID, ASN: 64496, Connects: 2},
			{CredID: "crd_asn_vless", InboundID: f.inboundID, ASN: 64497, Connects: 9}, // wrong protocol for this inbound
			{CredID: "crd_asn_vless", InboundID: inboundID, ASN: 64497, Connects: 4},
			{CredID: "crd_asn_unknown", InboundID: f.inboundID, ASN: 64498, Connects: 8},
		}
		out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: f.nodeID, Instance: "asn-instance", Seq: uint64(seq + 1), Now: f.now,
			HourStart: hour, Networks: []FleetNetwork{counters, {InboundID: "inb_asn_foreign", ASN: 64499, Connects: 10}}, PersonNetworks: personRows})
		if err != nil || out.Duplicate {
			t.Fatalf("IngestStats network batch %d = %+v, %v", seq+1, out, err)
		}
	}
	var attempts, unfinished, connects, broken, people int64
	if err := st.R.QueryRowContext(ctx, `SELECT attempts, unfinished, connects, broken, people FROM network_hour
		WHERE node_id = ? AND hour_start = ? AND inbound_id = ? AND asn = 64496`, f.nodeID, hour, f.inboundID).
		Scan(&attempts, &unfinished, &connects, &broken, &people); err != nil {
		t.Fatal(err)
	}
	if attempts != 12 || unfinished != 3 || connects != 7 || broken != 3 || people != 3 {
		t.Fatalf("network_hour = %d/%d/%d/%d people %d, want 12/3/7/3 people 3", attempts, unfinished, connects, broken, people)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM network_hour`); got != 1 {
		t.Fatalf("network rows = %d, want only the inbound belonging to this node", got)
	}
	var userID string
	var userConnects, lastHour int64
	if err := st.R.QueryRowContext(ctx, `SELECT user_id, connects, last_hour FROM user_network WHERE asn = 64496`).Scan(&userID, &userConnects, &lastHour); err != nil {
		t.Fatal(err)
	}
	if userID != f.userID || userConnects != 4 || lastHour != hour {
		t.Fatalf("user_network = %q/%d/%d, want %q/4/%d", userID, userConnects, lastHour, f.userID, hour)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM user_network`); got != 2 {
		t.Fatalf("person network rows = %d, want only the valid AWG and VLESS credentials", got)
	}

	if _, err := st.W.ExecContext(ctx, `INSERT INTO network_hour (node_id, hour_start, inbound_id, asn, attempts, unfinished, connects, broken, people)
		VALUES (?, ?, ?, ?, 0, 0, 0, 0, 0)`, f.nodeID, hour+3600, f.inboundID, 64500); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE user_network SET last_hour = ? WHERE asn = 64496`, hour+3600); err != nil {
		t.Fatal(err)
	}
	deletedHours, err := st.PruneNetworkHours(ctx, time.Unix(hour+3600, 0))
	if err != nil || deletedHours != 1 {
		t.Fatalf("PruneNetworkHours() = %d, %v; want one expired bucket", deletedHours, err)
	}
	deletedPeople, err := st.PruneUserNetworks(ctx, time.Unix(hour+3600, 0))
	if err != nil || deletedPeople != 1 {
		t.Fatalf("PruneUserNetworks() = %d, %v; want one expired person row", deletedPeople, err)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM network_hour`); got != 1 {
		t.Fatalf("network rows after prune = %d, want one newer bucket", got)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM user_network`); got != 1 {
		t.Fatalf("person rows after prune = %d, want one newer row", got)
	}
}

func TestSaveNetworkModeDeletesObsoleteRowsWithSetting(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if err := st.SaveNetworkMode(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO network_hour (node_id, hour_start, inbound_id, asn, attempts, unfinished, connects, broken, people)
		VALUES ('nod_mode', 1700000000, 'inb_mode', 64496, 0, 0, 3, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO user_network (user_id, asn, connects, last_hour) VALUES ('usr_alice', 64496, 2, 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNetworkMode(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if mode, err := st.NetworkMode(ctx); err != nil || mode != 1 {
		t.Fatalf("mode after 2 -> 1 = %d, %v", mode, err)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM network_hour`); got != 1 {
		t.Fatalf("network_hour after 2 -> 1 = %d, want kept", got)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM user_network`); got != 0 {
		t.Fatalf("user_network after 2 -> 1 = %d, want deleted", got)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO user_network (user_id, asn, connects, last_hour) VALUES ('usr_bob', 64497, 1, 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNetworkMode(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if mode, err := st.NetworkMode(ctx); err != nil || mode != 0 {
		t.Fatalf("mode after off = %d, %v", mode, err)
	}
	for _, table := range []string{"network_hour", "user_network"} {
		if got := queryInt(t, st, `SELECT COUNT(*) FROM `+table); got != 0 {
			t.Errorf("%s after off = %d, want deleted", table, got)
		}
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO user_network (user_id, asn, connects, last_hour) VALUES ('usr_bob', 64497, 1, 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNetworkMode(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM user_network`); got != 0 {
		t.Fatalf("user_network after 0 -> 1 = %d, want deleted unconditionally", got)
	}
	if err := st.SaveNetworkMode(ctx, 3); err != ErrInvalidNetworkMode {
		t.Fatalf("mode 3 error = %v, want ErrInvalidNetworkMode", err)
	}
}

func TestInFlightNetworkBatchCannotReinsertAfterOff(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	f := newFleetBatchFixture(t, st, "network_off_race")
	if _, _, err := st.NodeHello(ctx, f.nodeID, 1, HelloInfo{AgentVersion: "test", Instance: "network-off"}, f.now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNetworkMode(ctx, 2); err != nil {
		t.Fatal(err)
	}
	// The network rows have already passed the fleet's in-memory mode-2 guard when the owner switches off.
	if err := st.SaveNetworkMode(ctx, 0); err != nil {
		t.Fatal(err)
	}
	_, err := st.IngestStats(ctx, FleetStatsIn{NodeID: f.nodeID, Instance: "network-off", Seq: 1, Now: f.now,
		HourStart:      f.now.Unix() / 3600 * 3600,
		Networks:       []FleetNetwork{{InboundID: f.inboundID, ASN: 64496, Connects: 3}},
		PersonNetworks: []FleetPersonNetwork{{CredID: f.credID, InboundID: f.inboundID, ASN: 64496, Connects: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM network_hour`); got != 0 {
		t.Fatalf("network_hour after guarded batch committed after off = %d, want 0", got)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM user_network`); got != 0 {
		t.Fatalf("user_network after guarded batch committed after off = %d, want 0", got)
	}
}

func TestProbePersonNetworkRowsAreNotDroppedAsUnknownCredentials(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	f := newFleetBatchFixture(t, st, "network_probe")
	if _, _, err := st.NodeHello(ctx, f.nodeID, 1, HelloInfo{AgentVersion: "test", Instance: "network-probe"}, f.now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNetworkMode(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO health_probe_cred (inbound_id, cred_id, secret_enc, data_json, created_at)
		VALUES (?, 'crd_probe', X'01', '{}', ?)`, f.inboundID, f.now.Unix()); err != nil {
		t.Fatal(err)
	}
	out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: f.nodeID, Instance: "network-probe", Seq: 1, Now: f.now,
		HourStart:      f.now.Unix() / 3600 * 3600,
		PersonNetworks: []FleetPersonNetwork{{CredID: "crd_probe", InboundID: f.inboundID, ASN: 64496, Connects: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 0 || out.Probe == 0 {
		t.Fatalf("probe person row result skipped=%d probe=%d; want no dropped entry and recognized probe", out.Skipped, out.Probe)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM user_network`); got != 0 {
		t.Fatalf("user_network contains a probe row: %d", got)
	}
}

func TestNetworkHourStmtsAreOmittedWithoutRows(t *testing.T) {
	in := FleetStatsIn{NodeID: "nod_de1"}
	build := func(networks []FleetNetwork, persons []userNetworkWrite) ([]Stmt, error) {
		return fleetStatsWriteStmts(in, "instance", 0, nil, nil, nil, nil, nil, nil, nil, networks, persons, FleetLive{})
	}
	base, err := build(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	withNetwork, err := build([]FleetNetwork{{InboundID: "inb_de1", ASN: 64496, Connects: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	withPerson, err := build(nil, []userNetworkWrite{{UserID: "usr_alice", ASN: 64496, Connects: 1}})
	if err != nil {
		t.Fatal(err)
	}
	withBoth, err := build([]FleetNetwork{{InboundID: "inb_de1", ASN: 64496, Connects: 1}}, []userNetworkWrite{{UserID: "usr_alice", ASN: 64496, Connects: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 11 || len(withNetwork) != len(base)+1 || len(withPerson) != len(base)+1 || len(withBoth) != len(base)+2 {
		t.Fatalf("write statement counts base/network/person/both = %d/%d/%d/%d, want 11/12/12/13", len(base), len(withNetwork), len(withPerson), len(withBoth))
	}
	var networkHourInsert, userNetworkInsert bool
	for _, stmt := range withBoth {
		networkHourInsert = networkHourInsert || strings.Contains(stmt.Query, "INSERT INTO network_hour")
		userNetworkInsert = userNetworkInsert || strings.Contains(stmt.Query, "INSERT INTO user_network")
	}
	if !networkHourInsert || !userNetworkInsert {
		t.Fatalf("combined D1 network write shape has network_hour=%v, user_network=%v", networkHourInsert, userNetworkInsert)
	}
}

func TestFleetNetworkHourSumsPrioritizesBaselineAndExcludesRetiredNodes(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	f := newFleetBatchFixture(t, st, "network_sums")
	since := time.Unix(1_700_000_000, 0).UTC().Truncate(time.Hour)
	previousSince := since.Add(-time.Hour)
	until := since.Add(time.Hour)
	for _, row := range []struct {
		hour     int64
		inbound  string
		asn      uint32
		connects int64
	}{
		{previousSince.Unix(), "inb_baseline", 64496, 12},
		{since.Unix(), "inb_current", 64497, 5},
	} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO network_hour (node_id, hour_start, inbound_id, asn, attempts, unfinished, connects, broken, people)
			VALUES (?, ?, ?, ?, 1, 0, ?, 0, 0)`, f.nodeID, row.hour, row.inbound, row.asn, row.connects); err != nil {
			t.Fatal(err)
		}
	}
	sums, err := st.FleetNetworkHourSums(ctx, since, previousSince, until, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 || sums[0].InboundID != "inb_baseline" || sums[0].Connects != 0 || sums[0].BaselineConnects != 12 {
		t.Fatalf("top network sum = %+v, want baseline-only row with 12 baseline connects", sums)
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE node SET state = 'retired' WHERE id = ?`, f.nodeID); err != nil {
		t.Fatal(err)
	}
	sums, err = st.FleetNetworkHourSums(ctx, since, previousSince, until, "", 10)
	if err != nil || len(sums) != 0 {
		t.Fatalf("retired-node sums = %+v, %v; want no rows", sums, err)
	}
}

func TestDeleteUsersDeletesUserNetworksInSameBatch(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	f := newFleetBatchFixture(t, st, "delete_user_network")
	if _, err := st.W.ExecContext(ctx, `INSERT INTO user_network (user_id, asn, connects, last_hour) VALUES (?, 64496, 2, 1700000000)`, f.userID); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.Access().DeleteUsers(ctx, []string{f.userID})
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteUsers = %d, %v; want one user", deleted, err)
	}
	if got := queryInt(t, st, `SELECT COUNT(*) FROM user_network`); got != 0 {
		t.Fatalf("user_network after deleting its user = %d, want 0", got)
	}
}
