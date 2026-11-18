package agent

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// speed-channel/1: the 8-stream, timed mode of the speed test (agent.proto "CHANNEL MEASURE"), over loopback.

var channelToken = []byte("fedcba9876543210")

// unbounded is a volume cap no test reaches: loopback moves gigabytes a second, so only a window can end a run.
const unbounded = 1 << 40

// slowTarget accepts streams and trickles bytes at them, so a run lasts its whole window.
func slowTarget(t *testing.T) net.Listener {
	t.Helper()
	ln := channelListener(t)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.ReadFull(c, make([]byte, channelHelloLength))
				for {
					if _, err := c.Write(make([]byte, 1024)); err != nil {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
		}
	}()
	return ln
}

func channelMessage(id string, step uint32, host string, port, mb, seconds uint32, token []byte, up bool) *pb.ConnectResponse {
	u := &pb.UdpSend{RequestId: id, Size: step, Host: host, Tag: token, Count: mb, Pps: seconds, Df: up}
	if port != 0 {
		u.Ports = []uint32{port}
	}
	return &pb.ConnectResponse{Message: &pb.ConnectResponse_SpeedTest{SpeedTest: u}}
}

func channelListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func channelDial(t *testing.T, ln net.Listener, token []byte, idx byte) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Write(append(append([]byte(nil), token...), idx)); err != nil {
		t.Fatal(err)
	}
	return c
}

// closedWithin says whether the other side closed the connection (a read ends) within the wait, whatever it sent before.
func closedWithin(c net.Conn, wait time.Duration) (bool, int64) {
	c.SetReadDeadline(time.Now().Add(wait))
	n, err := io.Copy(io.Discard, c)
	return err == nil, n
}

func TestSpeedChannelCapabilityIsAdvertised(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if got := (<-h.panel.hellos).Capabilities; !contains(got, "speed-channel/1") {
		t.Fatalf("capabilities %v do not list speed-channel/1", got)
	}
}

func TestChannelMovesDataOverEightStreamsBothWays(t *testing.T) {
	lim := channelLimits{window: 400 * time.Millisecond, hard: 3 * time.Second}
	for _, down := range []bool{true, false} {
		ln := channelListener(t)
		type served struct {
			moved int64
			err   error
		}
		ch := make(chan served, 1)
		go func() {
			moved, err := serveChannel(context.Background(), ln, channelToken, down, unbounded, 5*time.Second, lim.hard)
			ch <- served{moved, err}
		}()
		res, err := runChannel(context.Background(), ln.Addr().String(), channelToken, down, unbounded, lim)
		if err != nil {
			t.Fatalf("down=%v: %v", down, err)
		}
		if res.streams != channelStreams || res.bytes <= 0 || res.mbit() <= 0 || res.capped {
			t.Fatalf("down=%v: result %+v", down, res)
		}
		if down && (res.duration > lim.window+200*time.Millisecond || res.firstByte <= 0) {
			t.Errorf("down: the window is %v, took %v, first byte %v", lim.window, res.duration, res.firstByte)
		}
		s := <-ch
		if s.err != nil || s.moved <= 0 {
			t.Fatalf("down=%v: the target ended with %+v", down, s)
		}
		if !down && res.bytes != s.moved {
			t.Errorf("up: the sender counted %d, the target received %d", res.bytes, s.moved)
		}
		// the eighth stream closed the listener: nobody gets in any more
		if c, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
			c.Close()
			t.Errorf("down=%v: the listener still accepts after its eight streams", down)
		}
	}
}

func TestChannelListenerAcceptsNothingWithoutTheToken(t *testing.T) {
	for _, down := range []bool{true, false} {
		ln := channelListener(t)
		done := make(chan int64, 1)
		go func() {
			moved, _ := serveChannel(context.Background(), ln, channelToken, down, 1<<20, 600*time.Millisecond, time.Second)
			done <- moved
		}()
		for name, hello := range map[string][]byte{
			"a wrong token": append([]byte("0123456789abcdef"), 0),
			"a short hello": []byte("fedcba98"),
			"no token":      nil,
		} {
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			c.Write(hello)
			c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			if n, _ := c.Read(make([]byte, 1)); n != 0 {
				t.Errorf("down=%v, %s: got data", down, name)
			}
			c.Close()
		}
		if moved := <-done; moved != 0 {
			t.Errorf("down=%v: %d bytes moved for strangers", down, moved)
		}
	}
}

// tokenCap is far above what the socket buffers of the first stream can swallow while the other seven connect (a few MB on Linux), so
// the shared volume is still there for them.
const tokenCap = 64 << 20

