//go:build !js

package store

import (
	"context"
	"testing"
)

// 00154 adds the built-in "Russia: Russian sites direct (community list)": the stock Russian split's resolvers and suffixes plus the
// country list "ru", split direct on, plain, listed right after the stock Russian split. The stock split is not touched.
func TestDNSGeoRUMigrationAddsTheBuiltIn(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	d := s.DNS()
	r, err := d.Get(ctx, "dns_builtin_ru_geo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r.Name != "Россия: российские сайты напрямую (список сообщества)" || !r.Builtin || !r.SplitDirect || r.IPv4Only || r.Transport != "plain" {
		t.Errorf("row = %+v", r)
	}
	stock, err := d.Get(ctx, "dns_builtin_ru_split")
	if err != nil {
		t.Fatal(err)
	}
	if r.ServersJSON != stock.ServersJSON {
		t.Errorf("servers = %s, stock %s", r.ServersJSON, stock.ServersJSON)
	}
	const suffixes = `".ru",".su",".xn--p1ai","yandex.com","yandex.net","yastatic.net","vk.com","userapi.com","sberbank.com","vtb.com"`
	if r.SplitJSON != `[{"suffixes":[`+suffixes+`],"geo":["ru"],"servers":[{"variant":"yandex/basic"}]}]` {
		t.Errorf("split = %s", r.SplitJSON)
	}
	if stock.SplitJSON != `[{"suffixes":[`+suffixes+`],"servers":[{"variant":"yandex/basic"}]}]` {
		t.Errorf("the stock Russian split changed: %s", stock.SplitJSON)
	}
	var sort int
	if err := s.R.QueryRowContext(ctx, `SELECT sort FROM dns_preset WHERE id = ?`, r.ID).Scan(&sort); err != nil || sort != 15 {
		t.Errorf("sort = %d %v", sort, err)
	}
}
