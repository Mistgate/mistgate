package hysteria2

import (
	"bytes"
	"errors"
	"testing"

	"github.com/apernet/hysteria/core/v2/server"
)

type requestHookStub struct {
	checkResult bool
	checkUDP    bool
	checkAddr   string
	tcpRewrite  string
	tcpPutback  []byte
	tcpErr      error
	udpRewrite  string
	udpErr      error
}

func (s *requestHookStub) Check(isUDP bool, reqAddr string) bool {
	s.checkUDP, s.checkAddr = isUDP, reqAddr
	return s.checkResult
}

func (s *requestHookStub) TCP(_ server.HyStream, reqAddr *string) ([]byte, error) {
	*reqAddr = s.tcpRewrite
	return s.tcpPutback, s.tcpErr
}

func (s *requestHookStub) UDP(_ []byte, reqAddr *string) error {
	*reqAddr = s.udpRewrite
	return s.udpErr
}

func TestRouteOnlyRequestHookDelegatesAndRestoresIPRequests(t *testing.T) {
	sniffErr := errors.New("sniff failed")
	putback := []byte{0x16, 0x03, 0x01, 0x00}
	delegate := &requestHookStub{
		checkResult: true,
		tcpRewrite:  "www.google.com:443",
		tcpPutback:  putback,
		tcpErr:      sniffErr,
		udpRewrite:  "www.google.com:443",
		udpErr:      sniffErr,
	}
	hook := &routeOnlyRequestHook{delegate: delegate}

	if !hook.Check(false, "192.0.2.7:443") || delegate.checkUDP || delegate.checkAddr != "192.0.2.7:443" {
		t.Fatalf("TCP Check was not delegated unchanged: result=%v udp=%v addr=%q", delegate.checkResult, delegate.checkUDP, delegate.checkAddr)
	}
	delegate.checkResult = false
	if hook.Check(false, "app.example.test:443") || delegate.checkAddr != "app.example.test:443" {
		t.Fatalf("false Check result was not preserved for a domain request: result=%v addr=%q", delegate.checkResult, delegate.checkAddr)
	}
	delegate.checkResult = true
	addr := "192.0.2.7:443"
	gotPutback, err := hook.TCP(nil, &addr)
	if addr != "192.0.2.7:443" || !bytes.Equal(gotPutback, putback) || !errors.Is(err, sniffErr) {
		t.Fatalf("TCP result addr=%q putback=%v err=%v", addr, gotPutback, err)
	}

	if !hook.Check(true, "[2001:db8::7]:443") || !delegate.checkUDP || delegate.checkAddr != "[2001:db8::7]:443" {
		t.Fatalf("UDP Check was not delegated unchanged: result=%v udp=%v addr=%q", delegate.checkResult, delegate.checkUDP, delegate.checkAddr)
	}
	addr = "[2001:db8::7]:443"
	if err := hook.UDP([]byte("first packet"), &addr); !errors.Is(err, sniffErr) || addr != "[2001:db8::7]:443" {
		t.Fatalf("UDP result addr=%q err=%v", addr, err)
	}
}

func TestRouteOnlyRequestHookKeepsSniffedDomain(t *testing.T) {
	delegate := &requestHookStub{tcpRewrite: "www.google.com:443", udpRewrite: "www.google.com:443"}
	hook := &routeOnlyRequestHook{delegate: delegate}
	addr := "app.example.test:443"
	if _, err := hook.TCP(nil, &addr); err != nil {
		t.Fatal(err)
	}
	if addr != "www.google.com:443" {
		t.Fatalf("domain request address = %q, want sniffed domain", addr)
	}
	addr = "app.example.test:443"
	if err := hook.UDP([]byte("first packet"), &addr); err != nil {
		t.Fatal(err)
	}
	if addr != "www.google.com:443" {
		t.Fatalf("UDP domain request address = %q, want sniffed domain", addr)
	}
}
