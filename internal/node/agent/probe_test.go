package agent

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/tspuprobe"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

// probe/1: UdpSend.df, the "connect" kind and timings of TcpProbe, and PathTrace (agent.proto "PATH PROBE").

func TestProbeCapabilityIsAdvertised(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if got := (<-h.panel.hellos).Capabilities; !contains(got, "probe/1") {
		t.Fatalf("capabilities %v do not list probe/1", got)
	}
}

func dfSendMessage(id, host string, port, size uint32, df bool) *pb.ConnectResponse {
	m := udpSendMessage(id, host, []uint32{port}, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 2, 50, size)
	m.GetUdpSend().Df = df
	return m
}

func TestUDPSendDontFragmentRaisesTheSizeCapAndUsesTheDFSender(t *testing.T) {
	h, _ := newUDPTestHarness(t, nil)
	h.waitConnected()
	var gotDF atomic.Bool
	var gotSize atomic.Int64
	h.a.sendUDPDF = func(_ context.Context, _ string, _ []uint16, _ [8]byte, count, _, size int, df bool) (string, int, error) {
		gotDF.Store(df)
		gotSize.Store(int64(size))
		return "4", count, nil
	}
	h.a.sendUDP = func(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error) {
		t.Error("a don't-fragment send went through the plain sender")
		return "4", 0, nil
	}
	h.panel.send(dfSendMessage("df", "127.0.0.1", 443, 1472, true))
	r := h.panel.nextCmd()
	if !r.Ok || r.Params["sent"] != "2" || !gotDF.Load() || gotSize.Load() != 1472 {
		t.Fatalf("answer %+v, df %v, size %d", r, gotDF.Load(), gotSize.Load())
	}
	// without the flag the cap stays 1200, and above 1472 nothing is allowed
	for name, m := range map[string]*pb.ConnectResponse{
		"1201 without df": dfSendMessage("big", "127.0.0.1", 443, 1201, false),
		"1473 with df":    dfSendMessage("huge", "127.0.0.1", 443, 1473, true),
	} {
		h.panel.send(m)
		if r := h.panel.nextCmd(); r.Ok || r.Error != "bad_params" {
			t.Errorf("%s: %+v, want bad_params", name, r)
		}
	}
}

func TestUDPSendDontFragmentOnTheRealSocket(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h, _ := newUDPTestHarness(t, nil)
	h.waitConnected()
	h.panel.send(dfSendMessage("real", "127.0.0.1", uint32(listener.LocalAddr().(*net.UDPAddr).Port), 1452, true))
	r := h.panel.nextCmd()
	if runtime.GOOS != "linux" { // only Linux sets the flag: another host refuses cleanly
		if r.Ok || r.Error != "unsupported_host" {
			t.Fatalf("a host that cannot set DF answered %+v", r)
		}
		return
	}
	if !r.Ok || r.Params["sent"] != "2" {
		t.Fatalf("answer %+v", r)
	}
	if err := listener.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2000)
	if n, _, err := listener.ReadFromUDP(buf); err != nil || n != 1452 {
		t.Fatalf("received %d bytes, %v; want 1452", n, err)
	}
}

func TestUDPCheckDontFragmentValidation(t *testing.T) {
	if err := udpcheck.ValidateDF("127.0.0.1", []uint16{443}, 10, 10, udpcheck.MaxSizeDF, true); err != nil {
		t.Errorf("1472 with df: %v", err)
	}
	if err := udpcheck.ValidateDF("127.0.0.1", []uint16{443}, 10, 10, udpcheck.MaxSizeDF, false); !errors.Is(err, udpcheck.ErrBadParams) {
		t.Errorf("1472 without df: %v", err)
	}
}

