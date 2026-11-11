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
	udpDone     bool
	udpCalls    int
	udpPackets  [][]byte
}

func (s *requestHookStub) Check(isUDP bool, reqAddr string) bool {
	s.checkUDP, s.checkAddr = isUDP, reqAddr
	return s.checkResult
}

func (s *requestHookStub) TCP(_ server.HyStream, reqAddr *string) ([]byte, error) {
	*reqAddr = s.tcpRewrite
	return s.tcpPutback, s.tcpErr
}

func (s *requestHookStub) UDP(packets [][]byte, reqAddr *string) (bool, error) {
	s.udpCalls++
	s.udpPackets = packets
	*reqAddr = s.udpRewrite
	return s.udpDone, s.udpErr
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
		udpDone:     false,
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
	// An IP literal is never held back and never reaches the sniffer.
	if done, err := hook.UDP([][]byte{[]byte("first packet")}, &addr); !done || err != nil || addr != "[2001:db8::7]:443" || delegate.udpCalls != 0 {
		t.Fatalf("UDP result addr=%q done=%v err=%v delegate calls=%d", addr, done, err, delegate.udpCalls)
	}
}

func TestRouteOnlyRequestHookPassesHeldUDPPacketsOfDomainRequests(t *testing.T) {
	sniffErr := errors.New("sniff failed")
	delegate := &requestHookStub{udpRewrite: "app.example.test:443", udpErr: sniffErr}
	hook := &routeOnlyRequestHook{delegate: delegate}
	packets := [][]byte{[]byte("first"), []byte("second")}
	addr := "app.example.test:443"
	if done, err := hook.UDP(packets, &addr); done || !errors.Is(err, sniffErr) || delegate.udpCalls != 1 || len(delegate.udpPackets) != 2 {
		t.Fatalf("held UDP result done=%v err=%v calls=%d packets=%d", done, err, delegate.udpCalls, len(delegate.udpPackets))
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
	delegate.udpDone = true
	if done, err := hook.UDP([][]byte{[]byte("first packet")}, &addr); !done || err != nil {
		t.Fatalf("UDP done=%v err=%v", done, err)
	}
	if addr != "www.google.com:443" {
		t.Fatalf("UDP domain request address = %q, want sniffed domain", addr)
	}
}
