package fleet

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"time"

	"github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// CreateProvisionEnrollment creates the pending node, or rotates its pending enrollment token when a job is resumed.
// The token is returned exactly once to the caller and is never written to an audit row.
func (f *Fleet) CreateProvisionEnrollment(ctx context.Context, spec provision.NodeSpec, actor string, now, expires time.Time) (string, string, error) {
	if spec.ID == "" || spec.Name == "" || spec.Address == "" || expires.Before(now) {
		return "", "", errors.New("fleet: invalid node provisioning request")
	}
	newNode := &store.NodeRow{
		ID: spec.ID, Name: spec.Name, Address: spec.Address,
		CountryCode: spec.CountryCode, Location: spec.Location, Provider: spec.Provider, Share: spec.Sharing,
	}
	_, err := f.st.Node(ctx, spec.ID)
	if err == nil {
		newNode = nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", "", err
	}
	token := randomToken()
	_, err = f.st.CreateEnrollment(ctx, newNode, spec.ID, hashToken(token), actor, now, expires)
	if err != nil {
		return "", "", err
	}
	return token, f.ca.fingerprint, nil
}

// ProvisionNodeState reports the durable fleet state and connection used to resume an installation safely.
func (f *Fleet) ProvisionNodeState(ctx context.Context, nodeID string) (string, bool, error) {
	live, err := f.liveRowsForNode(ctx, nodeID, false)
	if err != nil {
		return "", false, err
	}
	if live.State == "" {
		return "", false, store.ErrNotFound
	}
	return live.State, live.Connected, nil
}

// ErrEnrollRefused is EnrollInSession's answer to an unknown, expired or used token.
var ErrEnrollRefused = errors.New("fleet: enrollment token unknown, expired or used")

// EnrollInSession enrols a node over a session the caller already holds (the SSH install of the first subscription host,
// which cannot reach the panel before it is the host): the same exchange as EnrollmentService.Enroll with a plain token.
func (f *Fleet) EnrollInSession(ctx context.Context, token string, csrDER []byte) (nodeID string, certPEM, caPEM []byte, err error) {
	if token == "" || len(token) > 512 {
		return "", nil, nil, ErrEnrollRefused
	}
	pub, err := parseCSR(csrDER)
	if err != nil {
		return "", nil, nil, err
	}
	keyDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", nil, nil, errors.New("fleet: bad CSR key")
	}
	keyHash := sha256.Sum256(keyDER)
	now := f.now().UTC()
	res, err := f.st.Enroll(ctx, hashToken(token), keyHash[:], now, func(nodeID string) (store.CertRow, error) {
		return f.ca.issueNode(nodeID, pub, now)
	})
	switch {
	case errors.Is(err, store.ErrEnrollToken):
		return "", nil, nil, ErrEnrollRefused
	case err != nil:
		return "", nil, nil, err
	}
	if !res.Replay {
		f.event(ctx, 1, "node_enrolled", res.NodeID, map[string]string{"via": "ssh_session"})
	}
	return res.NodeID, []byte(res.Cert.PEM), []byte(f.ca.pem), nil
}
