package fleet

import (
	"context"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The panel's side of the country probe (agent.proto "COUNTRY PROBE"): a node inside a country tests whether TCP SYNs to the
// other nodes of the fleet survive the national DPI. This file only sends the command to one node and parses the answer;
// choosing the node, the targets and the schedule, and storing the result, belong to the scheduler (step E).

const (
	capCountryProbe = "country-probe/1"
	// countryProbeWait is the agent's own total deadline (120 s) and a margin for the answer.
	countryProbeWait = 130 * time.Second
)

// CountryProbeTarget is one fleet node as the panel knows it: node.address and the TCP ports to test.
type CountryProbeTarget struct {
	Host  string
	Ports []uint16
}

// CountryProbeRequest is what the scheduler asks of a probe node. HopLimit and MaxTTL are 0 for the agent's defaults (3 and 30).
type CountryProbeRequest struct {
	Control    string // an IPv4 address that is not blocked in the country (RU: 77.88.55.88)
	ControlSNI string // reserved, the SYN method sends no hello
	BlockedRef string // an IPv4 address that is blocked in the country; "" = the agent answers no_dpi_hop
	Targets    []CountryProbeTarget
	HopLimit   uint32
	MaxTTL     uint32
}

// CountryProbeResult is the agent's answer. Code is "" when the probe ran; otherwise the agent's word for why it did not:
// control_failed (the method does not apply on that network, no verdict about any node; DPIHop is set), no_dpi_hop, busy,
// bad_params, unsupported_host, unsupported, or failed. Verdicts maps "host:port" to blocked | open | refused | timeout,
// for the pairs the agent answered.
type CountryProbeResult struct {
	DPIHop    int
	ControlOK bool
	Verdicts  map[string]string
	Code      string
}

// CountryProbe asks one node to run the probe and returns its answer. An error means no answer at all: the node is offline,
// its agent does not list country-probe/1 ("agent too old"), the link was lost or the 130 s ran out.
func (f *Fleet) CountryProbe(ctx context.Context, nodeID string, req CountryProbeRequest) (CountryProbeResult, error) {
	wire := make([]*agentv1.CountryProbeTarget, len(req.Targets))
	for i, t := range req.Targets {
		wire[i] = &agentv1.CountryProbeTarget{Host: t.Host, Ports: uint32Ports(t.Ports)}
	}
	result, err := f.command(ctx, nodeID, countryProbeWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_CountryProbe{CountryProbe: &agentv1.CountryProbe{
			RequestId: reqID, ControlTarget: req.Control, ControlSni: req.ControlSNI, BlockedRef: req.BlockedRef, Targets: wire,
			HopLimit: req.HopLimit, MaxTtl: req.MaxTTL,
		}}}
	})
	if err != nil {
		return CountryProbeResult{}, err
	}
	return parseCountryProbe(result, req), nil
}

// parseCountryProbe reads a CommandResult of CountryProbe. A verdict that is not one of the four words is left out.
func parseCountryProbe(result *agentv1.CommandResult, req CountryProbeRequest) CountryProbeResult {
	out := CountryProbeResult{}
	if result == nil {
		out.Code = "failed"
		return out
	}
	out.DPIHop, _ = strconv.Atoi(result.Params["dpi_hop"])
	out.ControlOK = result.Params["control"] == "ok"
	if !result.Ok {
		out.Code = countryProbeCode(result.Error)
		return out
	}
	out.Verdicts = make(map[string]string)
	for _, t := range req.Targets {
		for _, port := range t.Ports {
			key := t.Host + ":" + strconv.Itoa(int(port))
			if v := result.Params["t"+key]; validCountryVerdict(v) {
				out.Verdicts[key] = v
			}
		}
	}
	return out
}

// countryProbeCode keeps the agent's words and flattens "failed: <detail>" (the detail is the agent's own error text).
func countryProbeCode(agentError string) string {
	switch code, _, _ := strings.Cut(agentError, ":"); code {
	case "control_failed", "no_dpi_hop", "busy", "bad_params", "unsupported_host", "unsupported":
		return code
	default:
		return "failed"
	}
}

func validCountryVerdict(v string) bool {
	switch v {
	case "blocked", "open", "refused", "timeout":
		return true
	}
	return false
}
