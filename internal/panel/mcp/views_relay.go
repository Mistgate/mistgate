package mcp

import (
	"strings"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The relay views. The raw Relay carries the address the exit was resolved to (to_ip) and RelayCheck the "address:port" a
// check was made for (to): neither is copied, and a node's own error text (last_error) is cut to its code, the word before
// the first colon, because the detail after it may name an address.

type relayStageV struct {
	Status  string `json:"status,omitempty"` // ok, fail, skipped; empty: did not run
	Code    string `json:"code,omitempty"`
	Sender  string `json:"sender,omitempty"` // the node that sent the probe, or "panel"
	Verdict string `json:"verdict,omitempty"`
	Sent    uint32 `json:"sent,omitempty"`
	Got     uint32 `json:"got,omitempty"`
	AtUnix  int64  `json:"at_unix,omitempty"`
}

type relayCheckV struct {
	AtUnix   int64        `json:"at_unix"`
	Network  string       `json:"network"`
	Port     uint32       `json:"port"`
	OK       bool         `json:"ok"`
	Stage    string       `json:"failed_stage,omitempty"` // leg or path
	Verified bool         `json:"verified"`               // both stages ran and passed: it works from outside
	Leg      *relayStageV `json:"leg,omitempty"`
	Path     *relayStageV `json:"path,omitempty"`
}

type relayV struct {
	ID             string       `json:"id"`
	EntryNodeID    string       `json:"entry_node_id"`
	EntryNode      string       `json:"entry_node"`
	ExitNodeID     string       `json:"exit_node_id"`
	ExitNode       string       `json:"exit_node"`
	InboundID      string       `json:"inbound_id"`
	ProfileID      string       `json:"profile_id"`
	Profile        string       `json:"profile"`
	Protocol       string       `json:"protocol"`
	Network        string       `json:"network"`
	Port           uint32       `json:"port"`      // on the entry
	ExitPort       uint32       `json:"exit_port"` // of the server on the exit
	Enabled        bool         `json:"enabled"`
	State          string       `json:"state"` // what the entry's agent reported: pending, active, failed
	LastErrorCode  string       `json:"last_error_code,omitempty"`
	ExitLive       bool         `json:"exit_live"`       // the exit's server is usable now
	AgentSupported bool         `json:"agent_supported"` // the entry's agent can take the forward
	CreatedUnix    int64        `json:"created_unix"`
	AppliedUnix    int64        `json:"applied_unix,omitempty"`
	Check          *relayCheckV `json:"last_check,omitempty"`
}

func relayStageView(s *adminv1.RelayStage) *relayStageV {
	if s == nil || s.GetStatus() == "" {
		return nil
	}
	return &relayStageV{Status: clean(s.GetStatus(), 12), Code: clean(s.GetCode(), 40), Sender: nm(s.GetSenderName()),
		Verdict: clean(s.GetVerdict(), 12), Sent: s.GetSent(), Got: s.GetGot(), AtUnix: s.GetAtUnix()}
}

func relayView(r *adminv1.Relay) relayV {
	v := relayV{
		ID: clean(r.GetId(), 80), EntryNodeID: clean(r.GetViaNodeId(), 80), EntryNode: nm(r.GetViaNodeName()),
		ExitNodeID: clean(r.GetExitNodeId(), 80), ExitNode: nm(r.GetExitNodeName()), InboundID: clean(r.GetInboundId(), 80),
		ProfileID: clean(r.GetProfileId(), 80), Profile: nm(r.GetProfileName()), Protocol: clean(r.GetProtocol(), 40),
		Network: clean(r.GetNetwork(), 8), Port: r.GetPort(), ExitPort: r.GetExitPort(), Enabled: r.GetEnabled(),
		State:         enumName("INBOUND_STATE_", r.GetState().String()),
		ExitLive:      r.GetExitLive(),
		LastErrorCode: errorCode(r.GetLastError()), AgentSupported: r.GetAgentSupported(),
		CreatedUnix: r.GetCreatedUnix(), AppliedUnix: r.GetAppliedUnix(),
	}
	if c := r.GetCheck(); c != nil && c.GetAtUnix() != 0 {
		v.Check = &relayCheckV{AtUnix: c.GetAtUnix(), Network: clean(c.GetNetwork(), 8), Port: c.GetPort(), OK: c.GetOk(),
			Stage: clean(c.GetStage(), 8), Verified: c.GetVerified(), Leg: relayStageView(c.GetLeg()), Path: relayStageView(c.GetPath())}
	}
	return v
}

// errorCode is the word an agent's error text starts with ("refused: dial 203.0.113.9" -> "refused").
func errorCode(s string) string {
	head, _, _ := strings.Cut(s, ":")
	return clean(head, 40)
}

// RelaysV is the relays_list result.
type RelaysV struct {
	Relays    []relayV `json:"relays"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (v *RelaysV) shrink() bool {
	if cut(&v.Relays) {
		v.Truncated = true
		return true
	}
	return false
}

func relaysView(r *adminv1.ListRelaysResponse) *RelaysV {
	v := &RelaysV{Relays: []relayV{}}
	for _, x := range r.GetRelays() {
		if x != nil {
			v.Relays = append(v.Relays, relayView(x))
		}
	}
	return v
}

type relayCountryV struct {
	Verdict string   `json:"verdict"` // ok, partial, blocked, listed or no_data
	Dark    []string `json:"dark_networks,omitempty"`
	AtUnix  int64    `json:"at_unix,omitempty"`
}

type relayReachV struct {
	Verdict string `json:"verdict"` // reach, partial, blocked or no_data
	Source  string `json:"source,omitempty"`
	AtUnix  int64  `json:"at_unix,omitempty"`
}

type relayCandidateV struct {
	InboundID   string         `json:"inbound_id"`
	ExitNodeID  string         `json:"exit_node_id"`
	ExitNode    string         `json:"exit_node"`
	ProfileID   string         `json:"profile_id"`
	Profile     string         `json:"profile"`
	Protocol    string         `json:"protocol"`
	Network     string         `json:"network"`
	ExitPort    uint32         `json:"exit_port"`
	Unsupported string         `json:"unsupported,omitempty"` // empty: it can be relayed
	Hop         bool           `json:"hop,omitempty"`         // the server has a hop range; a relay passes one port
	RelayID     string         `json:"relay_id,omitempty"`    // this entry already relays it
	Country     *relayCountryV `json:"exit_country_check,omitempty"`
	Reach       *relayReachV   `json:"entry_reaches_exit,omitempty"`
}

// RelayCandidatesV is the relay_candidates result.
type RelayCandidatesV struct {
	EntryNodeID    string            `json:"entry_node_id"`
	EntryNode      string            `json:"entry_node"`
	AgentSupported bool              `json:"agent_supported"`
	EntryCountry   *relayCountryV    `json:"entry_country_check,omitempty"`
	Candidates     []relayCandidateV `json:"candidates"`
	Truncated      bool              `json:"truncated,omitempty"`
}

func (v *RelayCandidatesV) shrink() bool {
	if cut(&v.Candidates) {
		v.Truncated = true
		return true
	}
	return false
}

func relayCountryView(c *adminv1.RelayCountryHint) *relayCountryV {
	if c == nil {
		return nil
	}
	v := &relayCountryV{Verdict: clean(c.GetVerdict(), 12), AtUnix: c.GetAtUnix()}
	for i, d := range c.GetDark() {
		if i == 8 {
			break
		}
		v.Dark = append(v.Dark, nm(d))
	}
	return v
}

func relayCandidatesView(entryID, entryName string, r *adminv1.ListRelayCandidatesResponse) *RelayCandidatesV {
	v := &RelayCandidatesV{EntryNodeID: entryID, EntryNode: nm(entryName), AgentSupported: r.GetAgentSupported(),
		EntryCountry: relayCountryView(r.GetEntryCountry()), Candidates: []relayCandidateV{}}
	for _, c := range r.GetCandidates() {
		if c == nil {
			continue
		}
		row := relayCandidateV{
			InboundID: clean(c.GetInboundId(), 80), ExitNodeID: clean(c.GetNodeId(), 80), ExitNode: nm(c.GetNodeName()),
			ProfileID: clean(c.GetProfileId(), 80), Profile: nm(c.GetProfileName()), Protocol: clean(c.GetProtocol(), 40),
			Network: clean(c.GetNetwork(), 8), ExitPort: c.GetPort(), Unsupported: clean(c.GetUnsupported(), 24), Hop: c.GetHop(),
			RelayID: clean(c.GetRelayId(), 80), Country: relayCountryView(c.GetCountry()),
		}
		if x := c.GetReach(); x != nil {
			row.Reach = &relayReachV{Verdict: clean(x.GetVerdict(), 12), Source: clean(x.GetSource(), 20), AtUnix: x.GetAtUnix()}
		}
		v.Candidates = append(v.Candidates, row)
	}
	return v
}
