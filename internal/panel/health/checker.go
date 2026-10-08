package health

import (
	"context"
	"hash/fnv"
	"math/rand/v2"
	"sync"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The synthetic checker: every inbound of an ONLINE node in state ACTIVE is dialled as a client and
// probed through the tunnel, every interval (300 s, jittered, each inbound with its own stable phase), every 60 s
// while it fails, plus on demand. A round is one attempt and, if it failed, one retry; the round's last
// outcome is what is stored. Skips (node offline, inbound not active, no client) are not failures and leave the
// fail streak alone.

const (
	jitter        = 10 * time.Second
	manualMinGap  = 30 * time.Second // RunChecksNow: at most one round per inbound per 30 s
	firstRunSpan  = 30 * time.Second // a new target is first probed within this, spread
	recentSamples = 30               // rounds read back to rebuild a fail streak after a restart
)

// cell is the state of one inbound: its last round and how many rounds in a row failed.
type cell struct {
	last   *Result // nil before the first round
	streak int
}

// schedule is when an inbound is probed next.
type schedule struct {
	next      time.Time
	lastStart time.Time
	queued    bool
	running   bool
}

// cellOf returns the cell of an inbound, rebuilt from the stored rounds the first time it is seen (a panel
// restart must not forget a streak that was about to open an alert).
func (s *Service) cellOf(ctx context.Context, inboundID string) cell {
	s.cellMu.Lock()
	defer s.cellMu.Unlock()
	c, ok := s.cells[inboundID]
	if !ok {
		c = &cell{}
		if rows, err := s.st.RecentSamples(ctx, inboundID, recentSamples); err == nil && len(rows) > 0 {
			r := sampleResult(rows[0])
			c.last = &r
			for _, x := range rows {
				if x.Status != int(adminv1.CheckStatus_CHECK_STATUS_FAILED) {
					break
				}
				c.streak++
			}
		}
		s.cells[inboundID] = c
	}
	return *c
}

func sampleResult(x store.CheckSample) Result {
	return Result{Status: adminv1.CheckStatus(x.Status), At: x.At, LatencyMS: x.LatencyMS, ExitIP: x.ExitIP, ExitCountry: x.ExitCountry,
		ErrorCode: x.ErrorCode, ErrorDetail: x.ErrorDetail}
}

// record stores a finished round and updates the cell.
func (s *Service) record(ctx context.Context, inboundID string, res Result) {
	s.cellOf(ctx, inboundID) // load before the new round is stored, or it would be counted twice
	if err := s.st.InsertSample(ctx, res.sample(inboundID)); err != nil && err != store.ErrNotFound {
		s.log.Warn("health: store check round", "inbound", inboundID, "err", err)
	}
	s.cellMu.Lock()
	c := s.cells[inboundID]
	c.last = &res
	if res.failed() {
		c.streak++
	} else {
		c.streak = 0
	}
	s.cellMu.Unlock()
}

// phase is the stable offset of an inbound inside the interval, so that the fleet is spread evenly.
func phase(inboundID string, interval time.Duration) time.Duration {
	h := fnv.New64a()
	h.Write([]byte(inboundID))
	return time.Duration(h.Sum64()%uint64(interval/time.Millisecond)) * time.Millisecond
}

// nextRun is the first moment after now that is on the inbound's phase, plus jitter of +-10 s (at most a tenth of
// the interval). rnd in [0,1).
func nextRun(now time.Time, inboundID string, interval time.Duration, rnd float64) time.Time {
	j := min(jitter, interval/10)
	base := now.Truncate(interval).Add(phase(inboundID, interval))
	for !base.After(now.Add(j)) {
		base = base.Add(interval)
	}
	return base.Add(time.Duration((rnd*2 - 1) * float64(j)))
}

func (s *Service) runChecker(ctx context.Context) {
	var wg sync.WaitGroup
	for range s.cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-s.work:
					s.runRound(ctx, id)
				}
			}
		}()
	}
	tick := time.NewTicker(s.cfg.Tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-tick.C:
			s.scheduleDue(ctx)
		}
	}
}

