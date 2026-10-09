package torrentguard

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestWrapTCPDropsSplitHandshake(t *testing.T) {
	local, remote := net.Pipe()
	attempted := make(chan struct{}, 1)
	readDone := make(chan []byte, 1)
	go func() {
		got, _ := io.ReadAll(remote)
		readDone <- got
	}()
	guarded := WrapTCP(local, func() { attempted <- struct{}{} })
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

func TestWrapTCPPassesOrdinaryDataAfterPrefixMismatch(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	guarded := WrapTCP(local, func() { t.Error("reported an ordinary stream as BitTorrent") })
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

// Close must interrupt a blocked network write without waiting for it to release wrapper state.
func TestWrapTCPCloseDoesNotWaitForABlockedWrite(t *testing.T) {
	for name, write := range map[string]func(net.Conn) error{
		"flushing the held prefix": func(guarded net.Conn) error {
			if n, err := guarded.Write([]byte("\x13Bit")); err != nil || n != 4 {
				return err
			}
			_, err := guarded.Write([]byte("ordinary"))
			return err
		},
		"after the stream was decided": func(guarded net.Conn) error {
			_, err := guarded.Write([]byte("GET / HTTP/1.1\r\n"))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			local, remote := net.Pipe()
			defer remote.Close()
			started := make(chan struct{})
			guarded := WrapTCP(&writeSignalConn{Conn: local, started: started}, nil)
			done := make(chan error, 1)
			go func() { done <- write(guarded) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("write did not reach the blocking connection")
			}
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
		})
	}
}

type writeSignalConn struct {
	net.Conn
	once    sync.Once
	started chan struct{}
}

func (c *writeSignalConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func TestWrapTCPCloseWriteFlushesPrefixAndHalfCloses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	guarded := WrapTCP(client, nil)
	prefix := []byte("\x13Bit")
	if n, err := guarded.Write(prefix); err != nil || n != len(prefix) {
		t.Fatalf("held prefix write = %d, %v", n, err)
	}
	if err := guarded.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite = %v", err)
	}
	got, err := io.ReadAll(server)
	if err != nil {
		t.Fatalf("read after CloseWrite = %v", err)
	}
	if string(got) != string(prefix) {
		t.Fatalf("peer received %q, want held prefix %q", got, prefix)
	}
}

func TestWrapUDPDropsOnlyDetectedDatagrams(t *testing.T) {
	base := &recordingUDPConn{}
	var mu sync.Mutex
	var detected []Protocol
	var evidence []Evidence
	var addrs []string
	guarded := WrapUDP(base, func(p Protocol, e Evidence, addr string) {
		mu.Lock()
		detected = append(detected, p)
		evidence = append(evidence, e)
		addrs = append(addrs, addr)
		mu.Unlock()
	})
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
	if len(detected) != 1 || detected[0] != ProtocolBitTorrentDHT {
		t.Fatalf("detected protocols = %v", detected)
	}
	if len(evidence) != 1 || evidence[0] != EvidenceDHTQuery || len(addrs) != 1 || Port(addrs[0]) != "6881" {
		t.Fatalf("evidence = %v, destinations = %v", evidence, addrs)
	}
}

// DNS goes through untouched: a query is arbitrary bytes that can resemble a tracker announce or a uTP SYN.
func TestWrapUDPNeverInspectsDNS(t *testing.T) {
	base := &recordingUDPConn{}
	detected := 0
	guarded := WrapUDP(base, func(Protocol, Evidence, string) { detected++ })
	syn := make([]byte, 20)
	syn[0] = 0x41
	binary.BigEndian.PutUint32(syn[4:8], 1)
	binary.BigEndian.PutUint32(syn[12:16], 1<<20)
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
	if _, err := guarded.WriteTo(syn, "203.0.113.5:51413"); err != nil || detected != 1 || len(base.writes) != 6 {
		t.Fatalf("SYN to a peer port: detected %d, forwarded %d, err %v", detected, len(base.writes), err)
	}
}

func TestPortKeepsOnlyTheNumber(t *testing.T) {
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
		if got := Port(addr); got != want {
			t.Errorf("Port(%q) = %q, want %q", addr, got, want)
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
