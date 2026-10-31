package access

import (
	"cmp"
	"context"
	"errors"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The checks of a relay (relay.proto CheckRelay) and the rule that lets one be switched on. The probes are the fleet module's
// (fleet.CheckRelay, handed in as Config.CheckRelay); here are the rule, the wait for the entry's forward and the candidate list.
//
// A relay is switched on in two steps, and no step is skipped:
//
//  1. The gate (relayCheckGate): the leg check of CheckRelay passed, less than relayCheckTTL ago, for the relay's present target. A relay that is off has no forward, so the leg is all that can be proved before.
//  2. relaySettle, once the switch is stored and the entry has been told: wait for the entry to report the forward, then check
//     the path from outside. A relay that fails here is switched off again, so a relay stays on only when it works from outside.

// relayCheckTTL is how fresh a passing leg check must be to switch a relay on. The owner reads the result and decides, which
// takes minutes, and the leg (a block of the exit by the entry's network, the exit's firewall) does not change in that time
// often enough to matter; an hour-old check says nothing about now. The path is proved again right after the switch anyway.
const relayCheckTTL = 15 * time.Minute

// relayApplyWait is how long switching on waits for the entry to report its forward (the desired state goes out at once and
// the agent installs the table in a second or two; an entry that is off line never answers).
const relayApplyWait = 30 * time.Second

// relayTo is the target the forward points at: the exit's address and its server's port, the text a check is made for.
func (s *Service) relayTo(ctx context.Context, l store.RelayLink) string {
	f, merged, err := s.loadFull(ctx, l.InboundID)
	if err != nil {
		return ""
	}
	spec, err := s.buildSpec(f, merged)
	if err != nil {
		return ""
	}
	return net.JoinHostPort(f.Node.Address, strconv.Itoa(int(spec.Listen.Port)))
}

// relayCheckGate lets a relay be switched on: its latest check proved the leg, recently, for this target. (The leg does not depend
// on the entry's port; a port moved after the check is covered by the path check that follows the switch.)
func (s *Service) relayCheckGate(ctx context.Context, l store.RelayLink) error {
	c, ok := l.Check()
	if !ok || s.cfg.CheckRelay == nil || c.Leg.Status == "" {
		return coded(connect.CodeFailedPrecondition, "relay_unchecked")
	}
	age := s.now().Sub(time.Unix(c.Leg.At, 0))
	if age > relayCheckTTL || age < -time.Minute || c.Network != l.Network || c.To != s.relayTo(ctx, l) {
		return coded(connect.CodeFailedPrecondition, "relay_check_stale")
	}
	if c.Leg.Status != "ok" {
		return coded(connect.CodeFailedPrecondition, "relay_check_failed", "stage", store.RelayStageLeg, "code", c.Leg.Code)
	}
	return nil
}

// relaySettle runs after a relay was switched on: the entry installs the forward, then the path is checked from outside. When
// either fails the relay is switched off again (the reason goes to its check and to the audit) and the caller is refused.
func (s *Service) relaySettle(ctx context.Context, l store.RelayLink) error {
	stage, code := store.RelayStagePath, s.awaitRelayForward(ctx, l.ID)
	if code == "" {
		code = s.relayPathCheck(ctx, l.ID)
	} else {
		s.recordRelayPathFailure(ctx, l.ID, code)
	}
	if code == "" {
		return nil
	}
	bg := context.WithoutCancel(ctx)
	off := false
	s.relayMu.Lock()
	err := s.st.UpdateRelay(bg, l.ID, store.RelayPatch{Enabled: &off}, s.now())
	s.relayMu.Unlock()
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("access: switch a failed relay off", "relay", l.ID, "err", err)
	}
	s.audit(bg, actor(ctx), "relay.update", map[string]any{"relay": l.ID, "via": l.ViaNodeName, "exit": l.ExitNodeName,
		"enabled": false, "reason": "check_failed", "stage": stage, "code": code})
	s.notify.StateChanged()
	return coded(connect.CodeFailedPrecondition, "relay_check_failed", "stage", stage, "code", code)
}

// awaitRelayForward waits for the entry's agent to report the forward: "" when it is active, else why not.
func (s *Service) awaitRelayForward(ctx context.Context, id string) string {
	deadline := time.NewTimer(s.relayWait)
	defer deadline.Stop()
	tick := time.NewTicker(s.relayPoll)
	defer tick.Stop()
	for {
		l, err := s.st.RelayLink(context.WithoutCancel(ctx), id)
		switch {
		case err != nil:
			return "failed"
		case !l.Enabled:
			return "switched_off"
		case l.State == "active":
			return ""
		case l.State == "failed":
			return "apply_failed"
		}
		select {
		case <-ctx.Done():
			return "apply_timeout"
		case <-deadline.C:
			return "apply_timeout"
		case <-tick.C:
		}
	}
}

