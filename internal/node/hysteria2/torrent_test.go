package hysteria2

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/torrentguard"
	"github.com/mistgate/mistgate/internal/plugin"
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
	var evidence []torrentguard.Evidence
	var addrs []string
	guarded := torrentUDPConn{UDPConn: base, attempt: func(p torrentguard.Protocol, e torrentguard.Evidence, addr string) {
		mu.Lock()
		detected = append(detected, p)
		evidence = append(evidence, e)
		addrs = append(addrs, addr)
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
	if len(evidence) != 1 || evidence[0] != torrentguard.EvidenceDHTQuery || len(addrs) != 1 || torrentPort(addrs[0]) != "6881" {
		t.Fatalf("evidence = %v, destinations = %v", evidence, addrs)
	}
}

// DNS goes through untouched: a query is arbitrary bytes that can have the shape of a tracker announce or a uTP SYN.
func TestTorrentUDPConnNeverInspectsDNS(t *testing.T) {
	base := &recordingUDPConn{}
	detected := 0
	guarded := torrentUDPConn{UDPConn: base, attempt: func(torrentguard.Protocol, torrentguard.Evidence, string) { detected++ }}
	syn := make([]byte, 20)
	syn[0] = 0x41
	dns := dnsQueryWithEDNSCookie()
	for _, addr := range []string{"1.1.1.1:53", "[2606:4700::1111]:53", "224.0.0.251:5353"} {
		for _, p := range [][]byte{dns, syn} {
			if n, err := guarded.WriteTo(p, addr); err != nil || n != len(p) {
				t.Fatalf("write to %s = %d, %v", addr, n, err)
			}
		}
	}
	if detected != 0 || len(base.writes) != 6 {
		t.Fatalf("detected %d, forwarded %d datagrams; want 0 and 6", detected, len(base.writes))
	}
	// the same SYN shape to another port is still a uTP start
	if _, err := guarded.WriteTo(syn, "203.0.113.5:51413"); err != nil || detected != 1 || len(base.writes) != 6 {
		t.Fatalf("SYN to a peer port: detected %d, forwarded %d, err %v", detected, len(base.writes), err)
	}
}

// dnsQueryWithEDNSCookie is a plain 98-byte DNS query (ID 0x1234, one question, an EDNS OPT record with a COOKIE option).
// Its bytes have the exact layout of a BEP 15 announce.
func dnsQueryWithEDNSCookie() []byte {
	packet := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 1}
	for _, label := range []int{20, 20, 15} {
		packet = append(packet, byte(label))
		for i := 0; i < label; i++ {
			packet = append(packet, 'a'+byte(i%26))
		}
	}
	packet = append(packet, 0, 0, 1, 0, 1)
	packet = append(packet, 0, 0, 41, 0x04, 0xd0, 0, 0, 0, 0, 0, 12)
	return append(packet, 0, 10, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8)
}

func TestTorrentPortKeepsOnlyTheNumber(t *testing.T) {
	for addr, want := range map[string]string{
		"198.51.100.3:6881":  "6881",
		"[2001:db8::1]:6969": "6969",
		"tracker.example:80": "80",
		"198.51.100.3":       "",
		"198.51.100.3:http":  "",
		"198.51.100.3:0":     "",
		"198.51.100.3:70000": "",
		"":                   "",
	} {
		if got := torrentPort(addr); got != want {
			t.Errorf("torrentPort(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestReportTorrentCarriesEvidenceAndPortNeverAddress(t *testing.T) {
	var got engine.Event
	e := &eng{}
	e.env.Event = func(ev engine.Event) { got = ev }
	in := &inbound{e: e, spec: plugin.InboundSpec{ID: "hy-1"}}
	in.reportTorrent(torrentguard.ProtocolBitTorrentTracker, torrentguard.EvidenceTrackerConnect, "udp", "usr_a", torrentPort("198.51.100.9:6969"))
	want := map[string]string{"protocol": "udp", "torrent_protocol": "bittorrent_tracker", "evidence": "tracker_connect", "dst_port": "6969", "user_id": "usr_a"}
	if got.Code != "torrent_attempt" || len(got.Params) != len(want) {
		t.Fatalf("event = %+v", got)
	}
	for k, v := range want {
		if got.Params[k] != v {
			t.Errorf("param %s = %q, want %q", k, got.Params[k], v)
		}
	}
}

type recordingUDPConn struct{ writes [][]byte }

func (c *recordingUDPConn) ReadFrom([]byte) (int, string, error) { return 0, "", io.EOF }
func (c *recordingUDPConn) WriteTo(p []byte, _ string) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (*recordingUDPConn) Close() error { return nil }
