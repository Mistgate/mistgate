package health

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// roundBudget bounds one attempt: 8 s handshake, two 10 s probes and the short request on the open connection would be
// 31 s (the limit is 25 s).
const roundBudget = 25 * time.Second

// reuseTimeout bounds the extra generate_204 request on the already open connection: it needs one round trip, so
// a slow tunnel must not eat the time of the trace probe after it.
const reuseTimeout = 3 * time.Second

// Tunnel is an open client session of one inbound: a dialer that goes out through the node.
type Tunnel interface {
	// DialContext opens a TCP connection to addr ("host:port") through the node. The host name is NOT
	// resolved here: the node resolves it, so a broken resolver on the node shows up as a failed probe.
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Close() error
}

// Target is what a Dialer needs to connect to one inbound as a client.
type Target struct {
	Node     store.NodeRow
	Inbound  store.FleetInboundRow
	Spec     plugin.InboundSpec
	Settings []byte // merged profile settings (secrets in place)
	CredID   string // the system credential's id (AWG: the id of the probe "device", for its signature)
	Secret   string // the system credential's secret: what a client presents
}

// DialOptions bound a dial.
type DialOptions struct {
	HandshakeTimeout time.Duration
	testTLSPin       []byte // loopback TLS test only; deployed probes verify the normal certificate chain
}

// Dialer connects to an inbound as a client. It returns a *ProbeError for the failures it can name and
// errClientUnsupported for a configuration it has no client for.
type Dialer func(ctx context.Context, t Target, o DialOptions) (Tunnel, error)

// ProbeError is a failure with the machine code of health.proto CheckResult.error_code.
type ProbeError struct {
	Code, Detail string
	Skip         bool // not a failure: the inbound cannot be probed (the round is SKIPPED and not stored)
}

func (e *ProbeError) Error() string { return e.Code + ": " + e.Detail }

// errClientUnsupported makes the round SKIPPED (client_unsupported), not failed.
var errClientUnsupported = &ProbeError{Code: skipClientMissing, Detail: errUnsupportedText, Skip: true}

// Result is one finished round.
type Result struct {
	Status                 adminv1.CheckStatus
	At                     time.Time
	LatencyMS              uint32 // the cold time of the round: dial start to the first generate_204 response
	ConnectMS              uint32 // dial start to the first connection through the tunnel being open; 0 = not measured
	ResponseMS             uint32 // one more generate_204 on that connection; 0 = not measured or the connection was not reused
	UDPSent, UDPLost       uint32 // packets the panel's own QUIC client sent and declared lost in the round (Hysteria2; 0 = none)
	ExitIP, ExitCountry    string
	ErrorCode, ErrorDetail string
}

func (r Result) failed() bool { return r.Status == adminv1.CheckStatus_CHECK_STATUS_FAILED }

func (r Result) sample(inboundID string) store.CheckSample {
	return store.CheckSample{InboundID: inboundID, At: r.At, Status: int(r.Status), LatencyMS: r.LatencyMS, ConnectMS: r.ConnectMS,
		ResponseMS: r.ResponseMS, UDPSent: r.UDPSent, UDPLost: r.UDPLost, ExitIP: r.ExitIP, ExitCountry: r.ExitCountry,
		ErrorCode: r.ErrorCode, ErrorDetail: r.ErrorDetail}
}

// classify names a dial error. Anything that is not clearly a certificate, credential or refusal problem is a
// timeout: from the client's side a silent network and a dropped handshake look the same.
func classify(err error) (code, detail string) {
	var pe *ProbeError
	if errors.As(err, &pe) {
		return pe.Code, pe.Detail
	}
	detail = store.Clip(strings.Join(strings.Fields(err.Error()), " "), 200)
	low := strings.ToLower(detail)
	if status, ok := hysteriaAuthStatus(err); ok {
		return "auth", fmt.Sprintf("credential refused (HTTP %d)", status)
	}
	switch {
	case strings.Contains(low, "certificate") || strings.Contains(low, "x509") || strings.Contains(low, "tls:") || strings.Contains(low, "crypto_error"):
		return "tls", detail
	case strings.Contains(low, "refused") || strings.Contains(low, "unreachable") || strings.Contains(low, "connection reset"):
		return "refused", detail
	}
	return "timeout", detail
}

