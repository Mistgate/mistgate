//go:build !js

package warp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	panelhealth "github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// WARP reach: which sites show a block page to the WARP exit of a node. Through Cloudflare WARP some sites answer with a block
// page at one Cloudflare colo and not at another, while the tunnel is healthy (the node's own health check cannot see it). The
// owner lists the sites; the panel asks every node with WARP to fetch them through its tunnel (agent.proto "WARP REACH"), keeps
// the latest answer next to the account, shows it in GetWarp and warp_status, and raises the Health alert warp_reach for a site
// that was blocked twice in a row.

const (
	// SettingReach holds the owner's list as a JSON array of host names; absent = DefaultReachDomains, "[]" = the check is off.
	SettingReach = "warp.reach_domains"

	reachFirst = time.Minute      // the first run after the panel starts: nodes are connecting
	reachEvery = 15 * time.Minute // between runs; two blocked runs in a row (30 min) open the alert
	// reachFresh is how old a result may be and still keep an alert open: a node that stopped answering (offline, WARP down,
	// an agent that lost the capability) must not hold a warning for ever on a result nobody renews.
	reachFresh = 3 * time.Hour
	// reachAlertStreak is how many runs that said "blocked" (with no "ok" between) open the alert.
	reachAlertStreak = 2
	// reachAlertWhy is the i18n key of the alert's explanation (the texts live in the web app).
	reachAlertWhy = "health.alert.warp_reach.why"
)

// DefaultReachDomains is the built-in list: sites that people reach through a VPN every day, behind Cloudflare or a region
// check, and whose answer to a plain page request is an ordinary page when they are not refusing WARP. notion.com is the one
// owners reported (a block page at some colos); discord.com, chatgpt.com and claude.ai refuse by region and by address
// reputation. A site that always challenges scripted clients reads "error" (a challenge is not a refusal) and never alerts.
var DefaultReachDomains = []string{"notion.com", "discord.com", "chatgpt.com", "claude.ai"}

// reachRecord is what reach_json holds: the run of one node.
type reachRecord struct {
	Colo    string        `json:"colo"`
	Domains []reachDomain `json:"domains"`
}

type reachDomain struct {
	Domain  string `json:"domain"`
	Verdict string `json:"verdict"`
	Status  uint32 `json:"status"`
	Ms      uint32 `json:"ms"`
	Streak  uint32 `json:"streak"` // runs that said "blocked" since the last "ok", this one included
}

// reachFleet is what the check needs of the fleet module on top of Fleet (*fleet.Fleet implements it; the Cloudflare edition's
// build has no such method, and no check).
type reachFleet interface {
	// WarpReach asks the node to fetch the sites through its WARP tunnel (agent.proto "WARP REACH") and returns the colo of
	// the exit at that run and one answer per site, in the order asked; an error is no usable answer.
	WarpReach(ctx context.Context, nodeID string, domains []string) (string, []store.WarpReachItem, error)
}

// reachDomains is the owner's list (customised) or the built-in one. A stored value that cannot be read, or has names that are
// not host names, gives the valid ones it has (a list of nothing valid, with the setting present, is "off").
func (s *Service) reachDomains(ctx context.Context) (domains []string, customised bool) {
	raw, err := s.st.Setting(ctx, SettingReach)
	if errors.Is(err, store.ErrNotFound) {
		return slices.Clone(DefaultReachDomains), false
	}
	if err != nil {
		s.log.Warn("warp: read reach domains", "code", "warp_reach_read_failed", "err", err)
		return slices.Clone(DefaultReachDomains), false
	}
	var list []string
	_ = json.Unmarshal([]byte(raw), &list)
	for _, d := range list {
		if plugin.ValidWarpReachDomain(d) && !slices.Contains(domains, d) && len(domains) < plugin.MaxWarpReachDomains {
			domains = append(domains, d)
		}
	}
	return domains, true
}

func decodeReach(a store.WarpAccountRow) (reachRecord, bool) {
	var rec reachRecord
	if a.ReachJSON == "" || json.Unmarshal([]byte(a.ReachJSON), &rec) != nil {
		return reachRecord{}, false
	}
	return rec, true
}

// reachView is GetWarpResponse.reach and reach_state of a node's account.
func (s *Service) reachView(ctx context.Context, n store.NodeRow, a store.WarpAccountRow) (*adminv1.WarpReachView, string) {
	domains, _ := s.reachDomains(ctx)
	rec, have := decodeReach(a)
	switch {
	case len(domains) == 0:
		return nil, "disabled"
	case !slices.Contains(n.AgentCaps, agentcap.WarpReach):
		return nil, "agent_too_old"
	case !have:
		return nil, "waiting"
	}
	v := &adminv1.WarpReachView{CheckedUnix: a.ReachAt.Unix(), Colo: rec.Colo}
	for _, d := range rec.Domains {
		v.Domains = append(v.Domains, &adminv1.WarpReachDomain{Domain: d.Domain, Verdict: d.Verdict, HttpStatus: d.Status, LatencyMs: d.Ms, BlockedStreak: d.Streak})
	}
	return v, "ready"
}

