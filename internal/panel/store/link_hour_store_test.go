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
		client, platform := "Happ/3.4.2", "android"
		if u == "usr_lh_other" {
			client, platform = "AmneziaVPN/4.5", "windows"
		}
		if _, err := st.W.ExecContext(ctx, `INSERT INTO device (id, user_id, hwid_hash, kind, platform, client, first_seen_at, last_seen_at, created_at)
			VALUES (?, ?, ?, 'link', ?, ?, 0, 0, ?)`, "dev_"+u, u, []byte(u), platform, client, base.Unix()); err != nil {
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
		FleetLink{CredID: "crd_lh_hy2", InboundID: "inb_lh_hy2", Connects: 3, Drops: 1, ClientCloses: 2, Freezes: 3, Quiet: 4,
			RttMSSum: 1500, RttSamples: 3, PktsSent: 500, PktsLost: 5},
		FleetLink{InboundID: "inb_lh_awg", UnknownKeys: 5, Hellos: 10, Handshakes: 4, Accepted: 9, HellosForeign: 7},
		FleetLink{InboundID: "inb_lh_hy2", Accepted: 10, HellosForeign: 2},
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

	// rows: the four keys/inbound rows of the first hour, the next-hour key and inbound row, and the older key. Node B is ingested below.
	var rows int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM link_hour`).Scan(&rows); err != nil || rows != 8 {
		t.Fatalf("rows %d, %v, want 8", rows, err)
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
	ingest(hour-10*3600, FleetLink{CredID: "crd_lh_other", InboundID: "inb_lh_awg", OnlineS: 1<<32 - 1,
		ClientCloses: 1<<32 - 1, RttMSSum: ^uint64(0), RttSamples: 1<<32 - 1, PktsSent: ^uint64(0), PktsLost: ^uint64(0)})
	var online int64
	if err := st.R.QueryRowContext(ctx, `SELECT online_s FROM link_hour WHERE hour_start = ?`, hour-10*3600).Scan(&online); err != nil || online != maxLinkCount {
		t.Errorf("a clamped counter = %d, %v, want %d", online, err, maxLinkCount)
	}
	var closeCount, rttSum, packetsSent, packetsLost int64
	if err := st.R.QueryRowContext(ctx, `SELECT client_closes, rtt_ms_sum, pkts_sent, pkts_lost FROM link_hour WHERE hour_start = ?`, hour-10*3600).
		Scan(&closeCount, &rttSum, &packetsSent, &packetsLost); err != nil || closeCount != maxLinkCount || rttSum != maxLinkSum ||
		packetsSent != maxLinkSum || packetsLost != maxLinkSum {
		t.Errorf("clamped new counters = %d/%d/%d/%d, %v, want %d/%d/%d/%d", closeCount, rttSum, packetsSent, packetsLost,
			err, maxLinkCount, maxLinkSum, maxLinkSum, maxLinkSum)
	}
	if _, err := st.W.ExecContext(ctx, `DELETE FROM link_hour WHERE hour_start = ?`, hour-10*3600); err != nil {
		t.Fatal(err)
	}
	// the node's inbounds, from the hour of base on: the five probe handshakes in this window are taken off accepted, hellos and
	// handshakes (the differences stop at zero); the next-hour check can make the aggregate accepted count low by two.
	sums, err := st.NodeLinkSums(ctx, "nod_lh_a", base)
	if err != nil || len(sums) != 2 {
		t.Fatalf("NodeLinkSums = %+v, %v", sums, err)
	}
	if want := (LinkInboundSums{InboundID: "inb_lh_awg", Connects: 4, Rejects: 9, Stalls: 1, OnlineS: 100, UnknownKeys: 5, Hellos: 5,
		Accepted: 4, HellosForeign: 7, KeysStalled: 1, KeysTried: 1}); sums[0] != want {
		t.Errorf("awg inbound = %+v, want %+v", sums[0], want)
	}
	if want := (LinkInboundSums{InboundID: "inb_lh_hy2", Connects: 3, Drops: 1, ClientCloses: 2, Freezes: 3, Quiet: 4,
		Accepted: 10, HellosForeign: 2, RttMSSum: 1500, RttSamples: 3, PktsSent: 500, PktsLost: 5, KeysTried: 1}); sums[1] != want {
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
		t.Errorf("NodeLinkSums before the other node's own report = %+v, %v", sums, err)
	}
	if _, err := st.IngestStats(ctx, FleetStatsIn{NodeID: "nod_lh_b", Instance: "lhb", Seq: 1, Now: base, HourStart: hour,
		Links: []FleetLink{{CredID: "crd_lh_other", InboundID: "inb_lh_b", Connects: 9}}}); err != nil {
		t.Fatalf("ingest the second node: %v", err)
	}
	sums, err = st.NodeLinkSums(ctx, "nod_lh_b", base)
	if err != nil || len(sums) != 1 || sums[0].Connects != 9 {
		t.Errorf("NodeLinkSums of the second node = %+v, %v", sums, err)
	}

	// The node report adds per-hour rows and a distinct-device app breakdown in one read, while preserving the same totals.
	reports, err := st.NodeLinkReports(ctx, "nod_lh_a", base, true)
	if err != nil || len(reports) != 2 {
		t.Fatalf("NodeLinkReports = %+v, %v", reports, err)
	}
	if got, want := reports[0].Totals, sumsByID(t, st, ctx, "nod_lh_a", base, "inb_lh_awg"); got != want {
		t.Errorf("report totals = %+v, want %+v", got, want)
	}
	awgReport := reports[0]
	if awgReport.Totals.InboundID != "inb_lh_awg" || len(awgReport.Hours) != 2 || awgReport.Hours[0].HourStart != hour ||
		awgReport.Hours[0].Hellos != 7 || awgReport.Hours[0].Handshakes != 1 || awgReport.Hours[0].Accepted != 6 ||
		awgReport.Hours[0].HellosForeign != 7 || awgReport.Hours[1].HourStart != hour+3600 || awgReport.Hours[1].Rejects != 2 ||
		awgReport.Hours[1].Hellos != 0 || awgReport.Hours[1].Handshakes != 0 {
		t.Errorf("node AWG hours = %+v", awgReport.Hours)
	}
	hy2Report := reports[1]
	if hy2Report.Totals.Accepted != 10 || hy2Report.Totals.HellosForeign != 2 || hy2Report.Totals.RttMSSum != 1500 ||
		hy2Report.Totals.RttSamples != 3 || hy2Report.Totals.PktsSent != 500 || hy2Report.Totals.PktsLost != 5 ||
		len(hy2Report.Clients) != 1 || hy2Report.Clients[0].Freezes != 3 || hy2Report.Clients[0].Quiet != 4 ||
		hy2Report.Clients[0].RttMSSum != 1500 || hy2Report.Clients[0].RttSamples != 3 || hy2Report.Clients[0].PktsSent != 500 ||
		hy2Report.Clients[0].PktsLost != 5 || len(hy2Report.Hours) != 1 || hy2Report.Hours[0].RttSamples != 3 {
		t.Errorf("node Hysteria2 quality report = %+v", hy2Report)
	}
	if len(awgReport.Clients) != 2 || awgReport.Clients[0] != (LinkClientSums{Client: "Happ/3.4.2", Platform: "android", Devices: 1, Connects: 4, Rejects: 2, Stalls: 1, OnlineS: 100}) ||
		awgReport.Clients[1] != (LinkClientSums{Client: "AmneziaVPN/4.5", Platform: "windows", Devices: 1, Rejects: 7}) {
		t.Errorf("node AWG clients = %+v", awgReport.Clients)
	}
	withoutHours, err := st.NodeLinkReports(ctx, "nod_lh_a", base, false)
	if err != nil || len(withoutHours) != 2 || len(withoutHours[0].Hours) != 0 || len(withoutHours[0].Clients) != 2 {
		t.Errorf("node report without by_hour = %+v, %v", withoutHours, err)
	}
	fleetSums, err := st.FleetNodeLinkSums(ctx, base)
	if err != nil || len(fleetSums) != 3 || fleetSums[0].NodeID != "nod_lh_a" || fleetSums[0].InboundID != "inb_lh_awg" ||
		fleetSums[0].Connects != 4 || fleetSums[0].Accepted != 4 || fleetSums[0].HellosForeign != 7 ||
		fleetSums[2].NodeID != "nod_lh_b" || fleetSums[2].Connects != 9 {
		t.Errorf("fleet link sums = %+v, %v", fleetSums, err)
	}

	// the person: per device, node and inbound; node name, then profile name; the other person's key is not here
	links, err := st.UserLinks(ctx, "usr_lh", base)
	if err != nil || len(links) != 2 {
		t.Fatalf("UserLinks = %+v, %v", links, err)
	}
	if want := (UserLink{DeviceID: "dev_usr_lh", NodeID: "nod_lh_a", NodeName: "de1", InboundID: "inb_lh_hy2", ProfileName: "Fast", Protocol: "hysteria2",
		Platform: "android", Client: "Happ/3.4.2", Kind: "link",
		Connects: 3, Drops: 1, ClientCloses: 2, Freezes: 3, Quiet: 4, RttMSSum: 1500, RttSamples: 3, PktsSent: 500, PktsLost: 5,
		LastHour: time.Unix(hour, 0)}); !sameLink(links[0], want) {
		t.Errorf("first row = %+v, want %+v", links[0], want)
	}
	if want := (UserLink{DeviceID: "dev_usr_lh", NodeID: "nod_lh_a", NodeName: "de1", InboundID: "inb_lh_awg", ProfileName: "Home AWG", Protocol: "awg",
		Platform: "android", Client: "Happ/3.4.2", Kind: "link",
		Connects: 4, Rejects: 2, Stalls: 1, OnlineS: 100, LastHour: time.Unix(hour+3600, 0)}); !sameLink(links[1], want) {
		t.Errorf("second row = %+v, want %+v", links[1], want)
	}
	links, err = st.UserLinksByHour(ctx, "usr_lh", base, true)
	if err != nil || len(links) != 2 || len(links[0].Hours) != 1 || links[0].Hours[0] != (LinkHourSums{HourStart: hour, Connects: 3, Drops: 1,
		ClientCloses: 2, Freezes: 3, Quiet: 4, RttMSSum: 1500, RttSamples: 3, PktsSent: 500, PktsLost: 5}) ||
		len(links[1].Hours) != 2 || links[1].Hours[0] != (LinkHourSums{HourStart: hour, Connects: 4, Stalls: 1, OnlineS: 100}) ||
		links[1].Hours[1] != (LinkHourSums{HourStart: hour + 3600, Rejects: 2}) {
		t.Errorf("user by_hour = %+v, %v", links, err)
	}
	if links, err = st.UserLinks(ctx, "usr_lh_other", base); err != nil || len(links) != 2 || links[0].Rejects != 7 || links[1].NodeID != "nod_lh_b" || links[1].Connects != 9 {
		t.Errorf("the other person = %+v, %v", links, err)
	}
	if links, err = st.UserLinks(ctx, "usr_nobody", base); err != nil || len(links) != 0 {
		t.Errorf("UserLinks of nobody = %+v, %v", links, err)
	}

	// the retention keeps the bucket that holds its cut (12:20 is in the 12:00 bucket) and drops the older ones
	if n, err := st.PruneLinkHours(ctx, base); err != nil || n != 1 {
		t.Errorf("pruned %d, %v, want the one row of three hours earlier", n, err)
	}
	if n, err := st.PruneLinkHours(ctx, base.Add(time.Hour)); err != nil || n != 6 {
		t.Errorf("pruned %d, %v, want the six rows of the first hour", n, err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM link_hour`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("rows after the retention %d, %v, want 2", rows, err)
	}
	if LinkKeepHours != 168 || LinkWindowHours(168) != 168 || LinkWindowHours(5000) != 168 || LinkWindowHours(0) != 24 {
		t.Errorf("link retention/window = %d / %d / %d / %d, want 168 / 168 / 168 / 24", LinkKeepHours, LinkWindowHours(168), LinkWindowHours(5000), LinkWindowHours(0))
	}
	for _, at := range []int64{hour - 167*3600, hour - 169*3600} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO link_hour (node_id, hour_start, inbound_id, cred_id, connects) VALUES ('nod_lh_a', ?, 'inb_lh_awg', '', 1)`, at); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.PruneLinkHours(ctx, base.Add(-168*time.Hour)); err != nil || n != 1 {
		t.Errorf("168-hour retention pruned %d, %v, want only the bucket older than the retention", n, err)
	}
	var retained int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM link_hour WHERE hour_start = ?`, hour-167*3600).Scan(&retained); err != nil || retained != 1 {
		t.Errorf("the 167-hour bucket count = %d, %v, want 1", retained, err)
	}
}

