package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// relayAudienceProof runs on SQLite and D1: the two reads behind the audience of a relay candidate.
func relayAudienceProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	for _, name := range []string{"de1", "msk1", "nl1"} {
		relayExec(t, st, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)`, "nod_au_"+name, name, name+".example.invalid")
	}
	if err := st.Access().CreateProfile(ctx, AccessProfile{ID: "prf_au", Protocol: "awg", Name: "au-de1", SettingsJSON: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateGroup(ctx, AccessGroup{ID: "grp_au", Name: "au", ProfileIDs: []string{"prf_au"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i, u := range []struct {
		id, name string
		all      int
		disabled int
	}{{"usr_au_alice", "alice", 0, 0}, {"usr_au_bob", "bob", 1, 1}} {
		relayExec(t, st, `INSERT INTO user (id, name, group_id, all_nodes, disabled, period_start, sub_token_hash, sub_token_enc, created_at)
			VALUES (?, ?, 'grp_au', ?, ?, 1, ?, x'01', 1)`, u.id, u.name, u.all, u.disabled, []byte{byte(i + 1)})
	}
	// alice holds the entry, the server and a node outside the candidate set; only the first two come back.
	for _, n := range []string{"nod_au_msk1", "nod_au_de1", "nod_au_nl1"} {
		relayExec(t, st, `INSERT INTO user_node (user_id, node_id) VALUES ('usr_au_alice', ?)`, n)
	}
	users, err := st.Access().RelayAudienceUsers(ctx, []string{"nod_au_de1", "nod_au_msk1"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, u := range users {
		got[u.ID] = u.NodeIDs
	}
	if want := map[string][]string{"usr_au_alice": {"nod_au_de1", "nod_au_msk1"}, "usr_au_bob": nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audience users = %v, want %v", got, want)
	}

	device := func(id, kind string, revoked bool) {
		t.Helper()
		relayExec(t, st, `INSERT INTO device (id, user_id, hwid_hash, kind, first_seen_at, last_seen_at, created_at, revoked_at)
			VALUES (?, 'usr_au_alice', ?, ?, 1, 1, 1, ?)`, id, []byte(id), kind, map[bool]any{true: 2, false: nil}[revoked])
	}
	cred := func(id, device, path string, revoked bool) {
		t.Helper()
		relayExec(t, st, `INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, config_epoch, created_at, revoked_at)
			VALUES (?, ?, 'usr_au_alice', 'awg', 'prf_au', ?, x'01', '{}', 0, 1, ?)`, id, device, path, map[bool]any{true: 2, false: nil}[revoked])
	}
	device("dev_au_key", "key", false)
	cred("crd_au_key", "dev_au_key", "", false)
	cred("crd_au_path", "dev_au_key", "relay:nod_au_msk1", false) // a path key is not a static key of its own
	device("dev_au_gone", "key", true)
	cred("crd_au_gone", "dev_au_gone", "", false)
	device("dev_au_link", "link", false)
	cred("crd_au_link", "dev_au_link", "", false)
	device("dev_au_old", "key", false)
	cred("crd_au_old", "dev_au_old", "", true)
	counts, err := st.Access().RelayStaticAWGDeviceCounts(ctx, []string{"prf_au"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]map[string]uint64{"usr_au_alice": {"prf_au": 1}}; !reflect.DeepEqual(counts, want) {
		t.Fatalf("static key counts = %v, want %v", counts, want)
	}
}
