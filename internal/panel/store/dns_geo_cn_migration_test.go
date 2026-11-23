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
