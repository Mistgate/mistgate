//go:build !js

package health

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// "Check now" of a node also asks the country check ("From Russia") for a round out of turn. What each source does with the request:
//   - Globalping measures this node once, but only from the tests that are left in the current window of its hourly limit after the
//     planned rounds that are still to come in that window have been set aside (nowReserve): a round by hand never eats the budget
//     of the schedule, and without that surplus Globalping is skipped ("no_budget") while the other sources go on. The credits that
//     pay for tests above the limit are never touched.
//   - The TSPU probe, cheburcheck (public and with the token) and the owner's own probes are asked for this node (Config.CountryNow).
//   - bsbord is paid and runs only on its schedule. The block lists and the kl!ck reports are read again from what the panel holds.
//
// A node is asked at most once in countryNowGap; the time of the last round, and what each source answered, is kept in a setting so
// it survives a restart. The work runs in the background (a round takes up to a few minutes) and the answer says at once which source
// runs and which does not. The rows it stores are marked "manual" and the schedule does not count them.

const (
	countryNowGap     = 10 * time.Minute
	countryNowSetting = "health.country_now"
	countryNowBound   = 6 * time.Minute // the background work of a round ends by then; a round that runs longer is a lost one
	countryNowKeep    = 7 * 24 * time.Hour
	sourceKlick       = "klick"
	nowLimitsWait     = 10 * time.Second
)

// countryNowOrder is the order of CountryNow.sources.
var countryNowOrder = []string{countryreach.SourceGlobalping, countryreach.SourceTSPU, countryreach.SourceCheburcheck, countryreach.SourceCheburcheckOwn,
	countryreach.SourceProbes, countryreach.SourceBsbord, countryreach.SourceLists, sourceKlick}

// CountryBudget is what a country source that counts its tests can say about the window of its limit.
type CountryBudget interface {
	// Remaining is the tests left in the current window and the time until it resets.
	Remaining(ctx context.Context) (left int, resetIn time.Duration, err error)
}

type nowEntry struct {
	Source string `json:"s"`
	State  string `json:"st"`
	Reason string `json:"r,omitempty"`
}

type nowRecord struct {
	At      int64      `json:"at"`
	Asked   int64      `json:"ak,omitempty"` // when the sources below were asked: later than At when nothing could be
	Sources []nowEntry `json:"src"`
	// Globalping's budget as it was read when the round was asked: the tests left for a round by hand, its cost, the time.
	Left int   `json:"gl,omitempty"`
	Read int64 `json:"ga,omitempty"`
	Cost int   `json:"gc,omitempty"`
}

func (s *Service) nowLoad(ctx context.Context) map[string]nowRecord {
	recs := map[string]nowRecord{}
	if v, err := s.st.Setting(ctx, countryNowSetting); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &recs)
	}
	return recs
}

func (s *Service) nowSave(ctx context.Context, recs map[string]nowRecord) {
	cut := s.now().Add(-countryNowKeep).Unix()
	for id, r := range recs {
		if r.At < cut && r.Read < cut {
			delete(recs, id)
		}
	}
	b, _ := json.Marshal(recs)
	if err := s.st.SetSettings(ctx, map[string]string{countryNowSetting: string(b)}); err != nil {
		s.log.Warn("health: keep the country round asked by hand", "err", err)
	}
}

