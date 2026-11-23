//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

// 00155 switches block_max on for the two built-in Russian presets that send Russian sites direct and for nothing else: the other
// built-ins and a custom preset stay off, the migration is repeatable (down and up), and Update carries the value (the owner can
// switch a built-in off).
func TestDNSBlockMaxMigration(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	d := s.DNS()
	now := time.Unix(1_800_000_000, 0).UTC()
	p := DNSPreset{ID: "dns_own", Name: "Own", ServersJSON: `[]`, SplitJSON: `[]`, Transport: "plain", CreatedAt: now, UpdatedAt: now}
	if err := d.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"dns_builtin_ru_split": true, "dns_builtin_ru_geo": true}
	check := func(when string) {
		t.Helper()
		all, err := d.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range all {
			if r.BlockMax != want[r.ID] {
				t.Errorf("%s: %s block_max = %v", when, r.ID, r.BlockMax)
			}
		}
	}
	check("fresh")

	prov := cnProvider(t, s)
	if _, err := prov.DownTo(ctx, 154); err != nil {
		t.Fatalf("down to 154: %v", err)
	}
	if _, err := prov.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	check("after down and up")

	// Update writes it, a custom preset can have it, and the built-in can be switched off by the owner.
	own, _ := d.Get(ctx, "dns_own")
	own.BlockMax = true
	if err := d.Update(ctx, own); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Get(ctx, "dns_own"); err != nil || !got.BlockMax {
		t.Fatalf("custom after update: %+v %v", got, err)
	}
	ru, _ := d.Get(ctx, "dns_builtin_ru_split")
	ru.BlockMax = false
	if err := d.Update(ctx, ru); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Get(ctx, "dns_builtin_ru_split"); err != nil || got.BlockMax {
		t.Fatalf("built-in after switching off: %+v %v", got, err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE dns_preset SET block_max = 2 WHERE id = 'dns_own'`); err == nil {
		t.Fatal("CHECK (block_max IN (0, 1)) did not refuse 2")
	}
}
