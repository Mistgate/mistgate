//go:build !js

package mcp

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// Relays (relay.proto): a port of one node, the entry, that passes the encrypted traffic of a server on another node, the
// exit, on unchanged. The tools never show an address of a node: not the exit's, not the entry's.
//
//   - relays_list and relay_candidates read (ProfileReadonly).
//   - relay_check sends test packets between the owner's nodes and stores the result in the relay. It changes no switch and
//     touches no host, so it is a read like node_ports with check=true, not a plan. It needs the admin profile only because
//     the panel's CheckRelay is the owner's (a helper may not call it); it takes a minute at most, so it waits longer than a
//     stored read.
//   - relay_plan / relay_apply change what a node's firewall forwards and what people's subscriptions hold: always the
//     owner's approval.

func relayTools() []toolDef {
	out := []toolDef{
		readTool("relays_list", ProfileReadonly,
			procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.RelayServiceListRelaysProcedure),
			"The relays: each is a port on an entry node that passes one server of an exit node on, unchanged, for people whose network blocks the exit. "+
				"Per relay: the entry and exit nodes, the exit's server (profile, protocol, network), the port on the entry and on the exit, whether it is switched on, the owner's mode, active mode and reason, the bridge status, "+
				"what the entry's agent reported (state pending, active or failed, with the first word of its error), whether the exit's server is usable and the entry's agent can take the forward, "+
				"and the last check: the leg, reverse_reach, bridge and path stages, each ok, fail or skipped with its code, the node that sent the probe and, for UDP, the packets sent and counted. "+
				"Every protocol and switch state is included. Optional node (id or exact name) keeps the relays that have it at either end. Pages are oldest first: limit defaults to 10 (at most 100), offset starts at 0; continue at next_offset until it is absent. "+
				"Never an address of a node. Stored data only: it contacts no node.",
			func(c *call, in relaysListArg) (any, error) {
				if in.Offset < 0 || in.Limit < 0 {
					return nil, errors.New("offset and limit must not be negative")
				}
				id := ""
				if in.Node != "" {
					var err error
					if id, _, err = c.nodeRef(in.Node); err != nil {
						return nil, err
					}
				}
				r, err := c.cl.Relay.ListRelays(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.ListRelaysRequest{NodeId: id}))
				if err != nil {
					return nil, apiError(err)
				}
				limit := in.Limit
				if limit == 0 {
					limit = defaultRelayListLimit
				} else {
					limit = min(limit, maxListLimit)
				}
				return relaysView(r.Msg, in.Offset, limit), nil
			}),

		readTool("relay_candidates", ProfileReadonly,
			procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.RelayServiceListRelayCandidatesProcedure),
			"The servers of other nodes that an entry node could relay, with what the panel knows about reaching them. node is the entry (id or exact name). "+
				"Per server: its inbound_id (what relay_plan create takes), exit node, profile, protocol, network and port; unsupported (empty: it can be relayed; else cdn, acme_ip or build_failed); "+
				"hop (the server has a hop range, a relay passes one port); relay_id (this entry already relays it); "+
				"exit_country_check (how people inside the country see the exit: verdict ok, partial, blocked, listed or no_data, and the dark networks); "+
				"entry_reaches_exit (does the entry itself reach the exit: reach, partial, blocked or no_data, from the TSPU probe when it ran from that entry); "+
				"exit_reaches_entry (does the exit reach the selected bridge endpoint, the way a reverse relay needs: reach, partial, blocked or no_data, from the last relay check of that pair in a day). "+
				"entry_country_check is the same view of the entry itself: a relay helps only where people can see the entry. no_data means unknown, not fine. Never an address. Stored data only.",
			func(c *call, in relayCandidatesArg) (any, error) {
				id, name, err := c.nodeRef(in.Node)
				if err != nil {
					return nil, err
				}
				r, err := c.cl.Relay.ListRelayCandidates(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.ListRelayCandidatesRequest{ViaNodeId: id}))
				if err != nil {
					return nil, apiError(err)
				}
				return relayCandidatesView(id, name, r.Msg), nil
			}),

		readToolWithin(applyTimeout, "relay_check", ProfileAdmin, procs(adminv1connect.RelayServiceCheckRelayProcedure),
			"Check one relay now and store the result: the leg (the entry node tries the exit's server), reverse_reach (the exit node tries the selected bridge endpoint: it runs by itself when the leg fails, reverse_reach=true runs it anyway), and, when the relay is switched on, bridge and path stages. "+
				"It only sends test packets between the owner's nodes: it changes no switch and nothing on any host. It can take up to a minute. "+
				"Switching a relay on needs a leg check made less than 15 minutes earlier that passed or was skipped as unverifiable; this is that check. Returns the relay with its new last_check.",
			func(c *call, in relayCheckArg) (any, error) {
				if err := validID("relay_id", in.RelayID); err != nil {
					return nil, err
				}
				r, err := c.cl.Relay.CheckRelay(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.CheckRelayRequest{Id: in.RelayID, ReverseReach: in.ReverseReach}))
				if err != nil {
					return nil, apiError(err)
				}
				return relayView(r.Msg.GetRelay()), nil
			}),
	}
	return append(out, change(relayChange)...)
}

