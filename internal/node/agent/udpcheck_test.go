package agent

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/speedtest"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

type udpTestHost struct {
	hostctl.Host
	mu                    sync.Mutex
	armed                 bool
	tag                   [8]byte
	ports                 []uint16
	cleans                int
	cleaned               chan struct{}
	counts                map[uint16]hostctl.Count
	countErr              error
	cleanupBeforeBaseline bool
}

func (h *udpTestHost) CountUDP(tag [8]byte, ports []uint16) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.countErr != nil {
		return h.countErr
	}
	h.armed, h.tag, h.ports = true, tag, append([]uint16(nil), ports...)
	return nil
}

func (h *udpTestHost) TakeUDPCount() (map[uint16]hostctl.Count, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.armed {
		return nil, hostctl.ErrUnsupported
	}
	h.armed = false
	out := make(map[uint16]hostctl.Count, len(h.counts))
	for p, c := range h.counts {
		out[p] = c
	}
	return out, nil
}

func (h *udpTestHost) CleanupUDPCount(context.Context) error {
	base, _ := h.Host.(*fakeHost)
	if base != nil {
		base.mu.Lock()
		h.cleanupBeforeBaseline = base.baselines == 0
		base.mu.Unlock()
	}
	h.mu.Lock()
	h.armed = false
	h.cleans++
	h.mu.Unlock()
	select {
	case h.cleaned <- struct{}{}:
	default:
	}
	return nil
}

type manualUDPCountTimer struct {
	delay   time.Duration
	mu      sync.Mutex
	stopped bool
	f       func()
}

func (t *manualUDPCountTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (t *manualUDPCountTimer) fire() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	f := t.f
	t.mu.Unlock()
	f()
}

func newUDPTestHarness(t *testing.T, cfg func(*Config)) (*harness, *udpTestHost) {
	t.Helper()
	var host *udpTestHost
	h := newHarness(t, harnessOpts{
		cfg: cfg,
		wrapHost: func(base *fakeHost) hostctl.Host {
			host = &udpTestHost{Host: base, cleaned: make(chan struct{}, 8), counts: map[uint16]hostctl.Count{
				443: {Packets: 12, Bytes: 14400}, 8443: {Packets: 9, Bytes: 10800},
			}}
			return host
		},
	})
	return h, host
}

