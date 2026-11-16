//go:build !js

package access

import (
	"context"
	"errors"
	"time"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const trustTunnelKeyLabel = "TrustTunnel key"

// TrustTunnelKeyLinks is the non-secret result used by the generated UserService handlers.
type TrustTunnelKeyLinks struct {
	Links    []SubServer
	HasKey   bool
	DeviceID string
	IssuedAt time.Time
}

func (s *Service) trustTunnelPlugin() (protocols.Protocol, error) {
	p, ok := s.reg.Get(store.TrustTunnelProtocol)
	if !ok {
		return nil, failed("the TrustTunnel protocol is not available on this panel")
	}
	return p, nil
}

func (s *Service) newTrustTunnelCred(userID, deviceID string) (store.AccessCred, error) {
	p, err := s.trustTunnelPlugin()
	if err != nil {
		return store.AccessCred{}, err
	}
	issued, err := p.IssueCredential(protocols.IssueInput{UserID: userID, DeviceID: deviceID})
	if err != nil {
		return store.AccessCred{}, s.internal("issue TrustTunnel key", err)
	}
	id := store.NewID("crd_")
	return store.AccessCred{ID: id, Protocol: p.ID(), SecretEnc: s.vault.Seal([]byte(issued.Secret), id), DataJSON: string(issued.NodeData)}, nil
}

func trustTunnelKeyDevice(userID, deviceID string) store.AccessDevice {
	return store.AccessDevice{ID: deviceID, UserID: userID, Platform: "other", Model: trustTunnelKeyLabel, Client: "trusttunnel"}
}

func (s *Service) trustTunnelKeyForNewUser(userID string, now time.Time) (store.AccessKeyDevice, error) {
	deviceID := store.NewID("dev_")
	cred, err := s.newTrustTunnelCred(userID, deviceID)
	if err != nil {
		return store.AccessKeyDevice{}, err
	}
	dev := trustTunnelKeyDevice(userID, deviceID)
	dev.Key, dev.CreatedAt = true, now
	cred.DeviceID, cred.UserID, cred.CreatedAt = deviceID, userID, now
	return store.AccessKeyDevice{Device: dev, Creds: []store.AccessCred{cred}}, nil
}

// issueTrustTunnelKey creates the user's one counted key device if needed. The secret stays sealed in the store.
func (s *Service) issueTrustTunnelKey(ctx context.Context, by string, u store.AccessUser) (made bool, err error) {
	if _, _, err := s.st.Access().TrustTunnelKey(ctx, u.ID); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, s.internal("TrustTunnel key", err)
	}
	deviceID := store.NewID("dev_")
	cred, err := s.newTrustTunnelCred(u.ID, deviceID)
	if err != nil {
		return false, err
	}
	err = s.st.Access().AddTrustTunnelKey(ctx, store.TrustTunnelKeyAdd{
		Device: trustTunnelKeyDevice(u.ID, deviceID), Cred: cred, Limit: u.DeviceLimit,
	}, s.now())
	switch {
	case errors.Is(err, store.ErrAccessExists):
		return false, nil
	case errors.Is(err, store.ErrNotFound):
		return false, notFound("user")
	case err != nil:
		var limit *store.AccessLimitError
		if errors.As(err, &limit) { // the code the admin words itself, as for an AmneziaVPN key: "device_limit: 5/5: names"
			return false, failed("device_limit: %d/%d%s", limit.Used, limit.Limit, s.limitDevices(ctx, u.ID))
		}
		return false, s.internal("issue TrustTunnel key", err)
	}
	s.notify.StateChanged()
	s.audit(ctx, by, "trusttunnel_key_issue", map[string]any{"user": u.ID, "device": deviceID})
	return true, nil
}

func (s *Service) revokeTrustTunnelKey(ctx context.Context, by, userID string) error {
	had, err := s.st.Access().RevokeTrustTunnelKey(ctx, userID, s.now())
	if err != nil {
		return s.internal("revoke TrustTunnel key", err)
	}
	if had {
		s.notify.StateChanged()
		s.audit(ctx, by, "trusttunnel_key_revoke", map[string]any{"user": userID})
	}
	return nil
}

// rollbackTrustTunnelKey removes a key issued immediately before an update when that update did not persist the app switch.
// Recheck the stored switch first: a concurrent update may already have made use of the same key.
func (s *Service) rollbackTrustTunnelKey(ctx context.Context, by, userID string, made bool) {
	if !made {
		return
	}
	current, err := s.st.Access().User(ctx, userID)
	if err == nil && current.AppTrustTunnel {
		return
	}
	_ = s.revokeTrustTunnelKey(ctx, by, userID)
}

func (s *Service) trustTunnelKeyLinks(ctx context.Context, u store.AccessUser) (TrustTunnelKeyLinks, error) {
	out := TrustTunnelKeyLinks{Links: []SubServer{}}
	dev, cred, err := s.st.Access().TrustTunnelKey(ctx, u.ID)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, s.internal("TrustTunnel key", err)
	}
	out.HasKey, out.DeviceID, out.IssuedAt = true, dev.ID, cred.CreatedAt
	if !u.AppTrustTunnel {
		return out, nil
	}
	if _, status := currentSubscriptionUser(u, s.now()); status != StatusActive {
		return out, nil
	}
	v, err := s.subView(ctx, u, false, SubOptions{NoPageData: true, IncludeTrustTunnel: true})
	if err != nil {
		return TrustTunnelKeyLinks{}, s.internal("TrustTunnel links", err)
	}
	out.Links = v.TrustTunnel
	return out, nil
}
