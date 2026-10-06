//go:build !js

package store

import (
	"context"
	"testing"
)

func tableCount(t *testing.T, s *Store, names ...string) int {
	t.Helper()
	n := 0
	for _, name := range names {
		n += countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = ?`, name)
	}
	return n
}

// A live instance at version 15 (an admin, users, an audit trail, a rollout): 00016 must apply on top of that data
// without touching it, its CHECKs and indexes must hold, and rolling back must drop only the token tables.
func TestTokensMigrationUpgradeAndRollback(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 15); err != nil {
		t.Fatalf("up to 15: %v", err)
	}
	if tableCount(t, s, "api_token", "mcp_plan") != 0 {
		t.Fatal("the token tables exist before 00016")
	}
	execT(t, s, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_a', 'Ada', 'owner', x'01', 1)`)
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1)`)
	execT(t, s, `INSERT INTO audit (ts, actor, action, params, result, source, ip) VALUES (5, 'adm_a', 'login', '{}', 'ok', 'panel', '203.0.113.9')`)
	execT(t, s, `INSERT INTO setting (k, v) VALUES ('keep', 'me')`)

	if _, err := p.UpTo(ctx, 16); err != nil {
		t.Fatalf("up to 16: %v", err)
	}
	if tableCount(t, s, "api_token", "mcp_plan", "api_token_live_name", "mcp_plan_status", "mcp_plan_token") != 5 {
		t.Fatal("00016 did not create its tables and indexes")
	}
	// old data untouched
	if n := countT(t, s, `SELECT count(*) FROM audit WHERE actor = 'adm_a' AND ip = '203.0.113.9'`) +
		countT(t, s, `SELECT count(*) FROM user WHERE id = 'usr_a'`) + countT(t, s, `SELECT count(*) FROM setting WHERE k = 'keep'`) +
		countT(t, s, `SELECT count(*) FROM admin WHERE id = 'adm_a'`); n != 4 {
		t.Fatalf("existing rows after the upgrade: %d of 4", n)
	}

	ins := func(id, name, profile, hash string, rate int) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO api_token (id, name, name_key, profile, secret_hash, hint, rate_per_min, created_by, created_at, expires_at)
			VALUES (?, ?, lower(?), ?, ?, 'abcd', ?, 'adm_a', 1, 99)`, id, name, name, profile, []byte(hash), rate)
		return err
	}
	if err := ins("tok_1", "Claude", "readonly", "h1", 120); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"a profile that does not exist": ins("tok_2", "x", "root", "h2", 120),
		"a rate of zero":                ins("tok_3", "x", "admin", "h3", 0),
		"a rate above 600":              ins("tok_4", "x", "admin", "h4", 601),
		"the same name in other case":   ins("tok_5", "CLAUDE", "admin", "h5", 120),
		"the same secret hash twice":    ins("tok_6", "other", "admin", "h1", 120),
	} {
		if err == nil {
			t.Errorf("accepted: %s", name)
		}
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE api_token SET last_used_via = 'ssh' WHERE id = 'tok_1'`); err == nil {
		t.Error("an unknown channel accepted")
	}
	// a revocation frees the name
	execT(t, s, `UPDATE api_token SET revoked_at = 5 WHERE id = 'tok_1'`)
	if err := ins("tok_7", "claude", "operator", "h7", 60); err != nil {
		t.Fatalf("the name of a revoked token is free: %v", err)
	}

	plan := func(id, token, status string, needs int, confirm string) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO mcp_plan (id, token_id, tool, params_json, params_hash, confirm_hash, facts_json, summary, needs_approval, status, created_at, expires_at)
			VALUES (?, ?, 'node_fix', '{}', x'01', ?, '[]', 's', ?, ?, 1, 601)`, id, token, []byte(confirm), needs, status)
		return err
	}
	if err := plan("pln_1", "tok_7", "awaiting", 1, "c1"); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"a status that does not exist": plan("pln_2", "tok_7", "done", 1, "c2"),
		"needs_approval of 2":          plan("pln_3", "tok_7", "planned", 2, "c3"),
		"an unknown token":             plan("pln_4", "tok_none", "planned", 0, "c4"),
		"the same confirm hash twice":  plan("pln_5", "tok_7", "planned", 0, "c1"),
	} {
		if err == nil {
			t.Errorf("accepted: %s", name)
		}
	}

	// down: only the token tables go; the rest of the schema and its data stay
	if _, err := p.DownTo(ctx, 15); err != nil {
		t.Fatalf("down to 15: %v", err)
	}
	if tableCount(t, s, "api_token", "mcp_plan", "api_token_live_name", "mcp_plan_status", "mcp_plan_token") != 0 {
		t.Fatal("00016 left objects behind after the rollback")
	}
	if n := countT(t, s, `SELECT count(*) FROM audit WHERE actor = 'adm_a'`) + countT(t, s, `SELECT count(*) FROM user WHERE id = 'usr_a'`) +
		countT(t, s, `SELECT count(*) FROM admin WHERE id = 'adm_a'`) + tableCount(t, s, "warp_account", "awg_peer"); n != 5 {
		t.Fatalf("the rollback touched older data or tables: %d of 5", n)
	}
	// and the upgrade can be done again
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if tableCount(t, s, "api_token", "mcp_plan") != 2 {
		t.Fatal("tables missing after the second upgrade")
	}
}