func askUDPCount(h *harness, id string, tag []byte, ports []uint32, hold uint32, stop bool) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_UdpCount{UdpCount: &pb.UdpCount{
		RequestId: id, Tag: tag, Ports: ports, HoldS: hold, Stop: stop,
	}}})
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("UDP count answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

func udpSendMessage(id, host string, ports []uint32, tag []byte, count, pps, size uint32) *pb.ConnectResponse {
	return &pb.ConnectResponse{Message: &pb.ConnectResponse_UdpSend{UdpSend: &pb.UdpSend{
		RequestId: id, Host: host, Ports: ports, Tag: tag, Count: count, Pps: pps, Size: size,
	}}}
}

func askUDPSend(h *harness, id, host string, ports []uint32, tag []byte, count, pps, size uint32) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(udpSendMessage(id, host, ports, tag, count, pps, size))
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("UDP send answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

func TestUDPCheckCapabilityNeedsCounterHost(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if got := (<-h.panel.hellos).Capabilities; contains(got, "udpcheck/1") {
		t.Fatalf("host without UDPCounter advertised udpcheck/1: %v", got)
	}
	h.stop()

	h, _ = newUDPTestHarness(t, nil)
	if got := (<-h.panel.hellos).Capabilities; !contains(got, "udpcheck/1") {
		t.Fatalf("host with UDPCounter did not advertise udpcheck/1: %v", got)
	}
}

func TestUDPCountArmBusyForeignStopAndTake(t *testing.T) {
	h, host := newUDPTestHarness(t, nil)
	h.waitConnected()
	tag := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if r := askUDPCount(h, "arm_1", tag, []uint32{443, 8443}, 30, false); !r.Ok {
		t.Fatalf("arm: %+v", r)
	}
	if r := askUDPCount(h, "arm_2", tag, []uint32{443}, 30, false); r.Ok || r.Error != "busy" {
		t.Fatalf("second arm: %+v", r)
	}
	foreign := []byte{8, 7, 6, 5, 4, 3, 2, 1}
	if r := askUDPCount(h, "stop_foreign", foreign, nil, 0, true); r.Ok || r.Error != "not_armed" {
		t.Fatalf("foreign stop: %+v", r)
	}
	if r := askUDPCount(h, "stop_1", tag, nil, 0, true); !r.Ok || r.Params["p443"] != "12" || r.Params["b443"] != "14400" ||
		r.Params["p8443"] != "9" || r.Params["b8443"] != "10800" {
		t.Fatalf("stop: %+v", r)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.armed || len(host.ports) != 2 || host.ports[0] != 443 || host.ports[1] != 8443 {
		t.Fatalf("counter host state: armed=%v ports=%v", host.armed, host.ports)
	}
}

func TestUDPCountHoldExpiryCleansTable(t *testing.T) {
	h, host := newUDPTestHarness(t, nil)
	var timer *manualUDPCountTimer
	h.a.udpAfterFunc = func(d time.Duration, f func()) udpCountTimer {
		timer = &manualUDPCountTimer{delay: d, f: f}
		return timer
	}
	h.waitConnected()
	select {
	case <-host.cleaned: // Run removes a leftover table before ApplyBaseline.
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not clean a leftover table")
	}
	host.mu.Lock()
	cleanedBeforeBaseline := host.cleanupBeforeBaseline
	host.mu.Unlock()
	if !cleanedBeforeBaseline {
		t.Fatal("Run applied the baseline before removing the UDP check table")
	}
	if r := askUDPCount(h, "arm_hold", []byte{1, 2, 3, 4, 5, 6, 7, 8}, []uint32{443}, 7, false); !r.Ok {
		t.Fatalf("arm: %+v", r)
	}
	if timer == nil || timer.delay != 7*time.Second {
		t.Fatalf("hold timer = %#v", timer)
	}
	timer.fire()
	select {
	case <-host.cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("hold expiry did not clean the table")
	}
	host.mu.Lock()
	armed, cleans := host.armed, host.cleans
	host.mu.Unlock()
	if armed || cleans != 2 {
		t.Fatalf("after expiry: armed=%v clean calls=%d", armed, cleans)
	}
}

func TestUDPCountBadParams(t *testing.T) {
	h, _ := newUDPTestHarness(t, nil)
	h.waitConnected()
	valid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if r := askUDPCount(h, "bad_stop_tag", valid[:7], nil, 0, true); r.Ok || r.Error != "bad_params" {
		t.Fatalf("stop with a short tag: %+v", r)
	}
	for name, tc := range map[string]struct {
		tag   []byte
		ports []uint32
		hold  uint32
	}{
		"tag length":      {valid[:7], []uint32{443}, 30},
		"no ports":        {valid, nil, 30},
		"too many ports":  {valid, []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}, 30},
		"duplicate ports": {valid, []uint32{443, 443}, 30},
		"zero port":       {valid, []uint32{0}, 30},
		"out of range":    {valid, []uint32{65536}, 30},
		"zero hold":       {valid, []uint32{443}, 0},
		"long hold":       {valid, []uint32{443}, 61},
	} {
		t.Run(name, func(t *testing.T) {
			r := askUDPCount(h, "bad_"+name, tc.tag, tc.ports, tc.hold, false)
			if r.Ok || r.Error != "bad_params" {
				t.Fatalf("answer = %+v", r)
			}
		})
	}
}

func TestUDPSendMapsTransportErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"bad parameters":   {udpcheck.ErrBadParams, "bad_params"},
		"unsupported host": {udpcheck.ErrUnsupportedHost, "unsupported_host"},
		"no route":         {udpcheck.ErrNoRoute, "no_route"},
		"other failure":    {errors.New("send failed\nwith details"), "failed: send failed with details"},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newUDPTestHarness(t, nil)
			h.waitConnected()
			h.a.sendUDP = func(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error) {
				return "", 0, tc.err
			}
			r := askUDPSend(h, "send_error", "127.0.0.1", []uint32{443}, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 1, 1, 64)
			if r.Ok || r.Error != tc.want {
				t.Fatalf("answer = %+v, want error %q", r, tc.want)
			}
		})
	}
}

func TestUDPSendReachesLocalListener(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h, _ := newUDPTestHarness(t, nil)
	h.waitConnected()
	tag := []byte{8, 7, 6, 5, 4, 3, 2, 1}
	port := uint32(listener.LocalAddr().(*net.UDPAddr).Port)
	r := askUDPSend(h, "send_local", "127.0.0.1", []uint32{port}, tag, 1, 50, 64)
	if !r.Ok || r.Params["family"] != "4" || r.Params["sent"] != "1" {
		t.Fatalf("send result: %+v", r)
	}
	if err := listener.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	n, _, err := listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 64 || string(buf[:len(tag)]) != string(tag) {
		t.Fatalf("received %d bytes, prefix %x", n, buf[:len(tag)])
	}
}

