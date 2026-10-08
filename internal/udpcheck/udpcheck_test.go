package udpcheck

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestSendCountsTaggedDatagramsFromFourSourcePorts(t *testing.T) {
	const perPort = 8
	tag := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	listeners := make([]*net.UDPConn, 2)
	ports := make([]uint16, len(listeners))
	for i := range listeners {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		listeners[i] = c
		ports[i] = uint16(c.LocalAddr().(*net.UDPAddr).Port)
		defer c.Close()
	}

	type packet struct {
		port uint16
		from int
		data []byte
	}
	packets := make(chan packet, perPort*len(ports))
	var readers sync.WaitGroup
	for i, c := range listeners {
		readers.Add(1)
		go func(port uint16, c *net.UDPConn) {
			defer readers.Done()
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			for range perPort {
				buf := make([]byte, 1200)
				n, from, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				packets <- packet{port: port, from: from.Port, data: append([]byte(nil), buf[:n]...)}
			}
		}(ports[i], c)
	}

	family, sent, err := Send(context.Background(), "127.0.0.1", ports, tag, perPort, 50, 64)
	if err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	close(packets)
	if family != "4" || sent != perPort {
		t.Fatalf("Send = (%q, %d), want (4, %d)", family, sent, perPort)
	}
	counts := map[uint16]int{}
	sources := map[int]bool{}
	for p := range packets {
		counts[p.port]++
		sources[p.from] = true
		if len(p.data) != 64 || string(p.data[:len(tag)]) != string(tag[:]) {
			t.Errorf("payload = %d bytes, prefix %x; want 64 bytes with tag %x", len(p.data), p.data[:len(tag)], tag)
		}
	}
	for _, port := range ports {
		if counts[port] != perPort {
			t.Errorf("port %d received %d datagrams, want %d", port, counts[port], perPort)
		}
	}
	if len(sources) != 4 {
		t.Errorf("source ports = %v, want four distinct ports", sources)
	}
}

func TestSendRejectsEveryHardCap(t *testing.T) {
	tag := [8]byte{1}
	validPorts := []uint16{443}
	tooManyPorts := make([]uint16, 9)
	for i := range tooManyPorts {
		tooManyPorts[i] = uint16(1000 + i)
	}
	for name, tc := range map[string]struct {
		ports      []uint16
		count, pps int
		size       int
	}{
		"no ports":           {nil, 1, 1, 64},
		"too many ports":     {tooManyPorts, 1, 1, 64},
		"duplicate ports":    {[]uint16{443, 443}, 1, 1, 64},
		"port zero":          {[]uint16{0}, 1, 1, 64},
		"zero count":         {validPorts, 0, 1, 64},
		"too many datagrams": {validPorts, 301, 50, 64},
		"zero rate":          {validPorts, 1, 0, 64},
		"too high rate":      {validPorts, 1, 51, 64},
		"too small payload":  {validPorts, 1, 1, 63},
		"too large payload":  {validPorts, 1, 1, 1201},
		"over ten seconds":   {validPorts, 300, 29, 64},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Send(context.Background(), "127.0.0.1", tc.ports, tag, tc.count, tc.pps, tc.size)
			if !errors.Is(err, ErrBadParams) {
				t.Fatalf("Send error = %v, want ErrBadParams", err)
			}
		})
	}
}

func TestSendRejectsUnsupportedDestinations(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "224.0.0.1", "ff02::1", "255.255.255.255"} {
		t.Run(host, func(t *testing.T) {
			_, _, err := Send(context.Background(), host, []uint16{443}, [8]byte{1}, 1, 1, 64)
			if !errors.Is(err, ErrUnsupportedHost) {
				t.Fatalf("Send error = %v, want ErrUnsupportedHost", err)
			}
		})
	}
}

func TestSendStopsOnContextCancellation(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := uint16(listener.LocalAddr().(*net.UDPAddr).Port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct {
		sent int
		err  error
	}, 1)
	go func() {
		_, sent, err := Send(ctx, "127.0.0.1", []uint16{port}, [8]byte{1}, 300, 50, 64)
		done <- struct {
			sent int
			err  error
		}{sent, err}
	}()
	if err := listener.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listener.ReadFromUDP(make([]byte, 128)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || got.sent >= 300 {
			t.Fatalf("Send = (%d, %v), want early cancellation", got.sent, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not stop after cancellation")
	}
}

func TestSendValidatesBeforeResolving(t *testing.T) {
	_, _, err := Send(context.Background(), "not a valid host name . invalid", []uint16{443}, [8]byte{}, 1, 1, 63)
	if !errors.Is(err, ErrBadParams) {
		t.Fatalf("Send error = %v, want ErrBadParams", err)
	}
}
