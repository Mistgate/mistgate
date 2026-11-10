//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestAssistantBudgetCleanupDeletesSettledRowsFromEarlierMonths(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	currentMonth := now.Format("2006-01")
	previousMonth := now.AddDate(0, -1, 0).Format("2006-01")
	for _, month := range []struct{ id, key string }{
		{"asres_previous", previousMonth},
		{"asres_current", currentMonth},
	} {
		if err := st.ReconcileAssistantBudget(ctx, month.id, month.key, "adm_budget", 1, 10, 5, .01, true, now); err != nil {
			t.Fatalf("settle %s reservation: %v", month.key, err)
		}
	}
	deleted, err := st.DeleteStaleAssistantBudgetReservations(ctx, now.Add(-time.Minute), now)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted %d reservations, err %v; want the earlier-month marker only", deleted, err)
	}
	for _, month := range []struct {
		key  string
		want int
	}{{previousMonth, 0}, {currentMonth, 1}} {
		var count int
		if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM assistant_budget_reservation WHERE month=?`, month.key).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != month.want {
			t.Errorf("month %s has %d reservation rows, want %d", month.key, count, month.want)
		}
	}
}
