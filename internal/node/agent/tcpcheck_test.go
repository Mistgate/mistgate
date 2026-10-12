package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/speedtest"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// markCerts is a CertSource that arms marks like certs.Source, with a state the test picks.
type markCerts struct {
	engine.CertSource
	mu     sync.Mutex
	state  string
	armed  bool
	token  []byte
	answer []byte
	until  time.Time
}

func (m *markCerts) HTTP01State() string { return m.state }

func (m *markCerts) Mark(token, answer []byte, until time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.armed {
		return false
	}
	m.armed, m.token, m.answer, m.until = true, token, answer, until
	return true
}

func (m *markCerts) isArmed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.armed
}

func newMarkHarness(t *testing.T, state string) (*harness, *markCerts) {
	t.Helper()
	certs := &markCerts{state: state}
	h := newHarness(t, harnessOpts{cfg: func(c *Config) { c.Certs = certs }})
	return h, certs
}

func askHTTPMark(h *harness, id string, token, answer []byte, hold uint32) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_HttpMark{HttpMark: &pb.HttpMark{
		RequestId: id, Token: token, Answer: answer, HoldS: hold,
	}}})
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("HTTP mark answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

func tcpProbeMessage(id, host string, targets ...*pb.TcpTarget) *pb.ConnectResponse {
	return &pb.ConnectResponse{Message: &pb.ConnectResponse_TcpProbe{TcpProbe: &pb.TcpProbe{RequestId: id, Host: host, Targets: targets}}}
}

func askTCPProbe(h *harness, id, host string, targets ...*pb.TcpTarget) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(tcpProbeMessage(id, host, targets...))
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("TCP probe answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

var (
	markToken  = []byte("0123456789abcdef")
	markAnswer = []byte("fedcba9876543210")
)

func TestTCPCheckCapabilityIsAlwaysAdvertised(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if got := (<-h.panel.hellos).Capabilities; !contains(got, "tcpcheck/1") {
		t.Fatalf("capabilities = %v, want tcpcheck/1", got)
	}
}

func TestHTTPMarkArmsOnceAndReportsTheListener(t *testing.T) {
	h, certs := newMarkHarness(t, "serving")
	h.waitConnected()
	before := time.Now()
	r := askHTTPMark(h, "mark_1", markToken, markAnswer, 30)
	if !r.Ok || r.Params["http01"] != "serving" {
		t.Fatalf("arm: %+v", r)
	}
	certs.mu.Lock()
	armed, until := certs.armed, certs.until
	certs.mu.Unlock()
	if !armed || until.Before(before.Add(29*time.Second)) || until.After(time.Now().Add(31*time.Second)) {
		t.Fatalf("mark armed=%v until=%v", armed, until)
	}
	if r := askHTTPMark(h, "mark_2", []byte("abcdef0123456789"), markAnswer, 30); r.Ok || r.Error != "busy" {
		t.Fatalf("second arm: %+v", r)
	}
}

func TestHTTPMarkStatesWithoutServingListener(t *testing.T) {
	for _, state := range []string{"busy", "off"} {
		t.Run(state, func(t *testing.T) {
			h, certs := newMarkHarness(t, state)
			h.waitConnected()
			if r := askHTTPMark(h, "mark", markToken, markAnswer, 30); !r.Ok || r.Params["http01"] != state {
				t.Fatalf("answer: %+v", r)
			}
			if certs.isArmed() {
				t.Fatal("a mark was armed without a listener to serve it")
			}
		})
	}
	t.Run("no certificate source", func(t *testing.T) {
		h := newHarness(t, harnessOpts{})
		h.waitConnected()
		if r := askHTTPMark(h, "mark", markToken, markAnswer, 30); !r.Ok || r.Params["http01"] != "off" {
			t.Fatalf("answer: %+v", r)
		}
	})
}

func TestHTTPMarkBadParams(t *testing.T) {
	h, certs := newMarkHarness(t, "serving")
	h.waitConnected()
	for name, tc := range map[string]struct {
		token, answer []byte
		hold          uint32
	}{
		"short token":  {markToken[:15], markAnswer, 30},
		"short answer": {markToken, markAnswer[:15], 30},
		"zero hold":    {markToken, markAnswer, 0},
		"long hold":    {markToken, markAnswer, 61},
	} {
		t.Run(name, func(t *testing.T) {
			if r := askHTTPMark(h, "bad_"+name, tc.token, tc.answer, tc.hold); r.Ok || r.Error != "bad_params" {
				t.Fatalf("answer = %+v", r)
			}
		})
	}
	if certs.isArmed() {
		t.Fatal("a bad request armed a mark")
	}
}

func TestHTTPMarkAndTCPProbeDuringBandwidthMeasurementAreBusy(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	certs := &markCerts{state: "serving"}
	h := newHarness(t, harnessOpts{cfg: func(c *Config) {
		c.Certs = certs
		c.SpeedTest = func(ctx context.Context) (speedtest.Result, error) {
			close(started)
			select {
			case <-release:
				return speedtest.Result{DownMbps: 1}, nil
			case <-ctx.Done():
				return speedtest.Result{}, ctx.Err()
			}
		}
	}})
	h.waitConnected()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &pb.MeasureBandwidth{RequestId: "measure"}}})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("bandwidth measurement did not start")
	}
	if r := askHTTPMark(h, "mark_busy", markToken, markAnswer, 30); r.Ok || r.Error != "busy" {
		t.Fatalf("mark during measurement: %+v", r)
	}
	r := askTCPProbe(h, "probe_busy", "127.0.0.1", &pb.TcpTarget{Port: 443, Kind: "tls", Sni: "node-a.example.com"})
	if r.Ok || r.Error != "busy" {
		t.Fatalf("probe during measurement: %+v", r)
	}
	close(release)
	if r := h.panel.nextCmd(); r.RequestId != "measure" || !r.Ok {
		t.Fatalf("measurement answer: %+v", r)
	}
}