func TestChannelTokenWorksOncePerIndexAndForEightStreams(t *testing.T) {
	ln := channelListener(t)
	done := make(chan int64, 1)
	go func() {
		moved, _ := serveChannel(context.Background(), ln, channelToken, true, tokenCap, 5*time.Second, 2*time.Second)
		done <- moved
	}()
	first := channelDial(t, ln, channelToken, 3)
	// the first stream is in once it is served (the two are handled at the same time, so the replay waits for that)
	first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := first.Read(make([]byte, 4096)); n == 0 {
		t.Fatalf("the first stream got nothing: %v", err)
	}
	// the same token with the index of a stream that is already in: closed, no data
	replay := channelDial(t, ln, channelToken, 3)
	replay.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _ := replay.Read(make([]byte, 1)); n != 0 {
		t.Fatal("a replayed token+index was served")
	}
	// an index outside 0-7 is no stream
	bad := channelDial(t, ln, channelToken, 8)
	bad.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _ := bad.Read(make([]byte, 1)); n != 0 {
		t.Fatal("stream index 8 was served")
	}
	// seven more streams fill the eight; after them the listener is gone
	var conns []net.Conn
	for i := 0; i < channelStreams; i++ {
		if i == 3 {
			continue
		}
		conns = append(conns, channelDial(t, ln, channelToken, byte(i)))
	}
	for _, c := range conns {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, err := c.Read(make([]byte, 1)); n == 0 {
			t.Fatalf("a stream of the eight got nothing: %v", err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), 100*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("a ninth stream could still connect")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// the volume is held on the target: all the streams together got at most the cap
	for _, c := range append(conns, first) {
		io.Copy(io.Discard, c)
	}
	if moved := <-done; moved > tokenCap {
		t.Fatalf("the target sent %d bytes, its cap is %d", moved, int64(tokenCap))
	}
}

func TestChannelTargetHoldsItsVolumeCapWhoeverAsks(t *testing.T) {
	const cap = 1 << 20
	for _, down := range []bool{true, false} {
		ln := channelListener(t)
		done := make(chan int64, 1)
		go func() {
			moved, _ := serveChannel(context.Background(), ln, channelToken, down, cap, 5*time.Second, 3*time.Second)
			done <- moved
		}()
		var wg sync.WaitGroup
		for i := 0; i < channelStreams; i++ {
			c := channelDial(t, ln, channelToken, byte(i))
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				if down {
					io.Copy(io.Discard, c)
					return
				}
				// a sender that ignores the cap: it writes far more than the target will take
				buf := make([]byte, 64<<10)
				for j := 0; j < 160; j++ {
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}()
		}
		wg.Wait()
		if moved := <-done; moved != cap {
			t.Errorf("down=%v: the target moved %d bytes, want exactly its cap %d", down, moved, cap)
		}
	}
}

func TestChannelTargetHoldsItsTimeLimitWhateverThePeerDoes(t *testing.T) {
	for _, down := range []bool{true, false} {
		ln := channelListener(t)
		start := time.Now()
		done := make(chan struct{})
		go func() {
			// the function returns when the listener's time and every stream are over: a stream that outlives its limit hangs it
			serveChannel(context.Background(), ln, channelToken, down, unbounded, 700*time.Millisecond, 300*time.Millisecond)
			close(done)
		}()
		// shows the token and then does nothing: neither reads (down fills the buffers) nor writes (up waits)
		c := channelDial(t, ln, channelToken, 0)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("down=%v: the target is still serving long after its limit", down)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("down=%v: the target took %v for a limit of 300 ms", down, took)
		}
		if ok, _ := closedWithin(c, time.Second); !ok && !down {
			t.Errorf("up: the stream was left open")
		}
	}
}

func TestChannelCancelStopsEveryStream(t *testing.T) {
	ln := channelListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		serveChannel(ctx, ln, channelToken, true, unbounded, 5*time.Second, 10*time.Second)
		close(done)
	}()
	c := channelDial(t, ln, channelToken, 0)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, _ := c.Read(make([]byte, 1)); n == 0 {
		t.Fatal("no data before the cancel")
	}
	cancel() // the panel link is gone
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the target kept serving after its context ended")
	}
	if ok, _ := closedWithin(c, time.Second); !ok {
		t.Error("the stream survived the cancel")
	}

	// the sender: the same, mid-transfer
	ln2 := slowTarget(t)
	rctx, rcancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := runChannel(rctx, ln2.Addr().String(), channelToken, true, unbounded, channelLimits{window: 8 * time.Second, hard: 10 * time.Second})
		finished <- err
	}()
	time.Sleep(300 * time.Millisecond)
	rcancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Error("a cancelled run reported a result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the sender kept running after its context ended")
	}
}

