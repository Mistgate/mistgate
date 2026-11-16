package trusttunnel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	wire "github.com/mistgate/mistgate/internal/trusttunnel"
)

// noUser is the hash an unknown user name is compared against, so that "no such user" and "wrong password" cost the same
// work: one SHA-256 of the offered password and one constant-time comparison.
var noUser [sha256.Size]byte

// passwordDigest is a variable so that a test can count how often a CONNECT pays for it.
var passwordDigest = func(password string) [sha256.Size]byte { return sha256.Sum256([]byte(password)) }

func (in *inbound) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor != 2 || r.Method != http.MethodConnect {
		in.serveMasquerade(w, r)
		return
	}
	username, password, err := wire.ParseProxyAuthorization(r.Header.Get("Proxy-Authorization"))
	if err != nil {
		in.unknownKeys.Add(1)
		in.serveMasquerade(w, r)
		return
	}
	cs := in.idx.Load().byUser[username]
	want := noUser
	if cs != nil {
		want = cs.passHash
	}
	offered := passwordDigest(password)
	match := subtle.ConstantTimeCompare(offered[:], want[:]) == 1
	switch {
	case cs == nil:
		in.unknownKeys.Add(1)
		in.serveMasquerade(w, r)
		return
	case !match || cs.expired(in.e.now()):
		cs.authFailed.Add(1)
		in.serveMasquerade(w, r)
		return
	}

	// Every CONNECT is authorized on its own: one HTTP/2 connection can carry the tunnels of several credentials.
	stream, ok := in.beginStream(r, w, sitehttp.Conn(r.Context()), cs)
	if !ok {
		in.serveMasquerade(w, r)
		return
	}
	defer in.finishStream(stream)
	sitehttp.MarkAuthenticated(r.Context())
	// The site's Read/WriteTimeout belong to its pages. They would end a tunnel (and its own connect timeout) after 30 s,
	// so they go first, before anything is dialled.
	if stream.clearDeadlines() != nil {
		writeTunnelError(w, 300, "")
		return
	}

	authority, err := wire.ParseAuthority(r.Host)
	if err != nil {
		writeTunnelError(w, 300, "")
		return
	}
	switch authority.Kind {
	case wire.AuthorityCheck:
		w.WriteHeader(http.StatusOK)
		_ = stream.flush()
	case wire.AuthorityUDP2:
		in.serveUDP(w, r, stream)
	case wire.AuthorityICMP:
		writeTunnelError(w, 300, "")
	case wire.AuthorityHostPort:
		in.serveTCP(w, stream, authority)
	default:
		writeTunnelError(w, 300, "")
	}
}

func (in *inbound) masquerade() http.Handler {
	if in.e.env.Masquerade != nil {
		if handler := in.e.env.Masquerade(in.spec.ID); handler != nil {
			return handler
		}
	}
	return decoy.Handler()
}

// serveMasquerade answers as the site does. The credential header never reaches the site's handler.
func (in *inbound) serveMasquerade(w http.ResponseWriter, r *http.Request) {
	request := r.Clone(r.Context())
	request.Header = r.Header.Clone()
	request.Header.Del("Proxy-Authorization")
	in.masquerade().ServeHTTP(w, request)
}

func writeTunnelError(w http.ResponseWriter, code int, host string) {
	w.Header().Set("Content-Length", "0")
	switch code {
	case 301:
		w.Header().Set("X-Warning", "301 - Destination is unavailable")
	case 302:
		w.Header().Set("X-Warning", "302 - Connection timed out")
	case 310:
		w.Header().Set("X-Warning", "310 - Destination address is blocked")
		w.Header().Set("X-Adguard-Vpn-Error", host)
	default:
		w.Header().Set("X-Warning", "300 - Connection failed for some reason")
	}
	w.WriteHeader(http.StatusBadGateway)
}

func warningForDialError(err error) int {
	if errors.Is(err, egress.ErrBlocked) {
		return 310
	}
	if errors.Is(err, egress.ErrNoIPv4) {
		return 301
	}
	var netErr interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return 302
	}
	var opErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
		return 301
	}
	return 300
}
