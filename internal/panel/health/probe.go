package health

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	coreErrs "github.com/apernet/hysteria/core/v2/errors"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// roundBudget bounds one attempt: 8 s handshake and two 10 s probes would be 28 s (the limit is 25 s).
const roundBudget = 25 * time.Second

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
type DialOptions struct{ HandshakeTimeout time.Duration }

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
	LatencyMS              uint32
	ExitIP, ExitCountry    string
	ErrorCode, ErrorDetail string
}

func (r Result) failed() bool { return r.Status == adminv1.CheckStatus_CHECK_STATUS_FAILED }

func (r Result) sample(inboundID string) store.CheckSample {
	return store.CheckSample{InboundID: inboundID, At: r.At, Status: int(r.Status), LatencyMS: r.LatencyMS, ExitIP: r.ExitIP,
		ExitCountry: r.ExitCountry, ErrorCode: r.ErrorCode, ErrorDetail: r.ErrorDetail}
}

// classify names a dial error. Anything that is not clearly a certificate, credential or refusal problem is a
// timeout: from the client's side a silent network and a dropped handshake look the same.
func classify(err error) (code, detail string) {
	var pe *ProbeError
	if errors.As(err, &pe) {
		return pe.Code, pe.Detail
	}
	detail = store.Clip(strings.Join(strings.Fields(err.Error()), " "), 200)
	var ae coreErrs.AuthError
	low := strings.ToLower(detail)
	switch {
	case errors.As(err, &ae):
		return "auth", fmt.Sprintf("credential refused (HTTP %d)", ae.StatusCode)
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
	rctx, cancel := context.WithTimeout(ctx, min(s.cfg.HandshakeTimeout+2*s.cfg.ProbeTimeout, roundBudget))
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

// probe runs the two probes through an open tunnel: generate_204 (latency, from the start of the dial to the
// first response byte) and the Cloudflare trace (exit IP and country).
func (s *Service) probe(ctx context.Context, tn Tunnel, node store.NodeRow, start time.Time) Result {
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: tn.DialContext, TLSClientConfig: s.cfg.ProbeTLS, DisableKeepAlives: true,
			TLSHandshakeTimeout: s.cfg.ProbeTimeout, Proxy: nil,
		},
		Timeout:       s.cfg.ProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res := Result{At: s.now()}
	var bad []*ProbeError
	fail := func(code, format string, a ...any) {
		bad = append(bad, &ProbeError{Code: code, Detail: store.Clip(fmt.Sprintf(format, a...), 200)})
	}

	var latency, latencyTrace uint32
	ok204, okTrace := false, false

	if resp, err := getProbe(ctx, hc, s.cfg.Probe204URL); err != nil {
		fail("exit_unreachable", "generate_204: %s", probeErrText(err))
	} else {
		latency = uint32(max(time.Since(start).Milliseconds(), 1))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			ok204 = true
		} else {
			fail("http_status", "generate_204 answered %d", resp.StatusCode)
		}
	}

	if resp, err := getProbe(ctx, hc, s.cfg.ProbeTraceURL); err != nil {
		fail("exit_unreachable", "trace: %s", probeErrText(err))
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
	if res.Status != adminv1.CheckStatus_CHECK_STATUS_OK && len(bad) > 0 {
		res.ErrorCode, res.ErrorDetail = bad[0].Code, bad[0].Detail
	}
	return res
}

func getProbe(ctx context.Context, hc *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
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
