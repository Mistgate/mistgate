package access

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The Telegram key of a person (see store.AddTelegramKey): one device of kind 'key' with one MTProto credential, made when
// the owner turns Telegram on for the person or asks for it (IssueTelegramKey), replaced by RotateTelegramKey and revoked by
// RevokeTelegramKey, by turning Telegram off and by removing the device. The subscription fetch of an app never makes it and
// no link device holds one: only this credential goes to the nodes. It is listed among the person's devices but does not count
// against their device limit.

// telegramKeyLabel is the label (device.model) of the key device.
const telegramKeyLabel = "Telegram proxy"

func (s *Service) telegramPlugin() (protocols.Protocol, error) {
	p, ok := s.reg.Get(store.TelegramProtocol)
	if !ok {
		return nil, failed("the Telegram proxy is not available on this panel")
	}
	return p, nil
}

// newTelegramCred issues the credential of a key: a new secret, sealed by the vault (AAD = the credential's id).
func (s *Service) newTelegramCred(userID, deviceID string) (store.AccessCred, error) {
	p, err := s.telegramPlugin()
	if err != nil {
		return store.AccessCred{}, err
	}
	iss, err := p.IssueCredential(protocols.IssueInput{UserID: userID, DeviceID: deviceID})
	if err != nil {
		return store.AccessCred{}, s.internal("issue telegram key", err)
	}
	id := store.NewID("crd_")
	return store.AccessCred{ID: id, Protocol: p.ID(), SecretEnc: s.vault.Seal([]byte(iss.Secret), id), DataJSON: string(iss.NodeData)}, nil
}

func telegramKeyDevice(userID, deviceID string) store.AccessDevice {
	return store.AccessDevice{ID: deviceID, UserID: userID, Platform: "other", Model: telegramKeyLabel, Client: "telegram"}
}

// issueTelegramKey makes the key of u when u has none. made says whether it did. The device limit does not apply.
func (s *Service) issueTelegramKey(ctx context.Context, by string, u store.AccessUser) (made bool, err error) {
	devID := store.NewID("dev_")
	cred, err := s.newTelegramCred(u.ID, devID)
	if err != nil {
		return false, err
	}
	err = s.st.Access().AddTelegramKey(ctx, store.TelegramKeyAdd{Device: telegramKeyDevice(u.ID, devID), Cred: cred}, s.now())
	switch {
	case errors.Is(err, store.ErrAccessExists):
		return false, nil
	case errors.Is(err, store.ErrNotFound):
		return false, notFound("user")
	case err != nil:
		return false, s.internal("issue telegram key", err)
	}
	s.notify.StateChanged()
	s.audit(ctx, by, "telegram_key_issue", map[string]any{"user": u.ID, "device": devID})
	return true, nil
}

// telegramProxy is what the person's Telegram block shows: the key and the links. It writes nothing.
func (s *Service) telegramProxy(ctx context.Context, u store.AccessUser) (*adminv1.GetTelegramLinksResponse, error) {
	resp := &adminv1.GetTelegramLinksResponse{}
	dev, cred, err := s.st.Access().TelegramKey(ctx, u.ID)
	if errors.Is(err, store.ErrNotFound) {
		return resp, nil
	} else if err != nil {
		return nil, s.internal("telegram key", err)
	}
	resp.HasKey, resp.KeyDeviceId, resp.KeyIssuedUnix = true, dev.ID, cred.CreatedAt.Unix()
	if !u.AppTelegram {
		return resp, nil
	}
	v, err := s.subView(ctx, u, false, SubOptions{NoPageData: true})
	if err != nil {
		return nil, s.internal("telegram links", err)
	}
	for _, srv := range v.Telegram {
		if srv.URI == "" {
			continue
		}
		link := &adminv1.TelegramProxyLink{
			NodeId: srv.NodeID, NodeName: srv.Node, ProfileId: srv.ProfileID, ProfileName: srv.Profile, Url: srv.URI,
			Ipv6: srv.Mark == MarkIPv6,
		}
		link.Via, link.Relay = strings.CutPrefix(srv.Mark, MarkVia)
		link.Via = strings.TrimSpace(link.Via)
		resp.Links = append(resp.Links, link)
	}
	return resp, nil
}