type relaysListArg struct {
	Node   string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every relay"`
	Offset int    `json:"offset,omitempty" jsonschema:"zero-based offset; continue at next_offset"`
	Limit  int    `json:"limit,omitempty" jsonschema:"page size; defaults to 10, at most 100"`
}

const defaultRelayListLimit = 10

type relayCandidatesArg struct {
	Node string `json:"node" jsonschema:"the entry node, id or exact name"`
}

type relayCheckArg struct {
	RelayID      string `json:"relay_id" jsonschema:"a relay id from relays_list"`
	ReverseReach bool   `json:"reverse_reach,omitempty" jsonschema:"also test whether the exit reaches the entry's site when the direct leg passes"`
}

const (
	relayCreate  = "create"
	relayEnable  = "enable"
	relayDisable = "disable"
	relayPort    = "port"
	relayLeg     = "leg"
	relayDelete  = "delete"
)

type relayArgs struct {
	ReasonField
	Action    string `json:"action" jsonschema:"create, enable, disable, port, leg or delete"`
	Mode      string `json:"mode,omitempty" jsonschema:"auto, dnat or reverse; for create or enable, empty means auto or unchanged"`
	Leg       string `json:"leg,omitempty" jsonschema:"auto, ipv4 or ipv6: the address family of the hop between the entry and the exit (people always reach the entry as they do); for create, empty means auto; for the leg action"`
	RelayID   string `json:"relay_id,omitempty" jsonschema:"the relay (relays_list): for enable, disable, port, leg and delete"`
	Node      string `json:"node,omitempty" jsonschema:"the entry node, id or exact name: for create"`
	InboundID string `json:"inbound_id,omitempty" jsonschema:"the exit's server, an inbound_id from relay_candidates: for create"`
	Port      uint32 `json:"port,omitempty" jsonschema:"the port on the entry, 443 or 1024-65535: required for port; for create, omit it to let the panel choose (the exit's port when it is free on the entry)"`
}

func relayPortOK(p uint32) bool { return p == 443 || (p >= 1024 && p <= 65535) }

// effect words, English; what people see is the same whether the owner or an agent switches the relay.
const (
	relayEntryNote = "The entry node only sees who connects and how much, never contents or keys; accounting, limits and credentials stay on the exit."
	relayOnNote    = "Once it is on, everyone who may use that server and may use the entry node gets a second server marked \"via\" and the entry's country " +
		"in their subscription at its next refresh (every 12 hours by default); the servers they already use do not change."
	relayOffNote = "At their next subscription refresh the second server marked \"via\" leaves the subscriptions; anyone using it has to pick another server."
)