// CheckReach asks every node that can answer to check the owner's sites. A node that is offline, whose agent is old, whose WARP
// is not up or has been paused is skipped (its last result stays, and ages out of reachFresh).
func (s *Service) CheckReach(ctx context.Context) {
	domains, _ := s.reachDomains(ctx)
	if len(domains) == 0 {
		return
	}
	accounts, err := s.st.WarpAccounts(ctx)
	if err != nil {
		s.log.Warn("warp: reach: read accounts", "code", "warp_reach_accounts_failed", "err", err)
		return
	}
	for _, a := range accounts {
		if ctx.Err() != nil {
			return
		}
		s.checkReach(ctx, a, domains)
	}
}

// checkReach is the schedule's check of one node: it leaves a check by hand that is on (reach_now.go) to finish.
func (s *Service) checkReach(ctx context.Context, a store.WarpAccountRow, domains []string) {
	if !s.claimReach(a.NodeID) {
		return
	}
	s.runReach(ctx, a, domains)
}

// runReach is a check of one node whose busy flag the caller holds; it lets the flag go and keeps how the check ended. A node that
// cannot be asked at all (offline, an old agent, no tunnel) ends nothing: its last result simply stays.
func (s *Service) runReach(ctx context.Context, a store.WarpAccountRow, domains []string) {
	how := s.reachOnce(ctx, a, domains)
	s.releaseReach(a.NodeID, how)
}

// reachOnce asks the node and stores the answer. It returns how that ended ("" when the node was not asked).
func (s *Service) reachOnce(ctx context.Context, a store.WarpAccountRow, domains []string) string {
	n, err := s.st.Node(ctx, a.NodeID)
	if err != nil || n.State != "active" || !a.Enabled || !s.supportsReach(n) {
		return ""
	}
	live, err := s.liveNode(ctx, a.NodeID)
	if err != nil || !live.Connected || ViewState(&a, true, s.now()) != adminv1.WarpState_WARP_STATE_UP {
		return ""
	}
	rf, ok := s.fl.(reachFleet)
	if !ok {
		return ""
	}
	colo, items, err := rf.WarpReach(ctx, a.NodeID, domains)
	if err != nil {
		// busy, too_soon and warp_down are the node's say; a lost link or a timeout is the usual end of a run too
		s.log.Debug("warp: reach", "node", a.NodeID, "err", err)
		switch err.Error() {
		case "warp reach: busy":
			return "busy"
		case "warp reach: too_soon":
			return "too_soon"
		case "warp reach: warp_down":
			return "warp_down"
		}
		return "failed"
	}
	if len(items) != len(domains) {
		return "failed"
	}
	prev, _ := decodeReach(a)
	rec := reachRecord{Colo: store.Clip(colo, 16)}
	for i, d := range domains {
		it := reachDomain{Domain: d, Verdict: items[i].Verdict, Status: items[i].HTTPStatus, Ms: items[i].Millis}
		for _, p := range prev.Domains {
			if p.Domain == d {
				it.Streak = p.Streak
			}
		}
		switch it.Verdict {
		case "blocked":
			it.Streak++
		case "ok":
			it.Streak = 0 // only an answer that is not a block page ends it: a timeout or an error says nothing either way
		}
		rec.Domains = append(rec.Domains, it)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return "failed"
	}
	if err := s.st.SetWarpReach(ctx, a.NodeID, string(b), s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("warp: reach: store", "code", "warp_reach_store_failed", "node", a.NodeID, "err", err)
		return "failed"
	}
	return "ok"
}

func (s *Service) supportsReach(n store.NodeRow) bool {
	return slices.Contains(n.AgentCaps, agentcap.WarpReach)
}

// Conditions is the health condition source (health.Service.AddConditionSource): WARP_REACH for a site of the current list that
// the node's runs said "blocked" twice and that has not answered "ok" since, while the result is fresh and the account is not
// paused. It reads the
// stored results only, so it costs a read of the accounts and no request to any node.
func (s *Service) Conditions(ctx context.Context) []panelhealth.ExtCond {
	domains, _ := s.reachDomains(ctx)
	if len(domains) == 0 {
		return nil
	}
	accounts, err := s.st.WarpAccounts(ctx)
	if err != nil {
		return nil
	}
	var out []panelhealth.ExtCond
	for _, a := range accounts {
		rec, ok := decodeReach(a)
		if !ok || !a.Enabled || s.now().Sub(a.ReachAt) > reachFresh {
			continue
		}
		for _, d := range rec.Domains {
			if d.Streak < reachAlertStreak || !slices.Contains(domains, d.Domain) {
				continue
			}
			out = append(out, panelhealth.ExtCond{Kind: "warp_reach", NodeID: a.NodeID, Subject: d.Domain, Severity: 2, Why: reachAlertWhy,
				Params: map[string]string{"domain": d.Domain, "colo": rec.Colo, "http_status": strconv.FormatUint(uint64(d.Status), 10),
					"streak": strconv.FormatUint(uint64(d.Streak), 10)}})
		}
	}
	return out
}
