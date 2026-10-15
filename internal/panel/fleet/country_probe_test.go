package fleet

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func countryRequest() CountryProbeRequest {
	return CountryProbeRequest{
		Control: "198.51.100.10", ControlSNI: "ctl.example.net", BlockedRef: "198.51.100.66", HopLimit: 3, MaxTTL: 30,
		Targets: []CountryProbeTarget{{Host: "203.0.113.10", Ports: []uint16{443, 8443}}, {Host: "de2.example.net", Ports: []uint16{443}}},
	}
}

type countryAnswer struct {
	res CountryProbeResult
	err error
}

// askCountry calls CountryProbe in the background and answers it on the stream with result; it returns the command the
// agent was sent and what the call returned.
func askCountry(t *testing.T, e *env, a *agent, c *conn, result *agentv1.CommandResult) (*agentv1.CountryProbe, countryAnswer) {
	t.Helper()
	done := make(chan countryAnswer, 1)
	go func() {
		res, err := e.f.CountryProbe(e.ctx, a.nodeID, countryRequest())
		done <- countryAnswer{res, err}
	}()
	cmd := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetCountryProbe() != nil }).GetCountryProbe()
	replyPortCommand(c, cmd.RequestId, result)
	select {
	case got := <-done:
		return cmd, got
	case <-time.After(10 * time.Second):
		t.Fatal("CountryProbe did not return")
		return nil, countryAnswer{}
	}
}

func TestCountryProbeSendsTheCommandAndParsesTheAnswer(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("ru-probe")
	c, _, _ := connectCaps(a, "ru-probe-1", capCountryProbe)
	cmd, got := askCountry(t, e, a, c, &agentv1.CommandResult{Ok: true, Params: map[string]string{
		"dpi_hop": "4", "control": "ok", "t203.0.113.10:443": "open", "t203.0.113.10:8443": "refused", "tde2.example.net:443": "blocked",
		"t198.51.100.99:443": "open", // not asked for: never reaches the caller
	}})
	if got.err != nil {
		t.Fatal(got.err)
	}
	if cmd.RequestId == "" || cmd.ControlTarget != "198.51.100.10" || cmd.ControlSni != "ctl.example.net" || cmd.BlockedRef != "198.51.100.66" ||
		cmd.HopLimit != 3 || cmd.MaxTtl != 30 || len(cmd.Targets) != 2 || cmd.Targets[0].Host != "203.0.113.10" ||
		len(cmd.Targets[0].Ports) != 2 || cmd.Targets[0].Ports[1] != 8443 || cmd.Targets[1].Host != "de2.example.net" {
		t.Fatalf("command = %+v", cmd)
	}
	r := got.res
	if r.Code != "" || r.DPIHop != 4 || !r.ControlOK || len(r.Verdicts) != 3 || r.Verdicts["203.0.113.10:443"] != "open" ||
		r.Verdicts["203.0.113.10:8443"] != "refused" || r.Verdicts["de2.example.net:443"] != "blocked" {
		t.Fatalf("result = %+v", r)
	}
}

