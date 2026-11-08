package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// linkReasonsStoreProof runs on SQLite and on D1 (link_hour_test.go, d1_store_test.go): the links/3 counters (migration 00114) go
// in with the batch's one statement, add up inside an hour, and come back in the node report, the fleet sums and a person's rows,
// with what decides which of them an inbound can have (the VLESS settings, the AWG backend).
func linkReasonsStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 11, 7, 12, 20, 0, 0, time.UTC)
	hour := base.Unix() / 3600 * 3600
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_lr', 'de1', '203.0.113.20', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []AccessProfile{
		{ID: "prf_lr_v", Protocol: "vless", Name: "Reality", SettingsJSON: `{"security":"reality","transport":"tcp","reality":{"target":"www.example.com:443"}}`},
		{ID: "prf_lr_w", Protocol: "awg", Name: "AWG", SettingsJSON: "{}"},
		{ID: "prf_lr_h", Protocol: "hysteria2", Name: "Fast", SettingsJSON: "{}"},
	} {
		p.CreatedAt = base
		if err := st.Access().CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range []AccessInbound{{ID: "inb_lr_v", ProfileID: "prf_lr_v", NodeID: "nod_lr"},
		{ID: "inb_lr_w", ProfileID: "prf_lr_w", NodeID: "nod_lr"}, {ID: "inb_lr_h", ProfileID: "prf_lr_h", NodeID: "nod_lr"}} {
		in.Enabled, in.CreatedAt = true, base
		if err := st.Access().CreateInbound(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE inbound SET awg_health_json = '{"backend":"userspace","ifaceUp":true}', awg_health_at = 1 WHERE id = 'inb_lr_w'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateGroup(ctx, AccessGroup{ID: "grp_lr", Name: "lr", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
		VALUES ('usr_lr', 'usr_lr', 'grp_lr', ?, ?, ?, ?)`, base.Unix(), []byte("usr_lr"), []byte{1}, base.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO device (id, user_id, hwid_hash, kind, platform, client, first_seen_at, last_seen_at, created_at)
		VALUES ('dev_lr', 'usr_lr', ?, 'link', 'android', 'Happ/3.4.2', 0, 0, ?)`, []byte("dev_lr"), base.Unix()); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{{"crd_lr_v", "vless", "prf_lr_v"}, {"crd_lr_w", "awg", "prf_lr_w"}} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
			VALUES (?, 'dev_lr', 'usr_lr', ?, ?, '{}', ?, ?, 0)`, c[0], c[1], []byte{1}, base.Unix(), c[2]); err != nil {
			t.Fatal(err)
		}
	}
	var seq uint64
	ingest := func(at int64, links ...FleetLink) {
		t.Helper()
		seq++
		if out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: "nod_lr", Instance: "lr", Seq: seq, Now: base, HourStart: at, Links: links}); err != nil || out.Duplicate || out.Skipped != 0 {
			t.Fatalf("IngestStats(%d) = %+v, %v", seq, out, err)
		}
	}
	ingest(hour,
		FleetLink{CredID: "crd_lr_v", InboundID: "inb_lr_v", Connects: 4, TCPStalls: 3, TCPStalls16K: 2},
		FleetLink{InboundID: "inb_lr_v", Hellos: 20, HellosForeign: 5, RealityFallback: 9, AuthFailed: 3, HsUnfinished: 2},
		FleetLink{InboundID: "inb_lr_h", HsUnfinished: 6, TLSAlertsSent: 1, TLSAlertsRecv: 4, TLSCertRefused: 3},
		FleetLink{CredID: "crd_lr_w", InboundID: "inb_lr_w", Initiations: 10, Responses: 9, ResponsesUnmatched: 8})
	ingest(hour, FleetLink{CredID: "crd_lr_v", InboundID: "inb_lr_v", TCPStalls: 1}) // a stall alone is a row
	ingest(hour+3600, FleetLink{CredID: "crd_lr_w", InboundID: "inb_lr_w", Initiations: 2, Responses: 2})

	reports, err := st.NodeLinkReports(ctx, "nod_lr", base, true)
	if err != nil || len(reports) != 3 {
		t.Fatalf("NodeLinkReports = %+v, %v", reports, err)
	}
	byID := map[string]NodeLinkReport{}
	for _, r := range reports {
		byID[r.Totals.InboundID] = r
	}
	if got, want := byID["inb_lr_v"].Totals.LinkReasons, (LinkReasons{HsUnfinished: 2, RealityFallback: 9, AuthFailed: 3, TCPStalls: 4, TCPStalls16K: 2}); got != want {
		t.Errorf("VLESS reasons = %+v, want %+v", got, want)
	}
	if v := byID["inb_lr_v"].Totals; v.Connects != 4 || v.Hellos != 20 || v.HellosForeign != 5 {
		t.Errorf("VLESS hellos and connects = %+v", v)
	}
	if got, want := byID["inb_lr_h"].Totals.LinkReasons, (LinkReasons{HsUnfinished: 6, TLSAlertsSent: 1, TLSAlertsRecv: 4, TLSCertRefused: 3}); got != want {
		t.Errorf("Hysteria2 reasons = %+v, want %+v", got, want)
	}
	if got, want := byID["inb_lr_w"].Totals.LinkReasons, (LinkReasons{Initiations: 12, Responses: 11, ResponsesUnmatched: 8}); got != want {
		t.Errorf("AWG reasons = %+v, want %+v", got, want)
	}
	if h := byID["inb_lr_w"].Hours; len(h) != 2 || h[0].ResponsesUnmatched != 8 || h[1].Initiations != 2 || h[1].Responses != 2 {
		t.Errorf("AWG hours = %+v", h)
	}
	fleet, err := st.FleetNodeLinkSums(ctx, base)
	if err != nil || len(fleet) != 3 {
		t.Fatalf("FleetNodeLinkSums = %+v, %v", fleet, err)
	}
	for _, f := range fleet {
		if f.LinkReasons != byID[f.InboundID].Totals.LinkReasons {
			t.Errorf("fleet sums of %s = %+v, the node report says %+v", f.InboundID, f.LinkReasons, byID[f.InboundID].Totals.LinkReasons)
		}
	}

	links, err := st.UserLinksByHour(ctx, "usr_lr", base, true)
	if err != nil || len(links) != 2 {
		t.Fatalf("UserLinksByHour = %+v, %v", links, err)
	}
	rows := map[string]UserLink{}
	for _, l := range links {
		rows[l.Protocol] = l
	}
	v, w := rows["vless"], rows["awg"]
	if v.TCPStalls != 4 || v.TCPStalls16K != 2 || v.Connects != 4 || v.AwgBackend != "" {
		t.Errorf("VLESS key row = %+v", v)
	}
	var variant struct {
		Security, Transport string
		CDN                 struct{ Host string } `json:"cdn"`
	}
	if err := json.Unmarshal([]byte(v.Variant), &variant); err != nil || variant.Security != "reality" || variant.Transport != "tcp" || variant.CDN.Host != "" {
		t.Errorf("VLESS variant %q = %+v, %v", v.Variant, variant, err)
	}
	if w.Initiations != 12 || w.Responses != 11 || w.ResponsesUnmatched != 8 || w.AwgBackend != "userspace" || w.Variant != "" || len(w.Hours) != 2 {
		t.Errorf("AWG key row = %+v", w)
	}
	if w.Hours[1].Initiations != 2 || w.Hours[1].Responses != 2 {
		t.Errorf("AWG key hours = %+v", w.Hours)
	}
}