var relayChange = changeSpec[relayArgs]{
	name: "relay", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.RelayServiceListRelaysProcedure,
		adminv1connect.RelayServiceListRelayCandidatesProcedure, adminv1connect.RelayServiceCheckRelayProcedure,
		adminv1connect.RelayServiceCreateRelayProcedure, adminv1connect.RelayServiceUpdateRelayProcedure, adminv1connect.RelayServiceDeleteRelayProcedure),
	desc: "Make, switch on, switch off, move, change the leg family of or delete a relay (action create, enable, disable, port, leg or delete). A relay is a port on an entry node that passes one server of an exit node on, unchanged. An unverifiable check does not block enabling; a failed check does. " +
		"Always needs the owner's approval in the admin panel: it changes what the entry node forwards and what people's subscriptions hold. " +
		"create makes the relay and switches it on after the leg from the entry to the exit and the path from a third node through the entry pass or are unverifiable; a relay that fails stays switched off and the apply reports the stage. " +
		"leg ipv6 makes the hop between the entry and the exit use IPv6 while people keep connecting to the entry over IPv4 (auto tries both); the direct way needs the entry's agent to list relay-forward/2, the bridge only needs the entry's IPv6 address. " +
		"enable runs the same checks. Look at relay_candidates first; check with relay_check; the plan never shows an address.",
	plan: func(c *call, a relayArgs) (*planned, error) {
		if err := relayArgsShape(a); err != nil {
			return nil, err
		}
		if a.Action == relayCreate {
			return c.relayCreatePlan(a)
		}
		return c.relayChangePlan(a)
	},
	apply: func(c *call, a relayArgs, _ Plan) (done, error) {
		switch a.Action {
		case relayCreate:
			r, err := c.cl.Relay.CreateRelay(c.ctx, connect.NewRequest(&adminv1.CreateRelayRequest{InboundId: a.InboundID, ViaNodeId: a.Node, Port: a.Port, Mode: a.Mode, Leg: a.Leg}))
			if err != nil {
				return done{}, apiError(err)
			}
			id := r.Msg.GetRelay().GetId()
			if err := c.relaySwitchOn(id, "Relay "+id+" was made but ", modeOrAuto(a.Mode)); err != nil {
				return done{}, err
			}
			return doneWith("relay_created", "Relay "+id+" made and switched on: the path from outside was checked.", "relay", id), nil
		case relayEnable:
			if err := c.relaySwitchOn(a.RelayID, "Relay ", a.Mode); err != nil {
				return done{}, err
			}
			return doneWith("relay_enabled", "Relay switched on: the path from outside was checked.", "relay", a.RelayID), nil
		case relayDisable:
			off := false
			if _, err := c.cl.Relay.UpdateRelay(c.ctx, connect.NewRequest(&adminv1.UpdateRelayRequest{Id: a.RelayID, Enabled: &off})); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("relay_disabled", "Relay switched off: the entry stops forwarding.", "relay", a.RelayID), nil
		case relayPort:
			if _, err := c.cl.Relay.UpdateRelay(c.ctx, connect.NewRequest(&adminv1.UpdateRelayRequest{Id: a.RelayID, Port: &a.Port})); err != nil {
				return done{}, relayRefusal("Relay ", apiError(err))
			}
			port := strconv.FormatUint(uint64(a.Port), 10)
			return doneWith("relay_moved", "Relay moved to port "+port+".", "relay", a.RelayID, "port", port), nil
		case relayLeg:
			if _, err := c.cl.Relay.UpdateRelay(c.ctx, connect.NewRequest(&adminv1.UpdateRelayRequest{Id: a.RelayID, Leg: &a.Leg})); err != nil {
				return done{}, relayRefusal("Relay ", apiError(err))
			}
			return doneWith("relay_leg", "Relay leg set to "+legOrAuto(a.Leg)+".", "relay", a.RelayID, "leg", a.Leg), nil
		default: // relayDelete: the shape was checked in the plan
			if _, err := c.cl.Relay.DeleteRelay(c.ctx, connect.NewRequest(&adminv1.DeleteRelayRequest{Id: a.RelayID})); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("relay_deleted", "Relay deleted.", "relay", a.RelayID), nil
		}
	},
}

// relayArgsShape: the action and exactly the fields it uses.
func relayArgsShape(a relayArgs) error {
	has := func(s string) bool { return s != "" }
	switch a.Action {
	case relayCreate:
		if !has(a.Node) || !has(a.InboundID) || has(a.RelayID) {
			return errors.New("create takes node (the entry), inbound_id (the exit's server), and optionally port, mode and leg")
		}
		if err := validID("inbound_id", a.InboundID); err != nil {
			return err
		}
	case relayEnable, relayDisable, relayDelete, relayPort, relayLeg:
		if !has(a.RelayID) || has(a.Node) || has(a.InboundID) {
			return errors.New(a.Action + " takes relay_id (and mode for enable, port for port, leg for leg) and nothing else")
		}
		if err := validID("relay_id", a.RelayID); err != nil {
			return err
		}
		if (a.Action == relayPort) != (a.Port != 0) {
			return errors.New("port is given for the port action and for no other")
		}
		if a.Action != relayEnable && a.Mode != "" {
			return errors.New("mode is given for create or enable only")
		}
		if a.Action == relayLeg && a.Leg == "" {
			return errors.New("leg takes a family: auto, ipv4 or ipv6")
		}
	default:
		return errors.New("action must be create, enable, disable, port, leg or delete")
	}
	if a.Mode != "" && a.Mode != "auto" && a.Mode != "dnat" && a.Mode != "reverse" {
		return errors.New("mode must be auto, dnat or reverse")
	}
	if a.Leg != "" && a.Action != relayCreate && a.Action != relayLeg {
		return errors.New("leg is given for create or the leg action only")
	}
	if a.Leg != "" && a.Leg != "auto" && a.Leg != "ipv4" && a.Leg != "ipv6" {
		return errors.New("leg must be auto, ipv4 or ipv6")
	}
	if a.Port != 0 && !relayPortOK(a.Port) {
		return errors.New("port must be 443 or between 1024 and 65535")
	}
	return nil
}

