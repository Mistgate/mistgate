package agent

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/tspuprobe"
)

func countryProbeMessage(id string, targets ...*pb.CountryProbeTarget) *pb.ConnectResponse {
	return &pb.ConnectResponse{Message: &pb.ConnectResponse_CountryProbe{CountryProbe: &pb.CountryProbe{
		RequestId: id, ControlTarget: "198.51.100.10", ControlSni: "ctl.example.net", BlockedRef: "198.51.100.66",
		Targets: targets, HopLimit: 3, MaxTtl: 30,
	}}}
}

func askCountryProbe(h *harness, id string, targets ...*pb.CountryProbeTarget) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(countryProbeMessage(id, targets...))
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("country probe answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

func ports(p ...uint32) []uint32 { return p }

func countryHarness(t *testing.T, supported bool) *harness {
	t.Helper()
	return newHarness(t, harnessOpts{tune: func(a *Agent) { a.country.supported = func() bool { return supported } }})
}

func TestCountryProbeCapabilityFollowsTheHost(t *testing.T) {
	for _, supported := range []bool{true, false} {
		h := countryHarness(t, supported)
		if got := contains((<-h.panel.hellos).Capabilities, "country-probe/1"); got != supported {
			t.Fatalf("supported = %v but the capability is listed = %v", supported, got)
		}
	}
}

func TestCountryProbeAnswersTheParamsOfTheRun(t *testing.T) {
	h := countryHarness(t, true)
	h.waitConnected()
	var got tspuprobe.Request
	h.a.country.run = func(_ context.Context, req tspuprobe.Request) (tspuprobe.Result, error) {
		got = req
		return tspuprobe.Result{DPIHop: 4, Verdicts: map[string]string{"203.0.113.10:443": "open", "203.0.113.10:8443": "refused",
			"203.0.113.20:443": "blocked", "de2.example.net:443": "timeout"}}, nil
	}
	r := askCountryProbe(h, "country",
		&pb.CountryProbeTarget{Host: "203.0.113.10", Ports: ports(443, 8443)},
		&pb.CountryProbeTarget{Host: "203.0.113.20", Ports: ports(443)},
		&pb.CountryProbeTarget{Host: "de2.example.net", Ports: ports(443)})
	want := map[string]string{"dpi_hop": "4", "control": "ok", "t203.0.113.10:443": "open", "t203.0.113.10:8443": "refused",
		"t203.0.113.20:443": "blocked", "tde2.example.net:443": "timeout"}
	if !r.Ok || r.Error != "" || len(r.Params) != len(want) {
		t.Fatalf("answer: %+v", r)
	}
	for k, v := range want {
		if r.Params[k] != v {
			t.Errorf("params[%q] = %q, want %q", k, r.Params[k], v)
		}
	}
	if got.Control != "198.51.100.10" || got.BlockedRef != "198.51.100.66" || got.HopLimit != 3 || got.MaxTTL != 30 || len(got.Targets) != 3 ||
		got.Targets[0].Host != "203.0.113.10" || len(got.Targets[0].Ports) != 2 || got.Targets[0].Ports[1] != 8443 {
		t.Fatalf("the run got %+v", got)
	}
}

func TestCountryProbeMapsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		res    tspuprobe.Result
		err    error
		want   string
		params map[string]string
	}{
		"control failed":   {tspuprobe.Result{DPIHop: 3}, tspuprobe.ErrControlFailed, "control_failed", map[string]string{"dpi_hop": "3", "control": "fail"}},
		"no dpi hop":       {tspuprobe.Result{}, tspuprobe.ErrNoDPIHop, "no_dpi_hop", nil},
		"no dpi hop, why":  {tspuprobe.Result{}, &tspuprobe.NoDPIHopError{Reason: tspuprobe.ReasonRefOpen}, "no_dpi_hop", map[string]string{"reason": "ref_open"}},
		"unsupported host": {tspuprobe.Result{}, tspuprobe.ErrUnsupportedHost, "unsupported_host", nil},
		"unsupported":      {tspuprobe.Result{}, tspuprobe.ErrUnsupported, "unsupported", nil},
		"bad parameters":   {tspuprobe.Result{}, tspuprobe.ErrBadParams, "bad_params", nil},
		"deadline":         {tspuprobe.Result{}, tspuprobe.ErrDeadline, "failed: deadline", nil},
		"other failure":    {tspuprobe.Result{}, errors.New("local error:\noperation not permitted"), "failed: local error: operation not permitted", nil},
	} {
		t.Run(name, func(t *testing.T) {
			h := countryHarness(t, true)
			h.waitConnected()
			h.a.country.run = func(context.Context, tspuprobe.Request) (tspuprobe.Result, error) { return tc.res, tc.err }
			r := askCountryProbe(h, "err", &pb.CountryProbeTarget{Host: "203.0.113.10", Ports: ports(443)})
			if r.Ok || r.Error != tc.want || len(r.Params) != len(tc.params) {
				t.Fatalf("answer = %+v, want error %q params %v", r, tc.want, tc.params)
			}
			for k, v := range tc.params {
				if r.Params[k] != v {
					t.Errorf("params[%q] = %q, want %q", k, r.Params[k], v)
				}
			}
		})
	}
}

