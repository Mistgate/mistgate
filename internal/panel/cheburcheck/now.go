//go:build !js

package cheburcheck

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// nowWaitSlot is how long a check by hand waits for the 30-second spacing of the site's calls (a scheduled call or the other
// source's check of the same round may hold it).
const nowWaitSlot = 3 * time.Minute

// NowPlan is cheburcheck's part of "Check now" of a node (health.CountryNowSource): the node is checked on one source (SourcePublic
// or SourceOwn) out of turn, whatever its interval says. It says at once why it will not (the reason words of
// health.CountryNowSource) or returns the check, which waits for the spacing of the site's calls, then stores the result like a
// scheduled one. The hold after a rate limit or repeated failures is kept: no call goes to the site during it.
func (s *Service) NowPlan(ctx context.Context, nodeID, source string) (run func(context.Context) (string, string), skip string) {
	gen := s.guard.Generation()
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("cheburcheck: read settings", "code", "cheburcheck_read_settings_failed", "err", err)
		return nil, "error"
	}
	state := s.sourceState(st)
	var token string
	switch source {
	case SourcePublic:
		if !state.PublicOn {
			return nil, "off"
		}
	case SourceOwn:
		if !state.OwnOn {
			return nil, "off"
		}
		token = string(s.token(st))
		if token == "" {
			return nil, "off"
		}
	default:
		return nil, "off"
	}
	if st.holdUntil.After(s.now()) {
		return nil, "held"
	}
	nodes, err := s.activeNodes(ctx)
	if err != nil {
		return nil, "error"
	}
	var d dueCheck
	for _, n := range nodes {
		if n.ID == nodeID {
			d = dueCheck{node: node{ID: n.ID}, row: n, source: source, token: s.token(st)}
		}
	}
	if d.node.ID == "" {
		return nil, "node_offline"
	}
	if source == SourcePublic {
		d.token = ""
	}
	ip, ok := s.cfg.Address(ctx, d.row)
	if !ok || !ip.Is4() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return nil, "no_address"
	}
	d.node.IP = ip
	return func(ctx context.Context) (string, string) {
		started := s.now()
		wait, cancel := context.WithTimeout(ctx, nowWaitSlot)
		defer cancel()
		for !s.takeCallSlot(wait, s.now()) {
			t := time.NewTimer(2 * time.Second)
			select {
			case <-wait.Done():
				t.Stop()
				return "failed", "busy"
			case <-t.C:
			}
		}
		if err := s.runDue(ctx, gen, st, d); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "failed", "timeout"
			}
			s.log.Warn("cheburcheck: check by hand failed", "code", "cheburcheck_manual_check_failed", "err", err)
			return "failed", "error"
		}
		// the verdict or the failure stored by the check: a result that says nothing is a failed check, not a verdict
		rows, err := s.cfg.Store.RecentExtChecks(ctx, 1, source)
		if err != nil {
			return "failed", "error"
		}
		for _, row := range rows {
			if row.NodeID != nodeID || row.At.Before(started) {
				continue
			}
			if row.Verdict == countryreach.VerdictUnknown {
				var detail countryreach.ExtDetail
				_ = json.Unmarshal([]byte(row.DetailJSON), &detail)
				if detail.Reason == "" {
					detail.Reason = "error"
				}
				return "failed", detail.Reason
			}
			return "done", ""
		}
		return "failed", "held" // nothing was stored: the site's hold started during the check
	}, ""
}