func TestCountryProbeAnswersThatAreNotVerdicts(t *testing.T) {
	for name, tc := range map[string]struct {
		result *agentv1.CommandResult
		code   string
		hop    int
	}{
		"control failed":         {&agentv1.CommandResult{Error: "control_failed", Params: map[string]string{"dpi_hop": "3", "control": "fail"}}, "control_failed", 3},
		"no dpi hop":             {&agentv1.CommandResult{Error: "no_dpi_hop"}, "no_dpi_hop", 0},
		"busy":                   {&agentv1.CommandResult{Error: "busy"}, "busy", 0},
		"bad params":             {&agentv1.CommandResult{Error: "bad_params"}, "bad_params", 0},
		"unsupported":            {&agentv1.CommandResult{Error: "unsupported"}, "unsupported", 0},
		"unsupported host":       {&agentv1.CommandResult{Error: "unsupported_host"}, "unsupported_host", 0},
		"a failure with details": {&agentv1.CommandResult{Error: "failed: local error: operation not permitted"}, "failed", 0},
		"a word nobody knows":    {&agentv1.CommandResult{Error: "boom"}, "failed", 0},
		"an error with nothing":  {&agentv1.CommandResult{}, "failed", 0},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			a := e.enroll("ru-probe")
			c, _, _ := connectCaps(a, "ru-probe-1", capCountryProbe)
			_, got := askCountry(t, e, a, c, tc.result)
			if got.err != nil || got.res.Code != tc.code || got.res.DPIHop != tc.hop || got.res.ControlOK || len(got.res.Verdicts) != 0 {
				t.Fatalf("result = %+v, err %v; want code %q and no verdicts", got.res, got.err, tc.code)
			}
		})
	}
}

// A no_dpi_hop carries the agent's reason; an older agent sends none, and a word nobody knows is left out.
func TestCountryProbeNoDPIHopReason(t *testing.T) {
	for name, tc := range map[string]struct {
		params map[string]string
		want   string
	}{
		"ref open":     {map[string]string{"reason": "ref_open"}, "ref_open"},
		"ref refused":  {map[string]string{"reason": "ref_refused"}, "ref_refused"},
		"no hops":      {map[string]string{"reason": "no_hops"}, "no_hops"},
		"no ref":       {map[string]string{"reason": "no_ref"}, "no_ref"},
		"an old agent": {nil, ""},
		"a new word":   {map[string]string{"reason": "unheard of"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			a := e.enroll("ru-probe")
			c, _, _ := connectCaps(a, "ru-probe-1", capCountryProbe)
			_, got := askCountry(t, e, a, c, &agentv1.CommandResult{Error: "no_dpi_hop", Params: tc.params})
			if got.err != nil || got.res.Code != "no_dpi_hop" || got.res.Reason != tc.want {
				t.Fatalf("result = %+v, err %v; want reason %q", got.res, got.err, tc.want)
			}
		})
	}
}

func TestCountryProbeLeavesOutWhatIsNotAVerdict(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("ru-probe")
	c, _, _ := connectCaps(a, "ru-probe-1", capCountryProbe)
	_, got := askCountry(t, e, a, c, &agentv1.CommandResult{Ok: true, Params: map[string]string{
		"dpi_hop": "x", "control": "ok", "t203.0.113.10:443": "ours", "t203.0.113.10:8443": "timeout",
	}})
	if got.err != nil || got.res.DPIHop != 0 || len(got.res.Verdicts) != 1 || got.res.Verdicts["203.0.113.10:8443"] != "timeout" {
		t.Fatalf("result = %+v, err %v", got.res, got.err)
	}
}

func TestCountryProbeNeedsAnAgentThatListsTheCapability(t *testing.T) {
	frame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_CountryProbe{CountryProbe: &agentv1.CountryProbe{}}}
	if capability, message := requestCapability(frame); capability != "country-probe/1" || message != "agent too old" {
		t.Fatalf("CountryProbe capability = %q, %q", capability, message)
	}

	e := newEnv(t)
	a := e.enroll("ru-probe")
	if _, err := e.f.CountryProbe(e.ctx, a.nodeID, countryRequest()); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node_offline" {
		t.Fatalf("offline: %v", err)
	}
	c, _, _ := connectCaps(a, "old", "doctor/1", "tcpcheck/1") // an agent from before this feature
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	if _, err := e.f.CountryProbe(ctx, a.nodeID, countryRequest()); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Fatalf("an agent without country-probe/1: %v", err)
	}
	// nothing was sent to it, so nothing is waited for
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case m := <-c.in:
			if m.GetCountryProbe() != nil {
				t.Fatal("the old agent was sent a CountryProbe")
			}
			continue
		case <-deadline:
		}
		break
	}
}
