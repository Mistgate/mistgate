package mtproto

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

var errUnknownCredential = errors.New("mtproto: credential is not present")

// ServeStream serves one plain obfuscated2 connection for a credential already identified by its caller. It reads the
// client's 64-byte header directly from conn, then uses the same registration, DC connection, relay and accounting as
// a Fake TLS session.
func (s *Server) ServeStream(ctx context.Context, conn net.Conn, credID string) error {
	a, err := s.admit(conn)
	if err != nil {
		return err
	}
	return s.serveStream(ctx, conn, a, credID)
}

func (s *Server) serveStream(ctx context.Context, conn net.Conn, a admission, credID string) error {
	deadline := time.Now().Add(s.cfg.HandshakeTimeout)
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	key, ok := s.creds.Load().byID[credID]
	if !ok {
		s.guard.leave(a.addr)
		s.untrack(conn)
		_ = conn.Close()
		return errUnknownCredential
	}

	var hdr [headerLen]byte
	_, err := io.ReadFull(conn, hdr[:])
	s.guard.leave(a.addr)
	if err != nil {
		s.untrack(conn)
		_ = conn.Close()
		return err
	}
	fromClient, toClient, tag, dc, err := openHeader(&hdr, &key)
	if err != nil {
		s.untrack(conn)
		_ = conn.Close()
		return err
	}
	sess := &session{
		info: SessionInfo{
			ID: a.seq, CredentialID: credID, DC: dc, Tag: tag,
			Remote: conn.RemoteAddr().String(), Since: s.cfg.Now(),
		},
		key: key, client: conn, tc: conn, fromCli: fromClient, toCli: toClient,
	}
	if err := s.register(sess, conn); err != nil {
		s.untrack(conn)
		_ = conn.Close()
		return err
	}
	return s.serveSession(ctx, conn, sess, deadline)
}
