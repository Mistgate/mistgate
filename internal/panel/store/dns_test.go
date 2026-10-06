//go:build !js

package store

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// A live instance was at version 6: migration 00007 must apply on top of existing users and groups, keep
// them, seed the built-ins, and roll back again.
func TestDNSMigrationUpgradesExistingData(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 6); err != nil {
		t.Fatalf("down to 6: %v", err)
	}
	var n int
	if err := s.W.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('user') WHERE name = 'dns_preset_id'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("user.dns_preset_id after down: n=%d err=%v", n, err)
	}
	for _, q := range []string{
		`INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'old group', 1)`,
		`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
		 VALUES ('usr_a', 'old user', 'grp_a', 1, x'01', x'02', 1)`,
	} {
		if _, err := s.W.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	var name, dns string
	if err := s.R.QueryRowContext(ctx, `SELECT u.name, coalesce(u.dns_preset_id, 'none') FROM user u WHERE id = 'usr_a'`).Scan(&name, &dns); err != nil || name != "old user" || dns != "none" {
		t.Fatalf("user after upgrade: %q %q %v", name, dns, err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM dns_preset WHERE builtin = 1`).Scan(&n); err != nil || n != 15 {
		t.Fatalf("built-in presets: %d %v", n, err)
	}
	d := s.DNS()
	l, err := d.Lookup(ctx)
	if err != nil || l.DefaultID != DNSBuiltinDefaultID {
		t.Fatalf("default: %q %v", l.DefaultID, err)
	}
	if id, _, src := l.Resolve("", ""); id != DNSBuiltinDefaultID || src != DNSSourceDefault {
		t.Fatalf("resolve: %q %q", id, src)
	}
}

// An instance at version 7 (DNS presets, no split_direct) upgrades with users and groups that reference
// presets: references survive, the Russia split becomes "direct" and is renamed, the proxied built-in is
// added, the instance default stays, and 00008 rolls back again.
func TestDNSSplitDirectMigrationUpgradesFrom7(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 7); err != nil {
		t.Fatalf("down to 7: %v", err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var n int
	if err := s.W.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('dns_preset') WHERE name = 'split_direct'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("split_direct after down: n=%d err=%v", n, err)
	}
	// a custom preset already named like the new built-in (case differs), users and groups on several presets
	exec(`INSERT INTO dns_preset (id, name, builtin, servers, created_at, updated_at) VALUES ('dns_custom', 'Россия через vpn', 0, '[{"kind":"plain","address":"9.9.9.9"}]', 1, 1)`)
	exec(`INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES ('grp_a', 'a', 1, 'dns_builtin_ru_split'), ('grp_b', 'b', 1, NULL)`)
	exec(`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at, dns_preset_id) VALUES
	      ('usr_1', 'one', 'grp_a', 1, x'01', x'02', 1, NULL),
	      ('usr_2', 'two', 'grp_b', 1, x'03', x'04', 1, 'dns_custom'),
	      ('usr_3', 'three', 'grp_b', 1, x'05', x'06', 1, 'dns_builtin_yandex')`)
	// an admin's own words on a built-in survive the shorter stock descriptions of 00009
	exec(`UPDATE dns_preset SET description = 'моё описание' WHERE id = 'dns_builtin_yandex'`)
	exec(`UPDATE dns_preset SET name = 'Мой семейный' WHERE id = 'dns_builtin_family'`) // an edited name is not renamed either
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	d := s.DNS()
	get := func(id string) DNSPreset {
		t.Helper()
		r, err := d.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return r
	}
	ru := get("dns_builtin_ru_split")
	if !ru.SplitDirect || ru.Name != "Россия: .ru напрямую" || !ru.Builtin || !strings.Contains(ru.Description, "напрямую") || !strings.Contains(ru.Description, "\n") {
		t.Errorf("ru split = %+v", ru)
	}
	if !strings.Contains(ru.SplitJSON, `".ru"`) || !strings.Contains(ru.ServersJSON, "cloudflare/standard") {
		t.Errorf("ru split lost its servers/split: %+v", ru)
	}
	px := get("dns_builtin_ru_proxied")
	if px.SplitDirect || !px.Builtin || px.SplitJSON != "[]" || px.Name != "Россия: всё через VPN" {
		t.Errorf("ru proxied = %+v", px) // 00008 gave it the suffix (the custom preset owned the name); 00009 renamed it
	}
	if std := get("dns_builtin_standard"); std.SplitDirect || std.ServersJSON != px.ServersJSON {
		t.Errorf("standard = %+v", std)
	}
	if c := get("dns_custom"); c.SplitDirect || c.Name != "Россия через vpn" {
		t.Errorf("custom = %+v", c)
	}
	if f := get("dns_builtin_family"); f.Name != "Мой семейный" {
		t.Errorf("edited name was replaced: %q", f.Name)
	}
	if a := get("dns_builtin_adblock"); a.Name != "AdGuard: без рекламы" {
		t.Errorf("stock name not renamed: %q", a.Name)
	}
	if y := get("dns_builtin_yandex"); y.Description != "моё описание" {
		t.Errorf("edited description was replaced: %q", y.Description)
	}
	if std := get("dns_builtin_standard"); std.Description != "Cloudflare и Google для всего\nCloudflare and Google for everything" {
		t.Errorf("stock description not shortened: %q", std.Description)
	}
	all, err := d.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range all {
		order = append(order, r.ID)
	}
	wantOrder := []string{
		"dns_builtin_ru_split", "dns_builtin_ru_proxied", "dns_builtin_yandex",
		"dns_builtin_standard", "dns_builtin_cloudflare", "dns_builtin_google", "dns_builtin_dnssb",
		"dns_builtin_adblock",
		"dns_builtin_family", "dns_builtin_cloudflare_family", "dns_builtin_yandex_family", "dns_builtin_opendns_family",
		"dns_builtin_quad9", "dns_builtin_cloudflare_security", "dns_builtin_yandex_safe",
		"dns_custom",
	}
	if !slices.Equal(order, wantOrder) {
		t.Errorf("order = %v, want %v", order, wantOrder)
	}
	var builtins int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM dns_preset WHERE builtin = 1`).Scan(&builtins); err != nil || builtins != 15 {
		t.Errorf("built-ins: %d %v", builtins, err)
	}

	// references and the default are untouched
	own, group, err := d.UserRefs(ctx, "usr_2")
	if err != nil || own != "dns_custom" || group != "" {
		t.Errorf("usr_2 refs: %q %q %v", own, group, err)
	}
	if _, group, _ = d.UserRefs(ctx, "usr_1"); group != "dns_builtin_ru_split" {
		t.Errorf("usr_1 group ref: %q", group)
	}
	l, err := d.Lookup(ctx)
	if err != nil || l.DefaultID != DNSBuiltinDefaultID {
		t.Fatalf("default: %q %v", l.DefaultID, err)
	}
	counts, err := d.UserCounts(ctx)
	if err != nil || counts["dns_builtin_ru_split"] != 1 || counts["dns_custom"] != 1 || counts["dns_builtin_yandex"] != 1 {
		t.Errorf("counts: %v %v", counts, err)
	}

	// 00008 rolls back: the proxied built-in goes (clearing references to it), the stock name returns
	exec(`UPDATE user SET dns_preset_id = 'dns_builtin_ru_proxied' WHERE id = 'usr_1'`)
	if _, err := p.DownTo(ctx, 7); err != nil {
		t.Fatalf("down to 7 again: %v", err)
	}
	if own, _, err := d.UserRefs(ctx, "usr_1"); err != nil || own != "" {
		t.Errorf("usr_1 after rollback: %q %v", own, err)
	}
	if ok, err := d.Exists(ctx, "dns_builtin_ru_proxied"); err != nil || ok {
		t.Errorf("proxied preset after rollback: %v %v", ok, err)
	}
	var name string
	if err := s.R.QueryRowContext(ctx, `SELECT name FROM dns_preset WHERE id = 'dns_builtin_ru_split'`).Scan(&name); err != nil || name != "Россия-сплит" {
		t.Errorf("ru split name after rollback: %q %v", name, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
