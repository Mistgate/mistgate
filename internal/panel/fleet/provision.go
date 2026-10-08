package fleet

import (
	"context"
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
		CountryCode: spec.CountryCode, Location: spec.Location, Provider: spec.Provider,
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