func TestTCPProbeConnectKindAnswersWithTimings(t *testing.T) {
	open, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	go func() {
		for {
			c, err := open.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	openPort := open.Addr().(*net.TCPAddr).Port
	closed, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()

	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	r := askTCPProbe(h, "connect", "127.0.0.1",
		&pb.TcpTarget{Port: uint32(openPort), Kind: "connect"}, &pb.TcpTarget{Port: uint32(closedPort), Kind: "connect"})
	if !r.Ok || r.Params["t"+strconv.Itoa(openPort)] != "ours" || r.Params["t"+strconv.Itoa(closedPort)] != "refused" {
		t.Fatalf("answer: %+v", r)
	}
	for _, port := range []int{openPort, closedPort} {
		ms, err := strconv.Atoi(r.Params["m"+strconv.Itoa(port)])
		if err != nil || ms < 0 || ms > 5000 {
			t.Errorf("the time of port %d = %q", port, r.Params["m"+strconv.Itoa(port)])
		}
	}
	// a connect target carries no payload
	if r := askTCPProbe(h, "bad", "127.0.0.1", &pb.TcpTarget{Port: uint32(openPort), Kind: "connect", Sni: "example.com"}); r.Ok || r.Error != "bad_params" {
		t.Errorf("connect with an SNI: %+v", r)
	}
}

func traceMessage(id, host string, port, ttl uint32) *pb.ConnectResponse {
	return &pb.ConnectResponse{Message: &pb.ConnectResponse_PathTrace{PathTrace: &pb.PathTrace{RequestId: id, Host: host, Port: port, MaxTtl: ttl}}}
}

func askTrace(h *harness, id, host string, port, ttl uint32) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(traceMessage(id, host, port, ttl))
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("trace answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

func traceHarness(t *testing.T, supported bool) *harness {
	t.Helper()
	return newHarness(t, harnessOpts{tune: func(a *Agent) { a.pathTrace.supported = func() bool { return supported } }})
}

func TestPathTraceAnswersHopsWithoutAddresses(t *testing.T) {
	h := traceHarness(t, true)
	h.waitConnected()
	var gotHost string
	var gotPort uint16
	var gotTTL int
	h.a.pathTrace.walk = func(_ context.Context, host string, port uint16, ttl int) ([]tspuprobe.WalkHop, bool, error) {
		gotHost, gotPort, gotTTL = host, port, ttl
		return []tspuprobe.WalkHop{
			{TTL: 1, Kind: tspuprobe.HopICMP, RTT: 1400 * time.Microsecond}, {TTL: 2, Kind: tspuprobe.HopSilent},
			{TTL: 3, Kind: tspuprobe.HopICMP, RTT: 9 * time.Millisecond}, {TTL: 4, Kind: tspuprobe.HopOpen, RTT: 12 * time.Millisecond}}, true, nil
	}
	r := askTrace(h, "trace", "203.0.113.10", 443, 20)
	want := map[string]string{"family": "4", "reached": "1", "h1": "icmp:1", "h2": "silent:0", "h3": "icmp:9", "h4": "open:12"}
	if !r.Ok || len(r.Params) != len(want) {
		t.Fatalf("answer %+v", r)
	}
	for k, v := range want {
		if r.Params[k] != v {
			t.Errorf("params[%q] = %q, want %q", k, r.Params[k], v)
		}
	}
	if gotHost != "203.0.113.10" || gotPort != 443 || gotTTL != 20 {
		t.Errorf("the walk got %q %d %d", gotHost, gotPort, gotTTL)
	}
	for k, v := range r.Params { // a hop is a kind and a time: never an address
		if k != "family" && !strings.Contains(v, ":") && k != "reached" {
			t.Errorf("param %s = %q", k, v)
		}
	}
}

func TestPathTraceCapabilityFollowsTheHostAndRefusesCleanly(t *testing.T) {
	// the capability is probe/1 whatever the host; a host that cannot walk answers unsupported and sends nothing
	h := traceHarness(t, false)
	h.a.pathTrace.walk = func(context.Context, string, uint16, int) ([]tspuprobe.WalkHop, bool, error) {
		t.Error("an unsupported host walked")
		return nil, false, nil
	}
	if !contains((<-h.panel.hellos).Capabilities, "probe/1") {
		t.Fatal("probe/1 not advertised")
	}
	h.waitConnected()
	if r := askTrace(h, "nope", "203.0.113.10", 443, 0); r.Ok || r.Error != "unsupported" {
		t.Fatalf("answer %+v", r)
	}
}

func TestPathTraceBadParamsAndErrorMapping(t *testing.T) {
	h := traceHarness(t, true)
	h.waitConnected()
	h.a.pathTrace.walk = func(context.Context, string, uint16, int) ([]tspuprobe.WalkHop, bool, error) {
		t.Error("a bad request walked")
		return nil, false, nil
	}
	for name, m := range map[string]*pb.ConnectResponse{
		"no host":   traceMessage("a", "", 443, 10),
		"no port":   traceMessage("b", "203.0.113.10", 0, 10),
		"big port":  traceMessage("c", "203.0.113.10", 70000, 10),
		"TTL cap":   traceMessage("d", "203.0.113.10", 443, 33),
		"all zeros": traceMessage("e", "", 0, 0),
	} {
		h.panel.send(m)
		if r := h.panel.nextCmd(); r.Ok || r.Error != "bad_params" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	for want, err := range map[string]error{
		"unsupported_host": tspuprobe.ErrUnsupportedHost, "no_route": tspuprobe.ErrNoRoute, "unsupported": tspuprobe.ErrUnsupported,
		"bad_params": tspuprobe.ErrBadParams, "failed: local error: operation not permitted": errors.New("local error:\noperation not permitted"),
	} {
		h.a.pathTrace.walk = func(context.Context, string, uint16, int) ([]tspuprobe.WalkHop, bool, error) { return nil, false, err }
		if r := askTrace(h, "err", "203.0.113.10", 443, 5); r.Ok || r.Error != want {
			t.Errorf("error %v: answer %+v, want %q", err, r, want)
		}
	}
}

func TestPathTraceIsOneAtATime(t *testing.T) {
	h := traceHarness(t, true)
	h.waitConnected()
	entered, release := make(chan struct{}), make(chan struct{})
	h.a.pathTrace.walk = func(context.Context, string, uint16, int) ([]tspuprobe.WalkHop, bool, error) {
		close(entered)
		<-release
		return []tspuprobe.WalkHop{{TTL: 1, Kind: tspuprobe.HopRefused, RTT: time.Millisecond}}, true, nil
	}
	h.panel.send(traceMessage("first", "203.0.113.10", 443, 5))
	<-entered
	if r := askTrace(h, "second", "203.0.113.10", 443, 5); r.Ok || r.Error != "busy" {
		t.Fatalf("a second walk: %+v", r)
	}
	close(release)
	if r := h.panel.nextCmd(); r.RequestId != "first" || !r.Ok || r.Params["h1"] != "refused:1" {
		t.Fatalf("the first walk: %+v", r)
	}
}
