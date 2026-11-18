//go:build !js

package store

import (
	"context"
	"strings"
	"testing"
)

func queryPlan(t *testing.T, st *Store, query string, args ...any) string {
	t.Helper()
	rows, err := st.R.QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+query, args...)
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
	return strings.Join(details, "\n")
}

// The desired state of a node is read for every node on every change, so its plan must start from the inbounds of the node and
// the people of the groups that hold their profiles, and reach the credentials through the person, never walk them all (the
// panel has no ANALYZE statistics, so the plan is the one the planner picks without them).
func TestDesiredCredsQueryPlan(t *testing.T) {
	st := openTemp(t)
	plan := queryPlan(t, st, accDesiredCredsSQL, "nod_de1")
	t.Log(plan)
	for _, want := range []string{"MATERIALIZE granted", "SEARCH i USING INDEX inbound_node", "SEARCH u USING INDEX user_by_group",
		"SEARCH c USING INDEX device_credential_user"} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan of DesiredCreds lacks %q:\n%s", want, plan)
		}
	}
	for _, bad := range []string{"SCAN c", "SCAN device_credential", "SCAN u", "SCAN user", "device_credential_protocol", "AUTOMATIC"} {
		if strings.Contains(plan, bad) {
			t.Errorf("the plan of DesiredCreds has %q:\n%s", bad, plan)
		}
	}
}
