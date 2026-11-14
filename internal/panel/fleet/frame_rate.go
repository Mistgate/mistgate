package fleet

import "time"

// What an honest agent sends per session, by the agent's code: a stats batch every 10 s (0.1 frames/s), reliable events
// (a few a minute at worst), an ApplyResult per applied state, a CommandResult per command or check, a DoctorReport per
// run, OsStatus on change, UpdateProgress at most every 5 s while downloading, LogChunks (at most 2/s per follow stream, 10
// frames for a 1,000-line tail) and the chunks of a debug capture (20,000 lines in 64-line chunks: 313 frames in one burst).
// After an outage the reliable outbox replays at once: up to 2,160 frames, plus the events kept beyond that count.
const (
	agentFrameBurst        = 2600                   // 2,160 replayed + a debug capture (313) + control frames, with room to spare
	agentFrameRefillEvery  = 200 * time.Millisecond // 5 frames/s: 50 times the honest steady state, two follow streams of a log
	agentFrameDropWindow   = time.Minute
	agentFrameDropsToClose = 100
)

type agentFrameBucket struct {
	tokens          int
	updated         time.Time
	dropWindowStart time.Time
	dropsInWindow   int
	totalDrops      uint64
	lastWarning     time.Time
}

func (b *agentFrameBucket) allow(now time.Time) bool {
	if b.updated.IsZero() {
		b.tokens, b.updated = agentFrameBurst, now
	} else if now.After(b.updated) {
		refill := int(now.Sub(b.updated) / agentFrameRefillEvery)
		if refill > 0 {
			b.tokens = min(agentFrameBurst, b.tokens+refill)
			b.updated = b.updated.Add(time.Duration(refill) * agentFrameRefillEvery)
			if b.tokens == agentFrameBurst && b.updated.Before(now) {
				b.updated = now
			}
		}
	}
	if b.tokens == 0 {
		return false
	}
	b.tokens--
	return true
}

func (b *agentFrameBucket) drop(now time.Time) (closeSession, report bool) {
	if b.dropWindowStart.IsZero() || now.Sub(b.dropWindowStart) >= agentFrameDropWindow || now.Before(b.dropWindowStart) {
		b.dropWindowStart, b.dropsInWindow = now, 0
	}
	b.dropsInWindow++
	b.totalDrops++
	closeSession = b.dropsInWindow >= agentFrameDropsToClose
	if b.lastWarning.IsZero() || now.Sub(b.lastWarning) >= agentFrameDropWindow || closeSession {
		b.lastWarning = now
		report = true
	}
	return closeSession, report
}