// attempt is one try: dial, probe, close. skipped reports a configuration the dialer has no client for (nothing
// is recorded then).
func (s *Service) attempt(ctx context.Context, t *target) (res Result, skipped bool) {
	cred, secret, err := s.probeCredSecret(ctx, t)
	if err != nil {
		s.log.Warn("health: probe credential unreadable", "inbound", t.in.ID, "err", err)
		return Result{}, true
	}
	start := s.now()
	rctx, cancel := context.WithTimeout(ctx, min(s.cfg.HandshakeTimeout+2*s.cfg.ProbeTimeout+reuseTimeout, roundBudget))
	defer cancel()
	tn, err := s.cfg.Dialers[t.in.Protocol](rctx, Target{Node: t.node, Inbound: t.in, Spec: t.spec, Settings: t.settings, CredID: cred.CredID, Secret: secret},
		DialOptions{HandshakeTimeout: s.cfg.HandshakeTimeout})
	if err != nil {
		var pe *ProbeError
		if errors.As(err, &pe) && pe.Skip {
			return Result{}, true
		}
		code, detail := classify(err)
		return Result{Status: adminv1.CheckStatus_CHECK_STATUS_FAILED, At: s.now(), ErrorCode: code, ErrorDetail: detail}, false
	}
	defer tn.Close()
	return s.probe(rctx, tn, t.node, start), false
}

// probe runs the probes through an open tunnel, all measured on the panel's server: generate_204 (latency, from
// the start of the dial to the first response byte), the same request once more on the connection the first one
// opened (response time, what a person feels while the tunnel is up) and the Cloudflare trace (exit IP and country).
// connect_ms is the time from start to the first successful dial through the tunnel; see connectReporter for a
// tunnel whose dial returns before the node is reached.
func (s *Service) probe(ctx context.Context, tn Tunnel, node store.NodeRow, start time.Time) Result {
	var connectNS atomic.Int64 // nanoseconds from start to the first successful dial; 0 = none yet
	var dials atomic.Int32     // dials started, to see whether a request reused a connection
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			c, err := tn.DialContext(ctx, network, addr)
			if err == nil {
				connectNS.CompareAndSwap(0, max(int64(time.Since(start)), 1))
			}
			return c, err
		},
		TLSClientConfig: s.cfg.ProbeTLS, TLSHandshakeTimeout: s.cfg.ProbeTimeout, Proxy: nil,
	}
	defer tr.CloseIdleConnections() // keep-alive is on for the second request; nothing may outlive the round
	hc := &http.Client{
		Transport:     tr,
		Timeout:       s.cfg.ProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res := Result{At: s.now()}
	var bad []*ProbeError
	fail := func(code, format string, a ...any) {
		bad = append(bad, &ProbeError{Code: code, Detail: store.Clip(fmt.Sprintf(format, a...), 200)})
	}
	failRequest := func(label string, err error) {
		var pe *ProbeError
		if errors.As(err, &pe) {
			fail(pe.Code, "%s: %s", label, pe.Detail)
			return
		}
		if c, ok := tn.(requestErrorClassifier); ok {
			pe = c.ClassifyRequestError(err)
			fail(pe.Code, "%s: %s", label, pe.Detail)
			return
		}
		fail("exit_unreachable", "%s: %s", label, probeErrText(err))
	}

	var latency, latencyTrace, responseMS uint32
	ok204, okTrace := false, false

	if resp, err := getProbe(ctx, hc, s.cfg.Probe204URL, false); err != nil {
		failRequest("generate_204", err)
	} else {
		latency = uint32(max(time.Since(start).Milliseconds(), 1))
		drainClose(resp.Body)
		if resp.StatusCode == http.StatusNoContent {
			ok204 = true
			responseMS = reusedProbe(ctx, hc, s.cfg.Probe204URL, &dials)
		} else {
			fail("http_status", "generate_204 answered %d", resp.StatusCode)
		}
	}

	if resp, err := getProbe(ctx, hc, s.cfg.ProbeTraceURL, true); err != nil {
		failRequest("trace", err)
	} else {
		latencyTrace = uint32(max(time.Since(start).Milliseconds(), 1))
		ip, loc := exitFromTrace(resp.Body)
		resp.Body.Close()
		switch {
		case resp.StatusCode != http.StatusOK:
			fail("http_status", "trace answered %d", resp.StatusCode)
		case ip == "":
			fail("http_status", "trace has no exit ip")
		default:
			okTrace, res.ExitIP, res.ExitCountry = true, ip, loc
		}
	}

	switch {
	case ok204 && okTrace:
		res.Status, res.LatencyMS = adminv1.CheckStatus_CHECK_STATUS_OK, latency
		// Noted only; a node behind NAT legitimately exits from another address.
		if a, err := netip.ParseAddr(node.Address); err == nil && a.String() != res.ExitIP {
			res.ErrorDetail = "exit ip " + res.ExitIP + " differs from the node address"
		}
	case ok204 || okTrace: // the tunnel works, one probe does not
		res.Status = adminv1.CheckStatus_CHECK_STATUS_DEGRADED
		res.LatencyMS = latency
		if !ok204 {
			res.LatencyMS = latencyTrace
		}
	default:
		res.Status = adminv1.CheckStatus_CHECK_STATUS_FAILED
	}
	if res.Status != adminv1.CheckStatus_CHECK_STATUS_FAILED {
		connect := time.Duration(connectNS.Load())
		if rep, ok := tn.(connectReporter); ok {
			if at := rep.ConnectedAt(); !at.IsZero() {
				connect = max(connect, at.Sub(start))
			}
		}
		res.ConnectMS, res.ResponseMS = uint32(max(connect.Milliseconds(), 1)), responseMS
	}
	if res.Status != adminv1.CheckStatus_CHECK_STATUS_OK && len(bad) > 0 {
		res.ErrorCode, res.ErrorDetail = bad[0].Code, bad[0].Detail
	}
	if u, ok := tn.(udpStatser); ok {
		sent, lost := u.UDPStats()
		res.UDPSent, res.UDPLost = uint32(min(sent, math.MaxUint32)), uint32(min(lost, math.MaxUint32))
	}
	return res
}

