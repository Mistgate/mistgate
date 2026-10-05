package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/speedtest"
)

// The agent side of "MeasureBandwidth" (agent.proto "BANDWIDTH TEST"): the capability, the answer, one run at a time, and
// the errors. The measurement itself is a fake here; its logic is tested against a local server in internal/node/speedtest.

func askMeasure(h *harness, id string) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &pb.MeasureBandwidth{RequestId: id}}})
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

func TestBandwidthIsListedAndAnswersWithTheMeasuredNumbers(t *testing.T) {
	h := newHarness(t, harnessOpts{cfg: func(c *Config) {
		c.SpeedTest = func(context.Context) (speedtest.Result, error) {
			return speedtest.Result{DownMbps: 939.6, UpMbps: 871.2, Server: "speed.cloudflare.com", DownStreams: 6, DownBytes: 700_000_000, UpBytes: 120_000_000, Seconds: 29.4, Runs: 1, RunsTotal: 3,
				Failures: []string{"rate_limited", "http_503"}, Detail: "MTS, Moscow", PeopleDownMbps: 34.6, PeopleUpMbps: 0.2}, nil
		}
	}})
	if !contains((<-h.panel.hellos).Capabilities, "bandwidth/1") {
		t.Error("the agent does not list bandwidth/1")
	}
	h.waitConnected()
	r := askMeasure(h, "req_b")
	want := map[string]string{"down_mbps": "940", "up_mbps": "871", "server": "speed.cloudflare.com", "streams": "6",
		"down_bytes": "700000000", "up_bytes": "120000000", "seconds": "29", "runs": "1", "down_people_mbps": "35", "up_people_mbps": "0",
		"runs_total": "3", "run_failures": "rate_limited,http_503", "server_detail": "MTS, Moscow"}
	if !r.Ok || r.Error != "" {
		t.Fatalf("answer = %+v", r)
	}
	for k, v := range want {
		if r.Params[k] != v {
			t.Errorf("params[%s] = %q, want %q", k, r.Params[k], v)
		}
	}
}

func TestBandwidthRunsOneAtATime(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	h := newHarness(t, harnessOpts{cfg: func(c *Config) {
		c.SpeedTest = func(ctx context.Context) (speedtest.Result, error) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return speedtest.Result{DownMbps: 100, Server: "x"}, nil
		}
	}})
	h.waitConnected()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &pb.MeasureBandwidth{RequestId: "req_1"}}})
	<-started
	r := askMeasure(h, "req_2")
	if r.Ok || r.Error != "busy" {
		t.Fatalf("a second request while one runs: %+v", r)
	}
	close(release)
	if r := h.panel.nextCmd(); r.RequestId != "req_1" || !r.Ok || r.Params["down_mbps"] != "100" {
		t.Fatalf("the first request's answer: %+v", r)
	}
	// and the node is free again (release is closed, so this one returns at once)
	if r := askMeasure(h, "req_3"); !r.Ok {
		t.Fatalf("not free after the run ended: %+v", r)
	}
}

func TestBandwidthErrorsAreCodes(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"nothing reachable": {fmt.Errorf("%w: cloudflare: dial tcp", speedtest.ErrUnreachable), "unreachable"},
		"anything else":     {errors.New("boom"), "failed: boom"},
	} {
		h := newHarness(t, harnessOpts{cfg: func(cf *Config) {
			cf.SpeedTest = func(context.Context) (speedtest.Result, error) { return speedtest.Result{}, c.err }
		}})
		h.waitConnected()
		r := askMeasure(h, "req_e")
		if r.Ok || r.Error != c.want || len(r.Params) != 0 {
			t.Errorf("%s: %+v, want error %q", name, r, c.want)
		}
	}
}

// A stream that ends during the test ends it (the test gets a cancelled context) and nothing is sent to a dead stream.
func TestBandwidthStopsWhenTheStreamEnds(t *testing.T) {
	stopped := make(chan struct{})
	started := make(chan struct{})
	h := newHarness(t, harnessOpts{cfg: func(c *Config) {
		c.SpeedTest = func(ctx context.Context) (speedtest.Result, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return speedtest.Result{}, ctx.Err()
		}
	}})
	h.waitConnected()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &pb.MeasureBandwidth{RequestId: "req_x"}}})
	<-started
	h.a.cur.Load().cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the test kept running after the stream ended")
	}
}