// RunCountryNow asks the country check for a round of one node out of turn. It returns at once: the sources that took the request
// are "running" and finish in the background.
func (s *Service) RunCountryNow(ctx context.Context, nodeID string) *adminv1.CountryNow {
	s.nowAsk.Lock()
	defer s.nowAsk.Unlock()
	now := s.now()
	s.nowMu.Lock()
	recs := s.nowLoad(ctx)
	rec, known := recs[nodeID]
	s.nowMu.Unlock()
	if known && rec.At > 0 {
		if next := time.Unix(rec.At, 0).Add(countryNowGap); now.Before(next) {
			v := s.nowView(nodeID, rec, now)
			v.RetryAfterS = uint32((next.Sub(now) + time.Second - 1) / time.Second)
			v.Sources = nil
			for _, id := range countryNowOrder {
				v.Sources = append(v.Sources, &adminv1.CountryNowSource{Source: id, State: "skipped", Reason: "too_early"})
			}
			return v
		}
	}

	entries := map[string]nowEntry{}
	runs := map[string]CountryNowRun{}
	take := func(source string, run CountryNowRun, skip string) {
		switch {
		case run != nil:
			entries[source], runs[source] = nowEntry{Source: source, State: "running"}, run
		case skip == "reread":
			entries[source] = nowEntry{Source: source, State: "reread"}
		default:
			entries[source] = nowEntry{Source: source, State: "skipped", Reason: skip}
		}
	}
	next := nowRecord{At: now.Unix(), Asked: now.Unix()}
	run, skip, left, cost := s.nowGlobalping(ctx, nodeID, now)
	take(countryreach.SourceGlobalping, run, skip)
	if left >= 0 {
		next.Left, next.Read, next.Cost = left, now.Unix(), cost
	}
	for _, id := range []string{countryreach.SourceTSPU, countryreach.SourceCheburcheck, countryreach.SourceCheburcheckOwn, countryreach.SourceProbes} {
		i := slices.IndexFunc(s.cfg.CountryNow, func(c CountryNowSource) bool { return c.Source == id })
		if i < 0 || s.cfg.CountryNow[i].Plan == nil {
			take(id, nil, "off")
			continue
		}
		run, skip := s.cfg.CountryNow[i].Plan(ctx, nodeID)
		take(id, run, skip)
	}
	bsbord := "off"
	if s.cfg.External != nil {
		for _, st := range s.cfg.External(ctx) {
			if st.Source == countryreach.SourceBsbord && st.On {
				bsbord = "schedule_only"
			}
		}
	}
	take(countryreach.SourceBsbord, nil, bsbord)
	if s.cfg.Lists != nil && s.cfg.Lists(ctx) {
		take(countryreach.SourceLists, nil, "reread")
	} else {
		take(countryreach.SourceLists, nil, "off")
	}
	take(sourceKlick, nil, "reread")
	for _, id := range countryNowOrder {
		next.Sources = append(next.Sources, entries[id])
	}

	s.nowMu.Lock()
	recs = s.nowLoad(ctx)
	cur := recs[nodeID]
	if next.Read == 0 { // the budget of an earlier ask stays on the screen until Globalping is asked again
		next.Left, next.Read, next.Cost = cur.Left, cur.Read, cur.Cost
	}
	if len(runs) == 0 { // nothing was asked: it is no round, the time of the last one stays and the owner may ask again at once
		next.At = cur.At
	}
	recs[nodeID] = next
	s.nowSave(ctx, recs)
	if len(runs) > 0 {
		s.nowRunning[nodeID] = now
	}
	s.nowMu.Unlock()
	if len(runs) == 0 {
		return s.nowView(nodeID, next, now)
	}

	s.nowWG.Add(1)
	go s.nowWork(ctx, nodeID, next.At, runs)
	return s.nowView(nodeID, next, now)
}

// nowWork runs the sources that took the request and keeps what each one answered.
func (s *Service) nowWork(ctx context.Context, nodeID string, at int64, runs map[string]CountryNowRun) {
	defer s.nowWG.Done()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), countryNowBound)
	defer cancel()
	var wg sync.WaitGroup
	for source, run := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, reason := "failed", "error"
			func() {
				defer func() {
					if r := recover(); r != nil {
						s.log.Error("health: a source of the country round asked by hand panicked", "source", source, "panic", r)
					}
				}()
				state, reason = run(rctx)
			}()
			s.nowFinish(rctx, nodeID, at, nowEntry{Source: source, State: state, Reason: reason})
		}()
	}
	wg.Wait()
	s.nowMu.Lock()
	delete(s.nowRunning, nodeID)
	s.nowMu.Unlock()
	s.ReconcileCountry(rctx)
	s.evaluateSoon()
}

func (s *Service) nowFinish(ctx context.Context, nodeID string, at int64, e nowEntry) {
	s.nowMu.Lock()
	defer s.nowMu.Unlock()
	recs := s.nowLoad(ctx)
	rec, ok := recs[nodeID]
	if !ok || rec.At != at {
		return
	}
	for i := range rec.Sources {
		if rec.Sources[i].Source == e.Source {
			rec.Sources[i] = e
		}
	}
	recs[nodeID] = rec
	s.nowSave(ctx, recs)
}

func nowSources(in []nowEntry) []*adminv1.CountryNowSource {
	out := make([]*adminv1.CountryNowSource, len(in))
	for i, e := range in {
		out[i] = &adminv1.CountryNowSource{Source: e.Source, State: e.State, Reason: e.Reason}
	}
	return out
}

// nowView is the state of a node's round by hand. A source that is "running" in the record while no round runs in this process (the
// panel restarted, or the round ran past its bound) is a lost one: it reads "failed", "interrupted".
func (s *Service) nowView(nodeID string, rec nowRecord, now time.Time) *adminv1.CountryNow {
	s.nowMu.Lock()
	started, running := s.nowRunning[nodeID]
	s.nowMu.Unlock()
	running = running && now.Sub(started) < countryNowBound+time.Minute
	v := &adminv1.CountryNow{NodeId: nodeID, Running: running, StartedUnix: rec.At, AskedUnix: max(rec.Asked, rec.At)}
	if rec.At > 0 {
		if next := rec.At + int64(countryNowGap/time.Second); next > now.Unix() {
			v.NextUnix = next
		}
	}
	v.Sources = nowSources(rec.Sources)
	if !running {
		for _, src := range v.Sources {
			if src.State == "running" {
				src.State, src.Reason = "failed", "interrupted"
			}
		}
	}
	if rec.Read > 0 {
		l := uint32(max(rec.Left, 0))
		v.GlobalpingLeft, v.GlobalpingCost, v.GlobalpingAtUnix = &l, uint32(rec.Cost), rec.Read
	}
	return v
}

