package store

import (
	"context"
	"testing"
	"time"
)

// linkHourStoreProof runs on SQLite and on D1 (see link_hour_test.go and d1_store_test.go): the counters of a StatsBatch go in as
// one statement, add up inside an hour bucket, pass the traffic's filter, and are read back per node and per person; the
// retention keeps the bucket that holds its cut.
func linkHourStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 23, 12, 20, 0, 0, time.UTC)
	hour := base.Unix() / 3600 * 3600
	for _, q := range []string{
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_lh_a', 'de1', '203.0.113.10', 'active', 1)`,
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_lh_b', 'nl1', '203.0.113.11', 'active', 1)`,
	} {
		if _, err := st.W.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []AccessProfile{{ID: "prf_lh_awg", Protocol: "awg", Name: "Home AWG"}, {ID: "prf_lh_hy2", Protocol: "hysteria2", Name: "Fast"}} {
		p.SettingsJSON, p.CreatedAt = "{}", base
		if err := st.Access().CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range []AccessInbound{{ID: "inb_lh_awg", ProfileID: "prf_lh_awg", NodeID: "nod_lh_a"},
		{ID: "inb_lh_hy2", ProfileID: "prf_lh_hy2", NodeID: "nod_lh_a"}, {ID: "inb_lh_b", ProfileID: "prf_lh_awg", NodeID: "nod_lh_b"}} {
		in.Enabled, in.CreatedAt = true, base
		if err := st.Access().CreateInbound(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Access().CreateGroup(ctx, AccessGroup{ID: "grp_lh", Name: "lh", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"usr_lh", "usr_lh_other"} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
			VALUES (?, ?, 'grp_lh', ?, ?, ?, ?)`, u, u, base.Unix(), []byte(u), []byte{1}, base.Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.ExecContext(ctx, `INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at)
			VALUES (?, ?, ?, 0, 0, ?)`, "dev_"+u, u, []byte(u), base.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range [][4]string{{"crd_lh_awg", "usr_lh", "awg", "prf_lh_awg"}, {"crd_lh_hy2", "usr_lh", "hysteria2", "prf_lh_hy2"},
		{"crd_lh_other", "usr_lh_other", "awg", "prf_lh_awg"}} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
			VALUES (?, ?, ?, ?, ?, '{}', ?, ?, 0)`, c[0], "dev_"+c[1], c[1], c[2], []byte{1}, base.Unix(), c[3]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO health_probe_cred (inbound_id, cred_id, secret_enc, data_json, created_at)
		VALUES ('inb_lh_awg', 'crd_lh_probe', ?, '{}', 1)`, []byte{1}); err != nil {
		t.Fatal(err)
	}

	var seq uint64
	ingest := func(at int64, links ...FleetLink) FleetStatsOut {
		t.Helper()
		seq++
		out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: "nod_lh_a", Instance: "lh", Seq: seq, Now: base, HourStart: at, Links: links})
		if err != nil || out.Duplicate {
			t.Fatalf("IngestStats(%d) = %+v, %v", seq, out, err)
		}
		return out
	}
	// the filter is the traffic's: a foreign inbound, a key of another protocol, an unknown key and the inbound row of a foreign
	// inbound are not stored (skipped); the probe key is told apart and its handshakes go to its inbound's row (not for an inbound
	// of another node); a row with no counter is not a row
	out := ingest(hour,
		FleetLink{CredID: "crd_lh_awg", InboundID: "inb_lh_awg", Connects: 2, Stalls: 1, OnlineS: 100},
		FleetLink{CredID: "crd_lh_awg", InboundID: "inb_lh_awg", Connects: 1},
		FleetLink{CredID: "crd_lh_hy2", InboundID: "inb_lh_hy2", Connects: 3, Drops: 1},
		FleetLink{InboundID: "inb_lh_awg", UnknownKeys: 5, Hellos: 10, Handshakes: 4},
		FleetLink{CredID: "crd_lh_other", InboundID: "inb_lh_awg", Rejects: 7},
		FleetLink{CredID: "crd_lh_awg", InboundID: "inb_lh_b", Connects: 9},
		FleetLink{CredID: "crd_lh_hy2", InboundID: "inb_lh_awg", Connects: 9},
		FleetLink{CredID: "crd_lh_unknown", InboundID: "inb_lh_awg", Connects: 9},
		FleetLink{InboundID: "inb_lh_b", Hellos: 9},
		FleetLink{CredID: "crd_lh_probe", InboundID: "inb_lh_awg", Connects: 3, Drops: 1},
		FleetLink{CredID: "crd_lh_probe", InboundID: "inb_lh_b", Connects: 9},
		FleetLink{CredID: "crd_lh_awg", InboundID: "inb_lh_awg"})
	if out.Skipped != 4 || out.Probe != 2 {
		t.Errorf("skipped %d, probe %d, want 4 and 2", out.Skipped, out.Probe)
	}
	// the same hour adds to the row; the next hour and an older one are rows of their own. The probe's check of the next hour
	// comes before its handshake (the agent flushes those every 10 minutes): its inbound row has nothing to take it off.
	ingest(hour, FleetLink{CredID: "crd_lh_awg", InboundID: "inb_lh_awg", Connects: 1})
	ingest(hour+3600, FleetLink{CredID: "crd_lh_awg", InboundID: "inb_lh_awg", Rejects: 2}, FleetLink{CredID: "crd_lh_probe", InboundID: "inb_lh_awg", Connects: 2})
	ingest(hour-3*3600, FleetLink{CredID: "crd_lh_awg", InboundID: "inb_lh_awg", Connects: 40})
	ingest(hour) // a batch without counters still runs the statement

	// rows: the awg key, the hysteria2 key, the other person's key and the inbound row in the hour, the awg key in two more hours
	// and the inbound row of the next hour (the probe's)
	var rows int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM link_hour`).Scan(&rows); err != nil || rows != 7 {
		t.Fatalf("rows %d, %v, want 7", rows, err)
	}
	var probe int64
	if err := st.R.QueryRowContext(ctx, `SELECT probe_handshakes FROM link_hour WHERE cred_id = '' AND inbound_id = 'inb_lh_awg' AND hour_start = ?`, hour).Scan(&probe); err != nil || probe != 3 {
		t.Fatalf("probe handshakes of the hour %d, %v, want 3", probe, err)
	}
	var user string
	if err := st.R.QueryRowContext(ctx, `SELECT user_id FROM link_hour WHERE cred_id = 'crd_lh_awg' AND hour_start = ?`, hour).Scan(&user); err != nil || user != "usr_lh" {
		t.Fatalf("user of the key row %q, %v", user, err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT user_id FROM link_hour WHERE cred_id = '' AND inbound_id = 'inb_lh_awg'`).Scan(&user); err != nil || user != "" {
		t.Fatalf("user of the inbound row %q, %v", user, err)
	}

	// a counter is clamped, so a row stays short enough for the largest batch to fit one D1 value
	ingest(hour-10*3600, FleetLink{CredID: "crd_lh_other", InboundID: "inb_lh_awg", OnlineS: 1<<32 - 1})
	var online int64
	if err := st.R.QueryRowContext(ctx, `SELECT online_s FROM link_hour WHERE hour_start = ?`, hour-10*3600).Scan(&online); err != nil || online != maxLinkCount {
		t.Errorf("a clamped counter = %d, %v, want %d", online, err, maxLinkCount)
	}
	if _, err := st.W.ExecContext(ctx, `DELETE FROM link_hour WHERE hour_start = ?`, hour-10*3600); err != nil {
		t.Fatal(err)
	}

	// the node's inbounds, from the hour of base on: the probe's five handshakes are off the inbound's hellos and handshakes (the
	// handshakes stop at zero), and nothing else of the probe shows
	sums, err := st.NodeLinkSums(ctx, "nod_lh_a", base)
	if err != nil || len(sums) != 2 {
		t.Fatalf("NodeLinkSums = %+v, %v", sums, err)
	}
	if want := (LinkInboundSums{InboundID: "inb_lh_awg", Connects: 4, Rejects: 9, Stalls: 1, OnlineS: 100, UnknownKeys: 5, Hellos: 5,
		KeysStalled: 1, KeysTried: 1}); sums[0] != want {
		t.Errorf("awg inbound = %+v, want %+v", sums[0], want)
	}
	if want := (LinkInboundSums{InboundID: "inb_lh_hy2", Connects: 3, Drops: 1, KeysTried: 1}); sums[1] != want {
		t.Errorf("hysteria2 inbound = %+v, want %+v", sums[1], want)
	}
	if sums, err = st.NodeLinkSums(ctx, "nod_lh_a", base.Add(time.Hour)); err != nil || len(sums) != 1 ||
		sums[0] != (LinkInboundSums{InboundID: "inb_lh_awg", Rejects: 2}) {
		t.Errorf("NodeLinkSums from the next hour = %+v, %v", sums, err)
	}
	if sums, err = st.NodeLinkSums(ctx, "nod_lh_a", base.Add(-4*time.Hour)); err != nil || sums[0].Connects != 44 {
		t.Errorf("NodeLinkSums over four more hours = %+v, %v", sums, err)
	}
	if sums, err = st.NodeLinkSums(ctx, "nod_lh_b", base.Add(-4*time.Hour)); err != nil || len(sums) != 0 {
		t.Errorf("NodeLinkSums of the other node = %+v, %v", sums, err)
	}

	// the person: per device, node and inbound; node name, then profile name; the other person's key is not here
	links, err := st.UserLinks(ctx, "usr_lh", base)
	if err != nil || len(links) != 2 {
		t.Fatalf("UserLinks = %+v, %v", links, err)
	}
	if want := (UserLink{DeviceID: "dev_usr_lh", NodeID: "nod_lh_a", NodeName: "de1", InboundID: "inb_lh_hy2", ProfileName: "Fast", Protocol: "hysteria2",
		Connects: 3, Drops: 1, LastHour: time.Unix(hour, 0)}); !sameLink(links[0], want) {
		t.Errorf("first row = %+v, want %+v", links[0], want)
	}
	if want := (UserLink{DeviceID: "dev_usr_lh", NodeID: "nod_lh_a", NodeName: "de1", InboundID: "inb_lh_awg", ProfileName: "Home AWG", Protocol: "awg",
		Connects: 4, Rejects: 2, Stalls: 1, OnlineS: 100, LastHour: time.Unix(hour+3600, 0)}); !sameLink(links[1], want) {
		t.Errorf("second row = %+v, want %+v", links[1], want)
	}
	if links, err = st.UserLinks(ctx, "usr_lh_other", base); err != nil || len(links) != 1 || links[0].Rejects != 7 {
		t.Errorf("the other person = %+v, %v", links, err)
	}
	if links, err = st.UserLinks(ctx, "usr_nobody", base); err != nil || len(links) != 0 {
		t.Errorf("UserLinks of nobody = %+v, %v", links, err)
	}

	// the retention keeps the bucket that holds its cut (12:20 is in the 12:00 bucket) and drops the older ones
	if n, err := st.PruneLinkHours(ctx, base); err != nil || n != 1 {
		t.Errorf("pruned %d, %v, want the one row of three hours earlier", n, err)
	}
	if n, err := st.PruneLinkHours(ctx, base.Add(time.Hour)); err != nil || n != 4 {
		t.Errorf("pruned %d, %v, want the four rows of the first hour", n, err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM link_hour`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("rows after the retention %d, %v, want 2", rows, err)
	}
}

func sameLink(a, b UserLink) bool {
	return a.LastHour.Equal(b.LastHour) && func() bool { a.LastHour, b.LastHour = time.Time{}, time.Time{}; return a == b }()
}
