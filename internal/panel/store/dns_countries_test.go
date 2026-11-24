//go:build !js

package store

import (
	"context"
	"testing"
)

var countrySplits = []string{"dns_builtin_ir_split", "dns_builtin_tr_split", "dns_builtin_kz_split", "dns_builtin_by_split", "dns_builtin_uz_split"}

// 00158 adds five built-ins and moves nobody onto them; Down takes them away again and lets go of whoever used them.
func TestDNSCountryBuiltinsMigration(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	d := s.DNS()
	for _, id := range countrySplits {
		if r, err := d.Get(ctx, id); err != nil || !r.Builtin || !r.SplitDirect || r.Transport != "plain" {
			t.Errorf("%s = %+v, %v", id, r, err)
		}
	}
	if l, err := d.Lookup(ctx); err != nil || l.DefaultID != DNSBuiltinDefaultID {
		t.Fatalf("default: %q %v", l.DefaultID, err)
	}
	execT(t, s, `INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES ('grp_a', 'g', 1, 'dns_builtin_tr_split')`)
	execT(t, s, `INSERT INTO setting (k, v) VALUES ('dns.default_preset', 'dns_builtin_ir_split')`)
	prov := cnProvider(t, s)
	if _, err := prov.DownTo(ctx, 157); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, id := range countrySplits {
		if ok, _ := d.Exists(ctx, id); ok {
			t.Errorf("%s is left after the rollback", id)
		}
	}
	if n := countT(t, s, `SELECT count(*) FROM user_group WHERE dns_preset_id IS NOT NULL`) + countT(t, s, `SELECT count(*) FROM setting WHERE k = 'dns.default_preset'`); n != 0 {
		t.Errorf("%d references are left", n)
	}
	if _, err := prov.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