func legOrAuto(leg string) string {
	if leg == "" {
		return "auto"
	}
	return leg
}

func countryText(c *adminv1.RelayCountryHint) string {
	if c == nil || c.GetVerdict() == "" {
		return "no_data"
	}
	s := clean(c.GetVerdict(), 12)
	if len(c.GetDark()) > 0 {
		dark := make([]string, 0, 4)
		for i, d := range c.GetDark() {
			if i == 4 {
				break
			}
			dark = append(dark, nm(d))
		}
		s += ", dark for " + strings.Join(dark, ", ")
	}
	return s
}

func (c *call) relayCreatePlan(a relayArgs) (*planned, error) {
	nodes, err := c.listNodes()
	if err != nil {
		return nil, err
	}
	entry, err := resolveNode(nodes, a.Node)
	if err != nil {
		return nil, err
	}
	id, name := entry.GetId(), entry.GetName()
	r, err := c.cl.Relay.ListRelayCandidates(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.ListRelayCandidatesRequest{ViaNodeId: id}))
	if err != nil {
		return nil, apiError(err)
	}
	var cand *adminv1.RelayCandidate
	for _, x := range r.Msg.GetCandidates() {
		if x.GetInboundId() == a.InboundID {
			cand = x
		}
	}
	switch {
	case cand == nil:
		return nil, errors.New("that server is not one the entry can relay: relay_candidates lists them")
	case cand.GetUnsupported() != "":
		return nil, errors.New("that server cannot be relayed: " + clean(cand.GetUnsupported(), 24))
	case cand.GetRelayId() != "":
		return nil, errors.New("this entry already relays that server (relay " + clean(cand.GetRelayId(), 80) + "): use enable, disable, port or delete on it")
	case !r.Msg.GetAgentSupported():
		return nil, errors.New("the entry node's agent is too old to forward: update it first")
	}
	port := "chosen by the panel: the exit's port " + strconv.Itoa(int(cand.GetPort())) + " when it is free on the entry, else a free one"
	if cand.GetProtocol() == "awg" {
		port = "chosen by the panel: the profile's disguise port when it is free and supported, else the exit's port " + strconv.Itoa(int(cand.GetPort())) + " when free, else another free one"
	}
	if a.Port != 0 {
		port = strconv.Itoa(int(a.Port))
	}
	facts := []Fact{
		{Key: "action", Value: relayCreate},
		{Key: "entry", Value: nm(name), Untrusted: true},
		{Key: "exit", Value: nm(cand.GetNodeName()), Untrusted: true},
		{Key: "server", Value: nm(cand.GetProfileName()) + " (" + clean(cand.GetProtocol(), 40) + ", " + clean(cand.GetNetwork(), 8) + " " + strconv.Itoa(int(cand.GetPort())) + ")", Untrusted: true},
		{Key: "entry_port", Value: port},
		{Key: "leg", Value: legOrAuto(a.Leg)},
		{Key: "exit_seen_from_inside", Value: countryText(cand.GetCountry()), Untrusted: true},
		{Key: "entry_seen_from_inside", Value: countryText(r.Msg.GetEntryCountry()), Untrusted: true},
		{Key: "entry_reaches_exit", Value: relayReachText(cand.GetReach())},
	}
	if cand.GetHop() {
		facts = append(facts, Fact{Key: "warning", Value: "the server has a hop range; the relay passes its one port only"})
	}
	mode := modeOrAuto(a.Mode)
	facts = append(facts, Fact{Key: "mode", Value: mode})
	if rb := cand.GetReverseBlocked(); rb != "" {
		facts = append(facts, Fact{Key: "reverse_bridge", Value: "unavailable: " + reverseBlockedText(rb)})
	} else {
		facts = append(facts, Fact{Key: "reverse_bridge", Value: "possible: both agents can run the bridge"},
			Fact{Key: "reverse_reach", Value: relayReachFreshness(cand.GetExitReachesEntry(), time.Now())})
	}
	if cand.GetProtocol() == "awg" {
		facts = append(facts, Fact{Key: "awg_path_keys", Value: "People allowed to use both nodes get their own AmneziaWG relay key. AmneziaVPN users must download and import the new config; apps using a subscription link get it on refresh."})
	}
	for _, exit := range nodes {
		if exit.GetId() == cand.GetNodeId() && entry.GetCountryCode() != "" && exit.GetCountryCode() != "" && strings.EqualFold(entry.GetCountryCode(), exit.GetCountryCode()) {
			facts = append(facts, Fact{Key: "warning", Value: "entry and exit are in the same country"})
			break
		}
	}
	checks := "auto checks the entry-to-exit leg; for TCP, when that fails it also checks whether the exit reaches the entry. It enables direct when the leg passes, or through the bridge when reverse reach passes, then checks the path from a third node. A failed stage switches the relay off."
	if mode == "dnat" {
		checks = "dnat checks the entry-to-exit leg, then a third node reaches the exit through the entry (path). A failed stage switches the relay off."
	} else if mode == "reverse" {
		checks = "reverse requires both agents to support the bridge, a TCP server, and reverse reach passed within 15 minutes. After enabling, the panel waits for the bridge and checks the path from a third node. A failed stage switches the relay off."
	}
	facts = append(facts,
		Fact{Key: "checks", Value: checks},
		Fact{Key: "effect", Value: relayEntryNote + " " + relayOnNote})
	return &planned{
		Summary: "Make a relay on one entry node for one server of an exit node, and switch it on if its checks pass or cannot verify it. The entry node gets one forwarding rule for its port. The owner has to approve it first.",
		Facts:   facts, Danger: []string{dangerFleet},
		Params: relayArgs{Action: relayCreate, Node: id, InboundID: a.InboundID, Port: a.Port, Mode: a.Mode, Leg: a.Leg},
	}, nil
}

