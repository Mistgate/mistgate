//go:build !js

package store

import (
	"context"
	"testing"
)

// Profiles made before VLESS could serve the node's own site have a target and no kind. The migration writes the kind they
// always had, leaves every other profile alone, rolls back to the old shape and applies again.
func TestVLESSTargetKindMigration(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 57); err != nil {
		t.Fatalf("up to 57: %v", err)
	}
	profiles := map[string]struct{ protocol, settings string }{
		"prf_legacy":  {"vless", `{"port":443,"reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`},
		"prf_ipv6":    {"vless", `{"reality":{"target":"[2001:db8::1]:443"}}`},
		"prf_site":    {"vless", `{"reality":{"target_kind":"site","target":"www.example.org:443"}}`},
		"prf_self":    {"vless", `{"reality":{"target_kind":"self","target":""}}`},
		"prf_noreal":  {"vless", `{"port":443}`},
		"prf_blank":   {"vless", `{"reality":{"target":"  "}}`},
		"prf_other":   {"hysteria2", `{"reality":{"target":"not a vless profile"}}`},
		"prf_untyped": {"vless", `{"reality":{"target":443}}`},
	}
	for id, row := range profiles {
		execT(t, s, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 1)`, id, row.protocol, id, row.settings)
	}
	kind := func(id string) string {
		t.Helper()
		var v *string
		if err := s.W.QueryRowContext(ctx, `SELECT json_extract(settings_json, '$.reality.target_kind') FROM profile WHERE id = ?`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			return "<none>"
		}
		return *v
	}
	settings := func(id string) string {
		t.Helper()
		var v string
		if err := s.W.QueryRowContext(ctx, `SELECT settings_json FROM profile WHERE id = ?`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := map[string]string{}
	for id := range profiles {
		before[id] = settings(id)
	}

	if _, err := p.UpTo(ctx, 58); err != nil {
		t.Fatalf("up to 58: %v", err)
	}
	for id, want := range map[string]string{
		"prf_legacy": "site", "prf_ipv6": "site", "prf_site": "site", "prf_self": "self",
		"prf_noreal": "<none>", "prf_blank": "<none>", "prf_other": "<none>", "prf_untyped": "<none>",
	} {
		if got := kind(id); got != want {
			t.Errorf("%s: target_kind = %s, want %s", id, got, want)
		}
	}
	// only the key was added: the rest of a migrated document reads as before
	if got := settings("prf_legacy"); got != `{"port":443,"reality":{"target":"www.example.org:443","server_names":["www.example.org"],"target_kind":"site"}}` {
		t.Errorf("migrated document = %s", got)
	}
	for _, id := range []string{"prf_site", "prf_self", "prf_noreal", "prf_blank", "prf_other", "prf_untyped"} {
		if settings(id) != before[id] {
			t.Errorf("%s changed: %s -> %s", id, before[id], settings(id))
		}
	}

	if _, err := p.DownTo(ctx, 57); err != nil {
		t.Fatalf("down to 57: %v", err)
	}
	for _, id := range []string{"prf_legacy", "prf_ipv6", "prf_site", "prf_self"} {
		if got := kind(id); got != "<none>" {
			t.Errorf("%s: target_kind after rolling back = %s", id, got)
		}
	}
	if got := settings("prf_legacy"); got != `{"port":443,"reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}` {
		t.Errorf("rolled-back document = %s", got)
	}
	if _, err := p.UpTo(ctx, 58); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if kind("prf_legacy") != "site" {
		t.Error("the second run did not migrate")
	}
}