// relayPathCheck runs the path stage through the fleet: "" when it passed, else the failure code.
func (s *Service) relayPathCheck(ctx context.Context, id string) string {
	if s.cfg.CheckRelay == nil {
		return "failed"
	}
	c, err := s.cfg.CheckRelay(ctx, id, store.RelayStagePath)
	switch {
	case err != nil:
		s.log.Warn("access: path check of a relay", "relay", id, "err", err)
		return "failed"
	case c.Path.Status != "ok":
		return cmp.Or(c.Path.Code, "failed")
	}
	return ""
}

// recordRelayPathFailure puts a path failure the fleet never saw (the forward was not installed) into the relay's check, next to
// the leg that allowed the switch.
func (s *Service) recordRelayPathFailure(ctx context.Context, id, code string) {
	bg := context.WithoutCancel(ctx)
	l, err := s.st.RelayLink(bg, id)
	if err != nil {
		return
	}
	c, ok := l.Check()
	if !ok {
		return
	}
	c.At = s.now().Unix()
	c.Path = store.RelayStage{Status: "fail", Code: code, At: c.At}
	c.Settle()
	if err := s.st.SetRelayCheck(bg, id, c); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("access: store a relay check", "relay", id, "err", err)
	}
}

// clearRelayPath forgets the path result of a relay about to be switched on or moved: that result described the forward as it was.
func (s *Service) clearRelayPath(ctx context.Context, id string) {
	l, err := s.st.RelayLink(ctx, id)
	if err != nil {
		return
	}
	c, ok := l.Check()
	if !ok || c.Path == (store.RelayStage{}) {
		return
	}
	c.Path = store.RelayStage{}
	c.Settle()
	if err := s.st.SetRelayCheck(ctx, id, c); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("access: store a relay check", "relay", id, "err", err)
	}
}

// SetRelayHints plugs in the source of the hints of ListRelayCandidates (health.Service.RelayHints). The health module is built
// after this one. Call it before the service takes requests.
func (s *Service) SetRelayHints(f func(ctx context.Context, entryID string, nodeIDs []string) (map[string]*adminv1.RelayCountryHint, map[string]*adminv1.RelayReachHint)) {
	s.relayHints = f
}

// SetRelaySettleForTest replaces the step that follows switching a relay on (nil restores the real one). Call it before the
// service takes requests.
func (s *Service) SetRelaySettleForTest(settle func(context.Context, store.RelayLink) error) {
	if settle == nil {
		settle = s.relaySettle
	}
	s.relaySettleFn = settle
}

// SetRelayWaitForTest shortens the wait for the entry's report and its poll.
func (s *Service) SetRelayWaitForTest(wait, poll time.Duration) {
	s.relayWait, s.relayPoll = wait, poll
}

func (s *Service) CheckRelay(ctx context.Context, req *connect.Request[adminv1.CheckRelayRequest]) (*connect.Response[adminv1.CheckRelayResponse], error) {
	link, err := s.st.RelayLink(ctx, req.Msg.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("relay")
	case err != nil:
		return nil, s.internal("get relay", err)
	}
	if s.cfg.CheckRelay == nil {
		return nil, coded(connect.CodeFailedPrecondition, "check_unavailable")
	}
	c, err := s.cfg.CheckRelay(ctx, link.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("relay")
	case err != nil:
		return nil, s.internal("check relay", err)
	}
	s.audit(ctx, actor(ctx), "relay.check", map[string]any{"relay": link.ID, "via": link.ViaNodeName, "exit": link.ExitNodeName,
		"ok": c.OK, "verified": c.Verified(), "stage": c.Stage, "leg": c.Leg.Status, "leg_code": c.Leg.Code, "path": c.Path.Status, "path_code": c.Path.Code})
	next, err := s.st.RelayLink(ctx, link.ID)
	if err != nil {
		return nil, s.internal("read relay", err)
	}
	return connect.NewResponse(&adminv1.CheckRelayResponse{Relay: s.relayMsg(ctx, next)}), nil
}

// relayUnsupported says why a server cannot be passed on by an entry, "" when it can: people reach a CDN server through the CDN,
// not the exit's address; a certificate on the exit's IP address would not fit the entry's; the AmneziaWG key goes through a
// relay only by a later step.
func relayUnsupported(protocol string, spec plugin.InboundSpec) string {
	switch {
	case protocol == "awg":
		return "awg"
	case spec.TLS.Mode == plugin.TLSAcmeIP:
		return "acme_ip"
	case protocols.ServerCDN(spec):
		return "cdn"
	}
	return ""
}

