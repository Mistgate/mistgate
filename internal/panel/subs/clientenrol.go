package subs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// ClientEnrol is what POST <link>/client/enrol calls; *access.Service implements it. A Source that does not leaves the endpoint
// off (the decoy answers a valid link, as for any path the panel does not have).
type ClientEnrol interface {
	ClientDeviceByKey(ctx context.Context, keyHash []byte) (store.AccessDevice, store.LinkDeviceKeyOwner, error)
	EnrolLinkDevice(ctx context.Context, userID, token string, hint access.DeviceHint, keyHash []byte) (store.LinkDeviceResult, error)
	ReenrolLinkDevice(ctx context.Context, userID, deviceID, token string, expectedKeyHash, keyHash []byte) (bool, error)
}

var _ ClientEnrol = (*access.Service)(nil)

const (
	// enrolSyncEvery is sync_every_s of the answer: how often the client asks the panel for its subscription.
	enrolSyncEvery = 3600
	// enrolPerHour is how many enrolments that make credentials (a new device, or the first key of a basic one) one link may make
	// an hour (Config.MaxEnrolsPerHour). A person has a handful of devices, so five is generous. It is a tighter budget than the
	// self-service writes (MaxWritesPerHour, taken too) because each such enrolment makes credentials the nodes then carry; a
	// re-enrolment and a repeat of a lost answer make none and are not counted.
	enrolPerHour = 5
)

func (h *handler) enrolWindow() securitylimit.Window {
	return securitylimit.Window{Name: "subscription-client-enrol", Limit: h.cfg.MaxEnrolsPerHour, Span: time.Hour}
}

// enrolRequest is the body of POST <link>/client/enrol, every field a string; an unknown field is refused. DeviceKey is the key
// the client made for itself: 32 random bytes in base64url without padding. The panel keeps its SHA-256 only.
type enrolRequest struct {
	HWID      string `json:"hwid"`
	Platform  string `json:"platform"`
	Model     string `json:"model"`
	OSVersion string `json:"os_version"`
	DeviceKey string `json:"device_key"`
}

// enrolAnswer is the 200 of enrol. base holds no token. The key is not in it: the client has it.
type enrolAnswer struct {
	DeviceID   string `json:"device_id"`
	Base       string `json:"base"`
	SyncEveryS int    `json:"sync_every_s"`
}