func sumsByID(t *testing.T, st *Store, ctx context.Context, nodeID string, since time.Time, inboundID string) LinkInboundSums {
	t.Helper()
	sums, err := st.NodeLinkSums(ctx, nodeID, since)
	if err != nil {
		t.Fatal(err)
	}
	for _, sum := range sums {
		if sum.InboundID == inboundID {
			return sum
		}
	}
	t.Fatalf("no sums for %s", inboundID)
	return LinkInboundSums{}
}

func sameLink(a, b UserLink) bool {
	return a.DeviceID == b.DeviceID && a.NodeID == b.NodeID && a.NodeName == b.NodeName && a.InboundID == b.InboundID &&
		a.ProfileName == b.ProfileName && a.Protocol == b.Protocol && a.Platform == b.Platform && a.Client == b.Client && a.Kind == b.Kind &&
		a.Connects == b.Connects && a.Rejects == b.Rejects && a.Drops == b.Drops && a.Stalls == b.Stalls && a.OnlineS == b.OnlineS &&
		a.ClientCloses == b.ClientCloses && a.Freezes == b.Freezes && a.Quiet == b.Quiet && a.Accepted == b.Accepted && a.HellosForeign == b.HellosForeign &&
		a.RttMSSum == b.RttMSSum && a.RttSamples == b.RttSamples && a.PktsSent == b.PktsSent && a.PktsLost == b.PktsLost &&
		a.LastHour.Equal(b.LastHour)
}
