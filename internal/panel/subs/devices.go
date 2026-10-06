package subs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Self-service of AmneziaWG devices on the public page. All under the subscription link, all
// POST with JSON in and out, all only for a valid token (an unknown one gets the decoy and counts as a miss, like a
// fetch):
//
//	POST <link>/devices                    {"profile_id","platform","label"}  add a device, answers with its configs
//	POST <link>/devices/<id>/configs                                           the configs again (marks them received)
//	POST <link>/devices/<id>/rotate                                            a new key on the same address
//	POST <link>/devices/<id>/revoke                                            remove the device
//	POST <link>/devices/<id>/rename        {"label"}                           rename it
//
// Answers carry `Cache-Control: no-store`. The keys are never embedded in the page: they leave only through these calls,
// on demand. Every call goes through http.CrossOriginProtection (a browser page of another origin cannot post), counts
// against a per-token hourly budget of its own, and is refused when the admin switched self-service off.

// Devices is what the endpoints call; *access.Service implements it. A Source that does not leaves the endpoints off.
type Devices interface {
	AddAWGDevice(ctx context.Context, by, userID, profileID, platform, label string) (store.AccessAWGDevice, []access.DeviceConfig, error)
	DeviceConfigs(ctx context.Context, by, owner, deviceID string) (store.AccessAWGDevice, []access.DeviceConfig, error)
	RotateDevice(ctx context.Context, by, owner, deviceID string) (store.AccessAWGDevice, []access.DeviceConfig, error)
	RelabelDevice(ctx context.Context, owner, deviceID, label string) (store.AccessAWGDevice, error)
	RevokeOwnDevice(ctx context.Context, by, owner, deviceID string) error
}

const (
	maxBody = 4 << 10 // a request is a few short strings

	// awgOnlineWindow is how recent a handshake must be for the page to call a device online.
	awgOnlineWindow = 3 * time.Minute
)

// ---- JSON shapes (the page's `amnezia` object and the answers of the endpoints) ----

type pageMinClient struct {
	Client string `json:"client"` // "amnezia" | "mihomo"
	App    string `json:"app"`
	Min    string `json:"min"`
}

type pageAWGDevice struct {
	ID                string `json:"id"`
	Platform          string `json:"platform"`
	Label             string `json:"label"`
	ProfileID         string `json:"profile_id"`
	ProfileName       string `json:"profile_name"`
	Version           string `json:"version"` // "3.1" | "2.0"
	Address           string `json:"address"` // without masks: "10.66.4.5, fd66:66:0:1::5"
	LastHandshakeUnix int64  `json:"last_handshake_unix"`
	Online            bool   `json:"online"`
	Stale             bool   `json:"stale"` // the profile changed or the person picked another DNS: fetch the configs and re-import them
	// StaleReason says why: "profile" (the servers changed: a new key is needed) or "dns" (the DNS of a server changed: the same
	// key, fetched again, carries it); "" when the device is not stale.
	StaleReason string          `json:"stale_reason"`
	MinClients  []pageMinClient `json:"min_clients"`
}

// staleOf is whether a device needs its configs again and why. A changed profile outranks a changed DNS: it needs more.
func staleOf(profileStale bool, dnsStale []string) (stale bool, reason string) {
	switch {
	case profileStale:
		return true, "profile"
	case len(dnsStale) > 0:
		return true, "dns"
	}
	return false, ""
}

type pageAWGProfile struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Egress    string   `json:"egress"`    // "direct" | "warp": the page offers a WARP one as the spare exit
	Countries []string `json:"countries"` // country codes of its nodes: the page names the choice by them
}

// pageAmnezia is the Amnezia section of the page data; no key material in it.
type pageAmnezia struct {
	Devices     []pageAWGDevice  `json:"devices"`
	Profiles    []pageAWGProfile `json:"profiles"` // what "add a device" can pick from
	CanAdd      bool             `json:"can_add"`
	SelfService bool             `json:"self_service"` // false: keys come from the admin, the page only lists the devices
	Endpoints   string           `json:"endpoints"`    // base URL of the self-service calls ("" = none: the admin's preview), <link>/devices
}

