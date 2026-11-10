package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/node/update"
	"github.com/mistgate/mistgate/internal/release"
)

const updateHTTPUserAgent = "Mistgate-Update/1"

// githubReleaseBase is fixed in production and may be replaced with -X only for loopback e2e builds.
var githubReleaseBase = "https://github.com/Mistgate/mistgate/releases/download/"

var cloudflareR2Host = regexp.MustCompile(`^[0-9a-f]{32}(\.eu|\.fedramp)?\.r2\.cloudflarestorage\.com$`)

var errLinkStalled = errors.New("link download stalled")

type httpStatusError struct{ status int }

func (httpStatusError) Error() string { return "update source returned an HTTP status" }

func (s *updSource) Fetchers(m *release.Manifest, f release.File) []update.Fetcher {
	if s.request == nil {
		return []update.Fetcher{s.panelFetcher(f)}
	}
	seen := make(map[string]bool, len(s.request.GetSources()))
	fetchers := make([]update.Fetcher, 0, len(s.request.GetSources()))
	for _, kind := range s.request.GetSources() {
		if seen[kind] {
			continue
		}
		seen[kind] = true
		switch kind {
		case "panel":
			fetchers = append(fetchers, s.panelFetcher(f))
		case "github":
			if release.ValidChannelTag(m.Version) {
				fetchers = append(fetchers, s.githubFetcher(m, f))
			}
		case "cloudflare":
			fetchers = append(fetchers, s.cloudflareFetcher(f, s.request.GetCloudflareUrl()))
		case "peer":
			// Peer fields are reserved on the wire; stage 3a implements the receiver.
		default:
			// Unknown kinds are ignored for forward compatibility.
		}
	}
	return fetchers
}

func (s *updSource) panelFetcher(f release.File) update.Fetcher {
	return update.Fetcher{Kind: "panel", Fetch: func(ctx context.Context, offset uint64, w io.Writer) (uint64, error) {
		return s.fetchPanelSource(ctx, f.Name, offset, w)
	}}
}

func (s *updSource) githubFetcher(m *release.Manifest, f release.File) update.Fetcher {
	base, err := url.Parse(githubReleaseBase)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil ||
		(strings.EqualFold(base.Hostname(), "github.com") && base.Port() != "" && base.Port() != "443") {
		return update.Fetcher{Kind: "github", Fetch: func(context.Context, uint64, io.Writer) (uint64, error) {
			return 0, update.PermanentSourceFailure("refused", errors.New("release source configuration refused"))
		}}
	}
	rawURL := strings.TrimRight(githubReleaseBase, "/") + "/" + url.PathEscape(m.Version) + "/" + url.PathEscape(f.Name)
	return update.Fetcher{Kind: "github", Fetch: func(ctx context.Context, offset uint64, w io.Writer) (uint64, error) {
		return s.fetchRangedHTTP(ctx, rawURL, offset, uint64(f.Size), w, rangedHTTPPolicy{
			redirect: func(req *http.Request, via []*http.Request) bool {
				if len(via) > 3 || req.URL.User != nil || req.URL.Scheme != "https" {
					return false
				}
				host := strings.ToLower(req.URL.Hostname())
				port := req.URL.Port()
				if host == strings.ToLower(base.Hostname()) && strings.EqualFold(req.URL.Host, base.Host) {
					return true // the e2e build's own loopback prefix may use a test port
				}
				if port != "" && port != "443" {
					return false
				}
				switch host {
				case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
					return true
				default:
					return false
				}
			},
			statusError: func(status int) error {
				if status >= 300 && status < 400 {
					return update.PermanentSourceFailure("refused", errors.New("source redirect refused"))
				}
				return mapHTTPSourceStatus("GitHub", status)
			},
		})
	}}
}

func (s *updSource) cloudflareFetcher(f release.File, rawURL string) update.Fetcher {
	return update.Fetcher{Kind: "cloudflare", Fetch: func(ctx context.Context, offset uint64, w io.Writer) (uint64, error) {
		if !validCloudflareURL(rawURL) {
			return 0, update.PermanentSourceFailure("refused", errors.New("cloudflare source URL refused"))
		}
		return s.fetchRangedHTTP(ctx, rawURL, offset, uint64(f.Size), w, rangedHTTPPolicy{})
	}}
}

func validCloudflareURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	return cloudflareR2Host.MatchString(strings.ToLower(u.Hostname()))
}

type rangedHTTPPolicy struct {
	redirect    func(*http.Request, []*http.Request) bool
	statusError func(int) error
}

func (s updSource) fetchRangedHTTP(ctx context.Context, rawURL string, offset, expected uint64, w io.Writer, policy rangedHTTPPolicy) (uint64, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return 0, update.PermanentSourceFailure("refused", errors.New("update source URL refused"))
	}
	dial := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if s.a.updateDialContext != nil {
		dial = s.a.updateDialContext
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.a.updateRootCAs},
		DialContext:           dial,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
	}
	client := &http.Client{Transport: transport}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if policy.redirect != nil && policy.redirect(req, via) {
			return nil
		}
		return http.ErrUseLastResponse
	}
	defer client.CloseIdleConnections()
	total, err := rangedGET(ctx, client, rawURL, offset, w, rangedGETPolicy{
		userAgent: updateHTTPUserAgent, strictTotal: true, expectedTotal: expected,
		parseRange: func(value string, offset uint64) (uint64, error) {
			total, err := parseRangedContentRange(value, offset, expected, true)
			if err != nil {
				return 0, update.PermanentSourceFailure("size", errors.New("source returned an invalid byte range"))
			}
			return total, nil
		},
		statusError: func(status int) error {
			if policy.statusError != nil {
				return policy.statusError(status)
			}
			if status >= 300 && status < 400 {
				return update.PermanentSourceFailure("refused", errors.New("source redirect refused"))
			}
			return mapHTTPSourceStatus("source", status)
		},
		requestError: classifyUpdateHTTPError,
		bodyReader: func(r io.Reader) io.Reader {
			return activityReader{r: r, touch: s.touch}
		},
	})
	if err == nil || errors.Is(err, update.ErrPermanent) || errors.Is(err, update.ErrRetryLater) || update.IsOverflow(err) {
		return total, err
	}
	if errors.Is(err, errRangedSize) {
		return total, update.PermanentSourceFailure("size", errors.New("source reported a different file size"))
	}
	return total, classifyUpdateHTTPError(err)
}