// countryNowViews are the rounds asked by hand that GetChecks tells of: every node with one, or the node asked for.
func (s *Service) countryNowViews(ctx context.Context, nodeID string) []*adminv1.CountryNow {
	s.nowMu.Lock()
	recs := s.nowLoad(ctx)
	s.nowMu.Unlock()
	now := s.now()
	var out []*adminv1.CountryNow
	for _, id := range slices.Sorted(maps.Keys(recs)) {
		if nodeID == "" || nodeID == id {
			out = append(out, s.nowView(id, recs[id], now))
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------------
// Globalping

// nowGlobalping decides whether Globalping may measure the node now: its work to run or the reason it does not, the tests left for a
// round by hand (-1 when that could not be read) and what the round costs.
func (s *Service) nowGlobalping(ctx context.Context, nodeID string, now time.Time) (run CountryNowRun, skip string, left, cost int) {
	left = -1
	cc, src, ok := s.countryConfig(ctx)
	if !ok {
		return nil, "off", left, 0
	}
	nodes, err := s.countryNodes(ctx)
	if err != nil {
		s.log.Warn("health: country round asked by hand: nodes", "err", err)
		return nil, "error", left, 0
	}
	i := slices.IndexFunc(nodes, func(n countryNode) bool { return n.node.ID == nodeID })
	if i < 0 {
		return nil, "node_offline", left, 0
	}
	target := nodes[i]
	if _, has := s.countryAddress(ctx, target.node); !has {
		return nil, "no_address", left, 0
	}
	if now.Before(s.countryHold(ctx)) {
		return nil, "rate_limited", left, 0
	}
	bud, ok := src.(CountryBudget)
	if !ok {
		return nil, "budget_unknown", left, 0
	}
	if cc.IPv6 {
		s.countryIPv6Facts(ctx, nodes)
		s.countryIPv6Members(ctx, cc.Country, nodes)
	}
	probes, every, _ := countryPlan(nodes, censorSimCountryInterval(cc.Interval), censorSimCountryHourlyLimit(src.HourlyLimit()))
	lctx, cancel := context.WithTimeout(ctx, nowLimitsWait)
	remaining, resetIn, err := bud.Remaining(lctx)
	cancel()
	if err != nil {
		s.log.Warn("health: country round asked by hand: Globalping limits", "err", err)
		return nil, "budget_unknown", left, 0
	}
	solo := []countryNode{target}
	solo[0].ipv6Member = false
	// a round may measure its control twice (the fallback of a round with too few home or mobile probes)
	cost = countryCost(solo, probes) + probes
	left = max(remaining-s.nowReserve(ctx, cc, nodes, probes, every, now, resetIn), 0)
	if left < cost {
		return nil, "no_budget", left, cost
	}
	if !s.countryMu.TryLock() {
		return nil, "busy", left, cost
	}
	wants := s.countryRoundWants(ctx, cc.Country)
	return func(ctx context.Context) (string, string) {
		defer s.countryMu.Unlock()
		if reason := s.countryRound(ctx, cc, src, solo, probes, s.now(), wants, familyV4, true); reason != "" {
			return "failed", reason
		}
		return "done", ""
	}, "", left, cost
}

// nowReserve is the tests the planned rounds will need before the window of the limit resets, which a round by hand must leave: every
// round of the schedule that falls due inside the window, per address family, at its cost with the current plan. A planned round that is
// due now counts. The window is taken as an hour when the source does not say.
func (s *Service) nowReserve(ctx context.Context, cc CountryConfig, nodes []countryNode, probes int, every time.Duration, now time.Time, window time.Duration) int {
	if window <= 0 {
		window = time.Hour
	}
	v4only := slices.Clone(nodes)
	for i := range v4only {
		v4only[i].ipv6Member = false
	}
	cost4 := countryCost(v4only, probes)
	cost6 := 0
	if cc.IPv6 {
		cost6 = max(countryCost(nodes, probes)-cost4, 0)
	}
	due := func(family, cost int) int {
		if cost == 0 {
			return 0
		}
		until := time.Duration(0)
		if last, err := s.st.LastCountryCheckAt(ctx, family); err == nil && !last.IsZero() {
			until = max(last.Add(every).Sub(now), 0)
		}
		if until > window {
			return 0
		}
		return cost * (1 + int((window-until)/every))
	}
	return due(familyV4, cost4) + due(familyV6, cost6)
}
