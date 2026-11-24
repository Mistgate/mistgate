//go:build !js

package store

import (
	"context"
	"testing"
)

var retiredSeven = []string{
	"dns_builtin_yandex", "dns_builtin_cloudflare", "dns_builtin_google", "dns_builtin_dnssb",
	"dns_builtin_cloudflare_family", "dns_builtin_yandex_family", "dns_builtin_opendns_family",
}

// 00157 retires seven built-in presets: one that is assigned in any of the seven places, or that the owner changed, stays as an own
// preset (builtin = 0, id and references as they were); every other one goes; a new installation has none of them; the rest of the
// built-ins are not touched; Down brings all seven back as built-ins.
func TestDNSRetireSevenMigration(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	prov := cnProvider(t, s)

	// read by SQL: after a rollback the table lacks the columns that the store of today selects
	present := func(id string) (DNSPreset, bool) {
		var r DNSPreset
		var b int
		err := s.R.QueryRowContext(ctx, `SELECT id, name, description, builtin, servers, preferred_transport FROM dns_preset WHERE id = ?`, id).
			Scan(&r.ID, &r.Name, &r.Description, &b, &r.ServersJSON, &r.Transport)
		r.Builtin = b == 1
		return r, err == nil
	}

	// A new installation: none of the seven, the others are there.
	for _, id := range retiredSeven {
		if _, ok := present(id); ok {
			t.Errorf("fresh: %s exists", id)
		}
	}
	others := []string{"dns_builtin_ru_split", "dns_builtin_ru_geo", "dns_builtin_ru_proxied", "dns_builtin_cn_split", "dns_builtin_standard",
		"dns_builtin_adblock", "dns_builtin_ads_device", "dns_builtin_family", "dns_builtin_quad9", "dns_builtin_cloudflare_security", "dns_builtin_yandex_safe"}
	checkOthers := func(when string) {
		t.Helper()
		for _, id := range others {
			if r, ok := present(id); !ok || !r.Builtin {
				t.Errorf("%s: %s = %+v, present %v", when, id, r, ok)
			}
		}
	}
	checkOthers("fresh")

	// Before the migration: the seven are built-ins, each used in one of the seven places.
	if _, err := prov.DownTo(ctx, 155); err != nil {
		t.Fatalf("down to 155: %v", err)
	}
	for _, id := range retiredSeven {
		if r, ok := present(id); !ok || !r.Builtin {
			t.Fatalf("after down: %s = %+v, present %v", id, r, ok)
		}
	}
	fixtureInbound(t, s, "a")
	execT(t, s, `INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES ('grp_a', 'g', 1, 'dns_builtin_google')`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at, dns_preset_id) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1, 'dns_builtin_cloudflare')`)
	execT(t, s, `INSERT INTO setting (k, v) VALUES ('dns.default_preset', 'dns_builtin_dnssb')`)
	execT(t, s, `INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES ('nod_a', 'dns_builtin_cloudflare_family', 0, 0)`)
	execT(t, s, `INSERT INTO user_node_dns (user_id, node_id, preset_id, updated_at) VALUES ('usr_a', 'nod_a', 'dns_builtin_yandex_family', 1)`)
	execT(t, s, `INSERT INTO link_dns_option (preset_id, position, is_default) VALUES ('dns_builtin_opendns_family', 0, 0)`)
	execT(t, s, `INSERT INTO user_link_dns (user_id, preset_id, updated_at) VALUES ('usr_a', 'dns_builtin_yandex', 1)`)
	if _, err := prov.Up(ctx); err != nil {
		t.Fatalf("up (all used): %v", err)
	}
	for _, id := range retiredSeven {
		if r, ok := present(id); !ok || r.Builtin {
			t.Errorf("used: %s = %+v, present %v; want an own preset", id, r, ok)
		}
	}
	if n := countT(t, s, `SELECT count(*) FROM node_dns_option WHERE preset_id = 'dns_builtin_cloudflare_family'`) +
		countT(t, s, `SELECT count(*) FROM user_node_dns WHERE preset_id = 'dns_builtin_yandex_family'`) +
		countT(t, s, `SELECT count(*) FROM link_dns_option WHERE preset_id = 'dns_builtin_opendns_family'`) +
		countT(t, s, `SELECT count(*) FROM user_link_dns WHERE preset_id = 'dns_builtin_yandex'`) +
		countT(t, s, `SELECT count(*) FROM user WHERE dns_preset_id = 'dns_builtin_cloudflare'`) +
		countT(t, s, `SELECT count(*) FROM user_group WHERE dns_preset_id = 'dns_builtin_google'`) +
		countT(t, s, `SELECT count(*) FROM setting WHERE k = 'dns.default_preset' AND v = 'dns_builtin_dnssb'`); n != 7 {
		t.Errorf("%d of the 7 references are left", n)
	}
	checkOthers("used")

	// Back, nothing assigned any more and one of them changed: only that one stays.
	if _, err := prov.DownTo(ctx, 155); err != nil {
		t.Fatalf("down again: %v", err)
	}
	for _, q := range []string{`UPDATE user SET dns_preset_id = NULL`, `UPDATE user_group SET dns_preset_id = NULL`, `DELETE FROM setting WHERE k = 'dns.default_preset'`,
		`DELETE FROM node_dns_option`, `DELETE FROM user_node_dns`, `DELETE FROM link_dns_option`, `DELETE FROM user_link_dns`,
		`UPDATE dns_preset SET description = 'моё описание' WHERE id = 'dns_builtin_google'`} {
		execT(t, s, q)
	}
	if _, err := prov.Up(ctx); err != nil {
		t.Fatalf("up (one changed): %v", err)
	}
	for _, id := range retiredSeven {
		r, ok := present(id)
		switch {
		case id == "dns_builtin_google" && (!ok || r.Builtin || r.Description != "моё описание"):
			t.Errorf("changed: %s = %+v, present %v; want an own preset with the owner's text", id, r, ok)
		case id != "dns_builtin_google" && ok:
			t.Errorf("unused and unchanged: %s is still there", id)
		}
	}
	checkOthers("one changed")

	// An edit of any other field counts as a change too.
	if _, err := prov.DownTo(ctx, 155); err != nil {
		t.Fatalf("down once more: %v", err)
	}
	execT(t, s, `UPDATE dns_preset SET split_direct = 1 WHERE id = 'dns_builtin_dnssb'`)
	execT(t, s, `UPDATE dns_preset SET servers = '[{"variant":"google/standard"}]' WHERE id = 'dns_builtin_yandex'`)
	execT(t, s, `UPDATE dns_preset SET preferred_transport = 'doh' WHERE id = 'dns_builtin_opendns_family'`)
	if _, err := prov.Up(ctx); err != nil {
		t.Fatalf("up (edited): %v", err)
	}
	for id, want := range map[string]bool{"dns_builtin_dnssb": true, "dns_builtin_yandex": true, "dns_builtin_opendns_family": true, "dns_builtin_google": true, "dns_builtin_cloudflare": false, "dns_builtin_yandex_family": false} {
		if r, ok := present(id); ok != want || (ok && r.Builtin) {
			t.Errorf("edited: %s = %+v, present %v; want present %v as an own preset", id, r, ok, want)
		}
	}

	// Down brings the seven back as the stock built-ins.
	if _, err := prov.DownTo(ctx, 155); err != nil {
		t.Fatalf("final down: %v", err)
	}
	for _, id := range retiredSeven {
		if r, ok := present(id); !ok || !r.Builtin {
			t.Errorf("down: %s = %+v, present %v", id, r, ok)
		}
	}
	if r, _ := present("dns_builtin_cloudflare_family"); r.Name != "Cloudflare Family: без вредоносных и 18+" || r.ServersJSON != `[{"variant":"cloudflare/family"}]` || r.Transport != "plain" {
		t.Errorf("restored row = %+v", r)
	}
	checkOthers("down")
}
