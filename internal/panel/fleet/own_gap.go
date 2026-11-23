package fleet

import (
	"sync"
	"time"
)

const (
	ownGapNodesWindow = time.Minute      // two different nodes must have been heard this recently before a silence
	ownGapKeep        = 15 * time.Minute // older gaps are dropped
	ownGapMax         = 8
	ownGapSlack       = 15 * time.Second // threshold = keepalive interval + this
)

type ownGapSpan struct {
	from, to time.Time
	reported bool // the one panel_link_gap event of this span is written
}

// ownGap notices that the panel itself heard no agent for longer than a keepalive can explain (its host lost the network,
// or froze). The agents it then sees coming back were never down: connectEvents must not blame them.
// It lives in memory only and is fed by VPS sessions; the edge edition's isolates share no memory, so there it stays empty
// and the old per-node behaviour stays too.
type ownGap struct {
	mu    sync.Mutex
	seen  map[string]time.Time // node id -> its last frame
	last  time.Time            // the last frame of any node
	spans []*ownGapSpan
}

// heard records a frame (or a Hello) of a node at now; ka is the keepalive interval the panel tells agents.
func (g *ownGap) heard(id string, now time.Time, ka time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.last.IsZero() && now.Sub(g.last) >= ka+ownGapSlack {
		n := 0
		for _, t := range g.seen {
			if !t.Before(g.last.Add(-ownGapNodesWindow)) {
				n++
			}
		}
		if n >= 2 {
			g.spans = append(g.spans, &ownGapSpan{from: g.last, to: now})
		}
	}
	if g.seen == nil {
		g.seen = map[string]time.Time{}
	}
	g.seen[id] = now
	if now.After(g.last) {
		g.last = now
	}
	for k, t := range g.seen {
		if t.Before(now.Add(-ownGapNodesWindow)) {
			delete(g.seen, k)
		}
	}
	g.spans = g.spans[max(0, len(g.spans)-ownGapMax):]
	for len(g.spans) > 0 && g.spans[0].to.Before(now.Add(-ownGapKeep)) {
		g.spans = g.spans[1:]
	}
}

// gapAt says whether a node whose last contact was at last went quiet inside a gap of the panel's own: last is not before the
// gap's start by more than a keepalive interval plus 5 s, and not after its end. first is true for the first node asking
// about this gap (it writes the event); seconds is the length of the silence.
func (g *ownGap) gapAt(last time.Time, ka time.Duration) (seconds int, hit, first bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.spans {
		if !last.Before(s.from.Add(-(ka + 5*time.Second))) && !last.After(s.to) {
			first, s.reported = !s.reported, true
			return int(s.to.Sub(s.from).Seconds()), true, first
		}
	}
	return 0, false, false
}

// keepaliveEvery is the keepalive interval the panel tells agents (nodeSettings).
func (f *Fleet) keepaliveEvery() time.Duration { return defaultKeepaliveInterval * f.unit }
