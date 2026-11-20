package bridge

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/plugin"
)

// The panel's exit announces its own receive windows: 1 MiB a stream in its SETTINGS and a 16 MiB connection window, not
// x/net's client defaults; an agent's exit announces exactly what x/net's default client does.
func TestBridgeTransportSendsExplicitWindows(t *testing.T) {
	streamWindow, connIncrement := announcedWindows(t, newBridgeTransport(true))
	if streamWindow != bridgeStreamWindow || connIncrement != bridgeConnWindow { // x/net adds the whole buffer to the initial 64 KiB
		t.Fatalf("stream window %d, connection increment %d; want %d and %d", streamWindow, connIncrement, bridgeStreamWindow, bridgeConnWindow)
	}
	agentStream, agentConn := announcedWindows(t, newBridgeTransport(false))
	defStream, defConn := announcedWindows(t, &http2.Transport{})
	if agentStream != defStream || agentConn != defConn || agentStream == bridgeStreamWindow {
		t.Fatalf("an agent's exit announces %d/%d, x/net's default %d/%d", agentStream, agentConn, defStream, defConn)
	}
	if newBridgeTransport(false).WriteByteTimeout != 0 {
		t.Fatal("an agent's exit got a write timeout")
	}
}

func announcedWindows(t *testing.T, tr *http2.Transport) (streamWindow, connIncrement uint32) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() { _, _ = tr.NewClientConn(client) }()

	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(server, preface); err != nil || string(preface) != http2.ClientPreface {
		t.Fatalf("preface %q, %v", preface, err)
	}
	framer := http2.NewFramer(io.Discard, server)
	for streamWindow == 0 || connIncrement == 0 {
		f, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v (stream window %d, connection increment %d)", err, streamWindow, connIncrement)
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if v, ok := f.Value(http2.SettingInitialWindowSize); ok {
				streamWindow = v
			}
		case *http2.WindowUpdateFrame:
			if f.StreamID == 0 {
				connIncrement = f.Increment
			}
		}
	}
	return streamWindow, connIncrement
}

// On the panel's exit a local target that stops reading loses its stream after the write progress timeout, instead of
// holding the pair; an agent's exit does not bound it (as before).
func TestDialerTargetWriteProgress(t *testing.T) {
	agent := NewDialer(func() DialIdentity { return DialIdentity{} })
	t.Cleanup(agent.Close)
	agent.timing.writeProgress = 50 * time.Millisecond
	{
		target, stuck := net.Pipe()
		t.Cleanup(func() { _ = target.Close(); _ = stuck.Close() })
		done := make(chan error, 1)
		go func() {
			done <- agent.responseToTarget(context.Background(), &dialRun{dial: plugin.BridgeDial{EntryNodeID: "nod_x"}}, newIdleConn(target, time.Minute),
				strings.NewReader(string(bytes.Repeat([]byte{1}, 64<<10))), newConnTracker())
		}()
		select {
		case err := <-done:
			t.Fatalf("an agent's exit gave up on a slow target: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
	}
	d := NewDialer(func() DialIdentity { return DialIdentity{Panel: true} })
	t.Cleanup(d.Close)
	d.timing.writeProgress = 50 * time.Millisecond
	target, stuck := net.Pipe() // stuck never reads
	t.Cleanup(func() { _ = target.Close(); _ = stuck.Close() })
	conn := newIdleConn(target, time.Minute)
	run := &dialRun{dial: plugin.BridgeDial{EntryNodeID: "nod_host"}}
	src := strings.NewReader(string(bytes.Repeat([]byte{1}, 64<<10)))
	done := make(chan error, 1)
	go func() { done <- d.responseToTarget(context.Background(), run, conn, src, newConnTracker()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a target that never reads took all the bytes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a target that never reads held the stream")
	}
}
