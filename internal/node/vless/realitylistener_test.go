package vless

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	xtlsreality "github.com/xtls/reality"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/reality"
)

// The listener of the vendored REALITY (third_party/reality/MISTGATE.md, "The listener") takes handshakes in goroutines and hands
// the finished ones to Accept. When the inner listener fails, a handshake that finishes afterwards must neither send on a closed
// channel (a data race the race detector reports) nor leave its connection open.
func TestRealityListenerCloseWhileHandshakesFinish(t *testing.T) {
	f := newRuntimeFixture(t, false)
	privateKey, err := base64.RawURLEncoding.DecodeString(testPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(testPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	shortID, _ := hex.DecodeString("6ba85179")
	shortID = append(shortID, make([]byte, 8-len(shortID))...)
	server := (&reality.Config{
		Type: "tcp", Dest: f.targetAddr, ServerNames: []string{"localhost"}, PrivateKey: privateKey, ShortIds: [][]byte{shortID},
	}).GetREALITYConfig()
	client := &reality.Config{Fingerprint: "chrome", ServerName: "localhost", PublicKey: publicKey, ShortId: shortID, SpiderX: "/"}

	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	l := xtlsreality.NewListener(inner, server)
	dest := xnet.TCPDestination(xnet.LocalHostIP, xnet.Port(inner.Addr().(*net.TCPAddr).Port))
	dial := func() net.Conn {
		raw, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Error(err)
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := reality.UClient(raw, client, ctx, dest)
		if err != nil {
			t.Errorf("REALITY handshake: %v", err)
			_ = raw.Close()
			return nil
		}
		return c
	}

	// A handshake finished while somebody accepts is delivered.
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	first := dial()
	if first == nil {
		return
	}
	defer first.Close()
	select {
	case c := <-accepted:
		if c == nil {
			t.Fatal("Accept returned no connection for a finished handshake")
		}
		_ = c.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("Accept did not deliver a finished handshake")
	}

	// Nobody accepts any more: handshakes finish and wait to be handed over while the inner listener fails.
	const n = 12
	conns := make([]net.Conn, n)
	var wg sync.WaitGroup
	for i := range conns {
		wg.Add(1)
		go func() { defer wg.Done(); conns[i] = dial() }()
	}
	time.Sleep(50 * time.Millisecond) // some handshakes are over, some are not
	_ = inner.Close()
	wg.Wait()

	// Every connection the listener never delivered is closed by it, whichever side of the close its handshake ended on.
	for i, c := range conns {
		if c == nil {
			continue
		}
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil || strings.Contains(err.Error(), "timeout") {
			t.Errorf("connection %d is still open after the listener failed: %v", i, err)
		}
		_ = c.Close()
	}
	for range 3 {
		if c, err := l.Accept(); c != nil || err == nil {
			t.Fatalf("Accept after the failure = (%v, %v), want the listener's error", c, err)
		}
	}

	// Neither the accept loop nor a handshake goroutine is left behind.
	deadline := time.Now().Add(10 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		stacks := string(buf[:runtime.Stack(buf, true)])
		if !strings.Contains(stacks, "reality.NewListener.func") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines of the REALITY listener are still running:\n%s", stacks)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
