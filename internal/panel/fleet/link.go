package fleet

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	"google.golang.org/protobuf/proto"
)

const (
	defaultLinkHandshakeTimeout = 10 * time.Second
)

type agentSessionStream interface {
	Context() context.Context
	Receive() (*agentv1.ConnectRequest, error)
	Send(*agentv1.ConnectResponse) error
}

type connectSessionStream struct {
	ctx    context.Context
	stream *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse]
}

func (s connectSessionStream) Context() context.Context                  { return s.ctx }
func (s connectSessionStream) Receive() (*agentv1.ConnectRequest, error) { return s.stream.Receive() }
func (s connectSessionStream) Send(m *agentv1.ConnectResponse) error     { return s.stream.Send(m) }

type websocketSessionStream struct {
	ctx   context.Context
	ioCtx context.Context
	conn  *websocket.Conn
}

func (s websocketSessionStream) Context() context.Context { return s.ctx }

func (s websocketSessionStream) Receive() (*agentv1.ConnectRequest, error) {
	typ, b, err := agentlink.ReadFrame(s.ioCtx, s.conn)
	if err != nil || typ != websocket.MessageBinary {
		if err == nil {
			err = errors.New("non-binary agent link frame")
		}
		return nil, err
	}
	var m agentv1.ConnectRequest
	if err := proto.Unmarshal(b, &m); err != nil {
		return nil, errors.New("invalid ConnectRequest frame")
	}
	return &m, nil
}

func (s websocketSessionStream) Send(m *agentv1.ConnectResponse) error {
	return agentlink.WriteFrame(s.ioCtx, s.conn, m)
}

// LinkHandler serves the public, signed WebSocket transport. Mount it only beneath the stored secret path prefix.
func (f *Fleet) LinkHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeID, ok := linkNodePath(r.URL.Path)
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if err != nil {
			return
		}
		ws.SetReadLimit(-1) // ReadFrame applies the shared 4 MiB cap and lets the handler choose the close status.
		closeStatus := websocket.StatusNormalClosure
		defer func() {
			_ = ws.Close(closeStatus, "")
		}()

		handshakeTimeout := f.linkHandshakeTimeout
		if handshakeTimeout <= 0 {
			handshakeTimeout = defaultLinkHandshakeTimeout
		}
		hsctx, cancelHandshake := context.WithCancel(r.Context())
		defer cancelHandshake()
		handshakeTimer := time.AfterFunc(handshakeTimeout, func() {
			_ = ws.Close(websocket.StatusPolicyViolation, "")
			cancelHandshake()
		})
		defer handshakeTimer.Stop()
		panelNonce := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, panelNonce); err != nil {
			_ = ws.Close(websocket.StatusPolicyViolation, "")
			return
		}
		audience := r.Host
		if audience == "" || agentlink.WriteFrame(hsctx, ws, &agentv1.LinkChallenge{Nonce: panelNonce, Audience: audience}) != nil {
			_ = ws.Close(websocket.StatusPolicyViolation, "")
			return
		}
		typ, frame, err := agentlink.ReadFrame(hsctx, ws)
		if err != nil || typ != websocket.MessageBinary {
			_ = ws.Close(websocket.StatusPolicyViolation, "")
			return
		}
		var auth agentv1.LinkAuth
		if proto.Unmarshal(frame, &auth) != nil || auth.NodeId != nodeID || len(auth.Nonce) != 32 {
			_ = ws.Close(websocket.StatusPolicyViolation, "")
			return
		}
		digest := agentlink.AgentDigest(nodeID, audience, panelNonce)
		pc, ok := f.verifyLinkAuth(hsctx, nodeID, digest, auth.Signature)
		if !ok {
			_ = ws.Close(websocket.StatusPolicyViolation, "")
			return
		}

		// Claim ownership as soon as the proof is accepted, before Hello arrives.
		owner, ownerCtx := f.claimOwner(nodeID, r.Context())
		stopOwnerClose := context.AfterFunc(ownerCtx, func() {
			if isSuperseded(context.Cause(ownerCtx)) {
				_ = ws.Close(websocket.StatusCode(4000), "")
			}
		})
		defer stopOwnerClose()
		panelSig, err := agentlink.Sign(f.ca.key, agentlink.PanelDigest(nodeID, audience, panelNonce, auth.Nonce))
		if err != nil || agentlink.WriteFrame(hsctx, ws, &agentv1.LinkAccept{Signature: panelSig}) != nil {
			return
		}
		handshakeTimer.Stop()
		cancelHandshake()

		if err := (agentService{f}).runSession(ownerCtx, nodeID, pc, owner, websocketSessionStream{ctx: ownerCtx, ioCtx: r.Context(), conn: ws}); err != nil {
			if isSuperseded(err) {
				closeStatus = websocket.StatusCode(4000)
			} else {
				closeStatus = websocket.StatusPolicyViolation
			}
		}
	})
}

func linkNodePath(path string) (string, bool) {
	if !strings.HasPrefix(path, "/link/") {
		return "", false
	}
	id := strings.TrimPrefix(path, "/link/")
	return id, id != "" && !strings.Contains(id, "/")
}

func (f *Fleet) verifyLinkAuth(ctx context.Context, nodeID string, digest, signature []byte) (peerCert, bool) {
	now := f.now().UTC()
	certs, err := f.st.LinkCertificates(ctx, nodeID, now)
	if err != nil {
		return peerCert{}, false
	}
	for _, row := range certs {
		block, _ := pem.Decode([]byte(row.PEM))
		if block == nil || block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || cert.SerialNumber.Text(16) != row.Serial || !linkCertNamesNode(cert, nodeID) {
			continue
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: f.ca.pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			continue
		}
		pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
		if !ok || pub.Curve != elliptic.P256() || !agentlink.Verify(pub, digest, signature) {
			continue
		}
		return peerCert{serial: row.Serial, notAfter: cert.NotAfter}, true
	}
	return peerCert{}, false
}

func linkCertNamesNode(cert *x509.Certificate, nodeID string) bool {
	if len(cert.URIs) != 1 {
		return false
	}
	u := cert.URIs[0]
	id := strings.TrimPrefix(u.Path, "/")
	legacy := u.Scheme == "mistgate" && u.Host == "node"
	if legacy {
		return id == nodeID
	}
	return u.Scheme == nodeURIScheme && u.Host == nodeURIHost && id == nodeID && !strings.Contains(id, "/")
}

func isSuperseded(err error) bool {
	var ce *connect.Error
	return errors.As(err, &ce) && ce.Code() == connect.CodeAborted
}