func minClientsJSON(rs []protocols.ClientReq) []pageMinClient {
	out := make([]pageMinClient, 0, len(rs))
	for _, r := range rs {
		c := string(r.Client)
		if r.Client == plugin.ClientAmnezia {
			c = "amnezia"
		}
		out = append(out, pageMinClient{Client: c, App: r.App, Min: r.Min})
	}
	return out
}

// amneziaData is the section for the user's page: nil (JSON null) unless the user has the Amnezia app. selfService is the
// admin's switch; the preview (the admin's look at the page) shows it as it is but has no endpoints: it cannot write.
// A person whose subscription is not active still gets it when they have the app or keys: the keys are listed and can be
// renamed and removed (the calls allow it), while nothing can be added (can_add false, no profiles to add on).
func amneziaData(v access.SubView, link string, selfService, preview bool, now time.Time) *pageAmnezia {
	hasKeys := slices.ContainsFunc(v.Devices, func(d access.SubDevice) bool { return d.AWG != nil })
	if !v.AccessAmnezia && !(v.Status != access.StatusActive && (v.AppAmnezia || hasKeys)) {
		return nil
	}
	a := &pageAmnezia{Devices: []pageAWGDevice{}, Profiles: []pageAWGProfile{}, SelfService: selfService}
	for _, d := range v.Devices {
		if d.AWG == nil {
			continue
		}
		stale, reason := staleOf(d.AWG.Stale, d.AWG.DNSStale)
		a.Devices = append(a.Devices, pageAWGDevice{
			ID: d.ID, Platform: d.Platform, Label: d.Model, ProfileID: d.AWG.ProfileID, ProfileName: d.AWG.ProfileName,
			Version: d.AWG.Version, Address: d.AWG.Address, LastHandshakeUnix: unixOrZero(d.AWG.LastHandshake),
			Online: online(d.AWG.LastHandshake, now), Stale: stale, StaleReason: reason, MinClients: minClientsJSON(d.AWG.MinClients),
		})
	}
	for _, p := range v.AWGProfiles {
		a.Profiles = append(a.Profiles, pageAWGProfile{ID: p.ID, Name: p.Name, Version: p.Version, Egress: p.Egress, Countries: nonNil(p.Countries)})
	}
	if selfService && !preview && link != "" {
		a.Endpoints = link + "/devices"
	}
	a.CanAdd = a.Endpoints != "" && v.Status == access.StatusActive && len(a.Profiles) > 0 && (v.DeviceLimit <= 0 || v.DevicesUsed < v.DeviceLimit)
	return a
}

func online(last, now time.Time) bool { return !last.IsZero() && now.Sub(last) < awgOnlineWindow }

type configJSON struct {
	NodeID string `json:"node_id"`
	// Label is the public name of the server, the same as servers[].label ("Germany · Frankfurt", "Germany 2", "Server").
	Label       string          `json:"label"`
	CountryCode string          `json:"country_code"`
	Version     string          `json:"version"`
	Conf        string          `json:"conf"`    // the .conf text (AmneziaVPN and the AmneziaWG apps import it; one QR)
	VPNKey      string          `json:"vpn_key"` // vpn:// for AmneziaVPN
	Filename    string          `json:"filename"`
	Stale       bool            `json:"stale"`
	Warnings    []string        `json:"warnings"` // codes the page translates: amnezia_desktop_mtu, dns_fallback, dns_no_split
	MinClients  []pageMinClient `json:"min_clients"`
}

type deviceAnswer struct {
	Device  pageAWGDevice `json:"device"`
	Configs []configJSON  `json:"configs,omitempty"`
}

// deviceOf describes a device as the answers do; the configs, when there are any, supply what the device row lacks
// (the profile version and the minimum clients).
func deviceOf(d store.AccessAWGDevice, cfgs []access.DeviceConfig, now time.Time) pageAWGDevice {
	out := pageAWGDevice{
		ID: d.ID, Platform: d.Platform, Label: d.Model, ProfileID: d.ProfileID, ProfileName: d.ProfileName,
		Version: profileVersion(d.ProfileSettingsJSON), Address: addressOf(d.DataJSON),
		LastHandshakeUnix: unixOrZero(d.LastSeenAt), Online: online(d.LastSeenAt, now), MinClients: []pageMinClient{},
	}
	out.Stale, out.StaleReason = staleOf(d.Stale(), d.DNSStale)
	if len(cfgs) > 0 {
		out.Version = cfgs[0].AWGVersion
		out.MinClients = minClientsJSON(cfgs[0].MinClients)
	}
	return out
}

