//go:build !js

package store

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestExtraGroupsSQLite(t *testing.T) { extraGroupStoreProof(t, openTemp) }

// Two writers of a person's groups at once, one that makes a group the main one and one that adds it as an extra group, in
// either order: the main group is never an extra group.
func TestExtraGroupNeverTheMainGroupUnderRace(t *testing.T) {
	ctx := context.Background()
	w := newEGWorld(t, openTemp(t))
	alice := w.user("alice", "family")
	for i := 0; i < 60; i++ {
		// the state both writers start from
		if _, err := w.st.W.ExecContext(ctx, `UPDATE user SET group_id = 'grp_family' WHERE id = ?`, alice.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.st.W.ExecContext(ctx, `DELETE FROM user_extra_group`); err != nil {
			t.Fatal(err)
		}
		stale, err := w.a.User(ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		toMain, addExtra := stale, stale
		toMain.GroupID = "grp_home"
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, run := range []func() error{
			func() error { return w.a.UpdateUser(ctx, toMain, false, AccessGroupChange{}) },
			func() error {
				return w.a.UpdateUser(ctx, addExtra, false, AccessGroupChange{Add: []string{"grp_home"}})
			},
		} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := run(); err != nil {
					t.Error(err)
				}
			}()
		}
		close(start)
		wg.Wait()
		var n int
		if err := w.st.R.QueryRowContext(ctx, `SELECT count(*) FROM user_extra_group x JOIN user u ON u.id = x.user_id AND u.group_id = x.group_id`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("round %d: %d extra rows hold the main group (err %v)", i, n, err)
		}
	}
}

// The plan of every query that reads who is in a group: the rows come from the indexes of the groups, never from a scan of
// the people or of the credentials.
func TestExtraGroupQueryPlans(t *testing.T) {
	st := openTemp(t)
	for name, c := range map[string]struct {
		query string
		args  []any
		want  []string
		bad   []string
	}{
		"DesiredCreds": {query: accDesiredCredsSQL, args: []any{"nod_de1"},
			want: []string{"MATERIALIZE granted", "SEARCH i USING INDEX inbound_node", "SEARCH u USING INDEX user_by_group",
				"SEARCH x USING COVERING INDEX user_extra_group_by_group", "SEARCH c USING INDEX device_credential_user"},
			bad: []string{"SCAN c", "SCAN device_credential", "SCAN u", "SCAN x", "SCAN user", "device_credential_protocol", "AUTOMATIC"}},
		"HasUserCredentialOnNode": {query: accHasCredentialSQL, args: []any{"nod_de1", "usr_alice"},
			want: []string{"SEARCH i USING INDEX inbound_node", "SEARCH u USING INDEX sqlite_autoindex_user_1", "SEARCH x USING"},
			bad:  []string{"SCAN c", "SCAN device_credential", "SCAN u", "SCAN user"}},
		"ListUsers filter by group": {query: `SELECT ` + accUserCols + accUserFrom + `WHERE ` + accGroupMemberCond, args: []any{"grp_family", "grp_family"},
			want: []string{"user_by_group (group_id=?)", "user_extra_group_by_group (group_id=?)"},
			bad:  []string{"SCAN user", "SCAN x"}},
		"AWGDevicesPerProfile of a group": {query: `SELECT c.profile_id, count(*) FROM device_credential c JOIN user u ON u.id = c.user_id
			WHERE c.path = '' AND c.revoked_at IS NULL AND (?1 = '' OR u.group_id = ?1 OR EXISTS (SELECT 1 FROM user_extra_group x WHERE x.user_id = u.id AND x.group_id = ?1))
			GROUP BY c.profile_id`, args: []any{"grp_family"}, want: []string{"SEARCH x USING"}},
	} {
		t.Run(name, func(t *testing.T) {
			plan := queryPlan(t, st, c.query, c.args...)
			t.Log("\n" + plan)
			for _, w := range c.want {
				if !strings.Contains(plan, w) {
					t.Errorf("the plan lacks %q:\n%s", w, plan)
				}
			}
			for _, b := range c.bad {
				if strings.Contains(plan, b) {
					t.Errorf("the plan has %q:\n%s", b, plan)
				}
			}
		})
	}
}

// A node books the traffic of its own people only: a person who has the node through an extra group is one of them.
func TestIngestStatsBooksTheTrafficOfAPersonWithAnExtraGroup(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	f := newFleetBatchFixture(t, s, "xg") // usr_xg in grp_xg holds prf_xg, deployed on nod_xg
	now := f.now
	if err := s.Access().CreateGroup(ctx, AccessGroup{ID: "grp_xg_other", Name: "other", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	relayExec(t, s, `INSERT INTO user (id, name, group_id, all_nodes, period_start, sub_token_hash, sub_token_enc, created_at)
		VALUES ('usr_xg2', 'xg2', 'grp_xg_other', 1, ?, ?, ?, ?)`, now.Unix(), []byte("xg2-sub"), []byte{1}, now.Unix())
	relayExec(t, s, `INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at) VALUES ('dev_xg2', 'usr_xg2', ?, 0, 0, ?)`, []byte("xg2-hwid"), now.Unix())
	relayExec(t, s, `INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at, profile_id, config_epoch)
		VALUES ('crd_xg2', 'dev_xg2', 'usr_xg2', 'awg', ?, '{}', ?, 'prf_xg', 0)`, []byte{1}, now.Unix())
	ingest := func(seq uint64) FleetStatsOut {
		t.Helper()
		out, err := s.IngestStats(ctx, FleetStatsIn{NodeID: f.nodeID, Instance: "instance-a", Seq: seq, Now: now, HourStart: now.Unix() / 3600 * 3600,
			Traffic: []FleetTraffic{{CredID: "crd_xg2", InboundID: f.inboundID, Up: 100, Down: 50}}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := ingest(1); out.OffNode != 1 {
		t.Fatalf("traffic of a person with no group of the node: %+v, want it off the node", out)
	}
	u, err := s.Access().User(ctx, "usr_xg2")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Access().UpdateUser(ctx, u, false, AccessGroupChange{Add: []string{"grp_xg"}}); err != nil {
		t.Fatal(err)
	}
	if out := ingest(2); out.OffNode != 0 || out.Skipped != 0 {
		t.Fatalf("traffic of a person with the group as an extra group: %+v, want it booked", out)
	}
}
