package vless

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/transport/internet/stat"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Why VLESS handshakes fail (agent.proto LinkCounters, links/3). Every hook is on the engine's own side of Xray:
//   - the tracked TCP connection (tcpmask.go) sees the first bytes of every connection of the inbound before REALITY does
//     (hellos), learns at the first dispatch which key it carries (connects) and reads TCP_INFO when it closes (tcp_stalls);
//   - REALITY decides "not a client" inside Xray and only says so in an Info log line with the client's address and the
//     reason; the address names the tracked connection that has just closed (it never leaves the process, see
//     recentCloses), the reason gives reality_fallback, auth_failed, hellos_foreign and hs_unfinished;
//   - the TLS front (front.go) does its own handshakes, so it sees hellos, alerts and timeouts directly.
// Counts only: no address is counted, logged or sent.

// inboundCounters are the inbound row's links/3 counters, kept per inbound id across restarts like the unknown keys.
type inboundCounters struct {
	hellos, hellosForeign, realityFallback, authFailed, hsUnfinished atomic.Uint32 // volume
	tlsAlertsSent, tlsAlertsRecv, tlsCertRefused                     atomic.Uint32 // volume
	tcpStalls, tcpStalls16K                                          atomic.Uint32 // event: the TLS front's connections
}

func (c *inboundCounters) take(inboundID string) plugin.LinkStat {
	return plugin.LinkStat{InboundID: inboundID, Hellos: c.hellos.Swap(0), HellosForeign: c.hellosForeign.Swap(0),
		RealityFallback: c.realityFallback.Swap(0), AuthFailed: c.authFailed.Swap(0), HsUnfinished: c.hsUnfinished.Swap(0),
		TLSAlertsSent: c.tlsAlertsSent.Swap(0), TLSAlertsRecv: c.tlsAlertsRecv.Swap(0), TLSCertRefused: c.tlsCertRefused.Swap(0),
		TCPStalls: c.tcpStalls.Swap(0), TCPStalls16K: c.tcpStalls16K.Swap(0)}
}

func (c *inboundCounters) stall(stalled, first16K bool) {
	if stalled {
		c.tcpStalls.Add(1)
		if first16K {
			c.tcpStalls16K.Add(1)
		}
	}
}

// handshakeCounters returns the inbound's counters, created on first use.
func (e *eng) handshakeCounters(inboundID string) *inboundCounters {
	e.unknownMu.Lock()
	defer e.unknownMu.Unlock()
	if e.handshakes == nil {
		e.handshakes = make(map[string]*inboundCounters)
	}
	c := e.handshakes[inboundID]
	if c == nil {
		c = &inboundCounters{}
		e.handshakes[inboundID] = c
	}
	return c
}

// isClientHello reports whether the first six bytes of a connection open a TLS handshake record with a ClientHello.
func isClientHello(b [6]byte) bool {
	return b[0] == 0x16 && b[1] == 0x03 && b[5] == 0x01
}

// trackedConnOf finds the engine's tracked TCP connection under the connection Xray hands to the VLESS handler: REALITY's
// connection (NetConn), a statistics wrapper, or the tracked connection itself. nil for a transport whose session is not
// one TCP connection (XHTTP, WebSocket).
func trackedConnOf(c net.Conn) *trackedTCPConn {
	for range 6 {
		switch v := c.(type) {
		case nil:
			return nil
		case *trackedTCPConn:
			return v
		case *stat.CounterConnection:
			c = v.Connection
		case interface{ NetConn() net.Conn }:
			c = v.NetConn()
		default:
			return nil
		}
	}
	return nil
}

// ---- REALITY's verdict ----

const (
	realityInvalidPrefix = "REALITY: processed invalid connection from "
	recentCloseTTL       = 10 * time.Second
)

// recentCloses holds the last connections of REALITY inbounds that closed before any key reached the engine: the client's
// address as REALITY prints it, and whose counters it adds to. REALITY closes the connection and logs its verdict in the
// same goroutine right after, so an entry is found within microseconds; it never outlives recentCloseTTL as a match and
// is overwritten after len(ring) more closes. The addresses stay in this process's memory and nowhere else.
var recentCloses struct {
	mu   sync.Mutex
	ring [256]recentClose
	next int
}

type recentClose struct {
	addr     string
	counters *inboundCounters
	at       time.Time
}

func rememberClose(addr string, counters *inboundCounters, now time.Time) {
	recentCloses.mu.Lock()
	recentCloses.ring[recentCloses.next] = recentClose{addr: addr, counters: counters, at: now}
	recentCloses.next = (recentCloses.next + 1) % len(recentCloses.ring)
	recentCloses.mu.Unlock()
}

