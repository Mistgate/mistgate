package store

import (
	"context"
	"testing"
	"time"
)

// 00044 on a live instance: the new tables are empty, the existing credentials keep their rows with configs_at 0 (the
// credential's own created_at then stands in for "the last fetch"), and going back down leaves the schema before it.
func TestNodeDNSMigrationUpgradesAndRollsBack(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 43); err != nil {
		t.Fatal(err)
	}
	fixtureInbound(t, s, "a")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1)`)
	execT(t, s, `INSERT INTO device (id, user_id, first_seen_at, last_seen_at, created_at) VALUES ('dev_hy', 'usr_a', 1, 1, 1)`)
	execT(t, s, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at) VALUES ('crd_hy', 'dev_hy', 'usr_a', 'hysteria2', x'01', '{}', 1)`)

	if _, err := p.UpTo(ctx, 44); err != nil {
		t.Fatalf("up to 44: %v", err)
	}
	for _, table := range []string{"node_dns_option", "user_node_dns"} {
		if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table) != 1 || countT(t, s, `SELECT count(*) FROM `+table) != 0 {
			t.Errorf("%s: missing or not empty after the upgrade", table)
		}
	}
	if n := countT(t, s, `SELECT count(*) FROM device_credential WHERE id = 'crd_hy' AND configs_at = 0`); n != 1 {
		t.Fatalf("the existing credential: %d", n)
	}
	// Constraints: one default per node, no two rows for one (node, preset), a person has one pick per node.
	execT(t, s, `INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES ('nod_a', 'dns_builtin_adblock', 0, 1)`)
	execT(t, s, `INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES ('nod_a', 'dns_builtin_standard', 1, 0)`)
	for name, q := range map[string]string{
		"a second default":  `INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES ('nod_a', 'dns_builtin_family', 2, 1)`,
		"the same preset":   `INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES ('nod_a', 'dns_builtin_standard', 3, 0)`,
		"an unknown node":   `INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES ('nod_x', 'dns_builtin_standard', 0, 0)`,
		"an unknown preset": `INSERT INTO node_dns_option (node_id, preset_id, position, is_default) VALUES ('nod_a', 'dns_nope', 0, 0)`,
	} {
		if _, err := s.W.ExecContext(ctx, q); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	execT(t, s, `INSERT INTO user_node_dns (user_id, node_id, preset_id, updated_at) VALUES ('usr_a', 'nod_a', 'dns_builtin_standard', 5)`)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO user_node_dns (user_id, node_id, preset_id, updated_at) VALUES ('usr_a', 'nod_a', 'dns_builtin_adblock', 6)`); err == nil {
		t.Error("two picks of one person on one node")
	}
	// A deleted preset takes its offers and picks along; a deleted node and a deleted user too.
	execT(t, s, `DELETE FROM dns_preset WHERE id = 'dns_builtin_standard'`)
	if n := countT(t, s, `SELECT count(*) FROM node_dns_option`) + countT(t, s, `SELECT count(*) FROM user_node_dns`); n != 1 {
		t.Errorf("rows left after the preset went: %d", n)
	}
	execT(t, s, `DELETE FROM user WHERE id = 'usr_a'`)
	execT(t, s, `DELETE FROM inbound WHERE node_id = 'nod_a'`)
	execT(t, s, `DELETE FROM node WHERE id = 'nod_a'`)
	if n := countT(t, s, `SELECT count(*) FROM node_dns_option`); n != 0 {
		t.Errorf("offers left after the node went: %d", n)
	}

	if _, err := p.DownTo(ctx, 43); err != nil {
		t.Fatalf("down to 43: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name IN ('node_dns_option', 'user_node_dns')`) != 0 ||
		countT(t, s, `SELECT count(*) FROM pragma_table_info('device_credential') WHERE name = 'configs_at'`) != 0 {
		t.Fatal("the schema of 00044 is left after the rollback")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestNodeDNSOptionsAndPicks(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	fixtureInbound(t, s, "a")
	fixtureInbound(t, s, "b")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_a', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_a', 'u', 'grp_a', 1, x'01', x'02', 1)`)
	d := s.DNS()

	if err := d.SetNodeOptions(ctx, "nod_a", []string{"dns_builtin_yandex", "dns_builtin_standard"}, "dns_builtin_yandex"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetNodeOptions(ctx, "nod_b", []string{"dns_builtin_adblock"}, ""); err != nil {
		t.Fatal(err)
	}
	got, err := d.NodeOptions(ctx)
	if err != nil || len(got["nod_a"]) != 2 || got["nod_a"][0].PresetID != "dns_builtin_yandex" || !got["nod_a"][0].Default ||
		got["nod_a"][1].PresetID != "dns_builtin_standard" || got["nod_a"][1].Default || len(got["nod_b"]) != 1 || got["nod_b"][0].Default {
		t.Fatalf("options = %+v, %v", got, err)
	}
	// Replacing reorders and moves the default; an empty list clears the node.
	if err := d.SetNodeOptions(ctx, "nod_a", []string{"dns_builtin_standard", "dns_builtin_yandex"}, "dns_builtin_standard"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.NodeOptions(ctx); got["nod_a"][0].PresetID != "dns_builtin_standard" || !got["nod_a"][0].Default || got["nod_a"][1].Default {
		t.Errorf("replaced: %+v", got["nod_a"])
	}
	if err := d.SetNodeOptions(ctx, "nod_b", nil, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.NodeOptions(ctx); len(got["nod_b"]) != 0 {
		t.Errorf("cleared: %+v", got["nod_b"])
	}
	if err := d.SetNodeOptions(ctx, "nod_a", []string{"dns_nope"}, ""); err != ErrNotFound {
		t.Errorf("an unknown preset: %v", err)
	}
	if got, _ := d.NodeOptions(ctx); len(got["nod_a"]) != 2 {
		t.Errorf("a refused write changed the offers: %+v", got["nod_a"])
	}

	t0 := time.UnixMilli(1_700_000_000_123)
	if err := d.SetUserNodeChoice(ctx, "usr_a", "nod_a", "dns_builtin_yandex", t0); err != nil {
		t.Fatal(err)
	}
	if err := d.SetUserNodeChoice(ctx, "usr_a", "nod_a", "dns_builtin_standard", t0.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	picks, err := d.UserNodeChoices(ctx, "usr_a")
	if err != nil || len(picks) != 1 || picks["nod_a"].PresetID != "dns_builtin_standard" || picks["nod_a"].UpdatedMs != t0.UnixMilli()+1 {
		t.Fatalf("picks = %+v, %v", picks, err)
	}
	if err := d.SetUserNodeChoice(ctx, "usr_x", "nod_a", "dns_builtin_standard", t0); err != ErrNotFound {
		t.Errorf("an unknown user: %v", err)
	}
	if err := d.SetUserNodeChoice(ctx, "usr_a", "nod_b", "dns_builtin_standard", t0); err != nil {
		t.Fatal(err)
	}
	if err := d.SetUserNodeChoice(ctx, "usr_a", "nod_b", "", t0); err != nil { // "" = back to the default
		t.Fatal(err)
	}
	if n, err := d.ResetUserNodeChoices(ctx, "usr_a"); err != nil || n != 1 {
		t.Errorf("reset: %d, %v", n, err)
	}
	if picks, _ := d.UserNodeChoices(ctx, "usr_a"); len(picks) != 0 {
		t.Errorf("picks after the reset: %+v", picks)
	}
}
