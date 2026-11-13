package access

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The client API enrols a device by its subscription link and gives it a key of its own (client-api.md). These are the calls the
// public handler (subs) makes; the hashes are the only form in which a key and the link reach the store.

// ClientDeviceByKey finds the link device a device-key hash belongs to. store.ErrNotFound: nobody's key. A key of a removed or
// blocked device comes back with store.ErrDeviceRevoked or store.ErrAccessBlocked and no device, only its owner.
func (s *Service) ClientDeviceByKey(ctx context.Context, keyHash []byte) (store.AccessDevice, store.LinkDeviceKeyOwner, error) {
	return s.st.Access().LinkDeviceByKeyHash(ctx, keyHash)
}

// ClientSubscription builds the subscription of the link device dev, authorised by its key, in the Mistgate format: structured
// protocol settings, not links. Unlike the basic link fetch it may issue missing credentials to this exact device (the store
// checks the key again in the write batch); the basic fetch refuses a device that holds a key, and still does. caps are the
// X-Mistgate-Caps entries (Gecko and the AmneziaWG versions are handed out only to a client that announced them). The owner's switch
// that takes AmneziaWG off the link apps does not apply to this app. Errors are the store's: ErrNotFound and ErrAccessExists when the
// device was removed, blocked or re-keyed meanwhile.
func (s *Service) ClientSubscription(ctx context.Context, dev store.AccessDevice, hint DeviceHint, caps []string) (SubView, error) {
	u, err := s.st.Access().User(ctx, dev.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return SubView{}, ErrUnknownToken
	}
	if err != nil {
		return SubView{}, err
	}
	off := u.AppMistgateOff
	if off {
		// The person's Mistgate app is switched off, as an app of theirs can be: with no app allowed nothing is usable, so the device
		// is served no server and nothing is issued to it. What it holds is kept (the nodes stop taking it, see store.DesiredCreds)
		// and works again when the switch is on. The status and the plan are the person's own and are still told.
		u.AppHapp, u.AppAmnezia, u.AppTelegram = false, false, false
	}
	v, err := s.subView(ctx, u, true, SubOptions{Format: plugin.FormatMistgate, Device: hint, EnrolledDevice: &dev, Capabilities: caps,
		NoPageData: true, IncludeEffectiveDNS: true})
	v.AppMistgateOff = off
	return v, err
}

// ClientLeaveDevice removes the device the key belongs to: the same operation as the person removing it on the page (its
// credentials leave the nodes with the next desired state, its AWG peers are quarantined), tied to the key that authorised the
// call. ErrNotFound when the key stopped being the device's meanwhile. The audit row has the user and the device, no address.
func (s *Service) ClientLeaveDevice(ctx context.Context, dev store.AccessDevice) error {
	if err := s.st.Access().RevokeEnrolledLinkDevice(ctx, dev, s.now()); err != nil {
		return err
	}
	s.notify.StateChanged()
	s.auditClientEnrolment(ctx, dev.UserID, dev.ID, "client_device_left")
	return nil
}

// EnrolLinkDevice makes the device of hint (its canonical HWID, platform, model and agent) hold credentials and the key whose
// hash is keyHash, in the store's one guarded batch: a new device (within the user's limit), or the user's own keyless device
// of that HWID, whose credentials are replaced. token is the subscription link's token, of which only the hash is kept (the
// device follows a rotated link by it). The errors are the store's: ErrAccessExists (the HWID is enrolled already),
// ErrAccessBlocked, *store.AccessLimitError, ErrNoLinkCredentials (none of the person's apps takes a link device, or their Mistgate
// app is switched off), ErrNotFound (the user is gone).
func (s *Service) EnrolLinkDevice(ctx context.Context, userID, token string, hint DeviceHint, keyHash []byte) (store.LinkDeviceResult, error) {
	u, err := s.st.Access().User(ctx, userID)
	if err != nil {
		return store.LinkDeviceResult{}, err
	}
	if u.AppMistgateOff { // read here, not from a cached view: a switch just turned off closes the door at once
		return store.LinkDeviceResult{}, store.ErrNoLinkCredentials
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
		s.limitReached(u.ID, limit.Used, u.DeviceLimit, limit.Mode, true, limit.WasIdle)
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