// reverseBlockedText says in words why a relay cannot go through the bridge (relay.proto reverse_blocked).
func reverseBlockedText(reason string) string {
	switch reason {
	case "udp":
		return "UDP cannot use the bridge; auto uses DNAT"
	case "entry_agent_old":
		return "the entry's agent is too old (no relay-reverse/1): update it"
	case "exit_agent_old":
		return "the exit's agent is too old (no relay-reverse/1): update it"
	}
	return clean(reason, 32)
}

func modeOrAuto(mode string) string {
	if mode == "" {
		return "auto"
	}
	return mode
}

func relayReachFreshness(h *adminv1.RelayReachHint, now time.Time) string {
	if h == nil || h.GetAtUnix() == 0 || h.GetVerdict() == "no_data" {
		return "unknown; reverse requires an exit-to-entry check passed within 15 minutes"
	}
	if now.Unix() < h.GetAtUnix() || now.Unix()-h.GetAtUnix() > int64(15*time.Minute/time.Second) {
		return "stale; reverse requires an exit-to-entry check passed within 15 minutes"
	}
	return clean(h.GetVerdict(), 12) + " at " + fmtTime(h.GetAtUnix())
}

func relayStageFreshness(stage *adminv1.RelayStage, now time.Time) string {
	if stage == nil || stage.GetAtUnix() == 0 || stage.GetStatus() == "" {
		return "unknown; reverse needs a passing check from the exit within 15 minutes"
	}
	if now.Unix() < stage.GetAtUnix() || now.Unix()-stage.GetAtUnix() > int64(15*time.Minute/time.Second) {
		return "stale; reverse needs a passing check from the exit within 15 minutes"
	}
	return clean(stage.GetStatus(), 12) + " " + clean(stage.GetCode(), 40) + " at " + fmtTime(stage.GetAtUnix())
}

func relayReachText(x *adminv1.RelayReachHint) string {
	if x == nil || x.GetVerdict() == "" {
		return "no_data"
	}
	return clean(x.GetVerdict(), 12) + " (" + clean(x.GetSource(), 20) + ")"
}

