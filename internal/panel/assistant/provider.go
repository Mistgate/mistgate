//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/mcp"
)

const (
	maxProviderResponse          = 4 << 20
	providerTimeout              = 120 * time.Second
	defaultMaxOutputTokens int64 = 8192
)

type modelInfo struct {
	id, name                string
	context                 int64
	inputPrice, outputPrice float64
	recommended             bool
}

type modelToolCall struct {
	id, name string
	input    []byte
}

type modelRequest struct {
	Model           string
	System          string
	Stable          string
	Effort          adminv1.AssistantEffort
	MaxOutputTokens int64
	History         []jsonRaw
	Tools           []mcp.AssistantTool
}

// jsonRaw is a named alias so provider transcript blocks cannot accidentally be modified through a shared buffer.
type jsonRaw = json.RawMessage

type modelResponse struct {
	Transcript    []jsonRaw
	Tools         []modelToolCall
	Text          string
	Stop          string
	InputTokens   int64
	OutputTokens  int64
	UsageReported bool
}

type adapter interface {
	Provider() adminv1.AssistantProvider
	Check(context.Context, string) ([]modelInfo, error)
	Stream(context.Context, string, modelRequest, func(string) error) (modelResponse, error)
	ToolResults([]modelToolCall, []toolOutput) []jsonRaw
}

type toolOutput struct {
	text string
	err  bool
}

type providerError struct {
	Code string
}

func (e *providerError) Error() string { return "provider request failed" }

func apiError(code string) error { return &providerError{Code: code} }

func providerCode(err error) string {
	var pe *providerError
	if errors.As(err, &pe) {
		return pe.Code
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "provider_unreachable"
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return "provider_unreachable"
	}
	return "provider_unreachable"
}

func codeConnect(code string) error {
	var cc connect.Code
	switch code {
	case "bad_key":
		cc = connect.CodeInvalidArgument
	case "no_balance":
		cc = connect.CodeInvalidArgument
	case "provider_overloaded", "provider_unreachable":
		cc = connect.CodeUnavailable
	case "budget_reached", "rate_limited":
		cc = connect.CodeResourceExhausted
	case "assistant_off", "not_allowed", "no_provider", "turn_running", "plan_not_pending", "follow_up_not_pending":
		cc = connect.CodeFailedPrecondition
	case "text_too_long", "note_too_long", "too_many_notes", "invalid_provider", "invalid_effort":
		cc = connect.CodeInvalidArgument
	default:
		cc = connect.CodeUnavailable
	}
	e := connect.NewError(cc, errors.New("assistant request could not be completed"))
	if detail, err := connect.NewErrorDetail(&adminv1.AssistantError{Code: code}); err == nil {
		e.AddDetail(detail)
	}
	return e
}

type providerHTTP struct {
	base   *url.URL
	client *http.Client
}

func newProviderHTTP(base string, client *http.Client) (providerHTTP, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return providerHTTP{}, errors.New("assistant provider: invalid fixed endpoint")
	}
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		client = &http.Client{Transport: transport, Timeout: providerTimeout}
	} else {
		clientCopy := *client
		client = &clientCopy
	}
	if client.Timeout == 0 || client.Timeout > providerTimeout {
		client.Timeout = providerTimeout
	}
	if client.Transport == nil {
		client.Transport = http.DefaultTransport
	}
	client.Transport = cappedRoundTripper{base: client.Transport}
	prev := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !strings.EqualFold(req.URL.Scheme, "https") || len(via) > 0 && !strings.EqualFold(req.URL.Host, u.Host) {
			return http.ErrUseLastResponse
		}
		if prev != nil {
			return prev(req, via)
		}
		if len(via) >= 5 {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return providerHTTP{base: u, client: client}, nil
}

func (p providerHTTP) request(ctx context.Context, method, path, key, contentType string, body io.Reader) (*http.Response, error) {
	u := *p.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, apiError("provider_unreachable")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	return p.do(req)
}

func (p providerHTTP) do(req *http.Request) (*http.Response, error) {
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, apiError("provider_unreachable")
	}
	return resp, nil
}

type cappedRoundTripper struct{ base http.RoundTripper }

func (t cappedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp != nil && resp.Body != nil {
		resp.Body = &cappedBody{ReadCloser: resp.Body, max: maxProviderResponse}
	}
	return resp, err
}

func mapStatus(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "bad_key"
	case http.StatusPaymentRequired:
		return "no_balance"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "provider_overloaded"
	default:
		if status >= 500 {
			return "provider_overloaded"
		}
		return "provider_unreachable"
	}
}

type cappedBody struct {
	io.ReadCloser
	max, read int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.read >= b.max {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, errors.New("assistant provider response exceeds size limit")
		}
		return 0, err
	}
	left := b.max - b.read
	if int64(len(p)) > left {
		p = p[:left]
	}
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)
	return n, err
}