func TestChannelRunHoldsItsOwnVolumeCap(t *testing.T) {
	// the target would send far more than the sender's cap
	ln := channelListener(t)
	go serveChannel(context.Background(), ln, channelToken, true, unbounded, 5*time.Second, 5*time.Second)
	res, err := runChannel(context.Background(), ln.Addr().String(), channelToken, true, 2<<20, channelLimits{window: 5 * time.Second, hard: 8 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.bytes > 2<<20 || !res.capped || res.duration > 3*time.Second {
		t.Fatalf("a run capped at 2 MiB read %d bytes in %v, capped=%v", res.bytes, res.duration, res.capped)
	}
	// up: the sender stops writing at its cap, so the target counts no more than that
	ln2 := channelListener(t)
	moved := make(chan int64, 1)
	go func() {
		m, _ := serveChannel(context.Background(), ln2, channelToken, false, unbounded, 5*time.Second, 5*time.Second)
		moved <- m
	}()
	res, err = runChannel(context.Background(), ln2.Addr().String(), channelToken, false, 2<<20, channelLimits{window: 5 * time.Second, hard: 8 * time.Second})
	if err != nil || !res.capped || res.bytes > 2<<20 {
		t.Fatalf("an upload capped at 2 MiB: %+v, %v", res, err)
	}
	if m := <-moved; m > 2<<20 {
		t.Fatalf("the target received %d bytes of a 2 MiB upload", m)
	}
}

func TestChannelRunFailsCleanlyWhereNobodyListens(t *testing.T) {
	closed := channelListener(t)
	addr := closed.Addr().String()
	closed.Close()
	if _, err := runChannel(context.Background(), addr, channelToken, true, 1<<20, channelLimits{window: time.Second, hard: 2 * time.Second}); err == nil {
		t.Fatal("a run against a closed port reported a result")
	}
}

func TestChannelCommandThroughTheHandler(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.a.speed.testLimits = &channelLimits{window: 300 * time.Millisecond, hard: 2 * time.Second}

	// refused: outside the caps, wrong size of the token, an unknown shape
	for name, m := range map[string]*pb.ConnectResponse{
		"63 MiB":            channelMessage("a", 4, "", 0, 63, 20, channelToken, false),
		"2049 MiB":          channelMessage("b", 4, "", 0, 2049, 20, channelToken, false),
		"serve over 20 s":   channelMessage("c", 4, "", 0, 2048, 21, channelToken, false),
		"serve no limit":    channelMessage("d", 4, "", 0, 2048, 0, channelToken, false),
		"a short token":     channelMessage("e", 4, "", 0, 2048, 20, []byte("short"), false),
		"run under 12 s":    channelMessage("f", 5, "127.0.0.1", 4000, 2048, 11, channelToken, false),
		"run over 15 s":     channelMessage("g", 5, "127.0.0.1", 4000, 2048, 16, channelToken, false),
		"run, no host":      channelMessage("h", 5, "", 4000, 2048, 15, channelToken, false),
		"run, no port":      channelMessage("i", 5, "127.0.0.1", 0, 2048, 15, channelToken, false),
		"run, port 70000":   channelMessage("j", 5, "127.0.0.1", 70000, 2048, 15, channelToken, false),
		"a step in between": channelMessage("k", 6, "", 0, 2048, 15, channelToken, false),
	} {
		h.panel.send(m)
		if r := h.panel.nextCmd(); r.Ok || r.Error != "bad_params" {
			t.Errorf("%s: %+v, want bad_params", name, r)
		}
	}
	// never a destination a probe refuses
	for _, host := range []string{"169.254.169.254", "0.0.0.0", "224.0.0.1"} {
		h.panel.send(channelMessage("host", 5, host, 4000, 2048, 15, channelToken, false))
		if r := h.panel.nextCmd(); r.Ok || r.Error != "unsupported_host" {
			t.Errorf("%s: %+v, want unsupported_host", host, r)
		}
	}

	// serve: one test at a time (a second serve and a speed/1 serve are both busy), a stop with another token does nothing
	h.panel.send(channelMessage("serve", 4, "", 0, 2048, 20, channelToken, false))
	serve := h.panel.nextCmd()
	port, err := strconv.Atoi(serve.Params["port"])
	if !serve.Ok || err != nil || port == 0 {
		t.Fatalf("serve answered %+v", serve)
	}
	h.panel.send(channelMessage("again", 4, "", 0, 2048, 20, []byte("0123456789abcdef"), false))
	if r := h.panel.nextCmd(); r.Ok || r.Error != "busy" {
		t.Fatalf("a second channel serve answered %+v, want busy", r)
	}
	h.panel.send(speedMessage("old", 1, "", 0, 8, []byte("0123456789abcdef"), false))
	if r := h.panel.nextCmd(); r.Ok || r.Error != "busy" {
		t.Fatalf("a speed/1 serve during a channel serve answered %+v, want busy", r)
	}
	h.panel.send(channelMessage("wrong", 3, "", 0, 0, 0, []byte("0123456789abcdef"), false))
	h.panel.nextCmd()
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second); err != nil {
		t.Fatal("a stop with another token closed the listener")
	} else {
		c.Close()
	}
	// a stop with the token closes it at once
	h.panel.send(channelMessage("stop", 3, "", 0, 0, 0, channelToken, false))
	if r := h.panel.nextCmd(); !r.Ok {
		t.Fatalf("stop answered %+v", r)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("the listener still accepts after a stop with its token")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// run: the agent is the sender against a target of the real code
	for _, up := range []bool{false, true} {
		ln := channelListener(t)
		go serveChannel(context.Background(), ln, channelToken, !up, 2<<30, 5*time.Second, 2*time.Second)
		waitFree(t, h)
		h.panel.send(channelMessage("run", 5, "127.0.0.1", uint32(ln.Addr().(*net.TCPAddr).Port), 2048, 15, channelToken, up))
		r := h.panel.nextCmd()
		if !r.Ok || r.Params["streams"] != "8" {
			t.Fatalf("up=%v: run answered %+v", up, r)
		}
		if f, _ := strconv.ParseFloat(r.Params["mbit_s"], 64); f <= 0 {
			t.Errorf("up=%v: mbit_s = %q", up, r.Params["mbit_s"])
		}
		if b, _ := strconv.ParseInt(r.Params["bytes"], 10, 64); b <= 0 || b > 2<<30 {
			t.Errorf("up=%v: bytes = %q", up, r.Params["bytes"])
		}
	}
}

// waitFree retries a harmless stop until the agent has released the previous test.
func waitFree(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.a.speed.mu.Lock()
		busy := h.a.speed.busy
		h.a.speed.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent stayed busy")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestChannelStopEndsARunningMeasurement(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.a.speed.testLimits = &channelLimits{window: 8 * time.Second, hard: 10 * time.Second}
	ln := slowTarget(t)
	h.panel.send(channelMessage("run", 5, "127.0.0.1", uint32(ln.Addr().(*net.TCPAddr).Port), 2048, 15, channelToken, false))
	time.Sleep(400 * time.Millisecond)
	start := time.Now()
	h.panel.send(channelMessage("stop", 3, "", 0, 0, 0, channelToken, false))
	var got []*pb.CommandResult
	for len(got) < 2 {
		got = append(got, h.panel.nextCmd())
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the run took %v to stop", took)
	}
	for _, r := range got {
		if r.RequestId == "run" && r.Ok {
			t.Fatalf("a stopped run still answered a result: %+v", r)
		}
	}
}

func TestChannelBudgetStopsAPanelThatLoops(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	for i := 0; i < channelBudget; i++ {
		h.panel.send(channelMessage("serve", 4, "", 0, 2048, 1, channelToken, false))
		if r := h.panel.nextCmd(); !r.Ok {
			t.Fatalf("step %d answered %+v", i, r)
		}
		h.panel.send(channelMessage("stop", 3, "", 0, 0, 0, channelToken, false))
		h.panel.nextCmd()
		waitFree(t, h)
	}
	h.panel.send(channelMessage("serve", 4, "", 0, 2048, 1, channelToken, false))
	if r := h.panel.nextCmd(); r.Ok || r.Error != "too_soon" {
		t.Fatalf("step %d answered %+v, want too_soon", channelBudget+1, r)
	}
	// the budget is of steps in ten minutes: a step older than that is forgotten
	h.a.speed.mu.Lock()
	for i := range h.a.speed.steps {
		h.a.speed.steps[i] = time.Now().Add(-channelBudgetFor - time.Second)
	}
	h.a.speed.mu.Unlock()
	h.panel.send(channelMessage("serve", 4, "", 0, 2048, 1, channelToken, false))
	if r := h.panel.nextCmd(); !r.Ok {
		t.Fatalf("after the window the step answered %+v", r)
	}
}

func TestChannelIsBusyWhileABandwidthTestRuns(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.a.measuring.Store(true)
	defer h.a.measuring.Store(false)
	h.panel.send(channelMessage("busy", 4, "", 0, 2048, 20, channelToken, false))
	if r := h.panel.nextCmd(); r.Ok || r.Error != "busy" {
		t.Fatalf("answered %+v, want busy", r)
	}
}

func TestChannelAckIsTheBytesTheTargetReceived(t *testing.T) {
	ln := channelListener(t)
	go serveChannel(context.Background(), ln, channelToken, false, 1<<30, 5*time.Second, 3*time.Second)
	c := channelDial(t, ln, channelToken, 0)
	const sent = 300_000
	c.Write(make([]byte, sent))
	c.(*net.TCPConn).CloseWrite()
	var ack [8]byte
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, ack[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint64(ack[:]); got != sent {
		t.Fatalf("the target says it received %d of %d", got, sent)
	}
}
