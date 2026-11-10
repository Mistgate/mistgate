//go:build !js || !wasm

package fleet

import "time"

func (f *Fleet) lockStatsBudget(state *SessionState, now time.Time) statsBudgetLease {
	f.statsBudgetMapMu.Lock()
	if f.statsBudgets == nil {
		f.statsBudgets = make(map[string]*statsBudgetEntry)
	}
	entry := f.statsBudgets[state.NodeID]
	if entry == nil {
		entry = &statsBudgetEntry{}
		f.statsBudgets[state.NodeID] = entry
	}
	f.statsBudgetMapMu.Unlock()

	entry.mu.Lock()
	entry.bucket = refillStatsBudget(entry.bucket, now)
	setStatsBudgetState(state, entry.bucket)
	return statsBudgetLease{
		left: entry.bucket.left,
		spend: func(span time.Duration) {
			entry.bucket.left = max(time.Duration(0), entry.bucket.left-min(span, entry.bucket.left))
			setStatsBudgetState(state, entry.bucket)
		},
		release: entry.mu.Unlock,
	}
}

// forgetStatsBudget does not wait for the entry's own lock: a lease holds it while the batch is written to the
// database, and waiting here with the map locked would stop the stats of every other node for that long. A lease in
// flight finishes on the forgotten entry.
func (f *Fleet) forgetStatsBudget(nodeID string) {
	f.statsBudgetMapMu.Lock()
	delete(f.statsBudgets, nodeID)
	f.statsBudgetMapMu.Unlock()
}
