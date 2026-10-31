//go:build !js

package store

import (
	"context"
	"testing"
)

const adsDeviceID = "dns_builtin_ads_device"

// 00088 adds the built-in "No ads on the device": Cloudflare and Google, block_ads on, no split, plain, right after
// AdGuard. Nobody is moved onto it: users, groups, the default and the offers keep what they had.
func TestDNSAdsDeviceMigrationAddsTheBuiltIn(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p := cnProvider(t, s)
	if _, err := p.DownTo(ctx, 87); err != nil {
		t.Fatalf("down to 87: %v", err)
	}
	exec(`INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES ('grp_a', 'a', 1, 'dns_builtin_adblock')`)
	exec(`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at, dns_preset_id) VALUES
	      ('usr_1', 'one', 'grp_a', 1, x'01', x'02', 1, 'dns_builtin_standard'), ('usr_2', 'two', 'grp_a', 1, x'03', x'04', 1, NULL)`)
	exec(`INSERT INTO link_dns_option (preset_id, position, is_default) VALUES ('dns_builtin_adblock', 0, 1)`)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	d := s.DNS()
	r, err := d.Get(ctx, adsDeviceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r.Name != "Без рекламы на устройстве" || !r.Builtin || !r.BlockAds || r.SplitDirect || r.IPv4Only || r.Transport != "plain" || r.SplitJSON != "[]" {
		t.Errorf("row = %+v", r)
	}
	if r.ServersJSON != `[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]` {
		t.Errorf("servers = %s", r.ServersJSON)
	}
	if r.Description != "Приложение само режет рекламу и трекеры по списку category-ads-all; DNS — Cloudflare и Google\nThe app drops ads and trackers by the category-ads-all list; Cloudflare and Google for DNS" {
		t.Errorf("description = %q", r.Description)
	}
	var sort int
	if err := s.R.QueryRowContext(ctx, `SELECT sort FROM dns_preset WHERE id = ?`, adsDeviceID).Scan(&sort); err != nil || sort != 210 {
		t.Errorf("sort = %d %v", sort, err)
	}
	// nobody moved
	if l, err := d.Lookup(ctx); err != nil || l.DefaultID != DNSBuiltinDefaultID {
		t.Errorf("default = %q %v", l.DefaultID, err)
	}
	if own, group, err := d.UserRefs(ctx, "usr_1"); err != nil || own != "dns_builtin_standard" || group != "dns_builtin_adblock" {
		t.Errorf("usr_1 refs = %q %q %v", own, group, err)
	}
	if own, _, err := d.UserRefs(ctx, "usr_2"); err != nil || own != "" {
		t.Errorf("usr_2 own = %q %v", own, err)
	}
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM link_dns_option WHERE preset_id = ?`, adsDeviceID).Scan(&n); err != nil || n != 0 {
		t.Errorf("offered on the link: %d %v", n, err)
	}
}

// A custom preset that already has the name does not fail the migration: the built-in takes a suffix, like in 00083.
func TestDNSAdsDeviceMigrationNameConflict(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p := cnProvider(t, s)
	if _, err := p.DownTo(ctx, 87); err != nil {
		t.Fatalf("down to 87: %v", err)
	}
	if ok, err := s.DNS().Exists(ctx, adsDeviceID); err != nil || ok {
		t.Fatalf("built-in after down: %v %v", ok, err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO dns_preset (id, name, builtin, servers, created_at, updated_at) VALUES ('dns_custom', 'Без рекламы на устройстве', 0, '[{"kind":"plain","address":"9.9.9.9"}]', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if r, err := s.DNS().Get(ctx, adsDeviceID); err != nil || r.Name != "Без рекламы на устройстве (встроенный)" || !r.Builtin {
		t.Errorf("built-in = %+v %v", r, err)
	}
	if c, err := s.DNS().Get(ctx, "dns_custom"); err != nil || c.Name != "Без рекламы на устройстве" || c.Builtin || c.BlockAds {
		t.Errorf("custom = %+v %v", c, err)
	}
}

// Rolling back deletes the built-in and sends whoever used it to the default: users, groups, the instance default, what a
// node or the link apps offer and a person's picks. Presets that are not it keep their references.
func TestDNSAdsDeviceMigrationDownClearsReferences(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_1', 'de1', 'de1.example.com', 'active', 1)`)
	exec(`INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES ('grp_a', 'a', 1, ?), ('grp_b', 'b', 1, 'dns_builtin_standard')`, adsDeviceID)
	exec(`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at, dns_preset_id) VALUES
	      ('usr_1', 'one', 'grp_b', 1, x'01', x'02', 1, ?),
	      ('usr_2', 'two', 'grp_b', 1, x'03', x'04', 1, 'dns_builtin_standard')`, adsDeviceID)
	exec(`INSERT INTO setting (k, v) VALUES ('dns.default_preset', ?)`, adsDeviceID)
	exec(`INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES ('nod_1', ?, 0, 1), ('nod_1', 'dns_builtin_standard', 1, 0)`, adsDeviceID)
	exec(`INSERT INTO user_node_dns (user_id, node_id, preset_id, updated_at) VALUES ('usr_1', 'nod_1', ?, 1)`, adsDeviceID)
	exec(`INSERT INTO link_dns_option (preset_id, position, is_default) VALUES (?, 0, 1), ('dns_builtin_standard', 1, 0)`, adsDeviceID)
	exec(`INSERT INTO user_link_dns (user_id, preset_id, updated_at) VALUES ('usr_2', ?, 1)`, adsDeviceID)

	p := cnProvider(t, s)
	if _, err := p.DownTo(ctx, 87); err != nil {
		t.Fatalf("down: %v", err)
	}
	d := s.DNS()
	if ok, err := d.Exists(ctx, adsDeviceID); err != nil || ok {
		t.Errorf("built-in after down: %v %v", ok, err)
	}
	if own, _, err := d.UserRefs(ctx, "usr_1"); err != nil || own != "" {
		t.Errorf("usr_1 own = %q %v", own, err)
	}
	if own, _, err := d.UserRefs(ctx, "usr_2"); err != nil || own != "dns_builtin_standard" {
		t.Errorf("usr_2 own = %q %v", own, err)
	}
	var groupRef *string
	if err := s.R.QueryRowContext(ctx, `SELECT dns_preset_id FROM user_group WHERE id = 'grp_a'`).Scan(&groupRef); err != nil || groupRef != nil {
		t.Errorf("grp_a ref = %v %v", groupRef, err)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM setting WHERE k = 'dns.default_preset'`: 0,
		`SELECT count(*) FROM node_dns_option`:                        1,
		`SELECT count(*) FROM user_node_dns`:                          0,
		`SELECT count(*) FROM link_dns_option`:                        1,
		`SELECT count(*) FROM user_link_dns`:                          0,
	} {
		var n int
		if err := s.R.QueryRowContext(ctx, q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d %v, want %d", q, n, err, want)
		}
	}
	// and up again puts it back
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if ok, err := d.Exists(ctx, adsDeviceID); err != nil || !ok {
		t.Errorf("built-in after up again: %v %v", ok, err)
	}
}