func configsOf(cfgs []access.DeviceConfig) []configJSON {
	out := make([]configJSON, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, configJSON{
			NodeID: c.NodeID, Label: c.Server, CountryCode: c.CountryCode, Version: c.AWGVersion, Conf: c.Conf, VPNKey: c.VPNKey,
			Filename: c.ConfFilename, Stale: c.Stale, Warnings: nonNil(c.Warnings), MinClients: minClientsJSON(c.MinClients),
		})
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func profileVersion(settings string) string {
	var v struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal([]byte(settings), &v)
	return v.Version
}

// addressOf lists the tunnel addresses of a device without masks.
func addressOf(dataJSON string) string {
	var nd awg.NodeData
	if json.Unmarshal([]byte(dataJSON), &nd) != nil {
		return ""
	}
	var out []string
	for _, a := range nd.AllowedIPs {
		if p, err := netip.ParsePrefix(a); err == nil {
			out = append(out, p.Addr().String())
		}
	}
	return strings.Join(out, ", ")
}

// ---- the endpoints ----

// serveDevices answers POST <prefix>/<token>/<sub> (sub starts with "devices"). Order: the token (unknown = decoy
// and a miss), the cross-origin check, the admin's switch, the user's status, the write budget, then the work.
func (h *handler) serveDevices(w http.ResponseWriter, r *http.Request, token, sub, client string, now time.Time) {
	ctx := r.Context()
	v, st, ok := h.enter(w, r, token, client, now, h.dev != nil, subsettings.SelfService, "self_service_disabled")
	if !ok {
		return
	}

	rest := strings.TrimPrefix(sub, "devices")
	var deviceID, action string
	if rest != "" {
		parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
		if len(parts) != 2 || !validDeviceID(parts[0]) {
			jsonError(w, http.StatusNotFound, "not_found", "")
			return
		}
		deviceID, action = parts[0], parts[1]
		switch action {
		case "configs", "rotate", "revoke", "rename":
		default:
			jsonError(w, http.StatusNotFound, "not_found", "")
			return
		}
	}
	// The keys leave only for a user who can use them; removing a device and renaming one never hurt.
	if !h.admitWrite(r.Context(), w, token, v, rest == "" || action == "configs" || action == "rotate", now) {
		return
	}

	by := "user:" + v.UserID
	var (
		dev  store.AccessAWGDevice
		cfgs []access.DeviceConfig
		err  error
	)
	switch {
	case rest == "":
		var in struct {
			ProfileID string `json:"profile_id"`
			Platform  string `json:"platform"`
			Label     string `json:"label"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		dev, cfgs, err = h.dev.AddAWGDevice(ctx, by, v.UserID, in.ProfileID, in.Platform, in.Label)
	case action == "configs":
		dev, cfgs, err = h.dev.DeviceConfigs(ctx, by, v.UserID, deviceID)
	case action == "rotate":
		dev, cfgs, err = h.dev.RotateDevice(ctx, by, v.UserID, deviceID)
	case action == "revoke":
		if err = h.dev.RevokeOwnDevice(ctx, by, v.UserID, deviceID); err == nil {
			st.drop() // the cached view still lists the device
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
	case action == "rename":
		var in struct {
			Label string `json:"label"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		dev, err = h.dev.RelabelDevice(ctx, v.UserID, deviceID, in.Label)
	}
	if err != nil {
		writeAccessError(w, err)
		return
	}
	st.drop()
	writeJSON(w, http.StatusOK, deviceAnswer{Device: deviceOf(dev, cfgs, now), Configs: configsOf(cfgs)})
}

// enter is the start every call under a link shares (the devices and the DNS pick), in this order: the page password (a
// locked page locks what it calls: answered before the database work, which only a cookie of this token earns; the
// answer is the same for a token that does not exist), the token (unknown = the decoy and a miss), the Source can do the
// call (else the decoy: the endpoint does not exist), the cross-origin check, the owner's switch (403 off). It answers
// and returns false when the call ends there. exists says whether the Source can do the call.
//
// A token the handler already knows (it was valid within this process's memory) gets the refusals its state can give
// before the view of the person is built, the way a fetch is answered 429 from the state: the cross-origin check, the
// owner's switch and an hourly budget of writes that is used up. The view is the expensive part, and a link that is over
// its budget must not cost one per request. An unknown token has no state and still gets the decoy first.
func (h *handler) enter(w http.ResponseWriter, r *http.Request, token, client string, now time.Time, exists bool,
	on func(*adminv1.SubscriptionSettings) bool, off string) (access.SubView, *tokenState, bool) {
	ctx := r.Context()
	set := h.settings(ctx)
	if exists && h.gated(set) && !h.unlocked(r, token) {
		jsonError(w, http.StatusUnauthorized, "locked", "")
		return access.SubView{}, nil, false
	}
	if _, ok := h.tokens.Get(token); ok && exists {
		switch {
		case h.cop.Check(r) != nil:
			if !h.confirmCachedToken(ctx, w, r, token, client, now) {
				return access.SubView{}, nil, false
			}
			jsonError(w, http.StatusForbidden, "cross_origin", "")
			return access.SubView{}, nil, false
		case !on(set):
			if !h.confirmCachedToken(ctx, w, r, token, client, now) {
				return access.SubView{}, nil, false
			}
			jsonError(w, http.StatusForbidden, off, "")
			return access.SubView{}, nil, false
		}
		if retry := h.writeWait(ctx, token, now); retry > 0 {
			if !h.confirmCachedToken(ctx, w, r, token, client, now) {
				return access.SubView{}, nil, false
			}
			tooManyWrites(w, retry)
			return access.SubView{}, nil, false
		}
	}
	v, st, err := h.identify(ctx, token, false)
	if errors.Is(err, access.ErrUnknownToken) {
		h.tokens.Delete(token)
		h.miss(ctx, client, now)
		h.decoy.ServeHTTP(w, r)
		return access.SubView{}, nil, false
	}
	if err != nil { // the access module logged the cause (never the token)
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return access.SubView{}, nil, false
	}
	if !exists {
		h.decoy.ServeHTTP(w, r)
		return access.SubView{}, nil, false
	}
	if err := h.cop.Check(r); err != nil {
		jsonError(w, http.StatusForbidden, "cross_origin", "")
		return access.SubView{}, nil, false
	}
	if !on(set) {
		jsonError(w, http.StatusForbidden, off, "")
		return access.SubView{}, nil, false
	}
	return v, st, true
}

// confirmCachedToken prevents a refused response from revealing a token that has since been rotated or deleted.
func (h *handler) confirmCachedToken(ctx context.Context, w http.ResponseWriter, r *http.Request, token, client string, now time.Time) bool {
	var err error
	if checker, ok := h.src.(TokenChecker); ok {
		err = checker.CheckSubscriptionToken(ctx, token)
	} else {
		_, err = h.fetch(ctx, token, plugin.FormatURIList, false, false)
	}
	if errors.Is(err, access.ErrUnknownToken) {
		h.tokens.Delete(token)
		h.miss(ctx, client, now)
		h.decoy.ServeHTTP(w, r)
		return false
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return false
	}
	return true
}

// admitWrite is the end of the shared start: the user's status (needActive: the call needs a user who can use the
// servers, 409 user_inactive otherwise) and the hourly budget of writes of the token (429), counted for the devices and
// the DNS together. It answers and returns false when the call ends there.
func (h *handler) admitWrite(ctx context.Context, w http.ResponseWriter, token string, v access.SubView, needActive bool, now time.Time) bool {
	if needActive && v.Status != access.StatusActive {
		jsonError(w, http.StatusConflict, "user_inactive", v.Status)
		return false
	}
	if retry := h.writeAdmit(ctx, token, now); retry > 0 {
		tooManyWrites(w, retry)
		return false
	}
	return true
}

func (h *handler) writeWait(ctx context.Context, token string, now time.Time) time.Duration {
	if h.cfg.MaxWritesPerHour < 0 {
		return 0
	}
	d, err := h.cfg.Limiter.CheckWindow(ctx, "subscription-writes", "t:"+token, now,
		h.cfg.MaxWritesPerHour, time.Hour, h.cfg.MaxKeys, true)
	if err != nil {
		return time.Hour
	}
	if !d.Allowed {
		return d.RetryAfter
	}
	return 0
}

func (h *handler) writeAdmit(ctx context.Context, token string, now time.Time) time.Duration {
	if h.cfg.MaxWritesPerHour < 0 {
		return 0
	}
	d, err := h.cfg.Limiter.RecordWindow(ctx, "subscription-writes", "t:"+token, now,
		h.cfg.MaxWritesPerHour, time.Hour, h.cfg.MaxKeys)
	if err != nil {
		return time.Hour
	}
	if !d.Allowed {
		return d.RetryAfter
	}
	return 0
}

func tooManyWrites(w http.ResponseWriter, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int((retry+time.Second-1)/time.Second)))
	jsonError(w, http.StatusTooManyRequests, "too_many_requests", "")
}

