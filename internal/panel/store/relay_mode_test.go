//go:build !js

package store

import (
	"context"
	"testing"
)

func TestRelayModeSQLite(t *testing.T) {
	relayModeStoreProof(t, openTemp(t))
}

// The migration on a database that has relays: every existing relay becomes "auto" on "dnat", nothing else changes, the down
// migration takes the columns away and a second up gives the same defaults.
func TestRelayReverseMigrationUpDown(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 140); err != nil {
		t.Fatalf("up to 140: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_de1', 'de1', '192.0.2.10', 'active', 1)`,
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_msk1', 'msk1', '192.0.2.20', 'active', 1)`,
		`INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_vl', 'vless', 'vl', '{}', 1, 1)`,
		`INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES ('inb_de1_vl', 'prf_vl', 'nod_de1', 1, 1)`,
		`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
			VALUES ('rly_old', 'inb_de1_vl', 'nod_msk1', 'tcp', 8443, 1, 'active', '{"ok":true}', 1, 1)`,
	} {
		execT(t, s, q)
	}
	if _, err := p.UpTo(ctx, 141); err != nil {
		t.Fatalf("up to 141: %v", err)
	}
	check := func(when string) {
		t.Helper()
		var mode, active, reason, state, checkJSON string
		var changed int64
		if err := s.R.QueryRowContext(ctx, `SELECT mode, active_mode, mode_reason, mode_changed_at, state, check_json FROM relay WHERE id = 'rly_old'`).
			Scan(&mode, &active, &reason, &changed, &state, &checkJSON); err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		if mode != "auto" || active != "dnat" || reason != "" || changed != 0 || state != "active" || checkJSON != `{"ok":true}` {
			t.Fatalf("%s: relay is %q %q %q %d %q %q", when, mode, active, reason, changed, state, checkJSON)
		}
		if countT(t, s, `SELECT count(*) FROM node WHERE bridge_port = 0 AND bridge_path = ''`) != 2 {
			t.Fatalf("%s: nodes do not have the default bridge port and path", when)
		}
	}
	check("after up")
	for _, bad := range []string{
		`UPDATE relay SET mode = 'both'`, `UPDATE relay SET active_mode = 'auto'`, `UPDATE node SET bridge_port = 70000`, `UPDATE node SET bridge_port = -1`,
	} {
		if _, err := s.W.ExecContext(ctx, bad); err == nil {
			t.Fatalf("%s was accepted", bad)
		}
	}
	if _, err := p.DownTo(ctx, 140); err != nil {
		t.Fatalf("down to 140: %v", err)
	}
	for _, column := range []string{"mode", "active_mode", "mode_changed_at", "mode_reason"} {
		if countT(t, s, `SELECT count(*) FROM pragma_table_info('relay') WHERE name = ?`, column) != 0 {
			t.Fatalf("relay.%s remains after down", column)
		}
	}
	for _, column := range []string{"bridge_port", "bridge_path"} {
		if countT(t, s, `SELECT count(*) FROM pragma_table_info('node') WHERE name = ?`, column) != 0 {
			t.Fatalf("node.%s remains after down", column)
		}
	}
	if countT(t, s, `SELECT count(*) FROM relay WHERE id = 'rly_old' AND enabled = 1 AND state = 'active'`) != 1 {
		t.Fatal("the relay did not survive the down migration")
	}
	if _, err := p.UpTo(ctx, 141); err != nil {
		t.Fatalf("up again: %v", err)
	}
	check("after up again")
}

func TestRelayTargetChangeSQLite(t *testing.T) { relayTargetChangeProof(t, openTemp(t)) }
