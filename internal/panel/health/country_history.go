package health

import (
	"context"
	"maps"
	"slices"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// tspuHistoryRows bounds one node's history to every 60-second manual test across seven days, including both window edges.
const tspuHistoryRows = 7*24*60 + 1

// CountryHistory is what CountryReachabilityService.GetReachabilityHistory shows: for every source that is on (the same test
// GetChecks makes), what it said of each node since `since`, as a lane of verdicts where consecutive equal ones are one point.
// Globalping's lane holds the rounds of the selected country; the block lists' lane is LISTED or OK; the TSPU probe's lane holds
// the runs of the current probe node; external sources each add a lane. Sources keep the existing order, then external sources;
// nodes are by id. The family is familyV4 or familyV6: IPv6 is measured by Globalping alone, so its history is that one lane,
// and it is empty while Globalping's IPv6 checks are off; the lists, the TSPU probe and the external sources are IPv4.
func (s *Service) CountryHistory(ctx context.Context, since time.Time, family int) (sources []string, nodes []*adminv1.NodeReachabilityHistory, err error) {
	now := s.now()
	type key struct{ node, source string }
	type point struct {
		at      time.Time
		verdict string
	}
	raw := map[key][]point{}
	until := map[key]time.Time{}

	if cc, _, on := s.countryConfig(ctx); on && (family == familyV4 || cc.IPv6) {
		sources = append(sources, countryreach.SourceGlobalping)
		rows, err := s.st.CountryVerdictChanges(ctx, cc.Country, family, since)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			k := key{r.NodeID, countryreach.SourceGlobalping}
			raw[k] = append(raw[k], point{r.At, r.Verdict})
		}
		latest, err := s.st.RecentCountryChecks(ctx, family, 2)
		if err != nil {
			return nil, nil, err
		}
		maxAges := countryRoundMaxAges(cc.Interval, latest)
		seen := map[string]bool{}
		for _, r := range latest {
			if seen[r.NodeID] {
				continue
			}
			seen[r.NodeID] = true
			if r.Country != cc.Country {
				continue
			}
			end := r.At.Add(maxAges[r.NodeID])
			if end.After(now) {
				end = now
			}
			until[key{r.NodeID, countryreach.SourceGlobalping}] = end
		}
		// The latest stored round can belong to another node or a country selected after this lane's last point. Keep a
		// per-lane freshness endpoint from its newest country-specific verdict change in those cases.
		for k, points := range raw {
			if k.source != countryreach.SourceGlobalping || len(points) == 0 {
				continue
			}
			age := maxAges[k.node]
			if age == 0 {
				age = countryMaxAge(cc.Interval, time.Time{}, time.Time{})
			}
			end := points[len(points)-1].at.Add(age)
			if end.After(now) {
				end = now
			}
			if end.After(until[k]) {
				until[k] = end
			}
		}
	}
	if family == familyV4 && s.cfg.Lists != nil && s.cfg.Lists(ctx) {
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
	if t := s.tspuState(ctx); family == familyV4 && t.On {
		sources = append(sources, countryreach.SourceTSPU)
		rows, err := s.st.TSPUHistorySince(ctx, t.Vantage, since, tspuHistoryRows)
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
	var activeExternal []ExternalState
	if family == familyV4 {
		activeExternal = s.activeExternalStates(ctx)
	}
	for _, state := range activeExternal {
		sources = append(sources, state.Source)
	}
	if len(activeExternal) > 0 {
		maxAge := make(map[string]time.Duration, len(activeExternal))
		for _, state := range activeExternal {
			maxAge[state.Source] = state.MaxAge
		}
		rows, err := s.st.ExtVerdictChanges(ctx, since)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			if _, ok := maxAge[r.Source]; !ok {
				continue
			}
			k := key{r.NodeID, r.Source}
			raw[k] = append(raw[k], point{r.At, r.Verdict})
		}
		latest, err := s.st.RecentExtChecks(ctx, 1, slices.Sorted(maps.Keys(maxAge))...)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range latest {
			end := r.At.Add(maxAge[r.Source])
			if end.After(now) {
				end = now
			}
			until[key{r.NodeID, r.Source}] = end
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
