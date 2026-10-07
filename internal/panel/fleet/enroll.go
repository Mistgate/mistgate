package fleet

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"net"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type enrollmentService struct{ f *Fleet }

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// parseCSR accepts only a valid, self-signed PKCS#10 request with an ECDSA P-256 key.
func parseCSR(der []byte) (*ecdsa.PublicKey, error) {
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, errors.New("bad CSR")
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, errors.New("bad CSR signature")
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("CSR key must be ECDSA P-256")
	}
	return pub, nil
}

func peerHost(req connect.AnyRequest) string {
	host, _, err := net.SplitHostPort(req.Peer().Addr)
	if err != nil {
		return req.Peer().Addr
	}
	return host
}

// Enroll exchanges a one-time token and a CSR for a node certificate. TLS is server-auth only here; the
// token is the authentication.
func (s enrollmentService) Enroll(ctx context.Context, req *connect.Request[agentv1.EnrollRequest]) (*connect.Response[agentv1.EnrollResponse], error) {
	f := s.f
	now := f.now().UTC()
	peer := limiterKey(auth.ClientIPFrom(ctx), peerHost(req))
	limit, limitErr := f.enrollLim.Peek(ctx, enrollmentWindow, peer)
	if limitErr != nil || !limit.Allowed {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many failed attempts, try later"))
	}
	m := req.Msg
	if m.ApiVersion != APIVersion {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("api_version unsupported"))
	}
	deny := func() error {
		decision, err := f.enrollLim.Record(ctx, enrollmentWindow, peer)
		if err != nil || !decision.Allowed {
			return connect.NewError(connect.CodeResourceExhausted, errors.New("too many failed attempts, try later"))
		}
		return connect.NewError(connect.CodeUnauthenticated, errors.New("enrollment token unknown, expired or used"))
	}
	if m.EnrollmentToken == "" || len(m.EnrollmentToken) > 512 { // real tokens are 52 characters (randomToken)
		return nil, deny()
	}
	pub, err := parseCSR(m.CsrDer)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	keyDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad CSR key"))
	}
	keyHash := sha256.Sum256(keyDER)

	res, err := f.st.Enroll(ctx, hashToken(m.EnrollmentToken), keyHash[:], now, func(nodeID string) (store.CertRow, error) {
		return f.ca.issueNode(nodeID, pub, now)
	})
	switch {
	case errors.Is(err, store.ErrEnrollToken):
		return nil, deny()
	case errors.Is(err, store.ErrNodeRetired):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node retired"))
	case err != nil:
		f.log.Error("enroll", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !res.Replay {
		f.event(ctx, 1, "node_enrolled", res.NodeID, map[string]string{"agent_version": m.AgentVersion})
		// Re-enrollment revoked the older certificates: a stream that still runs on one of them must go.
		if old := f.session(res.NodeID); old != nil {
			old.cancel(connect.NewError(connect.CodeAborted, errors.New("node re-enrolled")))
		}
	}
	return connect.NewResponse(&agentv1.EnrollResponse{
		NodeId:           res.NodeID,
		CertificatePem:   res.Cert.PEM,
		CaCertificatePem: f.ca.pem,
		NotAfterUnix:     res.Cert.NotAfter.Unix(),
		RenewAfterUnix:   res.Cert.NotAfter.Add(-renewBefore).Unix(),
	}), nil
}

// Renew issues a new certificate over mTLS with the current one. The CSR subject is ignored.
func (s enrollmentService) Renew(ctx context.Context, req *connect.Request[agentv1.RenewRequest]) (*connect.Response[agentv1.RenewResponse], error) {
	f := s.f
	id, ok := nodeID(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no node certificate"))
	}
	pub, err := parseCSR(req.Msg.CsrDer)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	now := f.now().UTC()
	cert, err := f.ca.issueNode(id, pub, now)
	if err == nil {
		err = f.st.RenewCert(ctx, cert, now, oldCertGrace)
	}
	if err != nil {
		f.log.Error("renew", "node", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&agentv1.RenewResponse{
		CertificatePem: cert.PEM,
		NotAfterUnix:   cert.NotAfter.Unix(),
		RenewAfterUnix: cert.NotAfter.Add(-renewBefore).Unix(),
	}), nil
}