func relayCheckText(r *adminv1.Relay) string {
	ck := r.GetCheck()
	if ck == nil {
		return "never checked"
	}
	stage := func(name string, s *adminv1.RelayStage) string {
		if s.GetStatus() == "" {
			return name + " did not run"
		}
		t := name + " " + clean(s.GetStatus(), 12)
		if s.GetCode() != "" {
			t += " (" + clean(s.GetCode(), 40) + ")"
		}
		return t
	}
	var parts []string
	if ck.GetAtUnix() > 0 {
		parts = append(parts, fmtTime(ck.GetAtUnix())+": "+stage("leg", ck.GetLeg())+", "+stage("path", ck.GetPath()))
	}
	if ck.GetReverseReach().GetStatus() != "" { // the exit's own probe of the entry's site; only when it ran
		parts = append(parts, stage("reverse_reach", ck.GetReverseReach())+" at "+fmtTime(ck.GetReverseReach().GetAtUnix()))
	}
	if ck.GetBridge().GetStatus() != "" {
		parts = append(parts, stage("bridge", ck.GetBridge()))
	}
	if len(parts) == 0 {
		return "never checked"
	}
	prefix := ""
	if ck.GetAtUnix() == 0 {
		prefix = "no full check yet: "
	}
	return prefix + strings.Join(parts, ", ")
}