func TestCountryProbeBadParamsAndForbiddenAddresses(t *testing.T) {
	h := countryHarness(t, true)
	h.waitConnected()
	h.a.country.run = func(context.Context, tspuprobe.Request) (tspuprobe.Result, error) {
		t.Error("a refused request reached the prober")
		return tspuprobe.Result{}, nil
	}
	target := func(host string, p ...uint32) *pb.CountryProbeTarget {
		return &pb.CountryProbeTarget{Host: host, Ports: p}
	}
	nine := make([]*pb.CountryProbeTarget, 9)
	for i := range nine {
		nine[i] = target("203.0.113."+strconv.Itoa(100+i), 443)
	}
	fine := []*pb.CountryProbeTarget{target("203.0.113.10", 443)}
	for name, tc := range map[string]struct {
		mutate  func(*pb.CountryProbe)
		targets []*pb.CountryProbeTarget
		want    string
	}{
		"no targets":        {nil, nil, "bad_params"},
		"nine targets":      {nil, nine, "bad_params"},
		"a nil target":      {nil, []*pb.CountryProbeTarget{nil}, "bad_params"},
		"no ports":          {nil, []*pb.CountryProbeTarget{target("203.0.113.10")}, "bad_params"},
		"zero port":         {nil, []*pb.CountryProbeTarget{target("203.0.113.10", 0)}, "bad_params"},
		"port out of range": {nil, []*pb.CountryProbeTarget{target("203.0.113.10", 65536)}, "bad_params"},
		"five ports":        {nil, []*pb.CountryProbeTarget{target("203.0.113.10", 1, 2, 3, 4, 5)}, "bad_params"},
		"hop limit 9":       {func(m *pb.CountryProbe) { m.HopLimit = 9 }, fine, "bad_params"},
		"max ttl 33":        {func(m *pb.CountryProbe) { m.MaxTtl = 33 }, fine, "bad_params"},
		"no control":        {func(m *pb.CountryProbe) { m.ControlTarget = "" }, fine, "bad_params"},
		"unspecified":       {nil, []*pb.CountryProbeTarget{target("0.0.0.0", 443)}, "unsupported_host"},
		"multicast":         {nil, []*pb.CountryProbeTarget{target("224.0.0.1", 443)}, "unsupported_host"},
		"broadcast":         {nil, []*pb.CountryProbeTarget{target("255.255.255.255", 443)}, "unsupported_host"},
		"link local":        {nil, []*pb.CountryProbeTarget{target("169.254.169.254", 443)}, "unsupported_host"},
		"an ipv6 literal":   {nil, []*pb.CountryProbeTarget{target("2001:db8::1", 443)}, "unsupported_host"},
		"a forbidden ref":   {func(m *pb.CountryProbe) { m.BlockedRef = "0.0.0.0" }, fine, "unsupported_host"},
	} {
		t.Run(name, func(t *testing.T) {
			msg := countryProbeMessage("bad_"+name, tc.targets...)
			if tc.mutate != nil {
				tc.mutate(msg.GetCountryProbe())
			}
			h.panel.send(msg)
			if r := h.panel.nextCmd(); r.Ok || r.Error != tc.want {
				t.Fatalf("answer = %+v, want %q", r, tc.want)
			}
		})
	}
}

func TestCountryProbeOneRunAtATime(t *testing.T) {
	h := countryHarness(t, true)
	h.waitConnected()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h.a.country.run = func(ctx context.Context, _ tspuprobe.Request) (tspuprobe.Result, error) {
		started <- struct{}{}
		select {
		case <-release:
			return tspuprobe.Result{DPIHop: 1, Verdicts: map[string]string{}}, nil
		case <-ctx.Done():
			return tspuprobe.Result{}, ctx.Err()
		}
	}
	target := &pb.CountryProbeTarget{Host: "203.0.113.10", Ports: ports(443)}
	h.panel.send(countryProbeMessage("first", target))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first run did not start")
	}
	if r := askCountryProbe(h, "second", target); r.Ok || r.Error != "busy" {
		t.Fatalf("second run: %+v", r)
	}
	close(release)
	if r := h.panel.nextCmd(); r.RequestId != "first" || !r.Ok {
		t.Fatalf("first run: %+v", r)
	}
	// and the place is free again
	h.a.country.run = func(context.Context, tspuprobe.Request) (tspuprobe.Result, error) { return tspuprobe.Result{}, nil }
	if r := askCountryProbe(h, "third", target); !r.Ok {
		t.Fatalf("third run: %+v", r)
	}
}

func TestCountryProbeBusyDuringABandwidthTest(t *testing.T) {
	h := countryHarness(t, true)
	h.waitConnected()
	h.a.measuring.Store(true)
	if r := askCountryProbe(h, "measuring", &pb.CountryProbeTarget{Host: "203.0.113.10", Ports: ports(443)}); r.Ok || r.Error != "busy" {
		t.Fatalf("answer: %+v", r)
	}
}

func TestCountryProbeOnAHostThatCannot(t *testing.T) {
	h := countryHarness(t, false)
	h.waitConnected()
	if r := askCountryProbe(h, "nope", &pb.CountryProbeTarget{Host: "203.0.113.10", Ports: ports(443)}); r.Ok || r.Error != "unsupported" {
		t.Fatalf("answer: %+v", r)
	}
}