func relayCheckMsg(c store.RelayCheck, nameOf func(string) string) *adminv1.RelayCheck {
	stage := func(s store.RelayStage) *adminv1.RelayStage {
		return &adminv1.RelayStage{Status: s.Status, Code: s.Code, SenderId: s.Sender, SenderName: nameOf(s.Sender),
			Verdict: s.Verdict, Sent: s.Sent, Got: s.Got, AtUnix: s.At}
	}
	return &adminv1.RelayCheck{AtUnix: c.At, Network: c.Network, Port: uint32(c.Port), To: c.To, Leg: stage(c.Leg), Path: stage(c.Path),
		Ok: c.OK, Stage: c.Stage, Verified: c.Verified()}
}

// relayCheckOf is the message of the relay's latest check (nil before the first), naming the sending nodes.
func (s *Service) relayCheckOf(ctx context.Context, l store.RelayLink) *adminv1.RelayCheck {
	c, ok := l.Check()
	if !ok || c.At == 0 {
		return nil
	}
	names := map[string]string{}
	return relayCheckMsg(c, func(id string) string {
		if id == "" || id == "panel" {
			return id
		}
		if _, seen := names[id]; !seen {
			names[id] = id
			if n, err := s.st.Access().Node(ctx, id); err == nil {
				names[id] = n.Name
			}
		}
		return names[id]
	})
}

func (s *Service) ListRelayCandidates(ctx context.Context, req *connect.Request[adminv1.ListRelayCandidatesRequest]) (*connect.Response[adminv1.ListRelayCandidatesResponse], error) {
	a := s.st.Access()
	entry, err := a.Node(ctx, req.Msg.ViaNodeId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("node")
	case err != nil:
		return nil, s.internal("get node", err)
	}
	nodes, err := a.Nodes(ctx)
	if err != nil {
		return nil, s.internal("list nodes", err)
	}
	links, err := s.st.RelayLinks(ctx, store.RelayFilter{ViaNodeID: entry.ID})
	if err != nil {
		return nil, s.internal("list relays", err)
	}
	relayOf := make(map[string]string, len(links)) // the exit's server -> this entry's relay
	for _, l := range links {
		relayOf[l.InboundID] = l.ID
	}

	var out []*adminv1.RelayCandidate
	var exits []string
	for _, n := range nodes {
		if n.ID == entry.ID || n.State != "active" {
			continue
		}
		full, err := a.InboundsFull(ctx, n.ID)
		if err != nil {
			return nil, s.internal("list inbounds", err)
		}
		for _, f := range full {
			if !store.InboundLive(f.Inbound.Enabled, f.Inbound.State, f.Node.State) {
				continue
			}
			c := &adminv1.RelayCandidate{InboundId: f.Inbound.ID, NodeId: n.ID, NodeName: n.Name, ProfileId: f.Profile.ID,
				ProfileName: f.Profile.Name, Protocol: f.Profile.Protocol, RelayId: relayOf[f.Inbound.ID]}
			if merged, err := s.mergedSettings(f.Profile); err != nil {
				return nil, s.internal("open profile secrets", err)
			} else if spec, err := s.buildSpec(f, merged); err != nil {
				c.Unsupported = "build_failed"
			} else {
				c.Network, c.Port, c.Hop = spec.Listen.Network, uint32(spec.Listen.Port), spec.Listen.HopFrom != 0
				c.Unsupported = relayUnsupported(f.Profile.Protocol, spec)
			}
			out = append(out, c)
			if !slices.Contains(exits, n.ID) {
				exits = append(exits, n.ID)
			}
		}
	}

	var country map[string]*adminv1.RelayCountryHint
	var reach map[string]*adminv1.RelayReachHint
	if s.relayHints != nil {
		country, reach = s.relayHints(ctx, entry.ID, exits)
	}
	hint := func(id string) *adminv1.RelayCountryHint {
		if h := country[id]; h != nil {
			return h
		}
		return &adminv1.RelayCountryHint{Verdict: "no_data"}
	}
	for _, c := range out {
		c.Country = hint(c.NodeId)
		if c.Reach = reach[c.NodeId]; c.Reach == nil {
			c.Reach = &adminv1.RelayReachHint{Verdict: "no_data"}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Unsupported == "") != (b.Unsupported == "") {
			return a.Unsupported == ""
		}
		if ra, rb := cutRank(a.Country.Verdict), cutRank(b.Country.Verdict); ra != rb {
			return ra < rb
		}
		if an, bn := strings.ToLower(a.NodeName), strings.ToLower(b.NodeName); an != bn {
			return an < bn
		}
		return strings.ToLower(a.ProfileName) < strings.ToLower(b.ProfileName)
	})
	return connect.NewResponse(&adminv1.ListRelayCandidatesResponse{Candidates: out, AgentSupported: slices.Contains(entry.AgentCaps, plugin.CapRelayForward),
		EntryCountry: hint(entry.ID)}), nil
}

// cutRank puts the exits the country check says are cut off first: those are the ones a relay is for.
func cutRank(verdict string) int {
	switch verdict {
	case "blocked":
		return 0
	case "partial":
		return 1
	}
	return 2
}