func (c *call) relayChangePlan(a relayArgs) (*planned, error) {
	l, err := c.cl.Relay.ListRelays(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.ListRelaysRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	var r *adminv1.Relay
	for _, x := range l.Msg.GetRelays() {
		if x.GetId() == a.RelayID {
			r = x
		}
	}
	if r == nil {
		return nil, errors.New("no such relay")
	}
	facts := []Fact{
		{Key: "action", Value: a.Action},
		{Key: "mode", Value: modeOrAuto(a.Mode)},
		{Key: "entry", Value: nm(r.GetViaNodeName()), Untrusted: true},
		{Key: "exit", Value: nm(r.GetExitNodeName()), Untrusted: true},
		{Key: "server", Value: nm(r.GetProfileName()) + " (" + clean(r.GetProtocol(), 40) + ", " + clean(r.GetNetwork(), 8) + " " + strconv.Itoa(int(r.GetExitPort())) + ")", Untrusted: true},
		{Key: "entry_port", Value: strconv.Itoa(int(r.GetPort()))},
		{Key: "switched_on", Value: strconv.FormatBool(r.GetEnabled())},
		{Key: "last_check", Value: relayCheckText(r)},
	}
	var summary, effect string
	switch a.Action {
	case relayEnable:
		switch {
		case r.GetEnabled():
			return nil, errors.New("the relay is already switched on")
		case !r.GetAgentSupported():
			return nil, errors.New("the entry node's agent is too old to forward: update it first")
		case !r.GetExitLive():
			return nil, errors.New("the exit's server is not usable now (switched off, failed or its node is not enrolled): fix that first")
		}
		requestedMode := a.Mode
		if requestedMode == "" {
			requestedMode = r.GetMode()
		}
		facts = append(facts, Fact{Key: "requested_mode", Value: modeOrAuto(requestedMode)})
		if requestedMode == "reverse" || requestedMode == "auto" {
			if r.GetReverseBlocked() != "" {
				facts = append(facts, Fact{Key: "reverse_bridge", Value: "unavailable: " + reverseBlockedText(r.GetReverseBlocked())})
			} else {
				facts = append(facts, Fact{Key: "reverse_bridge", Value: "possible: both agents can run the bridge"})
				if ck := r.GetCheck(); ck != nil {
					facts = append(facts, Fact{Key: "reverse_reach", Value: relayStageFreshness(ck.GetReverseReach(), time.Now())})
				} else {
					facts = append(facts, Fact{Key: "reverse_reach", Value: "unknown; reverse needs a passing check from the exit within 15 minutes"})
				}
			}
		}
		summary = "Switch one relay on if its checks pass or are unverifiable: the entry tries the exit's server, then a third node reaches the exit through the entry. The owner has to approve it first."
		effect = relayEntryNote + " " + relayOnNote
	case relayDisable:
		if !r.GetEnabled() {
			return nil, errors.New("the relay is already switched off")
		}
		summary = "Switch one relay off: the entry node stops forwarding its port. The owner has to approve it first."
		effect = relayOffNote
	case relayPort:
		if a.Port == r.GetPort() {
			return nil, errors.New("the relay is already on that port")
		}
		for i := range facts {
			if facts[i].Key == "entry_port" {
				facts[i] = Fact{Key: "entry_port", Value: strconv.Itoa(int(r.GetPort())) + " -> " + strconv.Itoa(int(a.Port))}
				break
			}
		}
		summary = "Move one relay to another port on its entry node. The owner has to approve it first."
		effect = "People who use the second server marked \"via\" need their subscription refreshed to get the new port."
		if r.GetEnabled() {
			effect += " A relay that is on installs the new forward and the path is checked from outside; if that fails the relay is switched off."
		}
	case relayLeg:
		if a.Leg == r.GetLeg() {
			return nil, errors.New("the relay already uses that leg family")
		}
		facts = append(facts, Fact{Key: "leg", Value: legOrAuto(r.GetLeg()) + " -> " + a.Leg},
			Fact{Key: "active_leg", Value: r.GetActiveLeg()},
			Fact{Key: "exit_has_ipv6", Value: "an IPv6 leg needs the other node's IPv6 address, proved by a check from this side: the exit's for the direct way, the entry's for the bridge"})
		summary = "Change the address family of the hop between the entry and the exit. People keep connecting to the entry as they do; only the entry-to-exit leg moves. The owner has to approve it first."
		effect = "A relay that is on installs the new forward and the path is checked from outside; if that fails the relay is switched off. Auto follows the leg that works by itself, at most once per half hour."
	default: // relayDelete
		summary = "Delete one relay: the entry node stops forwarding its port. The owner has to approve it first."
		effect = relayOffNote
	}
	facts = append(facts, Fact{Key: "effect", Value: effect})
	params := a
	if params.Action == relayEnable && params.Mode == "" {
		params.Mode = r.GetMode()
	}
	return &planned{Summary: summary, Facts: facts, Danger: []string{dangerFleet}, Params: params}, nil
}

// relaySwitchOn checks the leg of a relay, then switches it on (the panel checks the path from outside and switches it off
// again when that fails). A relay that does not end up on is a failure whose text starts with prefix and names the stage.
func (c *call) relaySwitchOn(id, prefix, mode string) error {
	chk, err := c.cl.Relay.CheckRelay(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.CheckRelayRequest{Id: id}))
	if err != nil {
		return apiError(err)
	}
	ck := chk.Msg.GetRelay().GetCheck()
	legOK := ck.GetLeg().GetStatus() == "ok" || ck.GetLeg().GetStatus() == "skipped" && ck.GetLeg().GetCode() == "unverifiable"
	if !legOK && mode != "reverse" {
		if mode != "auto" || ck.GetReverseReach().GetStatus() != "ok" {
			// the leg is what failed and its code is what the owner has always been told; the bridge was tried too, and the
			// relay's last check has what it found
			return relayCheckFailure(prefix, "leg", ck.GetLeg().GetCode())
		}
	}
	on := true
	update := &adminv1.UpdateRelayRequest{Id: id, Enabled: &on}
	if mode != "" {
		update.Mode = &mode
	}
	if _, err := c.cl.Relay.UpdateRelay(c.ctx, connect.NewRequest(update)); err != nil {
		return relayRefusal(prefix, apiError(err))
	}
	return nil
}

func relayCheckFailure(prefix, stage, code string) error {
	stage, code = clean(stage, 32), clean(code, 40)
	if code == "" {
		code = "unknown"
	}
	return failure("relay_check_failed", prefix+"stays switched off: the "+stage+" check failed ("+code+"). The result is in the relay's last check; the owner can retry or delete it.",
		"stage", stage, "code", code)
}

// relayRefusal words the panel's refusals of a relay that is not on (err is an apiError); any other error stays as it is.
func relayRefusal(prefix string, err error) error {
	o := outcomeOfError(err)
	switch o.Code {
	case "relay_check_failed":
		return relayCheckFailure(prefix, o.Params["stage"], o.Params["code"])
	case "relay_unchecked", "relay_check_stale":
		return failure(o.Code, prefix+"stays switched off: it needs a leg check made less than 15 minutes ago. Run relay_check and make a new plan.")
	case "reverse_unavailable":
		return failure(o.Code, prefix+"stays switched off: the reverse bridge is unavailable ("+clean(o.Params["reason"], 48)+"). Update the agents or choose auto/DNAT after checking the reverse path.", "reason", clean(o.Params["reason"], 48))
	}
	return err
}
