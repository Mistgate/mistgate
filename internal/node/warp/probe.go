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
	probeTotalTimeout = 6 * time.Second
	probeBodyLimit    = 4096
)

// httpProber fetches the two probe URLs through dial (the tunnel egress). Plain HTTP on purpose: no TLS stack
// needed, and a failure means the path, not a certificate.
type httpProber struct {
	dial       func(addr string) (net.Conn, error)
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

func (p *httpProber) client() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialCtx(ctx, p.dial, addr)
			},
			ResponseHeaderTimeout: p.limit(),
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// dialCtx adapts the context-less egress dial: the dial runs in a goroutine and a connection that arrives after
// the context ended is closed.
func dialCtx(ctx context.Context, dial func(string) (net.Conn, error), addr string) (net.Conn, error) {
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := dial(addr)
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
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
	resp, err := p.client().Do(req)
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
		return "", "", fmt.Errorf("probe A: HTTP %d", st)
	}
	flag, colo = parseTrace(body)
	if flag == "" {
		return "", colo, errors.New("probe A: no warp= line")
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
		return fmt.Errorf("probe B: HTTP %d", st)
	}
	return nil
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
