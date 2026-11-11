package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// helloKexStoreProof runs on SQLite and on D1 (link_hour_test.go, d1_store_test.go): the vless-hello-kex/1 counters (migration
// 00127) go in with the batch's one statement, add up inside an hour and across hours, are clamped, and come back per device
// (UserHelloKex), for the fleet (HelloKex) and as plain sums by device id (DevicesHelloKex).
func helloKexStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 11, 7, 12, 20, 0, 0, time.UTC)
	hour := base.Unix() / 3600 * 3600
	for _, q := range []string{
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_hk', 'de1', '203.0.113.30', 'active', 1)`,
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_hk_old', 'nl1', '203.0.113.31', 'retired', 1)`,
	} {
		if _, err := st.W.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Access().CreateProfile(ctx, AccessProfile{ID: "prf_hk", Protocol: "vless", Name: "Reality", CreatedAt: base,
		SettingsJSON: `{"security":"reality","transport":"xhttp","reality":{"target":"www.example.com:443"}}`}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []AccessInbound{{ID: "inb_hk", ProfileID: "prf_hk", NodeID: "nod_hk"}, {ID: "inb_hk_old", ProfileID: "prf_hk", NodeID: "nod_hk_old"}} {
		in.Enabled, in.CreatedAt = true, base
		if err := st.Access().CreateInbound(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Access().CreateGroup(ctx, AccessGroup{ID: "grp_hk", Name: "hk", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	// alice: two devices (one pq, one mixed); bob: classic only; carol: unknown only; dave: pq only, device revoked afterwards
	type dev struct{ user, name, id, client string }
	devices := []dev{{"usr_hk_a", "alice", "dev_hk_a1", "Happ/3.4.2"}, {"usr_hk_a", "alice", "dev_hk_a2", "v2rayNG/1.9.45"},
		{"usr_hk_b", "bob", "dev_hk_b", "SFA/1.14.4"}, {"usr_hk_c", "carol", "dev_hk_c", "Shadowrocket/2.2"}, {"usr_hk_d", "dave", "dev_hk_d", "Happ/3.4.2"}}
	seen := map[string]bool{}
	for _, d := range devices {
		if !seen[d.user] {
			seen[d.user] = true
			if _, err := st.W.ExecContext(ctx, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
				VALUES (?, ?, 'grp_hk', ?, ?, ?, ?)`, d.user, d.name, base.Unix(), []byte(d.user), []byte{1}, base.Unix()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.W.ExecContext(ctx, `INSERT INTO device (id, user_id, hwid_hash, kind, platform, client, first_seen_at, last_seen_at, created_at)
			VALUES (?, ?, ?, 'link', 'android', ?, 0, 0, ?)`, d.id, d.user, []byte(d.id), d.client, base.Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
			VALUES (?, ?, ?, 'vless', ?, '{}', ?, 'prf_hk', 0)`, "crd_"+d.id, d.id, d.user, []byte{1}, base.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	var seq uint64
	ingest := func(node string, at int64, links ...FleetLink) {
		t.Helper()
		seq++
		if out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: node, Instance: "hk-" + node, Seq: seq, Now: base, HourStart: at, Links: links}); err != nil || out.Duplicate || out.Skipped != 0 {
			t.Fatalf("IngestStats(%d) = %+v, %v", seq, out, err)
		}
	}
	ingest("nod_hk", hour,
		FleetLink{CredID: "crd_dev_hk_a1", InboundID: "inb_hk", HelloPQ: 5},
		FleetLink{CredID: "crd_dev_hk_a2", InboundID: "inb_hk", HelloPQ: 1, HelloClassic: 2},
		FleetLink{CredID: "crd_dev_hk_b", InboundID: "inb_hk", HelloClassic: 3, HelloUnknown: 1},
		FleetLink{CredID: "crd_dev_hk_c", InboundID: "inb_hk", HelloUnknown: 2},
		FleetLink{CredID: "crd_dev_hk_d", InboundID: "inb_hk", HelloPQ: 4},
		FleetLink{CredID: "crd_dev_hk_a1", InboundID: "inb_hk", Connects: 9}) // another row of the same hour: counters add up
	ingest("nod_hk", hour+3600, FleetLink{CredID: "crd_dev_hk_b", InboundID: "inb_hk", HelloClassic: 4},
		FleetLink{CredID: "crd_dev_hk_a1", InboundID: "inb_hk", HelloClassic: 1 << 30}) // an absurd number is clamped
	ingest("nod_hk_old", hour, FleetLink{CredID: "crd_dev_hk_d", InboundID: "inb_hk_old", HelloClassic: 50}) // a retired node does not count

	rows, err := st.UserHelloKex(ctx, "usr_hk_a", base.Add(-time.Hour))
	if err != nil || len(rows) != 2 {
		t.Fatalf("UserHelloKex(alice) = %+v, %v", rows, err)
	}
	if a1 := rows[0]; a1.DeviceID != "dev_hk_a1" || a1.UserName != "alice" || a1.PQ != 5 || a1.Classic != uint64(maxLinkCount) || a1.Unknown != 0 ||
		a1.Client != "Happ/3.4.2" || a1.LastHour.Unix() != hour+3600 {
		t.Fatalf("alice's first device = %+v", a1)
	}
	if a2 := rows[1]; a2.DeviceID != "dev_hk_a2" || a2.PQ != 1 || a2.Classic != 2 {
		t.Fatalf("alice's second device = %+v", a2)
	}
	if rows, err = st.UserHelloKex(ctx, "usr_hk_nobody", base.Add(-time.Hour)); err != nil || len(rows) != 0 {
		t.Fatalf("UserHelloKex(nobody) = %+v, %v", rows, err)
	}

	fleet, err := st.HelloKex(ctx, base.Add(-time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	// a1 is mixed (pq 5, classic clamped), a2 mixed, b classic only (3 + 4), c unknown only, d pq only
	if fleet.PQOnly != 1 || fleet.ClassicOnly != 1 || fleet.Mixed != 2 || fleet.UnknownOnly != 1 {
		t.Fatalf("fleet = %+v; want 1 pq only, 1 classic only, 2 mixed, 1 unknown only", fleet)
	}
	if len(fleet.Classic) != 2 || fleet.Classic[0].DeviceID != "dev_hk_a1" || fleet.Classic[1].DeviceID != "dev_hk_b" ||
		fleet.Classic[1].Classic != 7 || fleet.Classic[1].Unknown != 1 || fleet.Classic[1].UserName != "bob" ||
		fleet.Classic[1].Client != "SFA/1.14.4" || fleet.Classic[1].LastHour.Unix() != hour+3600 {
		t.Fatalf("classic devices cut to 2, most first = %+v", fleet.Classic)
	}

	// a window that starts after the first hour sees only the second: alice's first device and bob are classic only there
	later, err := st.HelloKex(ctx, time.Unix(hour+3600, 0), 10)
	if err != nil || later.ClassicOnly != 2 || later.Mixed != 0 || later.PQOnly != 0 || later.UnknownOnly != 0 {
		t.Fatalf("HelloKex(second hour) = %+v, %v", later, err)
	}

	// a revoked device can not connect and so can not break
	if _, err := st.W.ExecContext(ctx, `UPDATE device SET revoked_at = ? WHERE id = 'dev_hk_b'`, base.Unix()); err != nil {
		t.Fatal(err)
	}
	if fleet, err = st.HelloKex(ctx, base.Add(-time.Hour), 10); err != nil || fleet.ClassicOnly != 0 || len(fleet.Classic) != 2 {
		t.Fatalf("after revoking bob's device: %+v, %v", fleet, err)
	}

	// DevicesHelloKex: the sums of every device that has any, in one statement and by device id; a retired node counts nothing
	sums, err := st.DevicesHelloKex(ctx, base.Add(-time.Hour))
	if err != nil || len(sums) != 5 {
		t.Fatalf("DevicesHelloKex = %+v, %v", sums, err)
	}
	if a1, a2, b, c, d := sums["dev_hk_a1"], sums["dev_hk_a2"], sums["dev_hk_b"], sums["dev_hk_c"], sums["dev_hk_d"]; a1.PQ != 5 || a1.Classic != uint64(maxLinkCount) ||
		a2 != (HelloKexSums{PQ: 1, Classic: 2}) || b != (HelloKexSums{Classic: 7, Unknown: 1}) || c != (HelloKexSums{Unknown: 2}) || d != (HelloKexSums{PQ: 4}) {
		t.Fatalf("sums by device = %+v", sums)
	}
	if later, err := st.DevicesHelloKex(ctx, time.Unix(hour+3600, 0)); err != nil || len(later) != 2 || later["dev_hk_b"].Classic != 4 || later["dev_hk_a1"].PQ != 0 {
		t.Fatalf("DevicesHelloKex(second hour) = %+v, %v", later, err)
	}
	if none, err := st.DevicesHelloKex(ctx, base.Add(24*time.Hour)); err != nil || len(none) != 0 {
		t.Fatalf("DevicesHelloKex(after everything) = %+v, %v", none, err)
	}
	// a long list (more than a page of the screen): the statement does not grow with it, and every device comes back
	const bulk = 250
	if _, err := st.W.ExecContext(ctx, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
		VALUES ('usr_hk_bulk', 'bulk', 'grp_hk', ?, ?, ?, ?)`, base.Unix(), []byte("usr_hk_bulk"), []byte{1}, base.Unix()); err != nil {
		t.Fatal(err)
	}
	var links []FleetLink
	for i := range bulk {
		id := fmt.Sprintf("dev_hk_bulk%03d", i)
		if _, err := st.W.ExecContext(ctx, `INSERT INTO device (id, user_id, hwid_hash, kind, platform, client, first_seen_at, last_seen_at, created_at)
			VALUES (?, 'usr_hk_bulk', ?, 'link', 'android', 'Happ/3.4.2', 0, 0, ?)`, id, []byte(id), base.Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
			VALUES (?, ?, 'usr_hk_bulk', 'vless', ?, '{}', ?, 'prf_hk', 0)`, "crd_"+id, id, []byte{1}, base.Unix()); err != nil {
			t.Fatal(err)
		}
		links = append(links, FleetLink{CredID: "crd_" + id, InboundID: "inb_hk", HelloPQ: uint32(i + 1), HelloClassic: uint32(i % 3)})
	}
	ingest("nod_hk", hour+7200, links...)
	if sums, err = st.DevicesHelloKex(ctx, time.Unix(hour+7200, 0)); err != nil || len(sums) != bulk || sums["dev_hk_bulk007"] != (HelloKexSums{PQ: 8, Classic: 1}) {
		t.Fatalf("DevicesHelloKex(bulk) = %d devices, %+v, %v", len(sums), sums["dev_hk_bulk007"], err)
	}
}
