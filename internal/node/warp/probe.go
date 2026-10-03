package warp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	probeTotalTimeout = 20 * time.Second
	probeBodyLimit    = 4096
)

var errProbeTraceMissing = errors.New("probe A: no warp= line")

type probeHTTPStatusError struct {
	probe  string
	status int
}

func (e *probeHTTPStatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.probe, e.status) }

// httpProber fetches the two probe URLs through dial (the tunnel egress). Plain HTTP on purpose: no TLS stack
// needed, and a failure means the path, not a certificate.
type httpProber struct {
	dial       func(context.Context, string, string) (net.Conn, error)
	aURL, bURL string
	timeout    time.Duration // per probe, dial included (probeTotalTimeout when zero)
}

var _ prober = (*httpProber)(nil)

func (p *httpProber) limit() time.Duration {
	if p.timeout > 0 {
		return p.timeout
	}
	return probeTotalTimeout
}

func (p *httpProber) client(requestCtx context.Context) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(transportCtx context.Context, network, address string) (net.Conn, error) {
				dialCtx, cancel := context.WithCancel(requestCtx)
				stop := context.AfterFunc(transportCtx, cancel)
				defer stop()
				defer cancel()
				return p.dial(dialCtx, network, address)
			},
			ResponseHeaderTimeout: p.limit(),
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (p *httpProber) get(ctx context.Context, url string) (status int, body string, err error) {
	ctx, cancel := context.WithTimeout(ctx, p.limit())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "mistgate-node")
	resp, err := p.client(ctx).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
	return resp.StatusCode, string(b), err
}

// A asks Cloudflare's trace endpoint through the tunnel and returns its warp= flag ("on", "plus", "off") and colo.
func (p *httpProber) A(ctx context.Context) (flag, colo string, err error) {
	st, body, err := p.get(ctx, p.aURL)
	if err != nil {
		return "", "", err
	}
	if st != http.StatusOK {
		return "", "", &probeHTTPStatusError{probe: "probe A", status: st}
	}
	flag, colo = parseTrace(body)
	if flag == "" {
		return "", colo, errProbeTraceMissing
	}
	return flag, colo, nil
}

// B fetches a host that is not Cloudflare: any 2xx answer counts.
func (p *httpProber) B(ctx context.Context) error {
	st, _, err := p.get(ctx, p.bURL)
	if err != nil {
		return err
	}
	if st < 200 || st > 299 {
		return &probeHTTPStatusError{probe: "probe B", status: st}
	}
	return nil
}

// probeFailureCode carries a small, safe reason to the panel without exposing request URLs or arbitrary error text.
func probeFailureCode(err error, warpFlag string) string {
	if err == nil {
		switch warpFlag {
		case "":
			// Probe B is intentionally not Cloudflare and has no WARP trace flag.
			return ""
		case "on", "plus":
			return ""
		case "off":
			return "warp_off"
		default:
			return "invalid_trace"
		}
	}
	var statusErr *probeHTTPStatusError
	if errors.As(err, &statusErr) {
		return fmt.Sprintf("http_%d", statusErr.status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, errProbeTraceMissing) {
		return "invalid_trace"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" {
			return "connection"
		}
		return "network"
	}
	return "other"
}

// parseTrace reads "key=value" lines of /cdn-cgi/trace.
func parseTrace(body string) (warp, colo string) {
	for _, l := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		switch k {
		case "warp":
			warp = v
		case "colo":
			colo = v
		}
	}
	return warp, colo
}