// scheduleDue queues the inbounds whose time has come and forgets those that are not probed now.
func (s *Service) scheduleDue(ctx context.Context) {
	sn, err := s.snapshot(ctx)
	if err != nil {
		s.log.Warn("health: list probe targets", "err", err)
		return
	}
	liveRows, err := s.fl.Live(ctx)
	if err != nil {
		s.log.Warn("health: read live nodes", "err", err)
		return
	}
	live := indexLiveRows(liveRows)
	now := s.now()
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	for id := range s.sched {
		if t := sn.targets[id]; t == nil || s.skipReason(t, live[t.node.ID]) != "" {
			delete(s.sched, id) // gone, or not probed now: it gets a fresh first run when it is probed again
		}
	}
	for id, t := range sn.targets {
		if s.skipReason(t, live[t.node.ID]) != "" {
			continue
		}
		sc := s.sched[id]
		if sc == nil {
			sc = &schedule{next: now.Add(time.Duration(rand.Float64() * float64(min(firstRunSpan, s.interval(ctx)))))}
			s.sched[id] = sc
		}
		if !sc.running && !sc.queued && !now.Before(sc.next) {
			s.enqueueLocked(id, sc)
		}
	}
}

func (s *Service) enqueueLocked(id string, sc *schedule) bool {
	select {
	case s.work <- id:
		sc.queued = true
		return true
	default:
		return false // the queue is full: the next tick tries again
	}
}

// runRound probes one inbound (attempt, and a retry after a failure) and records the outcome.
func (s *Service) runRound(ctx context.Context, id string) {
	s.schedMu.Lock()
	sc := s.sched[id]
	if sc == nil { // the target was dropped between queueing and now
		s.schedMu.Unlock()
		return
	}
	sc.queued, sc.running, sc.lastStart = false, true, s.now()
	s.schedMu.Unlock()

	res, ok := s.round(ctx, id)

	interval := s.interval(ctx)
	s.schedMu.Lock()
	sc.running = false
	switch {
	case !ok:
		sc.next = s.now().Add(interval) // skipped: look again at the normal pace
	case res.failed():
		sc.next = s.now().Add(s.cfg.FailedEvery) // fast confirm and fast recovery
	default:
		sc.next = nextRun(s.now(), id, interval, rand.Float64())
	}
	s.schedMu.Unlock()
	if ok {
		s.evaluateSoon()
	}
}

// round does the attempt and its retry. ok is false when nothing was measured (skipped).
func (s *Service) round(ctx context.Context, id string) (Result, bool) {
	s.invalidateSnapshot() // the node and the inbound must be as they are now
	sn, err := s.snapshot(ctx)
	if err != nil {
		return Result{}, false
	}
	t := sn.targets[id]
	if t == nil {
		return Result{}, false
	}
	live, err := s.liveNode(ctx, t.node.ID)
	if err != nil || s.skipReason(t, live) != "" {
		return Result{}, false
	}
	res, skipped := s.attempt(ctx, t)
	if skipped {
		return Result{}, false
	}
	if res.failed() {
		select {
		case <-ctx.Done():
			return Result{}, false
		case <-time.After(s.cfg.RetryDelay):
		}
		live, err = s.liveNode(ctx, t.node.ID)
		if err != nil || s.skipReason(t, live) != "" { // the node went away while we waited: not a failure of the inbound
			return Result{}, false
		}
		if again, skipped := s.attempt(ctx, t); !skipped {
			res = again
		}
	}
	s.record(ctx, id, res)
	return res, true
}

// RunChecksNow schedules an immediate round for every inbound of a node (or of the fleet) that can be probed.
// Rounds that are running or started less than 30 s ago are refused: retry is the seconds until the soonest
// of them may run again (0 when nothing was refused for that reason).
func (s *Service) RunChecksNow(ctx context.Context, nodeID string) (scheduled, skipped int, retry time.Duration, err error) {
	sn, err := s.snapshot(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	var live map[string]store.NodeLiveRow
	if nodeID == "" {
		liveRows, err := s.fl.Live(ctx)
		if err != nil {
			return 0, 0, 0, err
		}
		live = indexLiveRows(liveRows)
	} else {
		row, err := s.liveNode(ctx, nodeID)
		if err != nil {
			return 0, 0, 0, err
		}
		live = map[string]store.NodeLiveRow{nodeID: row}
	}
	now := s.now()
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	for _, t := range sn.targets {
		if nodeID != "" && t.node.ID != nodeID {
			continue
		}
		if s.skipReason(t, live[t.node.ID]) != "" {
			skipped++
			continue
		}
		sc := s.sched[t.in.ID]
		if sc == nil {
			sc = &schedule{}
			s.sched[t.in.ID] = sc
		}
		if wait := sc.lastStart.Add(manualMinGap).Sub(now); sc.running || sc.queued || wait > 0 {
			skipped++
			if r := max(wait, time.Second); retry == 0 || r < retry {
				retry = r
			}
			continue
		}
		if s.enqueueLocked(t.in.ID, sc) {
			scheduled++
		} else {
			skipped++
		}
	}
	return scheduled, skipped, retry, nil
}
