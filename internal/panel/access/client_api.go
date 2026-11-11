package access

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The client API enrols a device by its subscription link and gives it a key of its own (client-api.md). These are the calls the
// public handler (subs) makes; the hashes are the only form in which a key and the link reach the store.

// ClientDeviceByKey finds the link device a device-key hash belongs to. store.ErrNotFound: nobody's key. A key of a removed or
// blocked device comes back with store.ErrDeviceRevoked or store.ErrAccessBlocked and no device, only its owner.
func (s *Service) ClientDeviceByKey(ctx context.Context, keyHash []byte) (store.AccessDevice, store.LinkDeviceKeyOwner, error) {
	return s.st.Access().LinkDeviceByKeyHash(ctx, keyHash)
}

// EnrolLinkDevice makes the device of hint (its canonical HWID, platform, model and agent) hold credentials and the key whose
// hash is keyHash, in the store's one guarded batch: a new device (within the user's limit), or the user's own keyless device
// of that HWID, whose credentials are replaced. token is the subscription link's token, of which only the hash is kept (the
// device follows a rotated link by it). The errors are the store's: ErrAccessExists (the HWID is enrolled already),
// ErrAccessBlocked, *store.AccessLimitError, ErrNoLinkCredentials, ErrNotFound (the user is gone).
func (s *Service) EnrolLinkDevice(ctx context.Context, userID, token string, hint DeviceHint, keyHash []byte) (store.LinkDeviceResult, error) {
	u, err := s.st.Access().User(ctx, userID)
	if err != nil {
		return store.LinkDeviceResult{}, err
	}
	canon := CanonHWID(hint.HWID)
	if canon == "" {
		return store.LinkDeviceResult{}, errors.New("access: invalid enrol HWID")
	}
	meta := hint.meta()
	dev := store.AccessDevice{
		UserID: userID, HWIDHash: store.HWIDHash(userID, canon), Platform: meta.Platform, Model: meta.Model,
		OSVersion: meta.OSVersion, Client: meta.Client, KeyHash: keyHash, EnrolTokenHash: hashToken(token),
	}
	result, err := s.st.Access().EnrolLinkDevice(ctx, dev, u.DeviceLimit, s.now(), func(deviceID string, _ map[string]bool) ([]store.AccessCred, error) {
		return s.newCreds(u.ID, deviceID, u.AppHapp, u.AppAmnezia, u.AppTelegram, map[string]bool{})
	})
	var limit *store.AccessLimitError
	if errors.As(err, &limit) {
		s.limitReached(u.ID, limit.Used, u.DeviceLimit, limit.Mode, true)
	}
	if err != nil {
		return store.LinkDeviceResult{}, err
	}
	s.placedAt(u, result.Used, result.Mode, result) // the sighting of a person over the limit, as a basic fetch reports it
	s.notify.StateChanged()                         // the nodes learn the credentials of the device (and forget those it had)
	s.auditClientEnrolment(ctx, userID, result.Device.ID, "client_device_enrolled")
	return result, nil
}

// ReenrolLinkDevice gives a live device of the user a new key, by compare-and-swap on the old one, and points it at the current
// link. Its credentials stay. false: the old key is not the device's any more (it was rotated, or the device removed, meanwhile).
func (s *Service) ReenrolLinkDevice(ctx context.Context, userID, deviceID, token string, expectedKeyHash, keyHash []byte) (bool, error) {
	changed, err := s.st.Access().SetLinkDeviceKeyHash(ctx, userID, deviceID, expectedKeyHash, keyHash, hashToken(token))
	if err != nil || !changed {
		return false, err
	}
	s.auditClientEnrolment(ctx, userID, deviceID, "client_device_reenrolled")
	return true, nil
}

// auditClientEnrolment writes the audit row of an enrolment: the user acting for themselves and the device id. No address
// (unlike s.audit), no HWID, no key.
func (s *Service) auditClientEnrolment(ctx context.Context, userID, deviceID, action string) {
	params, _ := json.Marshal(map[string]string{"user": userID, "device": deviceID})
	entry := store.AuditEntry{Actor: "user:" + userID, Action: action, Params: string(params), Result: "ok"}
	if err := s.st.Audit(ctx, s.now(), entry); err != nil {
		s.log.Warn("client API enrol audit write failed", "code", "client_api_enrol_audit_failed", "err", err)
	}
}
