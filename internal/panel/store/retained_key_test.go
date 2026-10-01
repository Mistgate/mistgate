package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A live database (00017) with an AWG inbound that carries key material: 00018 only adds a table, touches nothing,
// the table obeys its constraints and cascades, rolling back drops just it, and it applies again.
func TestRetainedKeyMigrationUpDownUp(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 17); err != nil {
		t.Fatalf("up to 17: %v", err)
	}
	nodeID, inboundID := fixtureInbound(t, s, "a")
	execT(t, s, `UPDATE inbound SET plugin_state_enc = x'0102', plugin_public_json = '{"public_key":"k"}' WHERE id = ?`, inboundID)

	if _, err := p.UpTo(ctx, 18); err != nil {
		t.Fatalf("up to 18: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM inbound WHERE plugin_state_enc = x'0102'`) != 1 {
		t.Fatal("00018 touched the inbound")
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO awg_retained_key (profile_id, node_id, state_enc, public_json, created_at) VALUES ('prf_a', ?, x'01', 'not json', 1)`, nodeID); err == nil {
		t.Fatal("a non-JSON public part was accepted")
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO awg_retained_key (profile_id, node_id, state_enc, public_json, created_at) VALUES ('prf_nope', ?, x'01', '{}', 1)`, nodeID); err == nil {
		t.Fatal("a row of an unknown profile was accepted")
	}
	execT(t, s, `INSERT INTO awg_retained_key (profile_id, node_id, state_enc, public_json, created_at) VALUES ('prf_a', ?, x'01', '{}', 1)`, nodeID)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO awg_retained_key (profile_id, node_id, state_enc, public_json, created_at) VALUES ('prf_a', ?, x'02', '{}', 1)`, nodeID); err == nil {
		t.Fatal("two rows for one (profile, node)")
	}
	// the node going away takes the row with it
	execT(t, s, `DELETE FROM inbound`)
	execT(t, s, `DELETE FROM node WHERE id = ?`, nodeID)
	if n := countT(t, s, `SELECT count(*) FROM awg_retained_key`); n != 0 {
		t.Fatalf("rows after deleting the node: %d", n)
	}
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_b', 'nb', 'b.example.com', 'active', 1)`)
	execT(t, s, `INSERT INTO awg_retained_key (profile_id, node_id, state_enc, public_json, created_at) VALUES ('prf_a', 'nod_b', x'01', '{}', 1)`)

	if _, err := p.DownTo(ctx, 17); err != nil {
		t.Fatalf("down to 17: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = 'awg_retained_key'`) != 0 {
		t.Fatal("the table is left after down")
	}
	if countT(t, s, `SELECT count(*) FROM profile`) != 1 || countT(t, s, `SELECT count(*) FROM node`) != 1 {
		t.Fatal("down touched other tables")
	}
	if _, err := p.UpTo(ctx, 18); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM awg_retained_key`) != 0 {
		t.Fatal("the table is not empty after up again")
	}
}

// The store side: DeleteInbound parks the key (re-sealed by the callback), CreateInbound takes it away, retiring
// the node and deleting the profile drop it, and a retired node parks nothing.
func TestRetainedKeyStoreFlow(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	a := s.Access()
	now := time.Unix(100, 0)
	nodeID, inboundID := fixtureInbound(t, s, "a")
	execT(t, s, `UPDATE inbound SET plugin_state_enc = x'0102', plugin_public_json = '{"public_key":"k"}' WHERE id = ?`, inboundID)
	reseal := func(enc []byte, profileID, nodeID string) ([]byte, error) {
		return append([]byte(profileID+nodeID+":"), enc...), nil
	}

	if err := a.DeleteInbound(ctx, inboundID, now, reseal); err != nil {
		t.Fatal(err)
	}
	k, err := a.RetainedKey(ctx, "prf_a", nodeID)
	if err != nil || string(k.StateEnc) != "prf_a"+nodeID+":\x01\x02" || k.PublicJSON != `{"public_key":"k"}` {
		t.Fatalf("retained = %q %q %v", k.StateEnc, k.PublicJSON, err)
	}
	if err := a.DeleteInbound(ctx, inboundID, now, reseal); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}

	// An inbound without key material (hysteria2) parks nothing; a callback that keeps nothing keeps the old row.
	in := AccessInbound{ID: "inb_b", ProfileID: "prf_a", NodeID: nodeID, Enabled: true, CreatedAt: now}
	if err := a.CreateInbound(ctx, in); err != nil { // takes the row
		t.Fatal(err)
	}
	if _, err := a.RetainedKey(ctx, "prf_a", nodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the row stayed after the key was taken: %v", err)
	}
	if err := a.DeleteInbound(ctx, "inb_b", now, reseal); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RetainedKey(ctx, "prf_a", nodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a keyless inbound parked a key: %v", err)
	}

	// Retiring the node drops the row; a retired node parks nothing.
	in.PluginStateEnc = []byte{9}
	if err := a.CreateInbound(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteInbound(ctx, "inb_b", now, reseal); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireNode(ctx, nodeID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RetainedKey(ctx, "prf_a", nodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the row survived retiring the node: %v", err)
	}
	if err := a.CreateInbound(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteInbound(ctx, "inb_b", now, reseal); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RetainedKey(ctx, "prf_a", nodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a retired node parked a key: %v", err)
	}

	// Deleting the profile drops the row (foreign key).
	nodeB, inB := fixtureInbound(t, s, "b")
	execT(t, s, `UPDATE inbound SET plugin_state_enc = x'05' WHERE id = ?`, inB)
	if err := a.DeleteInbound(ctx, inB, now, reseal); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RetainedKey(ctx, "prf_b", nodeB); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteProfile(ctx, "prf_b", now); err != nil {
		t.Fatal(err)
	}
	if n := countT(t, s, `SELECT count(*) FROM awg_retained_key`); n != 0 {
		t.Fatalf("rows after deleting the profile: %d", n)
	}
}
