package health

import (
	"context"
	"maps"
	"slices"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// tspuHistoryRows is how many runs of the probe node are read back per node for the history: a week at its shortest interval
// (30 minutes). Tests in a burst can push the oldest out of the window.
const tspuHistoryRows = 7 * 24 * 2

// CountryHistory is what CountryReachabilityService.GetReachabilityHistory shows: for every source that is on (the same test
// GetChecks makes), what it said of each node since `since`, as a lane of verdicts where consecutive equal ones are one point.
// Globalping's lane holds the rounds of the selected country; the block lists' lane is LISTED or OK; the TSPU probe's lane holds
// the runs of the current probe node, mapped as Combine maps them. Sources come in the order globalping, lists, tspu; nodes by id.
func (s *Service) CountryHistory(ctx context.Context, since time.Time) (sources []string, nodes []*adminv1.NodeReachabilityHistory, err error) {
	now := s.now()
	type key struct{ node, source string }
	type point struct {
		at      time.Time
		verdict string
	}
	raw := map[key][]point{}
	until := map[key]time.Time{}

	if cc, _, on := s.countryConfig(ctx); on {
		sources = append(sources, countryreach.SourceGlobalping)
		rows, err := s.st.CountryVerdictChanges(ctx, cc.Country, since)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			k := key{r.NodeID, countryreach.SourceGlobalping}
			raw[k], until[k] = append(raw[k], point{r.At, r.Verdict}), now
		}
	}
	if s.cfg.Lists != nil && s.cfg.Lists(ctx) {
		sources = append(sources, countryreach.SourceLists)
		rows, err := s.st.BlocklistHistory(ctx, since)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			v := verdictOK
			if r.InRKN && r.ListedBy != "" { // as combineCountry reads a hit
				v = verdictListed
			}
			k := key{r.NodeID, countryreach.SourceLists}
			raw[k], until[k] = append(raw[k], point{r.At, v}), now
		}
	}
	if t := s.tspuState(ctx); t.On {
		sources = append(sources, countryreach.SourceTSPU)
		rows, err := s.st.TSPUHistory(ctx, t.Vantage, tspuHistoryRows)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range tspuRounds(rows) {
			if r.row.At.Before(since) {
				continue
			}
			k := key{r.row.NodeID, countryreach.SourceTSPU}
			raw[k] = append(raw[k], point{r.row.At, r.verdict})
			// a run counts for three intervals, as on GetChecks; after that the lane has nothing to say
			end := r.row.At.Add(t.MaxAge)
			if end.After(now) {
				end = now
			}
			if end.After(until[k]) {
				until[k] = end
			}
		}
	}

	byNode := map[string]*adminv1.NodeReachabilityHistory{}
	for _, src := range sources {
		for k, pts := range raw {
			if k.source != src {
				continue
			}
			slices.SortFunc(pts, func(a, b point) int { return a.at.Compare(b.at) })
			lane := &adminv1.ReachabilityLane{Source: src, UntilUnix: until[k].Unix()}
			for _, p := range pts {
				v := countryVerdictProto[p.verdict]
				if n := len(lane.Points); n > 0 && lane.Points[n-1].Verdict == v {
					continue
				}
				lane.Points = append(lane.Points, &adminv1.ReachabilityPoint{AtUnix: p.at.Unix(), Verdict: v})
			}
			n := byNode[k.node]
			if n == nil {
				n = &adminv1.NodeReachabilityHistory{NodeId: k.node}
				byNode[k.node] = n
			}
			n.Lanes = append(n.Lanes, lane)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(byNode)) {
		nodes = append(nodes, byNode[id])
	}
	return sources, nodes, nil
}