func classifyUpdateHTTPError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return update.TransientSourceFailure("timeout", errors.New("source request timed out"))
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return update.TransientSourceFailure("refused", errors.New("source connection refused"))
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return update.TransientSourceFailure("timeout", errors.New("source request timed out"))
	}
	var recordErr *tls.RecordHeaderError
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &recordErr) || errors.As(err, &verifyErr) || strings.Contains(strings.ToLower(err.Error()), "tls") {
		return update.TransientSourceFailure("tls", errors.New("source TLS failed"))
	}
	return update.TransientSourceFailure("error", errors.New("source request failed"))
}

func (s updSource) fetchPanelSource(ctx context.Context, name string, offset uint64, w io.Writer) (uint64, error) {
	if s.a.linkOnly() {
		total, err := s.fetchLink(ctx, name, offset, w)
		if err != nil {
			return total, mapLinkSourceError(err)
		}
		return total, nil
	}
	st := s.a.settings.Load()
	if st == nil {
		st = &pb.NodeSettings{}
	}
	client := newHTTPClient(s.a.mtlsConfig(), dialTimeout(st), &http.HTTP2Config{
		SendPingTimeout: secs(st.KeepaliveIntervalS, 20*time.Second),
		PingTimeout:     secs(st.KeepaliveTimeoutS, 45*time.Second),
	})
	defer client.CloseIdleConnections()
	cl := agentv1connect.NewAgentServiceClient(client, "https://"+s.a.meta.Panel, connect.WithReadMaxBytes(1<<20))
	stream, err := cl.FetchUpdate(ctx, connect.NewRequest(&pb.FetchUpdateRequest{Name: name, Offset: offset}))
	if err != nil {
		return 0, mapPanelSourceError(err)
	}
	defer stream.Close()
	var total uint64
	for stream.Receive() {
		msg := stream.Msg()
		total = msg.TotalSize
		if len(msg.Data) != 0 {
			if _, err := w.Write(msg.Data); err != nil {
				return total, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return total, mapPanelSourceError(err)
	}
	return total, nil
}

func mapPanelSourceError(err error) error {
	code := connect.CodeOf(err)
	codeText := strings.ToLower(code.String())
	detail := errors.New("panel download failed: " + codeText)
	var connectErr *connect.Error
	isConnectError := errors.As(err, &connectErr)
	switch code {
	case connect.CodeResourceExhausted:
		return update.BusySourceFailure("busy", detail)
	case connect.CodeNotFound, connect.CodeFailedPrecondition, connect.CodePermissionDenied, connect.CodeUnimplemented:
		return update.PermanentSourceFailure("not_found", detail)
	default:
		if code != connect.CodeUnknown || isConnectError {
			return update.TransientSourceFailure(codeText, detail)
		}
		return sourceFailureWithDetail("panel", classifyUpdateHTTPError(err))
	}
}

func mapLinkSourceError(err error) error {
	var failure *update.FetchFailure
	if errors.As(err, &failure) {
		return err
	}
	var status *httpStatusError
	if errors.As(err, &status) {
		return mapHTTPSourceStatus("panel link", status.status)
	}
	switch {
	case errors.Is(err, update.ErrRetryLater):
		return update.BusySourceFailure("busy", errors.New("panel link download is busy"))
	case errors.Is(err, update.ErrPermanent):
		return update.PermanentSourceFailure("not_found", errors.New("panel link refused the file"))
	case errors.Is(err, errLinkStalled):
		return update.TransientSourceFailure("stalled", errors.New("panel link stopped sending bytes"))
	case errors.Is(err, errInvalidContentRange), errors.Is(err, errRangedOffsetTooLarge), errors.Is(err, errRangedSize):
		return update.PermanentSourceFailure("size", errors.New("panel link returned an invalid byte range"))
	case update.IsOverflow(err):
		return err
	default:
		return sourceFailureWithDetail("panel link", classifyUpdateHTTPError(err))
	}
}

func mapHTTPSourceStatus(source string, status int) error {
	code := "http_" + strconv.Itoa(status)
	detail := errors.New(source + " request failed: " + code)
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return update.BusySourceFailure("busy", detail)
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return update.PermanentSourceFailure(code, detail)
	default:
		return update.TransientSourceFailure(code, detail)
	}
}

func sourceFailureWithDetail(source string, err error) error {
	code := "error"
	var failure *update.FetchFailure
	if errors.As(err, &failure) {
		code = failure.Code
	}
	return update.TransientSourceFailure(code, errors.New(source+" request failed: "+code))
}
