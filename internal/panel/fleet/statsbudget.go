package fleet

import (
	"sync"
	"time"
)

// statsBudgetCap covers the agent's whole outbox: 2,160 pending batches, each of its 10-second interval plus the time
// the agent took to collect it (its timer is armed again only after a batch is built, so the batches of a full outbox
// span a little more than six hours). With exactly six hours the last batches of a replay after a long outage were
// dropped as "over the limit".
const statsBudgetCap = 7 * time.Hour

// retireStatsBudget is what a node may still report between its retirement and the end of its session: one normal
// interval, once, for the whole window.
const retireStatsBudget = 10 * time.Second

type statsBudgetBucket struct {
	left time.Duration
	at   time.Time
}

type statsBudgetEntry struct {
	mu     sync.Mutex
	bucket statsBudgetBucket
}

type statsBudgetLease struct {
	left    time.Duration
	spend   func(time.Duration)
	release func()
}

// sessionStatsBudgetLease keeps the bucket in the session's own state. The Cloudflare edition has no Fleet memory
// that outlives a request, so its bucket always lives there; on a server it is the bucket of a retiring node, whose
// entry in the Fleet's map is already forgotten (a new one would start full) and whose state was given
// retireStatsBudget when the retirement was sent. Every accepted batch spends from it: a lease that could not be
// spent let a retiring node claim an interval with every frame.
func sessionStatsBudgetLease(state *SessionState, now time.Time) statsBudgetLease {
	bucket := statsBudgetFromState(state, now)
	setStatsBudgetState(state, bucket)
	return statsBudgetLease{
		left: bucket.left,
		spend: func(span time.Duration) {
			bucket.left = max(time.Duration(0), bucket.left-min(span, bucket.left))
			setStatsBudgetState(state, bucket)
		},
		release: func() {},
	}
}

func refillStatsBudget(bucket statsBudgetBucket, now time.Time) statsBudgetBucket {
	if bucket.at.IsZero() {
		return statsBudgetBucket{left: statsBudgetCap, at: now}
	}
	bucket.left = min(max(bucket.left, time.Duration(0)), statsBudgetCap)
	if elapsed := now.Sub(bucket.at); elapsed > 0 {
		if elapsed >= statsBudgetCap-bucket.left {
			bucket.left = statsBudgetCap
		} else {
			bucket.left += elapsed
		}
		// Only forward: a frame stamped earlier that takes the lock later (a second session of the node) must not
		// move the mark back, or the same seconds would be credited twice.
		bucket.at = now
	}
	return bucket
}

func setStatsBudgetState(state *SessionState, bucket statsBudgetBucket) {
	state.StatsBudgetLeftNanos = int64(bucket.left)
	state.StatsBudgetAt = bucket.at
}

func statsBudgetFromState(state *SessionState, now time.Time) statsBudgetBucket {
	if state.StatsBudgetAt.IsZero() {
		return refillStatsBudget(statsBudgetBucket{}, now)
	}
	return refillStatsBudget(statsBudgetBucket{left: time.Duration(state.StatsBudgetLeftNanos), at: state.StatsBudgetAt}, now)
}
