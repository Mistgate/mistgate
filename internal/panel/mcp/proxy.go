package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunProxy is `mistgate mcp`: a local stdio MCP server for clients that cannot speak HTTP, which forwards every message to the
// panel's Streamable HTTP endpoint with the token as a Bearer header. It decides nothing, caches nothing
// and knows no tool: the panel's own server, with its profile and its limits, answers.
//
// rawURL is the admin URL `mistgate setup` prints (https://host/<prefix>/); the endpoint is that URL plus "mcp". The token is
// the first line of tokenFile. It is never printed, never taken from the command line or the environment. A plain http URL is
// refused unless the host is a loopback address: a token must not cross a network in the clear.
func RunProxy(ctx context.Context, rawURL, tokenFile string, stdin io.Reader, stdout, stderr io.Writer) error {
	endpoint, err := endpointURL(rawURL)
	if err != nil {
		return err
	}
	token, err := readTokenFile(tokenFile, stderr)
	if err != nil {
		return err
	}

	rt := &statusRT{token: token, base: http.DefaultTransport}
	hc := &http.Client{
		Transport: rt,
		// A redirect would send the token somewhere the owner did not name.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	up, err := (&mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}).Connect(ctx)
	if err != nil {
		return errors.New("cannot reach the panel")
	}
	defer up.Close()
	down, err := (&mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{stdout}}).Connect(ctx)
	if err != nil {
		return err
	}
	defer down.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)

	// panel -> client: responses (the stateless server sends nothing else)
	go func() {
		for {
			msg, err := up.Read(ctx)
			if err != nil {
				errc <- upstreamError(err, rt)
				return
			}
			if err := down.Write(ctx, msg); err != nil {
				errc <- err
				return
			}
		}
	}()

	// client -> panel: each message on its own goroutine, so a slow call does not hold up the next one or a cancellation
	go func() {
		var wg sync.WaitGroup
		defer func() { wg.Wait() }()
		for {
			msg, err := down.Read(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) || ctx.Err() != nil {
					errc <- nil // the client closed its end: a clean exit
				} else {
					errc <- err
				}
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := up.Write(ctx, msg); err != nil {
					if rt.refused() {
						errc <- errRefused
						return
					}
					// Tell the client its call failed instead of leaving it waiting; a notification has nobody to tell.
					if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() {
						down.Write(ctx, &jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the panel did not accept the request"}})
					}
					fmt.Fprintln(stderr, "mistgate mcp: request failed:", upstreamError(err, rt))
				}
			}()
		}
	}()

	err = <-errc
	cancel()
	return err
}

var errRefused = errors.New("the panel refused the token (expired, revoked or wrong profile)")

func upstreamError(err error, rt *statusRT) error {
	if rt.refused() {
		return errRefused
	}
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	return errors.New("lost the connection to the panel")
}

// statusRT adds the credential and notes whether the panel refused it. It is the only place that holds the token.
type statusRT struct {
	token string
	base  http.RoundTripper
	deny  atomic.Bool
}

func (s *statusRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.base.RoundTrip(r)
	if err != nil {
		// the transport's error text can quote the URL; the token is a header, but keep the message ours alone
		return nil, errors.New("request failed")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		s.deny.Store(true)
	}
	return resp, nil
}

func (s *statusRT) refused() bool { return s.deny.Load() }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// endpointURL checks the admin URL and returns the MCP endpoint under it.
func endpointURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("--url must be the admin URL of the panel, like https://host/<prefix>/")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("--url must not carry credentials, a query or a fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return "", errors.New("--url is plain http to a non-loopback host: the token would travel in the clear; use https")
		}
	default:
		return "", errors.New("--url must start with https://")
	}
	p := u.Path
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	u.Path = p + "mcp"
	return u.String(), nil
}

func loopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var tokenShape = regexp.MustCompile(`^tk1_[A-Za-z0-9_-]{43}$`)

// readTokenFile returns the first line of the file. Errors never quote its content.
func readTokenFile(path string, stderr io.Writer) (string, error) {
	if path == "" {
		return "", errors.New("--token-file is required: a file whose first line is the API token")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open the token file")
	}
	defer f.Close()
	if runtime.GOOS != "windows" {
		if st, err := f.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
			fmt.Fprintln(stderr, "mistgate mcp: warning: the token file is readable by others; chmod 600 it")
		}
	}
	line, err := bufio.NewReader(io.LimitReader(f, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("cannot read the token file")
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, string(rune(0xFEFF)))) // a Windows editor may add a byte order mark
	if !tokenShape.MatchString(line) {
		return "", errors.New("the token file does not start with an API token (tk1_...)")
	}
	return line, nil
}
