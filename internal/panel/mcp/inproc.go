package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// forwardedHeaders are the request headers of the agent's MCP call that an in-process API call carries: the credential
// (re-verified by the API on every call, so a revocation lands mid-flight) and what the trusted-proxy rules read to find
// the client address for the audit rows. Nothing else is copied: no cookie, no origin.
var forwardedHeaders = []string{"Authorization", "X-Forwarded-For", "X-Real-Ip", "Forwarded", "Cf-Connecting-Ip"}

// inprocTransport calls the admin API handler directly, with no socket: the generated Connect clients run over it. The
// request context (grants, channel) travels with the request.
type inprocTransport struct {
	h      http.Handler
	header http.Header // of the agent's MCP request
	remote string      // its RemoteAddr
}

func (t inprocTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for _, k := range forwardedHeaders {
		if v := t.header.Values(k); len(v) > 0 {
			r.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), v...)
		}
	}
	r.RemoteAddr = t.remote
	r.Host = "panel.invalid"
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

// clients are the Connect clients of the services the tools call.
type clients struct {
	Fleet        adminv1connect.FleetServiceClient
	Node         adminv1connect.NodeServiceClient
	Health       adminv1connect.HealthServiceClient
	User         adminv1connect.UserServiceClient
	Group        adminv1connect.GroupServiceClient
	Subs         adminv1connect.SubscriptionServiceClient
	Update       adminv1connect.UpdateServiceClient
	Auth         adminv1connect.AuthServiceClient
	Provisioning adminv1connect.ProvisioningServiceClient
	Warp         adminv1connect.WarpServiceClient
}

func newClients(h http.Handler, header http.Header, remote string) *clients {
	hc := &http.Client{Transport: inprocTransport{h: h, header: header, remote: remote}}
	const base = "http://panel.invalid"
	return &clients{
		Fleet:        adminv1connect.NewFleetServiceClient(hc, base),
		Node:         adminv1connect.NewNodeServiceClient(hc, base),
		Health:       adminv1connect.NewHealthServiceClient(hc, base),
		User:         adminv1connect.NewUserServiceClient(hc, base),
		Group:        adminv1connect.NewGroupServiceClient(hc, base),
		Subs:         adminv1connect.NewSubscriptionServiceClient(hc, base),
		Update:       adminv1connect.NewUpdateServiceClient(hc, base),
		Auth:         adminv1connect.NewAuthServiceClient(hc, base),
		Provisioning: adminv1connect.NewProvisioningServiceClient(hc, base),
		Warp:         adminv1connect.NewWarpServiceClient(hc, base),
	}
}

// apiError turns an error from an in-process call into a short message for the agent: the Connect code and the panel's
// own short text, cleaned and cut. Details never go to the agent. The error also carries the outcome for the owner's UI:
// the panel's refusal as a code (panelCode).
func apiError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failure("timeout", "timeout: the panel did not answer in time")
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Code() {
		case connect.CodeUnauthenticated:
			return failure("token_refused", "the token was refused (expired, revoked or wrong profile)")
		case connect.CodePermissionDenied:
			return failure("not_allowed", "not allowed for this token: "+clean(ce.Message(), maxError))
		case connect.CodeResourceExhausted:
			return failure("rate_limited", "rate limit: slow down")
		}
		code, params := panelCode(ce.Message())
		return &outcomeError{msg: ce.Code().String() + ": " + clean(ce.Message(), maxError), out: Outcome{Code: code, Params: params}}
	}
	return failure("", "the panel failed to answer")
}

// panelCode reads a refusal the panel writes as "code" or "code: k=v&k=v" (web/src/lib/errors.ts errorCode and
// errorVars): the words before the first colon, lower case, joined by "_" ("no trusted bundle" -> "no_trusted_bundle"), and
// the values after it. A free-form message gives a code nobody words, and the owner's UI then shows the message.
func panelCode(message string) (string, map[string]string) {
	head, detail, _ := strings.Cut(message, ":")
	var b strings.Builder
	for _, r := range strings.ToLower(head) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	code := strings.TrimSuffix(b.String(), "_")
	if len(code) > 48 {
		return "", nil
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return code, nil
	}
	params := map[string]string{"detail": clean(detail, 200)}
	if v, err := url.ParseQuery(detail); err == nil && strings.Contains(detail, "=") {
		for k, x := range v {
			if len(params) < 8 && len(x) > 0 {
				params[clean(k, 40)] = clean(x[0], 200)
			}
		}
	}
	return code, params
}
