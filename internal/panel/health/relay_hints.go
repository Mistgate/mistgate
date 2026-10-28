package health

import (
	"cmp"
	"context"
	"slices"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// RelayHints is what the country check and the TSPU probe know about nodes that could be the exit of a relay through an entry
// node (access.ListRelayCandidates, relay.proto RelayCountryHint and RelayReachHint). A node the sources say nothing fresh about
// is left out of the maps: "no data" is the caller's word, never a guess here.
//
// country: the combined verdict of the node seen from inside the country (also for entryID: do people see the entry?) and the
// networks that see it dark.
//
// reach: whether the entry reaches the exit, known only when the TSPU probe runs from the entry node. The probe's row for the
// exit from this entry says it ("tspu_row"); without a row, a run that found no TSPU on the entry's path (a blocked reference
// answered: reason refs_pass) says nothing is cut there ("refs_pass").
func (s *Service) RelayHints(ctx context.Context, entryID string, nodeIDs []string) (country map[string]*adminv1.RelayCountryHint, reach map[string]*adminv1.RelayReachHint) {
	country, reach = map[string]*adminv1.RelayCountryHint{}, map[string]*adminv1.RelayReachHint{}
	checks, err := s.countryChecks(ctx, "")
	if err != nil {
		s.log.Warn("relay hints: country checks", "err", err)
	}
	want := append([]string{entryID}, nodeIDs...)
	for _, c := range checks {
		if !slices.Contains(want, c.NodeId) {
			continue
		}
		if v := relayCountryVerdict(c.Verdict); v != "" {
			country[c.NodeId] = &adminv1.RelayCountryHint{Verdict: v, Dark: darkNetworks(c), AtUnix: c.AtUnix}
		}
	}

	tspu := s.tspuState(ctx)
	if !tspu.On || tspu.Vantage != entryID || len(nodeIDs) == 0 {
		return country, reach
	}
	rows, err := s.st.TSPUHistory(ctx, entryID, 1, nodeIDs...)
	if err != nil {
		s.log.Warn("relay hints: tspu rows", "err", err)
		return country, reach
	}
	now := s.now()
	for _, r := range tspuRounds(rows) {
		if now.Sub(r.row.At) > tspu.MaxAge { // a row from before the probe node was last off says nothing now
			continue
		}
		v := map[string]string{verdictOK: "reach", verdictPartial: "partial", verdictTCPBlocked: "blocked", verdictBlocked: "blocked"}[r.verdict]
		if v != "" {
			reach[r.row.NodeID] = &adminv1.RelayReachHint{Verdict: v, Source: "tspu_row", AtUnix: r.row.At.Unix()}
		}
	}
	if run := tspu.Run; run != nil && run.Status == "no_dpi_hop" && run.Reason == "refs_pass" && now.Sub(time.Unix(run.AtUnix, 0)) <= tspu.MaxAge {
		for _, id := range nodeIDs {
			if _, ok := reach[id]; !ok {
				reach[id] = &adminv1.RelayReachHint{Verdict: "reach", Source: "refs_pass", AtUnix: run.AtUnix}
			}
		}
	}
	return country, reach
}

// relayCountryVerdict is the word of the hint for a combined verdict; "" for unknown (no data).
func relayCountryVerdict(v adminv1.CountryVerdict) string {
	switch v {
	case adminv1.CountryVerdict_COUNTRY_VERDICT_OK:
		return "ok"
	case adminv1.CountryVerdict_COUNTRY_VERDICT_PARTIAL:
		return "partial"
	case adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED, adminv1.CountryVerdict_COUNTRY_VERDICT_BLOCKED:
		return "blocked"
	case adminv1.CountryVerdict_COUNTRY_VERDICT_LISTED:
		return "listed"
	}
	return ""
}

// darkNetworks names the networks that see the node dark, sorted and without repeats: the vantages in the dark state and, when
// the verdict is a full block (which says that every valid probe missed the node; the vantages of such a round are only
// "uncertain"), the networks of the valid probes.
func darkNetworks(c *adminv1.CountryCheck) []string {
	var out []string
	add := func(name string) {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	for _, v := range c.Vantages {
		if v.State == "dark" {
			add(cmp.Or(v.Network, v.Key))
		}
	}
	if c.Verdict == adminv1.CountryVerdict_COUNTRY_VERDICT_BLOCKED || c.Verdict == adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED {
		for _, p := range c.Probes {
			if p.Valid {
				add(p.Network)
			}
		}
	}
	slices.Sort(out)
	return out
}