func TestTCPProbeAnswersVerdictsFromTheProber(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	var gotHost string
	var gotTargets []tcpcheck.Target
	h.a.tcp.probe = func(_ context.Context, host string, targets []tcpcheck.Target) (string, map[uint16]string, error) {
		gotHost, gotTargets = host, targets
		return "4", map[uint16]string{80: "ours", 443: "refused"}, nil
	}
	r := askTCPProbe(h, "probe",
		"node-a.example.com", &pb.TcpTarget{Port: 80, Kind: "http_mark", Token: markToken, Answer: markAnswer},
		&pb.TcpTarget{Port: 443, Kind: "tls", Sni: "node-a.example.com", PinSha256: hex.EncodeToString(make([]byte, 32))})
	if !r.Ok || r.Params["family"] != "4" || r.Params["t80"] != "ours" || r.Params["t443"] != "refused" {
		t.Fatalf("answer: %+v", r)
	}
	if gotHost != "node-a.example.com" || len(gotTargets) != 2 || gotTargets[0].Port != 80 || gotTargets[0].Kind != tcpcheck.KindHTTPMark ||
		string(gotTargets[0].Token) != string(markToken) || gotTargets[1].SNI != "node-a.example.com" || len(gotTargets[1].PinSHA256) != 64 {
		t.Fatalf("prober got %q %+v", gotHost, gotTargets)
	}
}

func TestTCPProbeMapsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"unsupported host": {tcpcheck.ErrUnsupportedHost, "unsupported_host"},
		"no route":         {tcpcheck.ErrNoRoute, "no_route"},
		"bad parameters":   {tcpcheck.ErrBadParams, "bad_params"},
		"other failure":    {errors.New("probe failed\nwith details"), "failed: probe failed with details"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{})
			h.waitConnected()
			h.a.tcp.probe = func(context.Context, string, []tcpcheck.Target) (string, map[uint16]string, error) {
				return "", nil, tc.err
			}
			r := askTCPProbe(h, "probe_error", "127.0.0.1", &pb.TcpTarget{Port: 443, Kind: "tls", Sni: "node-a.example.com"})
			if r.Ok || r.Error != tc.want {
				t.Fatalf("answer = %+v, want error %q", r, tc.want)
			}
		})
	}
}

