package ownprobe

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// refoldRows is how many of the newest rows of a node a revocation or a network change rewrites: the health module reads
// back ten (countryRecent), and a point that is gone from the newest row but still in an older one would go on raising the alert.
const refoldRows = 16

type targetResult struct {
	target Target
	result SyncResult
}

// foldReport puts the points of one report into the rows of country_ext_check, one row per node the report measured. The row
// keeps the live points of the other probes and takes this probe's point in place of its previous one; the verdict of the row
// is judged over the points. The caller holds s.mu, so two reports never fold at once.
func (s *Service) foldReport(ctx context.Context, probe store.OwnProbe, report SyncReport, targets []Target, now time.Time, interval time.Duration) error {
	active, err := s.activeProbeIDs(ctx)
	if err != nil {
		return err
	}
	rows, err := s.cfg.Store.RecentExtChecks(ctx, 1, countryreach.SourceProbes)
	if err != nil {
		return err
	}
	byNode := make(map[string]store.ExtCheck, len(rows))
	for _, row := range rows {
		byNode[row.NodeID] = row
	}
	grouped := map[string][]targetResult{}
	for _, result := range report.Results {
		target := targets[result.Index-1]
		grouped[target.NodeID] = append(grouped[target.NodeID], targetResult{target: target, result: result})
	}
	// Without a control that answered, or with a reference that could not be tried, the probe says nothing about a node: its
	// point is "unknown, no_control" and the results are not read.
	blind := report.Control != "ok" || report.Reference == "fail"
	checks := make([]store.ExtCheck, 0, len(grouped))
	for _, nodeID := range slices.Sorted(maps.Keys(grouped)) {
		vantages := liveVantages(byNode[nodeID], active, now, interval, probe.ID)
		if blind {
			vantages = append(vantages, makeVantage(probe, countryreach.VantageStateUncertain, "no_control", false, false, now))
		} else {
			vantages = append(vantages, judgeProbe(probe, grouped[nodeID], report.Reference, now))
		}
		check, err := extCheck(nodeID, vantages, now)
		if err != nil {
			return err
		}
		checks = append(checks, check)
	}
	return s.cfg.Store.InsertExtChecks(ctx, checks)
}

func extCheck(nodeID string, vantages []countryreach.Vantage, at time.Time) (store.ExtCheck, error) {
	verdict, reason := countryreach.JudgeVantages(vantages, 1, countryreach.VerdictTCPBlocked)
	detail, err := json.Marshal(countryreach.ExtDetail{Reason: reason, Vantages: vantages})
	if err != nil {
		return store.ExtCheck{}, err
	}
	return store.ExtCheck{NodeID: nodeID, Source: countryreach.SourceProbes, At: at, Verdict: verdict, DetailJSON: string(detail)}, nil
}

// judgeProbe is the point of one probe for one node from its results: reach when some target connected and passed TLS and did not
// freeze; dark when a target timed out, was reset or froze; else uncertain. A reference that opened means the probe's network
// filters nothing, and then "reach" proves nothing: it drops to uncertain (no_filter), while dark stays.
func judgeProbe(probe store.OwnProbe, results []targetResult, reference string, now time.Time) countryreach.Vantage {
	var sawReach, sawDark, tcpOK, tlsOK bool
	var darkWhy, otherWhy string
	var darkPort, otherPort uint16 // the port of the target the reason comes from
	for _, item := range results {
		state, reason := targetVerdict(item.target, item.result)
		tcpOK = tcpOK || item.result.TCP == "ok"
		tlsOK = tlsOK || item.result.TLS == "ok"
		sawReach = sawReach || state == countryreach.VantageStateReach
		switch {
		case state == countryreach.VantageStateDark && !sawDark:
			sawDark, darkWhy, darkPort = true, reason, item.target.Port
		case state == countryreach.VantageStateUncertain && otherWhy == "":
			otherWhy, otherPort = reason, item.target.Port
		}
	}
	state, why, port := countryreach.VantageStateUncertain, otherWhy, otherPort
	switch {
	case sawReach:
		state, why, port = countryreach.VantageStateReach, "", 0
	case sawDark:
		state, why, port = countryreach.VantageStateDark, darkWhy, darkPort
	}
	if reference == "open" && state == countryreach.VantageStateReach {
		state, why = countryreach.VantageStateUncertain, "no_filter"
	}
	if why == "" || why == "no_filter" { // a port belongs to a reason that is about one target
		port = 0
	}
	v := makeVantage(probe, state, why, tcpOK, tlsOK, now)
	v.Port = port
	return v
}

