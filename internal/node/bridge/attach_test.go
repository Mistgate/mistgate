package bridge

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func echoOver(t *testing.T, conn net.Conn, pki testPKI) {
	t.Helper()
	client := tls.Client(conn, &tls.Config{RootCAs: pki.roots, ServerName: "msk1.example", MinVersion: tls.VersionTLS13})
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	payload := []byte("through a pair the caller attached")
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil || string(got) != string(payload) {
		t.Fatalf("echo %q, %v", got, err)
	}
}

// A caller that accepted a connection itself hands it to a pair (Attach), and the entry's own agent dials the exit's target
// the same way (DialExit); both reach the exit's target.
func TestEntryAttachAndDialExit(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, _, _ := newBridgeFixture(t, target.port, pki)
	waitFor(t, 10*time.Second, func() bool {
		states := dialer.States()
		return len(states) == 1 && states[0].Conns == 2 && entry.Connected("nod_de1")
	}, "dialer did not connect")

	person, accepted := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- entry.Attach(context.Background(), "nod_de1", Client{Conn: accepted, RelayID: "rly_a"})
	}()
	echoOver(t, person, pki)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Attach after the stream ended: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Attach did not return once the person left")
	}

	conn, err := entry.DialExit(context.Background(), "nod_de1", "rly_a", 0)
	if err != nil {
		t.Fatal(err)
	}
	echoOver(t, conn, pki)

	if err := entry.Attach(context.Background(), "nod_other", Client{Conn: accepted, RelayID: "rly_a"}); !errors.Is(err, ErrExitNotKnown) {
		t.Fatalf("an exit the entry does not serve: %v", err)
	}
	if _, err := entry.DialExit(context.Background(), "nod_de1", "bad relay id", 0); !errors.Is(err, ErrExitNotKnown) {
		t.Fatalf("a malformed relay id: %v", err)
	}
}

// Without a pair, Attach gives up after ClientWaitForStream and leaves the connection to its caller; DialExit gives up when
// its context ends.
func TestEntryAttachWithoutPairLeavesTheConnection(t *testing.T) {
	pki := newTestPKI(t)
	entry := NewEntry()
	t.Cleanup(entry.Close)
	if err := entry.SetServe(ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{"nod_de1"}, Roots: pki.roots}); err != nil {
		t.Fatal(err)
	}
	if entry.Connected("nod_de1") {
		t.Fatal("no exit is connected")
	}
	person, accepted := net.Pipe()
	t.Cleanup(func() { _ = person.Close(); _ = accepted.Close() })
	started := time.Now()
	if err := entry.Attach(context.Background(), "nod_de1", Client{Conn: accepted, RelayID: "rly_a"}); !errors.Is(err, ErrNoPair) {
		t.Fatalf("Attach without a pair: %v", err)
	}
	if waited := time.Since(started); waited < ClientWaitForStream/2 || waited > 5*ClientWaitForStream {
		t.Fatalf("Attach waited %v", waited)
	}
	go func() { _, _ = person.Write([]byte{1}) }()
	_ = accepted.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := accepted.Read(make([]byte, 1)); err != nil {
		t.Fatalf("the connection must stay open for its caller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := entry.DialExit(ctx, "nod_de1", "rly_a", 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DialExit with an ended context: %v", err)
	}
}

// A context that ends just as the stream gets its pair: DialExit returns the context's error, never a finished connection
// with a nil error.
func TestEntryDialExitCancelledAtAttach(t *testing.T) {
	pki := newTestPKI(t)
	target := startEchoTarget(t, pki.serverCert)
	entry, dialer, _, _ := newBridgeFixture(t, target.port, pki)
	waitFor(t, 10*time.Second, func() bool {
		states := dialer.States()
		return len(states) == 1 && states[0].Conns == 2 && entry.Connected("nod_de1")
	}, "dialer did not connect")
	ctx, cancel := context.WithCancel(context.Background())
	entry.paired = cancel
	conn, err := entry.DialExit(ctx, "nod_de1", "rly_a", 0)
	if !errors.Is(err, context.Canceled) || conn != nil {
		t.Fatalf("DialExit cancelled at attach = %v, %v; want context.Canceled and no connection", conn, err)
	}
	entry.paired = nil
	conn, err = entry.DialExit(context.Background(), "nod_de1", "rly_a", 0)
	if err != nil {
		t.Fatalf("the pair after a cancelled dial: %v", err)
	}
	echoOver(t, conn, pki)
}