func (s *Service) userForTelegram(ctx context.Context, id string) (store.AccessUser, error) {
	if id == "" {
		return store.AccessUser{}, invalid("user is required")
	}
	u, err := s.st.Access().User(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.AccessUser{}, notFound("user")
	} else if err != nil {
		return store.AccessUser{}, s.internal("get user", err)
	}
	return u, nil
}

// GetTelegramLinks returns the Telegram proxy of one person. It is a read: no device, no key and no fetch is recorded.
func (s *Service) GetTelegramLinks(ctx context.Context, req *connect.Request[adminv1.GetTelegramLinksRequest]) (*connect.Response[adminv1.GetTelegramLinksResponse], error) {
	u, err := s.userForTelegram(ctx, req.Msg.UserId)
	if err != nil {
		return nil, err
	}
	resp, err := s.telegramProxy(ctx, u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) IssueTelegramKey(ctx context.Context, req *connect.Request[adminv1.IssueTelegramKeyRequest]) (*connect.Response[adminv1.IssueTelegramKeyResponse], error) {
	u, err := s.userForTelegram(ctx, req.Msg.UserId)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, failed("user_disabled")
	}
	if !u.AppTelegram {
		return nil, failed("app_disabled")
	}
	if _, err := s.issueTelegramKey(ctx, actor(ctx), u); err != nil {
		return nil, err
	}
	proxy, err := s.telegramProxy(ctx, u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.IssueTelegramKeyResponse{Proxy: proxy}), nil
}

func (s *Service) RotateTelegramKey(ctx context.Context, req *connect.Request[adminv1.RotateTelegramKeyRequest]) (*connect.Response[adminv1.RotateTelegramKeyResponse], error) {
	u, err := s.userForTelegram(ctx, req.Msg.UserId)
	if err != nil {
		return nil, err
	}
	cred, err := s.newTelegramCred(u.ID, "")
	if err != nil {
		return nil, err
	}
	devID, err := s.st.Access().RotateTelegramKey(ctx, u.ID, cred, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("key")
	case err != nil:
		return nil, s.internal("rotate telegram key", err)
	}
	s.notify.StateChanged()
	s.audit(ctx, actor(ctx), "telegram_key_rotate", map[string]any{"user": u.ID, "device": devID})
	proxy, err := s.telegramProxy(ctx, u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RotateTelegramKeyResponse{Proxy: proxy}), nil
}

func (s *Service) RevokeTelegramKey(ctx context.Context, req *connect.Request[adminv1.RevokeTelegramKeyRequest]) (*connect.Response[adminv1.RevokeTelegramKeyResponse], error) {
	u, err := s.userForTelegram(ctx, req.Msg.UserId)
	if err != nil {
		return nil, err
	}
	if err := s.revokeTelegramKey(ctx, actor(ctx), u.ID); err != nil {
		return nil, err
	}
	proxy, err := s.telegramProxy(ctx, u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RevokeTelegramKeyResponse{Proxy: proxy}), nil
}

// revokeTelegramKey revokes the key of a user when there is one; the nodes drop its secret with the next desired state.
func (s *Service) revokeTelegramKey(ctx context.Context, by, userID string) error {
	had, err := s.st.Access().RevokeTelegramKey(ctx, userID, s.now())
	if err != nil {
		return s.internal("revoke telegram key", err)
	}
	if had {
		s.notify.StateChanged()
		s.audit(ctx, by, "telegram_key_revoke", map[string]any{"user": userID})
	}
	return nil
}

// telegramKeyForNewUser is the key device and credential CreateUser stores with a user who has Telegram on.
func (s *Service) telegramKeyForNewUser(userID string, now time.Time) (store.AccessDevice, []store.AccessCred, error) {
	devID := store.NewID("dev_")
	cred, err := s.newTelegramCred(userID, devID)
	if err != nil {
		return store.AccessDevice{}, nil, err
	}
	dev := telegramKeyDevice(userID, devID)
	dev.Key, dev.CreatedAt = true, now
	cred.DeviceID, cred.UserID, cred.CreatedAt = devID, userID, now
	return dev, []store.AccessCred{cred}, nil
}