// takeClose finds and forgets the newest close of addr within the TTL. A taken entry keeps its time and is skipped, so a
// verdict that comes after another connection's still finds its own.
func takeClose(addr string, now time.Time) *inboundCounters {
	recentCloses.mu.Lock()
	defer recentCloses.mu.Unlock()
	n := len(recentCloses.ring)
	for i := 1; i <= n; i++ {
		e := &recentCloses.ring[(recentCloses.next-i+n)%n]
		if e.at.IsZero() || now.Sub(e.at) > recentCloseTTL {
			return nil // never used, or older than the TTL: older entries are older still
		}
		if e.counters != nil && e.addr == addr {
			c := e.counters
			*e = recentClose{at: e.at}
			return c
		}
	}
	return nil
}

// realityVerdict counts one "processed invalid connection" line of REALITY (xtls/reality tls.go). It reports whether the
// line was one: such a line is never logged (it carries the client's address).
func realityVerdict(content string, now time.Time) bool {
	i := strings.Index(content, realityInvalidPrefix)
	if i < 0 {
		return false
	}
	rest := content[i+len(realityInvalidPrefix):]
	sep := strings.Index(rest, ": ")
	if sep <= 0 {
		return true
	}
	addr, reason := rest[:sep], rest[sep+2:]
	c := takeClose(addr, now)
	if c == nil {
		return true
	}
	switch {
	case strings.HasPrefix(reason, "server name mismatch"):
		c.realityFallback.Add(1)
		c.hellosForeign.Add(1)
	case strings.HasPrefix(reason, "authentication failed"):
		c.realityFallback.Add(1)
		c.authFailed.Add(1)
	case strings.HasPrefix(reason, "failed to read client hello"), strings.HasPrefix(reason, "unsupported TLS version"):
		c.realityFallback.Add(1)
	case strings.HasPrefix(reason, "handshake did not complete"):
		c.hsUnfinished.Add(1)
	}
	// "target sent incorrect server hello": the target is broken, which the target probe at the start reports
	return true
}

// ---- the TLS front ----

// tlsHandshakeFailure classifies the error of a TLS handshake (crypto/tls over TCP, server side): an alert the client sent
// ("remote error", certificate telling whether it refused the certificate), one the node sent, or neither (the connection
// ended or timed out: EOF, a reset, the deadline). crypto/tls sends an alert before it returns any error of its own
// ("tls: ..."), but returns that error rather than the "local error" it records, except for bytes that are no TLS record
// at all (RecordHeaderError), for which it sends nothing.
func tlsHandshakeFailure(err error) (sent, recv, certificate bool) {
	if err == nil {
		return false, false, false
	}
	var op *net.OpError
	if errors.As(err, &op) {
		v := reflect.ValueOf(op.Err) // crypto/tls's alert is an unexported uint8
		if op.Err == nil || v.Kind() != reflect.Uint8 {
			return false, false, false // a read or write of the socket failed
		}
		switch op.Op {
		case "remote error":
			return false, true, engine.CertificateAlert(uint8(v.Uint()))
		case "local error":
			return true, false, false
		}
		return false, false, false
	}
	var record tls.RecordHeaderError
	var ne net.Error
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &record) || errors.As(err, &ne) ||
		!strings.HasPrefix(err.Error(), "tls: ") {
		return false, false, false
	}
	return true, false, false
}

// ---- TCP stalls ----

// The stall rule (tcpStallOf on Linux): the connection ended with bytes of the node the client never acknowledged, after at
// least stallMinRetrans retransmissions. A stall whose client had acknowledged first16KMin to first16KMax bytes is the
// TSPU's freeze after the first bytes.
const (
	stallMinRetrans = 3
	first16KMin     = 8 << 10
	first16KMax     = 32 << 10
)

// tcpStall is the seam the closes go through (tests replace it; the Linux root test drives the real one).
var tcpStall = tcpStallOf

// tcpStallRule applies the rule to the TCP_INFO numbers: bytesSent counts retransmitted bytes too (0 on kernels before
// 4.19, where unackedSegs, the segments still in flight, decides).
func tcpStallRule(totalRetrans uint32, bytesSent, bytesRetrans, bytesAcked uint64, unackedSegs uint32) (stalled, first16K bool) {
	if totalRetrans < stallMinRetrans {
		return false, false
	}
	switch {
	case bytesSent > 0:
		fresh := bytesSent - min(bytesRetrans, bytesSent)
		if fresh <= bytesAcked {
			return false, false
		}
	case unackedSegs == 0:
		return false, false
	}
	return true, bytesAcked >= first16KMin && bytesAcked <= first16KMax
}
