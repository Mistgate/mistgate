package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	"google.golang.org/protobuf/proto"
)

const linkHandshakeTimeout = 10 * time.Second

type websocketAgentTransport struct {
	ctx       context.Context
	conn      *websocket.Conn
	closeOnce sync.Once
}

func (s *websocketAgentTransport) Context() context.Context { return s.ctx }

func (s *websocketAgentTransport) Send(m *pb.ConnectRequest) error {
	return agentlink.WriteFrame(s.ctx, s.conn, m)
}

func (s *websocketAgentTransport) Receive() (*pb.ConnectResponse, error) {
	typ, frame, err := agentlink.ReadFrame(s.ctx, s.conn)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, errors.New("panel sent a non-binary link frame")
	}
	var m pb.ConnectResponse
	if err := proto.Unmarshal(frame, &m); err != nil {
		return nil, errors.New("panel sent an invalid ConnectResponse frame")
	}
	return &m, nil
}

func (s *websocketAgentTransport) CloseSend() error { return s.Close() }

func (s *websocketAgentTransport) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.conn.Close(websocket.StatusNormalClosure, "") })
	return err
}

func parseLinkBase(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("link URL must be wss://host/<secret-prefix>")
	}
	if u.Path == "" || len(strings.Trim(u.Path, "/")) < 16 {
		return nil, errors.New("link URL must include a secret path prefix of at least 16 characters")
	}
	return u, nil
}

func (a *Agent) dialLink(ctx context.Context) (*websocketAgentTransport, func(), error) {
	base, err := parseLinkBase(a.cfg.LinkURL)
	if err != nil {
		return nil, nil, err
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/link/" + url.PathEscape(a.meta.NodeID)
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	endpoint.Scheme = "wss"

	client := a.linkHTTPClient
	var cleanup func()
	if client == nil {
		st := a.settings.Load()
		transport := &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}},
			DialContext:         (&net.Dialer{Timeout: dialTimeout(st), KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: dialTimeout(st),
			IdleConnTimeout:     30 * time.Second,
			MaxIdleConns:        1,
		}
		client = &http.Client{Transport: transport}
		cleanup = client.CloseIdleConnections
	}

	hctx, cancel := context.WithTimeout(ctx, linkHandshakeTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(hctx, endpoint.String(), &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, nil, errors.New("link TLS or WebSocket dial failed")
	}
	ws.SetReadLimit(agentlink.MaxFrameSize)
	refuse := func(reason error) (*websocketAgentTransport, func(), error) {
		_ = ws.Close(websocket.StatusPolicyViolation, "")
		if cleanup != nil {
			cleanup()
		}
		return nil, nil, reason
	}

	typ, frame, err := agentlink.ReadFrame(hctx, ws)
	if err != nil || typ != websocket.MessageBinary {
		return refuse(errors.New("panel link challenge rejected"))
	}
	var challenge pb.LinkChallenge
	if proto.Unmarshal(frame, &challenge) != nil || len(challenge.Nonce) != 32 || challenge.Audience != base.Host {
		return refuse(errors.New("panel link challenge rejected"))
	}
	id := a.id.Load()
	key, ok := id.cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return refuse(errors.New("node link key is invalid"))
	}
	nodeNonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nodeNonce); err != nil {
		return refuse(errors.New("node link nonce generation failed"))
	}
	signature, err := agentlink.Sign(key, agentlink.AgentDigest(a.meta.NodeID, challenge.Audience, challenge.Nonce))
	if err != nil {
		return refuse(errors.New("node link signature failed"))
	}
	if err := agentlink.WriteFrame(hctx, ws, &pb.LinkAuth{NodeId: a.meta.NodeID, Nonce: nodeNonce, Signature: signature}); err != nil {
		return refuse(errors.New("panel link authentication failed"))
	}
	typ, frame, err = agentlink.ReadFrame(hctx, ws)
	if err != nil || typ != websocket.MessageBinary {
		return refuse(errors.New("panel link authentication failed"))
	}
	var accepted pb.LinkAccept
	if proto.Unmarshal(frame, &accepted) != nil {
		return refuse(errors.New("panel link authentication failed"))
	}
	if id.caCert == nil {
		return refuse(errors.New("panel link authentication failed"))
	}
	caKey, ok := id.caCert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !agentlink.Verify(caKey, agentlink.PanelDigest(a.meta.NodeID, challenge.Audience, challenge.Nonce, nodeNonce), accepted.Signature) {
		return refuse(errors.New("panel link authentication failed"))
	}
	return &websocketAgentTransport{conn: ws}, cleanup, nil
}

var _ agentTransport = (*websocketAgentTransport)(nil)
