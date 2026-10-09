//go:build !js

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

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