// targetVerdict reads one result of one target. A TCP connect alone proves nothing about TLS, so a target of kind tcp only
// ever says "dark" (its SYN was dropped); refused is the node's own doing, not a filter.
func targetVerdict(target Target, result SyncResult) (string, string) {
	switch {
	case result.TCP == "timeout" || result.TCP == "unreach":
		return countryreach.VantageStateDark, "tcp_timeout"
	case result.TCP == "refused":
		return countryreach.VantageStateUncertain, "tcp_reset"
	case target.Kind == "tcp" || result.TCP != "ok":
		return countryreach.VantageStateUncertain, ""
	case result.TLS == "reset":
		return countryreach.VantageStateDark, "tls_reset"
	case result.TLS == "timeout":
		return countryreach.VantageStateDark, "tls_timeout"
	case result.Fetch == "freeze":
		return countryreach.VantageStateDark, "freeze"
	case result.TLS == "ok":
		return countryreach.VantageStateReach, ""
	}
	return countryreach.VantageStateUncertain, ""
}

// makeVantage names the point the way the owner reads it: operator and city, and the probe's name as a field of its own; no address
// is in it.
func makeVantage(probe store.OwnProbe, state, why string, tcpOK, tlsOK bool, at time.Time) countryreach.Vantage {
	network := strings.TrimSpace(probe.Operator)
	if network == "" {
		network = "AS" + strconv.FormatUint(uint64(probe.ASN), 10)
	}
	if probe.City != "" {
		network += " · " + probe.City
	}
	return countryreach.Vantage{Key: "probe:" + probe.ID, Network: network, Region: probe.City,
		ASN: strconv.FormatUint(uint64(probe.ASN), 10), Kind: probe.Kind, State: state, At: at.Unix(), Why: why,
		TCPOK: tcpOK, TLS: tlsOK, ProbeName: probe.Name}
}

func (s *Service) activeProbeIDs(ctx context.Context) (map[string]bool, error) {
	probes, err := s.cfg.Store.OwnProbes(ctx)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(probes))
	for _, probe := range probes {
		if probe.State == "active" {
			active[probe.ID] = true
		}
	}
	return active, nil
}

// liveVantages are the points of a row that still count: those of probes that are active, not older than three intervals and
// not from the future, without the point of except (the probe that is about to replace its own).
func liveVantages(row store.ExtCheck, active map[string]bool, now time.Time, interval time.Duration, except string) []countryreach.Vantage {
	if row.DetailJSON == "" {
		return nil
	}
	var detail countryreach.ExtDetail
	if json.Unmarshal([]byte(row.DetailJSON), &detail) != nil {
		return nil
	}
	cut := now.Add(-3 * interval).Unix()
	out := make([]countryreach.Vantage, 0, len(detail.Vantages)+1)
	for _, vantage := range detail.Vantages {
		id, ok := strings.CutPrefix(vantage.Key, "probe:")
		if !ok || id == except {
			continue
		}
		at := vantage.At
		if at == 0 {
			at = row.At.Unix()
		}
		if !active[id] || at < cut || at > now.Add(5*time.Minute).Unix() {
			continue
		}
		vantage.At = at
		out = append(out, vantage)
	}
	return out
}

// refold takes the points of probes that are no longer active out of the newest rows, in place (same node, same time), so a
// revoked probe or one that moved to another network stops counting at once, in the history the alerts read as well. With no
// active probe left the rows go. The caller holds s.mu.
func (s *Service) refold(ctx context.Context) error {
	active, err := s.activeProbeIDs(ctx)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		return s.cfg.Store.DeleteExtChecks(ctx, countryreach.SourceProbes)
	}
	rows, err := s.cfg.Store.RecentExtChecks(ctx, refoldRows, countryreach.SourceProbes)
	if err != nil {
		return err
	}
	var checks []store.ExtCheck
	for _, row := range rows {
		var detail countryreach.ExtDetail
		if json.Unmarshal([]byte(row.DetailJSON), &detail) != nil {
			continue
		}
		kept := make([]countryreach.Vantage, 0, len(detail.Vantages))
		for _, vantage := range detail.Vantages {
			if id, ok := strings.CutPrefix(vantage.Key, "probe:"); ok && active[id] {
				kept = append(kept, vantage)
			}
		}
		if len(kept) == len(detail.Vantages) {
			continue
		}
		check, err := extCheck(row.NodeID, kept, row.At)
		if err != nil {
			return err
		}
		checks = append(checks, check)
	}
	return s.cfg.Store.InsertExtChecks(ctx, checks)
}