// serveEnrol takes POST <prefix>/<token>/client/enrol. Order, as in serveProbe: the token (unknown = the decoy and a miss; the
// page password does not apply), a Source that can enrol, the owner's switch (403 client_api_off), the cross-origin check, the
// panel's public address (503 not_configured: base must not come from a Host header), the write budget of the link (429 +
// Retry-After), the content type, the size and the strict JSON of the body, then the work:
//   - the key of the body is already the key of this user's live device of this HWID: the answer to a request whose answer was
//     lost, 200 with the same device and nothing written, nothing counted;
//   - the Bearer key is the key of this user's live device of this HWID: re-enrolment, the device takes the new key and keeps its
//     credentials (no budget: no credentials are made);
//   - otherwise the device is made, or the keyless device of this HWID is enrolled, within the budget of enrols of the link: a unit
//     is kept only by an enrol that made or took over a device, a refusal gives it back (every request spends the write budget).
//
// Nothing of the body, the keys or the answer is logged, nor is the address.
func (h *handler) serveEnrol(w http.ResponseWriter, r *http.Request, token, client string, now time.Time) {
	ctx := r.Context()
	if !h.confirmCachedToken(ctx, w, r, token, client) {
		return
	}
	if h.enrol == nil {
		h.decoy.ServeHTTP(w, r)
		return
	}
	if h.cfg.Settings == nil || !h.cfg.Settings.ClientAPIEnabled(ctx) {
		jsonError(w, http.StatusForbidden, "client_api_off", "")
		return
	}
	if err := h.cop.Check(r); err != nil {
		jsonError(w, http.StatusForbidden, "cross_origin", "")
		return
	}
	if h.cfg.BaseURL == "" {
		jsonError(w, http.StatusServiceUnavailable, "not_configured", "")
		return
	}
	if retry := h.writeAdmit(ctx, token, client); retry > 0 {
		tooManyWrites(w, retry)
		return
	}
	in, ok := readEnrolRequest(w, r)
	if !ok {
		return
	}
	hwid := access.CanonHWID(in.HWID)
	platform, model, err := access.CleanClientDeviceInput(in.Platform, in.Model)
	newKey, keyErr := base64.RawURLEncoding.Strict().DecodeString(in.DeviceKey)
	if hwid == "" || err != nil || keyErr != nil || len(newKey) != 32 {
		jsonError(w, http.StatusBadRequest, "bad_request", "")
		return
	}
	v, st, err := h.identify(ctx, token, false)
	if errors.Is(err, access.ErrUnknownToken) { // rotated a moment ago
		h.tokens.Delete(token)
		h.miss(ctx, client)
		h.decoy.ServeHTTP(w, r)
		return
	}
	if err != nil { // the access module logged the cause
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	hwidHash := store.HWIDHash(v.UserID, hwid)
	newHash := sha256.Sum256(newKey)
	answer := func(deviceID string) {
		writeJSON(w, http.StatusOK, enrolAnswer{DeviceID: deviceID, Base: h.clientBase(r), SyncEveryS: enrolSyncEvery})
	}
	// holder is the live device of this user and HWID that holds the key whose hash is given; nil when there is none (an unknown
	// key, the key of a removed or blocked device, of another person's device or of another HWID of this person).
	holder := func(hash []byte) (*store.AccessDevice, error) {
		dev, _, err := h.enrol.ClientDeviceByKey(ctx, hash)
		switch {
		case err == nil && dev.UserID == v.UserID && bytes.Equal(dev.HWIDHash, hwidHash):
			return &dev, nil
		case err == nil, errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrDeviceRevoked), errors.Is(err, store.ErrAccessBlocked):
			return nil, nil
		}
		return nil, err
	}

	// The answer to this very request was lost on its way: the key is in place, so the device is the answer.
	if same, err := holder(newHash[:]); err != nil {
		h.enrolFailed(w, err)
		return
	} else if same != nil {
		answer(same.ID)
		return
	}

	// The old key, when it is the key of this user's live device of this HWID, makes this a re-enrolment; the key of one of this
	// user's removed or blocked devices says so; any other key (wrong, foreign, malformed) is no key at all.
	var current *store.AccessDevice
	if oldKey, present := bearerKey(r); present {
		oldHash := sha256.Sum256(oldKey)
		dev, owner, err := h.enrol.ClientDeviceByKey(ctx, oldHash[:])
		switch {
		case err == nil && dev.UserID == v.UserID && bytes.Equal(dev.HWIDHash, hwidHash):
			current = &dev
		case errors.Is(err, store.ErrDeviceRevoked) && owner.UserID == v.UserID:
			jsonError(w, http.StatusUnauthorized, "device_removed", "")
			return
		case errors.Is(err, store.ErrAccessBlocked) && owner.UserID == v.UserID:
			jsonError(w, http.StatusUnauthorized, "device_blocked", "")
			return
		case err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrDeviceRevoked) && !errors.Is(err, store.ErrAccessBlocked):
			h.enrolFailed(w, err)
			return
		}
	}

	var deviceID string
	if current != nil {
		changed, err := h.enrol.ReenrolLinkDevice(ctx, v.UserID, current.ID, token, current.KeyHash, newHash[:])
		switch {
		case errors.Is(err, store.ErrAccessExists): // the new key is another device's
			jsonError(w, http.StatusConflict, "device_exists", "")
			return
		case err != nil:
			h.enrolFailed(w, err)
			return
		case !changed:
			// The old key stopped being the device's between the lookup and the swap: a twin of this request got there first
			// (then the new key is in place and this is its answer), or the key was rotated by another, or the device removed.
			if same, err := holder(newHash[:]); err == nil && same != nil {
				answer(same.ID)
				return
			}
			h.refuseStaleKey(ctx, w, current.KeyHash)
			return
		}
		deviceID = current.ID
	} else {
		if v.Status != access.StatusActive {
			jsonError(w, http.StatusConflict, "user_inactive", v.Status)
			return
		}
		// Only the path that makes credentials is counted, and only when it makes them: the unit is reserved before the write (two
		// parallel enrols cannot both pass the last one) and given back when nothing was made (a refusal, or a twin's answer).
		refund := func() {}
		if h.cfg.MaxEnrolsPerHour > 0 {
			d, err := h.cfg.Limiter.RecordN(ctx, h.enrolWindow(), "t:"+token, 1)
			if err != nil {
				tooManyWrites(w, time.Hour) // the shared limiter did not answer: "later", not a free pass
				return
			} else if !d.Allowed {
				tooManyWrites(w, d.RetryAfter)
				return
			}
			refund = func() { // a client that hung up must not keep the unit
				if err := h.cfg.Limiter.UndoN(context.WithoutCancel(ctx), h.enrolWindow(), "t:"+token, 1, d.WindowStart); err != nil {
					h.cfg.Log.Warn("refund client API enrol budget", "code", "client_api_enrol_refund_failed", "err", err)
				}
			}
		}
		hint := access.DeviceHint{HWID: hwid, OS: platform, OSVersion: in.OSVersion, Model: model, Agent: r.UserAgent()}
		if hint.OSVersion == "" {
			hint.OSVersion = firstHeader(r, "X-Ver-Os")
		}
		res, err := h.enrol.EnrolLinkDevice(ctx, v.UserID, token, hint, newHash[:])
		if err != nil {
			refund()
		}
		var limit *store.AccessLimitError
		switch {
		case err == nil:
			deviceID = res.Device.ID
		case errors.Is(err, store.ErrAccessExists):
			// A twin of this request enrolled this HWID with this key a moment ago: this is its answer. Any other key: the HWID is taken.
			if same, lerr := holder(newHash[:]); lerr == nil && same != nil {
				answer(same.ID)
				return
			}
			jsonError(w, http.StatusConflict, "device_exists", "")
			return
		case errors.Is(err, store.ErrAccessBlocked):
			jsonError(w, http.StatusForbidden, "device_blocked", "")
			return
		case errors.As(err, &limit):
			jsonError(w, http.StatusConflict, "device_limit", "")
			return
		case errors.Is(err, store.ErrNoLinkCredentials): // none of the person's apps takes a link device
			jsonError(w, http.StatusConflict, "app_disabled", "")
			return
		case errors.Is(err, store.ErrNotFound): // the person was deleted a moment ago
			h.tokens.Delete(token)
			h.miss(ctx, client)
			h.decoy.ServeHTTP(w, r)
			return
		default:
			h.enrolFailed(w, err)
			return
		}
	}
	st.drop() // the cached views still hold the credentials this HWID was served before
	answer(deviceID)
}