// reusedProbe is one more GET of u, right after a first one that left its connection open. It returns the time of the
// request in milliseconds, or 0 when it failed, did not answer 204, or the transport opened a new connection (then it
// measured a handshake and not the response time; the dial counter of the transport tells, since a reused connection
// needs no dial).
func reusedProbe(ctx context.Context, hc *http.Client, u string, dials *atomic.Int32) uint32 {
	ctx, cancel := context.WithTimeout(ctx, reuseTimeout)
	defer cancel()
	before := dials.Load()
	t0 := time.Now()
	resp, err := getProbe(ctx, hc, u, true)
	if err != nil {
		return 0
	}
	ms := max(time.Since(t0).Milliseconds(), 1)
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusNoContent || dials.Load() != before {
		return 0
	}
	return uint32(ms)
}

// drainClose reads what is left of a small body so that the connection can be reused, then closes it.
func drainClose(b io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(b, 4096))
	b.Close()
}

// getProbe is one GET of u. last asks the server to close the connection after the answer: the request is the last on
// it, and a server that hangs up first leaves nothing half-closed behind when the tunnel is torn down right after.
func getProbe(ctx context.Context, hc *http.Client, u string, last bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Close = last
	return hc.Do(req)
}

// probeErrText is a probe error without the URL the http client puts in front of it.
func probeErrText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	return strings.Join(strings.Fields(err.Error()), " ")
}

// exitFromTrace reads "ip=" and "loc=" of a Cloudflare trace body. Values that are not an address or a two-letter
// country are dropped: the body comes through the node.
func exitFromTrace(r io.Reader) (ip, country string) {
	sc := bufio.NewScanner(io.LimitReader(r, 4096))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			if a, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
				ip = a.String()
			}
		case "loc":
			if v = strings.ToUpper(strings.TrimSpace(v)); len(v) == 2 && v[0] >= 'A' && v[0] <= 'Z' && v[1] >= 'A' && v[1] <= 'Z' {
				country = v
			}
		}
	}
	return ip, country
}

// requestErrorClassifier is implemented by a tunnel whose dial succeeds at once (a local proxy in front of the real
// client), so a failed request is the first place its failure shows: it names the failure from what the client reported.
type requestErrorClassifier interface {
	ClassifyRequestError(err error) *ProbeError
}

// connectReporter is implemented by a tunnel whose DialContext returns before the node is reached (VLESS: the loopback
// SOCKS5 reply of the local Xray helper comes at once). ConnectedAt is the moment the transport to the node (TCP and
// REALITY/TLS) was up, or the zero time when it is not known; connect_ms then runs to that moment.
type connectReporter interface {
	ConnectedAt() time.Time
}

// udpStatser is implemented by a tunnel that knows what its own UDP connection sent and declared lost during the round
// (Hysteria2: the QUIC client's statistics, read before the tunnel closes).
type udpStatser interface {
	UDPStats() (sent, lost uint64)
}