func TestTCPProbeReachesALocalMarkListener(t *testing.T) {
	path := "/.well-known/acme-challenge/" + hex.EncodeToString(markToken)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			_, _ = w.Write(markAnswer)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	closed, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()

	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	r := askTCPProbe(h, "probe_local", "127.0.0.1",
		&pb.TcpTarget{Port: uint32(port), Kind: "http_mark", Token: markToken, Answer: markAnswer},
		&pb.TcpTarget{Port: uint32(closedPort), Kind: "tls", Sni: "node-a.example.com"})
	if !r.Ok || r.Params["family"] != "4" || r.Params["t"+strconv.Itoa(port)] != "ours" || r.Params["t"+strconv.Itoa(closedPort)] != "refused" {
		t.Fatalf("answer: %+v", r)
	}
}

func TestTCPProbeFifthConcurrentRequestIsBusy(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	h.a.tcp.probe = func(ctx context.Context, _ string, targets []tcpcheck.Target) (string, map[uint16]string, error) {
		started <- struct{}{}
		select {
		case <-release:
			return "4", map[uint16]string{targets[0].Port: "ours"}, nil
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	}
	target := &pb.TcpTarget{Port: 443, Kind: "tls", Sni: "node-a.example.com"}
	for i := 0; i < 4; i++ {
		h.panel.send(tcpProbeMessage("probe_"+string(rune('1'+i)), "127.0.0.1", target))
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("four probes did not start")
		}
	}
	if r := askTCPProbe(h, "probe_5", "127.0.0.1", target); r.Ok || r.Error != "busy" {
		t.Fatalf("fifth probe: %+v", r)
	}
	close(release)
	for i := 0; i < 4; i++ {
		if r := h.panel.nextCmd(); !r.Ok {
			t.Fatalf("probe %s: %+v", r.RequestId, r)
		}
	}
}

func TestTCPProbeBadParams(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	tlsTarget := func(port uint32) *pb.TcpTarget {
		return &pb.TcpTarget{Port: port, Kind: "tls", Sni: "node-a.example.com"}
	}
	nine := make([]*pb.TcpTarget, 9)
	for i := range nine {
		nine[i] = tlsTarget(uint32(1000 + i))
	}
	for name, tc := range map[string]struct {
		host    string
		targets []*pb.TcpTarget
	}{
		"empty host":        {"", []*pb.TcpTarget{tlsTarget(443)}},
		"no targets":        {"127.0.0.1", nil},
		"too many targets":  {"127.0.0.1", nine},
		"duplicate port":    {"127.0.0.1", []*pb.TcpTarget{tlsTarget(443), tlsTarget(443)}},
		"zero port":         {"127.0.0.1", []*pb.TcpTarget{tlsTarget(0)}},
		"port out of range": {"127.0.0.1", []*pb.TcpTarget{tlsTarget(65536)}},
		"unknown kind":      {"127.0.0.1", []*pb.TcpTarget{{Port: 443, Kind: "ssh"}}},
		"tls without sni":   {"127.0.0.1", []*pb.TcpTarget{{Port: 443, Kind: "tls"}}},
		"short mark":        {"127.0.0.1", []*pb.TcpTarget{{Port: 80, Kind: "http_mark", Token: markToken[:8], Answer: markAnswer}}},
		"bad pin":           {"127.0.0.1", []*pb.TcpTarget{{Port: 443, Kind: "tls", Sni: "node-a.example.com", PinSha256: "abc"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if r := askTCPProbe(h, "bad_"+name, tc.host, tc.targets...); r.Ok || r.Error != "bad_params" {
				t.Fatalf("answer = %+v", r)
			}
		})
	}
}
