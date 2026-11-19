//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAccessUserSortQueryPlans(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	q := AccessUserQuery{Limit: 50,
		Now:               time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		DeviceOnlineAfter: time.Date(2026, 10, 1, 11, 57, 0, 0, time.UTC)}
	for _, sort := range []AccessUserSort{
		AccessUserSortName, AccessUserSortTraffic, AccessUserSortExpiry, AccessUserSortLastSeen,
		AccessUserSortDevices, AccessUserSortGroup, AccessUserSortStatus,
	} {
		t.Run(fmt.Sprintf("sort_%d", sort), func(t *testing.T) {
			q.Sort = sort
			query, args := accUserListQuery(q, "1 = 1", nil, "1 = 1", nil, accJSON(nil))
			rows, err := st.R.QueryContext(ctx, `EXPLAIN QUERY PLAN `+query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(details, "\n")
			t.Log(plan)
			if sort == AccessUserSortDevices || sort == AccessUserSortStatus {
				if !strings.Contains(plan, "SCAN c USING INDEX device_credential_live") || !strings.Contains(plan, "SEARCH d USING INDEX") ||
					!strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
					t.Fatalf("device-derived sort plan changed unexpectedly:\n%s", plan)
				}
			}
		})
	}
}

func TestResetUserPeriodDoesNotOverwriteANewerPeriod(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	oldPeriod := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	currentPeriod := oldPeriod.AddDate(0, 1, 0)
	stalePeriod := currentPeriod.AddDate(0, 1, 0)
	groupID, userID := NewID("grp_"), NewID("usr_")
	if err := st.Access().CreateGroup(ctx, AccessGroup{ID: groupID, Name: "alice", CreatedAt: oldPeriod}); err != nil {
		t.Fatal(err)
	}
	user := AccessUser{
		ID: userID, Name: "alice", GroupID: groupID, Status: "active", AppHapp: true, AppAmnezia: true,
		AllNodes: true, QuotaReset: "month", PeriodStart: oldPeriod, DeviceLimit: 5,
		SubTokenHash: []byte("alice-token-hash"), SubTokenEnc: []byte("sealed"), CreatedAt: oldPeriod,
	}
	if err := st.Access().CreateUser(ctx, user, AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}

	a := st.Access()
	if err := a.ResetUserPeriod(ctx, userID, oldPeriod, currentPeriod, "active"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddUsage(ctx, userID, 123); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetUserPeriod(ctx, userID, oldPeriod, stalePeriod, "expired"); err != nil {
		t.Fatal(err)
	}

	var periodStart, usedBytes int64
	var status string
	if err := st.R.QueryRowContext(ctx, `SELECT period_start, used_bytes, status FROM user WHERE id = ?`, userID).
		Scan(&periodStart, &usedBytes, &status); err != nil {
		t.Fatal(err)
	}
	if periodStart != currentPeriod.Unix() || usedBytes != 123 || status != "active" {
		t.Fatalf("stale reset left period=%d usage=%d status=%q; want period=%d usage=123 status=active",
			periodStart, usedBytes, status, currentPeriod.Unix())
	}
}

func TestAdminTrafficResetDoesNotDependOnStalePeriodStart(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	oldPeriod := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	currentPeriod := oldPeriod.AddDate(0, 1, 0)
	groupID, userID := NewID("grp_"), NewID("usr_")
	if err := st.Access().CreateGroup(ctx, AccessGroup{ID: groupID, Name: "alice", CreatedAt: oldPeriod}); err != nil {
		t.Fatal(err)
	}
	user := AccessUser{
		ID: userID, Name: "alice", GroupID: groupID, Status: "active", AppHapp: true, AppAmnezia: true,
		AllNodes: true, QuotaReset: "month", PeriodStart: oldPeriod, DeviceLimit: 5,
		SubTokenHash: []byte("alice-token-hash"), SubTokenEnc: []byte("sealed"), CreatedAt: oldPeriod,
	}
	if err := st.Access().CreateUser(ctx, user, AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
	a := st.Access()
	if err := a.AddUsage(ctx, userID, 123); err != nil {
		t.Fatal(err)
	}
	// Simulate UpdateUser moving the period after the admin read the user's old period start.
	if err := a.ResetUserPeriod(ctx, userID, oldPeriod, currentPeriod, "expired"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddUsage(ctx, userID, 321); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetUserTraffic(ctx, userID, "active"); err != nil {
		t.Fatal(err)
	}

	var periodStart, usedBytes int64
	var status string
	if err := st.R.QueryRowContext(ctx, `SELECT period_start, used_bytes, status FROM user WHERE id = ?`, userID).
		Scan(&periodStart, &usedBytes, &status); err != nil {
		t.Fatal(err)
	}
	if periodStart != currentPeriod.Unix() || usedBytes != 0 || status != "active" {
		t.Fatalf("admin reset left period=%d usage=%d status=%q; want period=%d usage=0 status=active",
			periodStart, usedBytes, status, currentPeriod.Unix())
	}
}

func TestAccessStatusWritesCannotReenableDisabledUser(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	groupID, userID := NewID("grp_"), NewID("usr_")
	if err := st.Access().CreateGroup(ctx, AccessGroup{ID: groupID, Name: "alice", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateUser(ctx, AccessUser{
		ID: userID, Name: "alice", GroupID: groupID, Status: "active", AppHapp: true, AppAmnezia: true,
		AllNodes: true, QuotaReset: "month", PeriodStart: now, DeviceLimit: 5,
		SubTokenHash: []byte("alice-token-hash"), SubTokenEnc: []byte("sealed"), CreatedAt: now,
	}, AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
	a := st.Access()
	if err := a.SetUsersDisabled(ctx, []string{userID}, true); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetUserTraffic(ctx, userID, "active"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserStatus(ctx, userID, "active"); err != nil {
		t.Fatal(err)
	}
	var disabled int
	var status string
	if err := st.R.QueryRowContext(ctx, `SELECT disabled, status FROM user WHERE id = ?`, userID).Scan(&disabled, &status); err != nil {
		t.Fatal(err)
	}
	if disabled != 1 || status != "disabled" {
		t.Fatalf("stale status writes left disabled=%d status=%q; want disabled=1 status=disabled", disabled, status)
	}
}

// A write of a person made from an old read is refused whatever it changed: the admin who switched the person off, extended
// the term or started the period meanwhile is not overwritten with the values that were read. What the fleet and the sweep
// write (usage, status, last seen) is no reason to refuse.
func TestUpdateUserRefusesAStaleRead(t *testing.T) {
	ctx := context.Background()
	world := func(t *testing.T) (*Store, func() AccessUser) {
		s := openTemp(t)
		rewriteAccessGroup(t, s, "grp_stale", "Stale")
		rewriteAccessGroup(t, s, "grp_other", "Other")
		rewriteAccessUser(t, s, "usr_stale", "Alice", "grp_stale")
		return s, func() AccessUser {
			t.Helper()
			u, err := s.Access().User(ctx, "usr_stale")
			if err != nil {
				t.Fatal(err)
			}
			return u
		}
	}
	renamed := func(u AccessUser, name string) AccessUser { u.Name = name; return u }
	edit := func(a Access, read func() AccessUser, change func(*AccessUser)) error {
		u := read()
		was := u
		change(&u)
		return a.UpdateUser(ctx, u, was, false, AccessGroupChange{})
	}

	for name, intervene := range map[string]func(a Access, read func() AccessUser) error{
		"switched off": func(a Access, _ func() AccessUser) error { return a.SetUsersDisabled(ctx, []string{"usr_stale"}, true) },
		"extended": func(a Access, _ func() AccessUser) error {
			return a.SetUserExpiry(ctx, "usr_stale", time.Time{}, t0.Add(24*time.Hour))
		},
		"new period": func(a Access, _ func() AccessUser) error {
			return a.ResetUserPeriod(ctx, "usr_stale", t0, t0.AddDate(0, 1, 0), "active")
		},
		"another edit": func(a Access, read func() AccessUser) error {
			return edit(a, read, func(u *AccessUser) { u.DeviceLimit = 9 })
		},
		"group moved": func(a Access, read func() AccessUser) error {
			return edit(a, read, func(u *AccessUser) { u.GroupID = "grp_other" })
		},
		"a letter of the name": func(a Access, read func() AccessUser) error {
			return edit(a, read, func(u *AccessUser) { u.Name = "ALICE" })
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, read := world(t)
			a := s.Access()
			was := read()
			if err := intervene(a, read); err != nil {
				t.Fatal(err)
			}
			after := read()
			err := a.UpdateUser(ctx, renamed(was, "Alice2"), was, false, AccessGroupChange{Add: []string{"grp_other"}})
			if !errors.Is(err, ErrAccessVersion) {
				t.Fatalf("write over an old read = %v, want ErrAccessVersion", err)
			}
			if got := read(); !reflect.DeepEqual(got, after) { // the extra group of the refused write is not among its changes either
				t.Fatalf("the refused write changed the person:\n got %+v\nwant %+v", got, after)
			}
			if err := a.UpdateUser(ctx, renamed(after, "Alice2"), after, false, AccessGroupChange{}); err != nil {
				t.Fatalf("the same edit on a fresh read: %v", err)
			}
		})
	}

	t.Run("what the fleet writes is not compared", func(t *testing.T) {
		s, read := world(t)
		a := s.Access()
		was := read()
		if err := a.AddUsage(ctx, "usr_stale", 500); err != nil {
			t.Fatal(err)
		}
		if err := a.SetUserStatus(ctx, "usr_stale", "limited"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.W.ExecContext(ctx, `UPDATE user SET last_seen_at = 99 WHERE id = 'usr_stale'`); err != nil {
			t.Fatal(err)
		}
		if err := a.UpdateUser(ctx, renamed(was, "Alice3"), was, false, AccessGroupChange{}); err != nil {
			t.Fatalf("usage, status or last seen refused an edit: %v", err)
		}
		if got := read(); got.Name != "Alice3" || got.UsedBytes != 500 || got.LastSeenAt.Unix() != 99 {
			t.Fatalf("the edit lost what the fleet wrote: %+v", got)
		}
	})

	t.Run("a node list changed meanwhile", func(t *testing.T) {
		s, read := world(t)
		a := s.Access()
		for _, id := range []string{"nod_a", "nod_b"} {
			if _, err := s.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)`, id, id, id+".example.com"); err != nil {
				t.Fatal(err)
			}
		}
		was := read()
		first := was
		first.AllNodes, first.NodeIDs = false, []string{"nod_a"}
		if err := a.UpdateUser(ctx, first, was, true, AccessGroupChange{}); err != nil {
			t.Fatal(err)
		}
		second := was // the same old read, another list (all_nodes is a column of the row, so it differs too)
		second.NodeIDs = []string{"nod_b"}
		if err := a.UpdateUser(ctx, second, was, true, AccessGroupChange{}); !errors.Is(err, ErrAccessVersion) {
			t.Fatalf("a list written over a changed list = %v, want ErrAccessVersion", err)
		}
		if got := read(); !reflect.DeepEqual(got.NodeIDs, []string{"nod_a"}) {
			t.Fatalf("nodes = %v, want the list of the first write", got.NodeIDs)
		}
		// the list alone: only the rows differ (an admin who saved the same switch and another list)
		if _, err := s.W.ExecContext(ctx, `INSERT INTO user_node (user_id, node_id) VALUES ('usr_stale', 'nod_b')`); err != nil {
			t.Fatal(err)
		}
		if err := a.UpdateUser(ctx, first, first, true, AccessGroupChange{}); !errors.Is(err, ErrAccessVersion) {
			t.Fatalf("a list written over one that grew = %v, want ErrAccessVersion", err)
		}
		fresh := read()
		fresh.NodeIDs = []string{"nod_b"}
		if err := a.UpdateUser(ctx, fresh, read(), true, AccessGroupChange{}); err != nil {
			t.Fatalf("a list on a fresh read: %v", err)
		}
		if got := read(); !reflect.DeepEqual(got.NodeIDs, []string{"nod_b"}) {
			t.Fatalf("nodes = %v", got.NodeIDs)
		}
	})

	t.Run("a person who is gone", func(t *testing.T) {
		s, read := world(t)
		gone := read()
		gone.ID = "usr_nope"
		if err := s.Access().UpdateUser(ctx, gone, gone, false, AccessGroupChange{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing person = %v, want ErrNotFound", err)
		}
	})
}
