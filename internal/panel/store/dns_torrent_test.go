//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

// 00159: torrent_direct is off for every preset that exists, built-in or not, and Create and Update carry it.
func TestDNSTorrentDirectColumn(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	d := s.DNS()
	all, err := d.List(ctx)
	if err != nil || len(all) == 0 {
		t.Fatalf("list: %d %v", len(all), err)
	}
	for _, r := range all {
		if r.TorrentDirect {
			t.Errorf("%s has torrent_direct on", r.ID)
		}
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	p := DNSPreset{ID: "dns_own", Name: "Own", ServersJSON: `[]`, SplitJSON: `[]`, Transport: "plain", TorrentDirect: true, CreatedAt: now, UpdatedAt: now}
	if err := d.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Get(ctx, "dns_own"); err != nil || !got.TorrentDirect {
		t.Fatalf("created: %+v %v", got, err)
	}
	p.TorrentDirect = false
	if err := d.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Get(ctx, "dns_own"); got.TorrentDirect {
		t.Error("update did not switch it off")
	}
	ru, _ := d.Get(ctx, "dns_builtin_ru_split")
	ru.TorrentDirect = true
	if err := d.Update(ctx, ru); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Get(ctx, "dns_builtin_ru_split"); !got.TorrentDirect {
		t.Error("a built-in could not be switched on")
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE dns_preset SET torrent_direct = 2 WHERE id = 'dns_own'`); err == nil {
		t.Fatal("CHECK (torrent_direct IN (0, 1)) did not refuse 2")
	}
}