func TestUDPSendFifthConcurrentRequestIsBusy(t *testing.T) {
	h, _ := newUDPTestHarness(t, nil)
	h.waitConnected()
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	h.a.sendUDP = func(ctx context.Context, _ string, _ []uint16, _ [8]byte, count, _, _ int) (string, int, error) {
		started <- struct{}{}
		select {
		case <-release:
			return "4", count, nil
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}
	for i := 0; i < 4; i++ {
		h.panel.send(udpSendMessage("send_"+string(rune('1'+i)), "127.0.0.1", []uint32{443}, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 1, 1, 64))
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("four sends did not start")
		}
	}
	r := askUDPSend(h, "send_5", "127.0.0.1", []uint32{443}, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 1, 1, 64)
	if r.Ok || r.Error != "busy" {
		t.Fatalf("fifth send: %+v", r)
	}
	close(release)
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		r := h.panel.nextCmd()
		seen[r.RequestId] = r.Ok && r.Error == ""
	}
	for i := 0; i < 4; i++ {
		id := "send_" + string(rune('1'+i))
		if !seen[id] {
			t.Errorf("missing success for %s: %v", id, seen)
		}
	}
}

func TestUDPSendDuringBandwidthMeasurementIsBusy(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	h, _ := newUDPTestHarness(t, func(c *Config) {
		c.SpeedTest = func(ctx context.Context) (speedtest.Result, error) {
			close(started)
			select {
			case <-release:
				return speedtest.Result{DownMbps: 1}, nil
			case <-ctx.Done():
				return speedtest.Result{}, ctx.Err()
			}
		}
	})
	h.waitConnected()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &pb.MeasureBandwidth{RequestId: "measure"}}})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("bandwidth measurement did not start")
	}
	r := askUDPSend(h, "send_busy", "127.0.0.1", []uint32{443}, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 1, 1, 64)
	if r.Ok || r.Error != "busy" {
		t.Fatalf("send during measurement: %+v", r)
	}
	close(release)
	if r := h.panel.nextCmd(); r.RequestId != "measure" || !r.Ok {
		t.Fatalf("measurement answer: %+v", r)
	}
}

func TestUDPCountArmDuringBandwidthMeasurementIsBusy(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	h, _ := newUDPTestHarness(t, func(c *Config) {
		c.SpeedTest = func(ctx context.Context) (speedtest.Result, error) {
			close(started)
			select {
			case <-release:
				return speedtest.Result{DownMbps: 1}, nil
			case <-ctx.Done():
				return speedtest.Result{}, ctx.Err()
			}
		}
	})
	h.waitConnected()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &pb.MeasureBandwidth{RequestId: "measure"}}})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("bandwidth measurement did not start")
	}
	r := askUDPCount(h, "arm_busy", []byte{1, 2, 3, 4, 5, 6, 7, 8}, []uint32{443}, 30, false)
	if r.Ok || r.Error != "busy" {
		t.Fatalf("arm during measurement: %+v", r)
	}
	close(release)
	if r := h.panel.nextCmd(); r.RequestId != "measure" || !r.Ok {
		t.Fatalf("measurement answer: %+v", r)
	}
}

func TestUDPSendBadParams(t *testing.T) {
	h, _ := newUDPTestHarness(t, nil)
	h.waitConnected()
	validTag := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	validPorts := []uint32{443}
	tooManyPorts := []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	for name, tc := range map[string]struct {
		host       string
		ports      []uint32
		tag        []byte
		count, pps uint32
		size       uint32
	}{
		"empty host":        {"", validPorts, validTag, 1, 1, 64},
		"tag length":        {"127.0.0.1", validPorts, validTag[:7], 1, 1, 64},
		"no ports":          {"127.0.0.1", nil, validTag, 1, 1, 64},
		"too many ports":    {"127.0.0.1", tooManyPorts, validTag, 1, 1, 64},
		"duplicate ports":   {"127.0.0.1", []uint32{443, 443}, validTag, 1, 1, 64},
		"zero port":         {"127.0.0.1", []uint32{0}, validTag, 1, 1, 64},
		"out of range port": {"127.0.0.1", []uint32{65536}, validTag, 1, 1, 64},
		"zero count":        {"127.0.0.1", validPorts, validTag, 0, 1, 64},
		"too high count":    {"127.0.0.1", validPorts, validTag, 301, 50, 64},
		"zero rate":         {"127.0.0.1", validPorts, validTag, 1, 0, 64},
		"too high rate":     {"127.0.0.1", validPorts, validTag, 1, 51, 64},
		"too small payload": {"127.0.0.1", validPorts, validTag, 1, 1, 63},
		"too large payload": {"127.0.0.1", validPorts, validTag, 1, 1, 1201},
		"over ten seconds":  {"127.0.0.1", validPorts, validTag, 300, 29, 64},
	} {
		t.Run(name, func(t *testing.T) {
			r := askUDPSend(h, "bad_"+name, tc.host, tc.ports, tc.tag, tc.count, tc.pps, tc.size)
			if r.Ok || r.Error != "bad_params" {
				t.Fatalf("answer = %+v", r)
			}
		})
	}
}
