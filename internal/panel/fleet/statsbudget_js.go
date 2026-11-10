//go:build js && wasm

package fleet

import "time"

func (f *Fleet) lockStatsBudget(state *SessionState, now time.Time) statsBudgetLease {
	return sessionStatsBudgetLease(state, now)
}

func (f *Fleet) forgetStatsBudget(string) {}
