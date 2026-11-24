//go:build !js

package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New builds the Streamable HTTP handler of the MCP endpoint. It must sit behind the bearer middleware
// of the auth package (auth.Service.RequireBearer), which authenticates the request against the token store, applies the
// token's rate limit and puts the principal in the request context; New never sees a secret. Without a principal the
// request is refused.
//
// The server is stateless on purpose: every POST is verified again, so a revoked or expired token stops at its very next
// request, there is no session id to hijack and nothing is pushed to the client. One server per profile, so tools/list
// shows only what the token may call.
func New(c Config) (http.Handler, error) {
	if c.Plans == nil || c.Auth == nil || c.API == nil {
		return nil, errors.New("mcp: Plans, Auth and API are required")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	e := &env{cfg: c, now: c.Now}
	servers := c.Servers
	if servers == nil {
		servers = new(ServerSet)
	}
	sdk := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if ti := sdkauth.TokenInfoFromContext(r.Context()); ti != nil && len(ti.Scopes) > 0 {
			return servers.get(e, Profile(ti.Scopes[0])) // nil for an unknown profile: the SDK answers 400
		}
		return nil
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The DNS-rebinding check guards a no-auth local server. This one is behind a secret prefix and a bearer token, and
		// a panel on a loopback listener behind a reverse proxy sees the public Host header.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        maxBodyBytes,
	})
	// The bearer middleware of the SDK carries the token into the tool handlers (RequestExtra.TokenInfo). The principal is
	// the one the auth package resolved; the SDK only transports it.
	toTokenInfo := sdkauth.RequireBearerToken(func(ctx context.Context, _ string, r *http.Request) (*sdkauth.TokenInfo, error) {
		p, ok := c.Auth.Principal(r.Context())
		if !ok || p.TokenID == "" || p.Profile.rank() == 0 {
			return nil, sdkauth.ErrInvalidToken
		}
		return &sdkauth.TokenInfo{UserID: p.TokenID, Scopes: []string{string(p.Profile)}}, nil
	}, &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(sdk)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := c.Auth.Principal(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		release, ok := e.acquire(p.TokenID)
		if !ok {
			w.Header().Set("Retry-After", "1")
			http.Error(w, fmt.Sprintf("at most %d tool calls at a time per token", maxConcurrent), http.StatusTooManyRequests)
			return
		}
		defer release()
		r = r.WithContext(context.WithValue(r.Context(), remoteKey{}, r.RemoteAddr))
		toTokenInfo.ServeHTTP(w, r)
	}), nil
}

// ServerSet holds the MCP server of each profile and builds one on its first use, once, however many requests ask at the
// same moment. Building registers every tool of the profile and keeps its JSON schemas on the heap, which a panel that
// nobody talks to over MCP should not pay for. The zero value is ready; Config.Servers lets New and NewAssistant share one
// set, so a profile is built once for both. The tool handlers of a server use the Config of whoever built it first, so
// sharers must pass equivalent Configs.
type ServerSet struct{ slots sync.Map } // Profile -> *serverSlot

type serverSlot struct {
	once sync.Once
	srv  *mcp.Server
}

// get returns the server of a profile, built on the first call; nil for a profile the registry does not know.
func (s *ServerSet) get(e *env, p Profile) *mcp.Server {
	if !slices.Contains(profiles, p) {
		return nil
	}
	v, ok := s.slots.Load(p)
	if !ok {
		v, _ = s.slots.LoadOrStore(p, new(serverSlot))
	}
	sl := v.(*serverSlot)
	sl.once.Do(func() { sl.srv = e.newServer(p) })
	return sl.srv
}

// built says whether the server of a profile exists yet (tests).
func (s *ServerSet) built(p Profile) bool {
	v, ok := s.slots.Load(p)
	return ok && v.(*serverSlot).srv != nil
}

// acquire takes one of the token's maxConcurrent slots.
func (e *env) acquire(tokenID string) (release func(), ok bool) {
	v, _ := e.sems.LoadOrStore(tokenID, make(chan struct{}, maxConcurrent))
	sem := v.(chan struct{})
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	default:
		return nil, false
	}
}
