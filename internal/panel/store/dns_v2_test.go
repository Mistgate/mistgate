package store

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// An instance at version 9 (DNS without providers) upgrades to DNS v2: the stock built-ins point at
// the catalog, an edited one and every custom preset stay as they were, the new built-ins appear (a taken name gets
// a suffix), every preset starts on plain, references survive, and 00011 rolls back to the old rows.
func TestDNSv2MigrationUpgradesFrom9(t *testing.T) {
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
	if _, err := p.DownTo(ctx, 9); err != nil {
		t.Fatalf("down to 9: %v", err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var n int
	if err := s.W.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('dns_preset') WHERE name = 'preferred_transport'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("preferred_transport after down: n=%d err=%v", n, err)
	}
	const stockCF = `[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]`
	// a custom preset that already owns a name of the new built-ins, edited built-ins, and users on several presets
	exec(`INSERT INTO dns_preset (id, name, builtin, servers, created_at, updated_at) VALUES ('dns_custom', 'cloudflare: без фильтров', 0, '[{"kind":"plain","address":"9.9.9.9"}]', 1, 1)`)
	exec(`UPDATE dns_preset SET servers = '[{"kind":"plain","address":"10.0.0.53"}]' WHERE id = 'dns_builtin_yandex'`)
	exec(`UPDATE dns_preset SET servers = '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"},{"kind":"plain","address":"9.9.9.9"}]' WHERE id = 'dns_builtin_standard'`)
	exec(`INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES ('grp_a', 'a', 1, 'dns_builtin_adblock')`)
	exec(`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at, dns_preset_id) VALUES
	      ('usr_1', 'one', 'grp_a', 1, x'01', x'02', 1, 'dns_custom'),
	      ('usr_2', 'two', 'grp_a', 1, x'03', x'04', 1, NULL)`)
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
	wantServers := map[string]string{
		"dns_builtin_ru_split":   `[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]`,
		"dns_builtin_ru_proxied": `[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]`,
		"dns_builtin_adblock":    `[{"variant":"adguard/default"}]`,
		"dns_builtin_family":     `[{"variant":"adguard/family"}]`,
		"dns_builtin_quad9":      `[{"variant":"quad9/standard"}]`,
		// edited by the admin: untouched
		"dns_builtin_yandex":   `[{"kind":"plain","address":"10.0.0.53"}]`,
		"dns_builtin_standard": `[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"},{"kind":"plain","address":"9.9.9.9"}]`,
		"dns_custom":           `[{"kind":"plain","address":"9.9.9.9"}]`,
		// new
		"dns_builtin_cloudflare":          `[{"variant":"cloudflare/standard"}]`,
		"dns_builtin_yandex_safe":         `[{"variant":"yandex/safe"}]`,
		"dns_builtin_opendns_family":      `[{"variant":"opendns/familyshield"}]`,
		"dns_builtin_cloudflare_security": `[{"variant":"cloudflare/security"}]`,
	}
	for id, want := range wantServers {
		if got := get(id); got.ServersJSON != want || got.Transport != "plain" {
			t.Errorf("%s: servers %s transport %q, want %s plain", id, got.ServersJSON, got.Transport, want)
		}
	}
	ru := get("dns_builtin_ru_split")
	if !strings.Contains(ru.SplitJSON, `"servers":[{"variant":"yandex/basic"}]`) || !ru.SplitDirect || ru.Name != "Россия: .ru напрямую" {
		t.Errorf("ru split = %+v", ru)
	}
	// the custom preset owned the name: the built-in took a suffix
	if cf := get("dns_builtin_cloudflare"); cf.Name != "Cloudflare: без фильтров (встроенный)" || !cf.Builtin || !strings.Contains(cf.Description, "\n") || cf.SplitJSON != "[]" {
		t.Errorf("new cloudflare built-in = %+v", cf)
	}
	if c := get("dns_custom"); c.Name != "cloudflare: без фильтров" || c.Builtin {
		t.Errorf("custom = %+v", c)
	}
	var builtins int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM dns_preset WHERE builtin = 1`).Scan(&builtins); err != nil || builtins != 15 {
		t.Errorf("built-ins: %d %v", builtins, err)
	}
	// references and the default are untouched
	if own, group, err := d.UserRefs(ctx, "usr_1"); err != nil || own != "dns_custom" || group != "dns_builtin_adblock" {
		t.Errorf("usr_1 refs: %q %q %v", own, group, err)
	}
	if l, err := d.Lookup(ctx); err != nil || l.DefaultID != DNSBuiltinDefaultID {
		t.Fatalf("default: %q %v", l.DefaultID, err)
	}
	// the transport round-trips through the store, and the column refuses anything else
	cf := get("dns_builtin_cloudflare")
	cf.Transport = "doh"
	if err := d.Update(ctx, cf); err != nil || get("dns_builtin_cloudflare").Transport != "doh" {
		t.Errorf("update transport: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE dns_preset SET preferred_transport = 'dou' WHERE id = 'dns_custom'`); err == nil {
		t.Error("an unknown transport was accepted by the CHECK")
	}

	// rolling back: the added built-ins go (references to them cleared), the stock rows return to bare addresses
	exec(`UPDATE user SET dns_preset_id = 'dns_builtin_yandex_safe' WHERE id = 'usr_2'`)
	if _, err := p.DownTo(ctx, 9); err != nil {
		t.Fatalf("down to 9 again: %v", err)
	}
	if own, _, err := d.UserRefs(ctx, "usr_2"); err != nil || own != "" {
		t.Errorf("usr_2 after rollback: %q %v", own, err)
	}
	if ok, err := d.Exists(ctx, "dns_builtin_yandex_safe"); err != nil || ok {
		t.Errorf("new built-in after rollback: %v %v", ok, err)
	}
	var servers, split string
	if err := s.R.QueryRowContext(ctx, `SELECT servers, split FROM dns_preset WHERE id = 'dns_builtin_ru_split'`).Scan(&servers, &split); err != nil ||
		servers != stockCF || !strings.Contains(split, `"servers":[{"kind":"plain","address":"77.88.8.8"},{"kind":"plain","address":"77.88.8.1"}]`) {
		t.Errorf("ru split after rollback: %s %s %v", servers, split, err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT servers FROM dns_preset WHERE id = 'dns_builtin_yandex'`).Scan(&servers); err != nil || servers != `[{"kind":"plain","address":"10.0.0.53"}]` {
		t.Errorf("edited built-in after rollback: %s %v", servers, err)
	}
	var sort int
	if err := s.R.QueryRowContext(ctx, `SELECT sort FROM dns_preset WHERE id = 'dns_builtin_yandex'`).Scan(&sort); err != nil || sort != 70 {
		t.Errorf("yandex sort after rollback: %d %v", sort, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
