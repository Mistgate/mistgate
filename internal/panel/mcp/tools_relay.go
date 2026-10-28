package mcp

import (
	"errors"
	"strconv"
	"strings"

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
				"Per relay: the entry and exit nodes, the exit's server (profile, protocol, network), the port on the entry and on the exit, whether it is switched on, "+
				"what the entry's agent reported (state pending, active or failed, with the first word of its error), whether the exit's server is usable and the entry's agent can take the forward, "+
				"and the last check: the leg (the entry reaches the exit) and the path (a third node reaches the exit through the entry), each ok, fail or skipped with its code, the node that sent the probe and, for UDP, the packets sent and counted. "+
				"Optional node (id or exact name) keeps the relays that have it at either end. Never an address of a node. Stored data only: it contacts no node.",
			func(c *call, in relaysListArg) (any, error) {
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
				return relaysView(r.Msg), nil
			}),

		readTool("relay_candidates", ProfileReadonly,
			procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.RelayServiceListRelayCandidatesProcedure),
			"The servers of other nodes that an entry node could relay, with what the panel knows about reaching them. node is the entry (id or exact name). "+
				"Per server: its inbound_id (what relay_plan create takes), exit node, profile, protocol, network and port; unsupported (empty: it can be relayed; else cdn, acme_ip, awg or build_failed); "+
				"hop (the server has a hop range, a relay passes one port); relay_id (this entry already relays it); "+
				"exit_country_check (how people inside the country see the exit: verdict ok, partial, blocked, listed or no_data, and the dark networks); "+
				"entry_reaches_exit (does the entry itself reach the exit: reach, partial, blocked or no_data, from the TSPU probe when it ran from that entry). "+
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
			"Check one relay now and store the result: the leg (the entry node tries the exit's server) and, when the relay is switched on, the path (a third node reaches the exit through the entry's port). "+
				"It only sends test packets between the owner's nodes: it changes no switch and nothing on any host. It can take up to a minute. "+
				"Switching a relay on needs a passing leg check made less than 15 minutes earlier; this is that check. Returns the relay with its new last_check.",
			func(c *call, in relayCheckArg) (any, error) {
				if err := validID("relay_id", in.RelayID); err != nil {
					return nil, err
				}
				r, err := c.cl.Relay.CheckRelay(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.CheckRelayRequest{Id: in.RelayID}))
				if err != nil {
					return nil, apiError(err)
				}
				return relayView(r.Msg.GetRelay()), nil
			}),
	}
	return append(out, change(relayChange)...)
}

type relaysListArg struct {
	Node string `json:"node,omitempty" jsonschema:"node id or exact name; empty = every relay"`
}

type relayCandidatesArg struct {
	Node string `json:"node" jsonschema:"the entry node, id or exact name"`
}

type relayCheckArg struct {
	RelayID string `json:"relay_id" jsonschema:"a relay id from relays_list"`
}

const (
	relayCreate  = "create"
	relayEnable  = "enable"
	relayDisable = "disable"
	relayPort    = "port"
	relayDelete  = "delete"
)

