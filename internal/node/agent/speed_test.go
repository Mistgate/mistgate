package agent

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// speed/1: the bounded throughput test between two nodes (agent.proto "PATH SPEED"), here over loopback.

func speedMessage(id string, step uint32, host string, port, mb uint32, token []byte, up bool) *pb.ConnectResponse {
	u := &pb.UdpSend{RequestId: id, Size: step, Host: host, Tag: token, Count: mb, Pps: 10, Df: up}
	if port != 0 {
		u.Ports = []uint32{port}
	}
	return &pb.ConnectResponse{Message: &pb.ConnectResponse_SpeedTest{SpeedTest: u}}
}

var speedToken = []byte("0123456789abcdef")

func TestSpeedCapabilityIsAdvertised(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if got := (<-h.panel.hellos).Capabilities; !contains(got, "speed/1") {
		t.Fatalf("capabilities %v do not list speed/1", got)
	}
}

func TestSpeedServeAndRunMoveTheDataBothWaysAndThenTheListenerIsGone(t *testing.T) {
	for _, up := range []bool{false, true} {
		h := newHarness(t, harnessOpts{})
		h.waitConnected()
		h.panel.send(speedMessage("serve", 1, "", 0, 8, speedToken, up))
		serve := h.panel.nextCmd()
		port, err := strconv.Atoi(serve.Params["port"])
		if !serve.Ok || err != nil || port == 0 {
			t.Fatalf("up=%v: serve answered %+v", up, serve)
		}
		// a second command while the listener is armed: one test at a time
		h.panel.send(speedMessage("again", 1, "", 0, 8, speedToken, up))
		if r := h.panel.nextCmd(); r.Ok || r.Error != "busy" {
			t.Fatalf("up=%v: a second serve answered %+v, want busy", up, r)
		}
		// a stranger that does not know the token is closed on and does not use the listener up
		stranger, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Fatal(err)
		}
		stranger.Write([]byte("not the token!!!"))
		stranger.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, _ := stranger.Read(make([]byte, 1)); n != 0 {
			t.Fatalf("up=%v: a stranger was served", up)
		}
		stranger.Close()

		// the sender is another agent in reality; the same one may run after its listener work is over, so use the functions
		res, err := runSpeed(context.Background(), net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), speedToken, !up, 8<<20)
		if err != nil {
			t.Fatalf("up=%v: %v", up, err)
		}
		if res.bytes != 8<<20 || res.mbit() <= 0 || res.duration <= 0 {
			t.Fatalf("up=%v: result %+v", up, res)
		}
		// one connection only: the listener is closed after it
		deadline := time.Now().Add(3 * time.Second)
		for {
			c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
			if err != nil {
				break
			}
			c.Close()
			if time.Now().After(deadline) {
				t.Fatalf("up=%v: the listener still accepts after its connection", up)
			}
			time.Sleep(20 * time.Millisecond)
		}
		// and the agent is free again
		deadline = time.Now().Add(3 * time.Second)
		for {
			h.panel.send(speedMessage("free", 1, "", 0, 8, []byte("fedcba9876543210"), up))
			r := h.panel.nextCmd()
			if r.Ok {
				h.panel.send(speedMessage("stop", 3, "", 0, 0, []byte("fedcba9876543210"), up))
				if r := h.panel.nextCmd(); !r.Ok {
					t.Fatalf("stop answered %+v", r)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("up=%v: the agent stayed busy: %+v", up, r)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestSpeedRunCommandThroughTheHandlerAnswersTheFigures(t *testing.T) {
	// the target side is a listener of the real code; the sender is the agent under test
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go serveSpeed(ln, speedToken, true, 8<<20, 10*time.Second)
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.send(speedMessage("run", 2, "127.0.0.1", uint32(ln.Addr().(*net.TCPAddr).Port), 8, speedToken, false))
	r := h.panel.nextCmd()
	if !r.Ok || r.Params["bytes"] != strconv.Itoa(8<<20) {
		t.Fatalf("run answered %+v", r)
	}
	for _, key := range []string{"mbit_s", "connect_ms", "first_byte_ms", "duration_ms"} {
		if _, err := strconv.ParseFloat(r.Params[key], 64); err != nil {
			t.Errorf("%s = %q", key, r.Params[key])
		}
	}
	if f, _ := strconv.ParseFloat(r.Params["mbit_s"], 64); f <= 0 {
		t.Errorf("mbit_s = %v", f)
	}
}

func TestSpeedRefusesWhatIsOutsideTheCapsAndTheWrongDestinations(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	for name, m := range map[string]*pb.ConnectResponse{
		"7 MB":            speedMessage("a", 1, "", 0, 7, speedToken, false),
		"33 MB":           speedMessage("b", 1, "", 0, 33, speedToken, false),
		"no limit":        {Message: &pb.ConnectResponse_SpeedTest{SpeedTest: &pb.UdpSend{RequestId: "c", Size: 1, Tag: speedToken, Count: 8}}},
		"over 30 s":       {Message: &pb.ConnectResponse_SpeedTest{SpeedTest: &pb.UdpSend{RequestId: "d", Size: 1, Tag: speedToken, Count: 8, Pps: 31}}},
		"a short token":   speedMessage("e", 1, "", 0, 8, []byte("short"), false),
		"an unknown step": speedMessage("f", 9, "", 0, 8, speedToken, false),
		"run, no host":    speedMessage("g", 2, "", 4000, 8, speedToken, false),
		"run, no port":    speedMessage("h", 2, "127.0.0.1", 0, 8, speedToken, false),
	} {
		h.panel.send(m)
		if r := h.panel.nextCmd(); r.Ok || r.Error != "bad_params" {
			t.Errorf("%s: %+v, want bad_params", name, r)
		}
	}
	// never a destination a probe refuses: link-local (cloud metadata), unspecified, multicast
	for _, host := range []string{"169.254.169.254", "0.0.0.0", "224.0.0.1"} {
		h.panel.send(speedMessage("host", 2, host, 4000, 8, speedToken, false))
		if r := h.panel.nextCmd(); r.Ok || r.Error != "unsupported_host" {
			t.Errorf("%s: %+v, want unsupported_host", host, r)
		}
	}
	// a port nobody listens on is "refused", not a hang
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	port := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	h.panel.send(speedMessage("closed", 2, "127.0.0.1", uint32(port), 8, speedToken, false))
	if r := h.panel.nextCmd(); r.Ok || r.Error != "refused" {
		t.Errorf("a closed port: %+v, want refused", r)
	}
}

func TestSpeedIsBusyWhileABandwidthTestRuns(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.a.measuring.Store(true)
	defer h.a.measuring.Store(false)
	h.panel.send(speedMessage("busy", 1, "", 0, 8, speedToken, false))
	if r := h.panel.nextCmd(); r.Ok || r.Error != "busy" {
		t.Fatalf("answered %+v, want busy", r)
	}
}

func TestSpeedListenerClosesByItselfAtTheLimitAndOnStop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serveSpeed(ln, speedToken, true, 8<<20, 300*time.Millisecond) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a listener nobody connected to ended without an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the listener outlived its limit")
	}
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		t.Error("the listener still accepts after its limit")
	}

	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.send(speedMessage("serve", 1, "", 0, 8, speedToken, false))
	port := h.panel.nextCmd().Params["port"]
	h.panel.send(speedMessage("stop", 3, "", 0, 0, []byte("another token!!!"), false)) // somebody else's token closes nothing
	if r := h.panel.nextCmd(); !r.Ok {
		t.Fatalf("stop answered %+v", r)
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), time.Second)
	if err != nil {
		t.Fatalf("a stop with another token closed the listener: %v", err)
	}
	c.Close()
	h.panel.send(speedMessage("stop", 3, "", 0, 0, speedToken, false))
	if r := h.panel.nextCmd(); !r.Ok {
		t.Fatalf("stop answered %+v", r)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 200*time.Millisecond)
		if err != nil {
			return
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("stop did not close the listener")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