// identify resolves a token to the user's view for a self-service call. Always from the source, never from the cache a page
// fetch left, and it leaves none: these calls reveal keys and change devices, so a link that was just rotated or a user that
// was just disabled must stop working at once (the cache is only for the read-only page view). It does not count against
// the fetch budget. page: the view carries the page's own data (the DNS of each server), which the answer of a DNS pick
// needs; the checks of a call do without it.
func (h *handler) identify(ctx context.Context, token string, page bool) (access.SubView, *tokenState, error) {
	v, err := h.fetch(ctx, token, plugin.FormatURIList, false, page) // the page asks, not an app
	if err != nil {
		return access.SubView{}, nil, err
	}
	st := h.tokens.GetOrCreate(token, func() *tokenState { return &tokenState{} })
	st.mu.Lock()
	st.userName = v.UserName
	st.mu.Unlock()
	return v, st, nil
}

// drop forgets the cached views of a token: a device was added, changed or removed, and the next fetch must show it.
func (st *tokenState) drop() {
	st.mu.Lock()
	st.cached = nil
	st.mu.Unlock()
}

// validDeviceID: the ids the panel issues are "dev_" + base32; anything else cannot be one.
func validDeviceID(id string) bool {
	if len(id) < 5 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// readJSON decodes a small JSON body into dst. An empty body is {} (every field is optional); a body that is not
// JSON, has unknown fields or is too long is refused (it answers and returns false).
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, http.StatusRequestEntityTooLarge, "too_large", "")
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return true
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		jsonError(w, http.StatusBadRequest, "bad_request", "")
		return false
	}
	return true
}