// refuseStaleKey answers a re-enrolment whose swap changed nothing: the key it showed was rotated or the device removed meanwhile.
func (h *handler) refuseStaleKey(ctx context.Context, w http.ResponseWriter, oldHash []byte) {
	_, _, err := h.enrol.ClientDeviceByKey(ctx, oldHash)
	switch {
	case errors.Is(err, store.ErrDeviceRevoked):
		jsonError(w, http.StatusUnauthorized, "device_removed", "")
	case errors.Is(err, store.ErrAccessBlocked):
		jsonError(w, http.StatusUnauthorized, "device_blocked", "")
	default:
		jsonError(w, http.StatusConflict, "device_exists", "")
	}
}

// enrolFailed answers an error nothing above expected. The cause is logged without the request; the store's errors carry no key.
func (h *handler) enrolFailed(w http.ResponseWriter, err error) {
	h.cfg.Log.Error("client API enrol failed", "code", "client_api_enrol_failed", "err", err)
	jsonError(w, http.StatusInternalServerError, "internal", "")
}

// readEnrolRequest reads the body: a JSON content type, at most maxBody bytes, one object whose fields are all known.
func readEnrolRequest(w http.ResponseWriter, r *http.Request) (enrolRequest, bool) {
	var in enrolRequest
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "")
		return in, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		jsonError(w, http.StatusRequestEntityTooLarge, "too_large", "")
		return in, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		jsonError(w, http.StatusBadRequest, "bad_request", "")
		return in, false
	}
	return in, true
}

// bearerKey is the device key of an "Authorization: Bearer <key>" header: 32 bytes in base64url without padding. present is false
// for no such header and for one that is not a key (which is then no key at all).
func bearerKey(r *http.Request) (key []byte, present bool) {
	auth := r.Header.Get("Authorization")
	if len(auth) < len("Bearer ")+1 || !strings.EqualFold(auth[:len("Bearer ")], "Bearer ") {
		return nil, false
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(auth[len("Bearer "):]))
	if err != nil || len(key) != 32 {
		return nil, false
	}
	return key, true
}

// clientBase is the absolute address of the client API: the subscription prefix and /client, where the configured public address
// is used when there is one (the Host of the request otherwise). It holds no token.
func (c *common) clientBase(r *http.Request) string {
	return strings.TrimRight(c.link(r, ""), "/") + "/client"
}
