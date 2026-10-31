//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

// block_ads is written by Create and Update and read back; a preset that existed before 00087 reads as off, and rolling
// back drops the column without losing the row.
func TestDNSBlockAds(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	d := s.DNS()
	now := time.Unix(1_800_000_000, 0).UTC()
	p := DNSPreset{ID: "dns_ads", Name: "Ads", ServersJSON: `[]`, SplitJSON: `[]`, Transport: "plain", BlockAds: true, CreatedAt: now, UpdatedAt: now}
	if err := d.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Get(ctx, p.ID); err != nil || !got.BlockAds {
		t.Fatalf("after create: %+v %v", got, err)
	}
	if got, err := d.Get(ctx, DNSBuiltinDefaultID); err != nil || got.BlockAds {
		t.Fatalf("a built-in must stay off: %+v %v", got, err)
	}
	p.BlockAds = false
	if err := d.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Get(ctx, p.ID); err != nil || got.BlockAds {
		t.Fatalf("after update: %+v %v", got, err)
	}
	p.BlockAds = true
	if err := d.Update(ctx, p); err != nil {
		t.Fatal(err)
	}

	count := func() (n int) {
		t.Helper()
		if err := s.W.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('dns_preset') WHERE name = 'block_ads'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	prov := cnProvider(t, s)
	if _, err := prov.DownTo(ctx, 86); err != nil {
		t.Fatalf("down to 86: %v", err)
	}
	if count() != 0 {
		t.Fatal("block_ads still there after down")
	}
	if ok, err := d.Exists(ctx, p.ID); err != nil || !ok {
		t.Fatalf("preset after down: %v %v", ok, err)
	}
	if _, err := prov.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if count() != 1 {
		t.Fatal("block_ads missing after up")
	}
	if got, err := d.Get(ctx, p.ID); err != nil || got.BlockAds {
		t.Fatalf("a preset from before the column reads as on: %+v %v", got, err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE dns_preset SET block_ads = 2 WHERE id = ?`, p.ID); err == nil {
		t.Fatal("CHECK (block_ads IN (0, 1)) did not refuse 2")
	}
}
