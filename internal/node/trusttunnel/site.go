package trusttunnel

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
	wire "github.com/mistgate/mistgate/internal/trusttunnel"
)

// noUser is the hash an unknown user name is compared against, so that "no such user" and "wrong password" cost the same
// work: one SHA-256 of the offered password and one constant-time comparison.
var noUser [sha256.Size]byte

// passwordDigest is a variable so that a test can count how often a CONNECT pays for it.
var passwordDigest = func(password string) [sha256.Size]byte { return sha256.Sum256([]byte(password)) }

func (in *inbound) serveHTTP(w http.ResponseWriter, r *http.Request) {
	isH2 := r.ProtoMajor == 2
	isH3 := r.ProtoMajor == 3 && in.h3Enabled
	if (!isH2 && !isH3) || r.Method != http.MethodConnect {
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
	var key any
	var closeConn func()
	if isH2 {
		conn := sitehttp.Conn(r.Context())
		key = conn
		if conn != nil {
			closeConn = func() { _ = conn.Close() }
		}
	} else {
		conn := h3ConnFromContext(r.Context())
		key = conn
		if conn != nil {
			closeConn = func() {
				_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "")
			}
		}
	}
	stream, ok := in.beginStreamOnConn(r, w, key, closeConn, w, cs)
	if !ok {
		in.serveMasquerade(w, r)
		return
	}
	defer in.finishStream(stream)
	defer func() {
		if stream.endedBeforeResponse() {
			panic(http.ErrAbortHandler)
		}
	}()
	if isH3 {
		in.h3Anonymous.MarkAuthenticated(key)
	} else {
		sitehttp.MarkAuthenticated(r.Context())
	}
	in.serveTunnel(w, r, stream)
}

// serveTunnel answers the CONNECT of an authenticated credential whose stream has begun.
func (in *inbound) serveTunnel(w http.ResponseWriter, r *http.Request, stream *streamState) {
	// The site's Read/WriteTimeout belong to its pages. They would end a tunnel (and its own connect timeout) after 30 s,
	// so they go first, before anything is dialled.
	if stream.clearDeadlines() != nil {
		in.refuse(plugin.RefusedOtherDeadlines, 0)
		writeTunnelError(w, 300, "")
		return
	}

	authority, err := wire.ParseAuthority(r.Host)
	if err != nil {
		in.refuse(plugin.RefusedOtherAuthority, 0)
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
		in.refuse(plugin.RefusedOtherICMP, 0)
		writeTunnelError(w, 300, "")
	case wire.AuthorityHostPort:
		in.serveTCP(w, stream, authority)
	default:
		in.refuse(plugin.RefusedOtherAuthority, 0)
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
	if in.h3Enabled && r.ProtoMajor < 3 {
		w.Header().Set("Alt-Svc", fmt.Sprintf(`h3=":%d"; ma=2592000`, in.spec.Listen.Port))
	}
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

func warningForDialError(err error) int { return warningFor(classifyDial(err)) }
