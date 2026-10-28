//go:build !js

package store

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

const cnSplitID = "dns_builtin_cn_split"

func cnProvider(t *testing.T, s *Store) *goose.Provider {
	t.Helper()
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// 00083 adds the built-in "China: Chinese sites direct": Cloudflare and Google through the tunnel, a geo rule for cn answered by
// AliDNS and DNSPod, split direct on, plain, listed after the Russian ones. Nobody is moved onto it.
func TestDNSGeoCNMigrationAddsTheBuiltIn(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	d := s.DNS()
	r, err := d.Get(ctx, cnSplitID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r.Name != "Китай: китайские сайты напрямую" || !r.Builtin || !r.SplitDirect || r.IPv4Only || r.Transport != "plain" {
		t.Errorf("row = %+v", r)
	}
	if r.ServersJSON != `[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]` {
		t.Errorf("servers = %s", r.ServersJSON)
	}
	if r.SplitJSON != `[{"suffixes":[],"geo":["cn"],"servers":[{"variant":"alidns/standard"},{"variant":"dnspod/standard"}]}]` {
		t.Errorf("split = %s", r.SplitJSON)
	}
	if r.Description != "Китайские сайты напрямую, остальное через VPN\nChinese sites direct, everything else through the VPN" {
		t.Errorf("description = %q", r.Description)
	}
	var sort int
	if err := s.R.QueryRowContext(ctx, `SELECT sort FROM dns_preset WHERE id = ?`, cnSplitID).Scan(&sort); err != nil || sort != 40 {
		t.Errorf("sort = %d %v", sort, err)
	}
	if l, err := d.Lookup(ctx); err != nil || l.DefaultID != DNSBuiltinDefaultID {
		t.Errorf("default = %q %v", l.DefaultID, err)
	}
}

// A custom preset that already has the name does not fail the migration: the built-in takes a suffix, like in 00011.
func TestDNSGeoCNMigrationNameConflict(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p := cnProvider(t, s)
	if _, err := p.DownTo(ctx, 82); err != nil {
		t.Fatalf("down to 82: %v", err)
	}
	if ok, err := s.DNS().Exists(ctx, cnSplitID); err != nil || ok {
		t.Fatalf("built-in after down: %v %v", ok, err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO dns_preset (id, name, builtin, servers, created_at, updated_at) VALUES ('dns_custom', 'Китай: китайские сайты напрямую', 0, '[{"kind":"plain","address":"9.9.9.9"}]', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	r, err := s.DNS().Get(ctx, cnSplitID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "Китай: китайские сайты напрямую (встроенный)" || !r.Builtin {
		t.Errorf("name = %q builtin = %v", r.Name, r.Builtin)
	}
	if c, err := s.DNS().Get(ctx, "dns_custom"); err != nil || c.Name != "Китай: китайские сайты напрямую" || c.Builtin {
		t.Errorf("custom = %+v %v", c, err)
	}
}

// Rolling back deletes the built-in and sends whoever used it to the default: users, groups, the instance default, what a node or
// the link apps offer and a person's pick. Presets that are not it keep their references.
func TestDNSGeoCNMigrationDownClearsReferences(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	d := s.DNS()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES ('grp_a', 'a', 1, ?), ('grp_b', 'b', 1, 'dns_builtin_standard')`, cnSplitID)
	exec(`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at, dns_preset_id) VALUES
	      ('usr_1', 'one', 'grp_b', 1, x'01', x'02', 1, ?),
	      ('usr_2', 'two', 'grp_b', 1, x'03', x'04', 1, 'dns_builtin_standard')`, cnSplitID)
	exec(`INSERT INTO setting (k, v) VALUES ('dns.default_preset', ?)`, cnSplitID)
	exec(`INSERT INTO link_dns_option (preset_id, position, is_default) VALUES (?, 0, 1), ('dns_builtin_standard', 1, 0)`, cnSplitID)
	exec(`INSERT INTO user_link_dns (user_id, preset_id, updated_at) VALUES ('usr_2', ?, 1)`, cnSplitID)

	p := cnProvider(t, s)
	if _, err := p.DownTo(ctx, 82); err != nil {
		t.Fatalf("down: %v", err)
	}
	if ok, err := d.Exists(ctx, cnSplitID); err != nil || ok {
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
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM setting WHERE k = 'dns.default_preset'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("default setting rows = %d %v", n, err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM link_dns_option`).Scan(&n); err != nil || n != 1 {
		t.Errorf("link options = %d %v", n, err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM user_link_dns`).Scan(&n); err != nil || n != 0 {
		t.Errorf("link picks = %d %v", n, err)
	}
	// and up again puts it back
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if ok, err := d.Exists(ctx, cnSplitID); err != nil || !ok {
		t.Errorf("built-in after up again: %v %v", ok, err)
	}
}