type relayArgs struct {
	ReasonField
	Action    string `json:"action" jsonschema:"create, enable, disable, port or delete"`
	RelayID   string `json:"relay_id,omitempty" jsonschema:"the relay (relays_list): for enable, disable, port and delete"`
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
	desc: "Make, switch on, switch off, move or delete a relay (action create, enable, disable, port or delete). A relay is a port on an entry node that passes one server of an exit node on, unchanged. " +
		"Always needs the owner's approval in the admin panel: it changes what the entry node forwards and what people's subscriptions hold. " +
		"create makes the relay and switches it on only if the checks pass (the leg from the entry to the exit, then the path from a third node through the entry); a relay that fails stays switched off and the apply reports the stage. " +
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
			r, err := c.cl.Relay.CreateRelay(c.ctx, connect.NewRequest(&adminv1.CreateRelayRequest{InboundId: a.InboundID, ViaNodeId: a.Node, Port: a.Port}))
			if err != nil {
				return done{}, apiError(err)
			}
			id := r.Msg.GetRelay().GetId()
			if err := c.relaySwitchOn(id, "Relay "+id+" was made but "); err != nil {
				return done{}, err
			}
			return doneWith("relay_created", "Relay "+id+" made and switched on: the path from outside was checked.", "relay", id), nil
		case relayEnable:
			if err := c.relaySwitchOn(a.RelayID, "Relay "); err != nil {
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
			return errors.New("create takes node (the entry) and inbound_id (the exit's server), and optionally port")
		}
		if err := validID("inbound_id", a.InboundID); err != nil {
			return err
		}
	case relayEnable, relayDisable, relayDelete, relayPort:
		if !has(a.RelayID) || has(a.Node) || has(a.InboundID) {
			return errors.New(a.Action + " takes relay_id (and port for the port action) and nothing else")
		}
		if err := validID("relay_id", a.RelayID); err != nil {
			return err
		}
		if (a.Action == relayPort) != (a.Port != 0) {
			return errors.New("port is given for the port action and for no other")
		}
	default:
		return errors.New("action must be create, enable, disable, port or delete")
	}
	if a.Port != 0 && !relayPortOK(a.Port) {
		return errors.New("port must be 443 or between 1024 and 65535")
	}
	return nil
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
	id, name, err := c.nodeRef(a.Node)
	if err != nil {
		return nil, err
	}
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
	if a.Port != 0 {
		port = strconv.Itoa(int(a.Port))
	}
	facts := []Fact{
		{Key: "action", Value: relayCreate},
		{Key: "entry", Value: nm(name), Untrusted: true},
		{Key: "exit", Value: nm(cand.GetNodeName()), Untrusted: true},
		{Key: "server", Value: nm(cand.GetProfileName()) + " (" + clean(cand.GetProtocol(), 40) + ", " + clean(cand.GetNetwork(), 8) + " " + strconv.Itoa(int(cand.GetPort())) + ")", Untrusted: true},
		{Key: "entry_port", Value: port},
		{Key: "exit_seen_from_inside", Value: countryText(cand.GetCountry()), Untrusted: true},
		{Key: "entry_seen_from_inside", Value: countryText(r.Msg.GetEntryCountry()), Untrusted: true},
		{Key: "entry_reaches_exit", Value: relayReachText(cand.GetReach())},
	}
	if cand.GetHop() {
		facts = append(facts, Fact{Key: "warning", Value: "the server has a hop range; the relay passes its one port only"})
	}
	facts = append(facts,
		Fact{Key: "checks", Value: "after it is made the entry tries the exit's server (leg), then the relay is switched on and a third node reaches the exit through the entry (path); if either fails the relay stays switched off"},
		Fact{Key: "effect", Value: relayEntryNote + " " + relayOnNote})
	return &planned{
		Summary: "Make a relay on one entry node for one server of an exit node, and switch it on if its checks pass. The entry node gets one forwarding rule for its port. The owner has to approve it first.",
		Facts:   facts, Danger: []string{dangerFleet},
		Params: relayArgs{Action: relayCreate, Node: id, InboundID: a.InboundID, Port: a.Port},
	}, nil
}

func relayReachText(x *adminv1.RelayReachHint) string {
	if x == nil || x.GetVerdict() == "" {
		return "no_data"
	}
	return clean(x.GetVerdict(), 12) + " (" + clean(x.GetSource(), 20) + ")"
}

func relayCheckText(r *adminv1.Relay) string {
	ck := r.GetCheck()
	if ck == nil || ck.GetAtUnix() == 0 {
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
	return fmtTime(ck.GetAtUnix()) + ": " + stage("leg", ck.GetLeg()) + ", " + stage("path", ck.GetPath())
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
		summary = "Switch one relay on if its checks pass: the entry tries the exit's server, then a third node reaches the exit through the entry. The owner has to approve it first."
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
		facts[3] = Fact{Key: "entry_port", Value: strconv.Itoa(int(r.GetPort())) + " -> " + strconv.Itoa(int(a.Port))}
		summary = "Move one relay to another port on its entry node. The owner has to approve it first."
		effect = "People who use the second server marked \"via\" need their subscription refreshed to get the new port."
		if r.GetEnabled() {
			effect += " A relay that is on installs the new forward and the path is checked from outside; if that fails the relay is switched off."
		}
	default: // relayDelete
		summary = "Delete one relay: the entry node stops forwarding its port. The owner has to approve it first."
		effect = relayOffNote
	}
	facts = append(facts, Fact{Key: "effect", Value: effect})
	return &planned{Summary: summary, Facts: facts, Danger: []string{dangerFleet}, Params: a}, nil
}

// relaySwitchOn checks the leg of a relay, then switches it on (the panel checks the path from outside and switches it off
// again when that fails). A relay that does not end up on is a failure whose text starts with prefix and names the stage.
func (c *call) relaySwitchOn(id, prefix string) error {
	chk, err := c.cl.Relay.CheckRelay(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.CheckRelayRequest{Id: id}))
	if err != nil {
		return apiError(err)
	}
	if leg := chk.Msg.GetRelay().GetCheck().GetLeg(); leg.GetStatus() != "ok" {
		return relayCheckFailure(prefix, "leg", leg.GetCode())
	}
	on := true
	if _, err := c.cl.Relay.UpdateRelay(c.ctx, connect.NewRequest(&adminv1.UpdateRelayRequest{Id: id, Enabled: &on})); err != nil {
		return relayRefusal(prefix, apiError(err))
	}
	return nil
}

func relayCheckFailure(prefix, stage, code string) error {
	stage, code = clean(stage, 8), clean(code, 40)
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
	}
	return err
}
