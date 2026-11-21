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
