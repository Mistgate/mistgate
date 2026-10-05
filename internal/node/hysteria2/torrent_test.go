package hysteria2

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/torrentguard"
)

func TestHysteriaRequestAttributionRejectsAmbiguousDestination(t *testing.T) {
	owner := func(id string) *credState {
		state := &credState{id: id}
		state.owner.Store(&credIdentity{userID: id})
		return state
	}
	newInbound := func() *inbound {
		e := &eng{}
		e.torrentEnabled.Store(true)
		in := &inbound{e: e}
		in.idx.Store(&index{byID: map[string]*credState{
			"cred-a": owner("user-a"),
			"cred-b": owner("user-b"),
		}})
		return in
	}

	t.Run("unique", func(t *testing.T) {
		in := newInbound()
		in.TCPRequest(nil, "cred-a", "Example.COM:443")
		if got := in.takeRequestUserID("example.com:443"); got != "user-a" {
			t.Fatalf("user id = %q, want user-a", got)
		}
	})
	t.Run("ambiguous concurrent users", func(t *testing.T) {
		in := newInbound()
		in.TCPRequest(nil, "cred-a", "example.com:443")
		in.TCPRequest(nil, "cred-b", "example.com:443")
		if got := in.takeRequestUserID("example.com:443"); got != "" {
			t.Fatalf("ambiguous user id = %q, want no attribution", got)
		}
		if got := in.takeRequestUserID("example.com:443"); got != "" {
			t.Fatalf("second ambiguous user id = %q, want no attribution", got)
		}
	})
}

func TestTorrentTCPConnDropsSplitHandshake(t *testing.T) {
	local, remote := net.Pipe()
	attempted := make(chan struct{}, 1)
	readDone := make(chan []byte, 1)
	go func() {
		got, _ := io.ReadAll(remote)
		readDone <- got
	}()
	guarded := newTorrentTCPConn(local, func() { attempted <- struct{}{} })
	signature := []byte("\x13BitTorrent protocol")
	if n, err := guarded.Write(signature[:7]); err != nil || n != 7 {
		t.Fatalf("first write = %d, %v", n, err)
	}
	if n, err := guarded.Write(signature[7:]); err != nil || n != len(signature)-7 {
		t.Fatalf("second write = %d, %v", n, err)
	}
	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("torrent attempt was not reported")
	}
	select {
	case got := <-readDone:
		if len(got) != 0 {
			t.Fatalf("remote received handshake bytes: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("remote connection did not close")
	}
	_ = remote.Close()
}

func TestTorrentTCPConnPassesOrdinaryDataAfterPrefixMismatch(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	guarded := newTorrentTCPConn(local, func() { t.Error("reported an ordinary stream as BitTorrent") })
	want := []byte("\x13BitTorrent protoxordinary data")
	gotCh := make(chan []byte, 1)
	go func() {
		got := make([]byte, len(want))
		_, _ = io.ReadFull(remote, got)
		gotCh <- got
	}()
	if n, err := guarded.Write(want[:8]); err != nil || n != 8 {
		t.Fatalf("first write = %d, %v", n, err)
	}
	if n, err := guarded.Write(want[8:]); err != nil || n != len(want)-8 {
		t.Fatalf("second write = %d, %v", n, err)
	}
	select {
	case got := <-gotCh:
		if string(got) != string(want) {
			t.Fatalf("remote got %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary stream was not forwarded")
	}
	_ = guarded.Close()
}

// A peer that reads nothing (a zero window) blocks a Write; Close must still return at once and end that Write, or
// every such stream leaks a goroutine, a stream and a socket.
func TestTorrentTCPConnCloseDoesNotWaitForABlockedWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		writes    []string
		readFirst int // bytes the peer reads before it stops reading
	}{
		"flushing the held prefix":     {writes: []string{"\x13Bit", "ordinary"}},
		"after the stream was decided": {writes: []string{"GET / HTTP/1.1\r\n", "more"}, readFirst: 16},
	} {
		writes := tc.writes
		t.Run(name, func(t *testing.T) {
			local, remote := net.Pipe() // a Write blocks until the peer reads
			defer remote.Close()
			if tc.readFirst > 0 {
				go func() { _, _ = io.ReadFull(remote, make([]byte, tc.readFirst)) }()
			}
			guarded := newTorrentTCPConn(local, nil)
			done := make(chan error, 1)
			go func() {
				var err error
				for _, w := range writes {
					if _, err = guarded.Write([]byte(w)); err != nil {
						break
					}
				}
				done <- err
			}()
			time.Sleep(50 * time.Millisecond) // let the Write block on the pipe
			closed := make(chan error, 1)
			go func() { closed <- guarded.Close() }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close waited for a blocked Write")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("the blocked Write succeeded after Close")
				}
			case <-time.After(time.Second):
				t.Fatal("the blocked Write did not end after Close")
			}
			if err := guarded.Close(); err != nil {
				t.Fatalf("second Close = %v", err)
			}
		})
	}
}

func TestTorrentUDPConnDropsOnlyDetectedDatagrams(t *testing.T) {
	base := &recordingUDPConn{}
	var mu sync.Mutex
	var detected []torrentguard.Protocol
	guarded := torrentUDPConn{UDPConn: base, attempt: func(p torrentguard.Protocol) {
		mu.Lock()
		detected = append(detected, p)
		mu.Unlock()
	}}
	query := []byte("d1:ad2:id20:aaaaaaaaaaaaaaaaaaaae1:q4:ping1:t2:aa1:y1:qe")
	if n, err := guarded.WriteTo(query, "198.51.100.3:6881"); err != nil || n != len(query) {
		t.Fatalf("torrent datagram write = %d, %v", n, err)
	}
	plain := []byte("ordinary udp")
	if n, err := guarded.WriteTo(plain, "198.51.100.3:6881"); err != nil || n != len(plain) {
		t.Fatalf("ordinary datagram write = %d, %v", n, err)
	}
	if len(base.writes) != 1 || string(base.writes[0]) != string(plain) {
		t.Fatalf("forwarded datagrams = %q, want only %q", base.writes, plain)
	}
	if len(detected) != 1 || detected[0] != torrentguard.ProtocolBitTorrentDHT {
		t.Fatalf("detected protocols = %v", detected)
	}
}

type recordingUDPConn struct{ writes [][]byte }

func (c *recordingUDPConn) ReadFrom([]byte) (int, string, error) { return 0, "", io.EOF }
func (c *recordingUDPConn) WriteTo(p []byte, _ string) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (*recordingUDPConn) Close() error { return nil }