// writeAccessError maps an error of the access module (Connect errors) to a status and a stable code: for
// FailedPrecondition the code is the text before the first ':' of the message ("device_limit: 5/5" -> device_limit).
func writeAccessError(w http.ResponseWriter, err error) {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	switch ce.Code() {
	case connect.CodeInvalidArgument:
		jsonError(w, http.StatusBadRequest, "invalid", ce.Message())
	case connect.CodeNotFound:
		jsonError(w, http.StatusNotFound, "not_found", "")
	case connect.CodeFailedPrecondition:
		jsonError(w, http.StatusConflict, errorCode(ce.Message()), ce.Message())
	case connect.CodeResourceExhausted:
		jsonError(w, http.StatusTooManyRequests, "too_many_requests", "")
	case connect.CodePermissionDenied, connect.CodeUnauthenticated:
		jsonError(w, http.StatusForbidden, "forbidden", "")
	default: // Internal and the rest: the cause was logged by the access module and stays there
		jsonError(w, http.StatusInternalServerError, "internal", "")
	}
}

// errorCode turns the leading words of a message into a code: lower case, letters, digits and underscores.
func errorCode(msg string) string {
	head, _, _ := strings.Cut(msg, ":")
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(head)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
	}
	if c := strings.Trim(b.String(), "_"); c != "" {
		return c
	}
	return "failed"
}

func jsonError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]string{"error": code}
	if message != "" {
		body["message"] = message
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":"internal"}`)
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/json; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	w.Write(b)
}
