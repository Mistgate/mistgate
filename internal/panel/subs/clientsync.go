package subs

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/clientprobe"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	// clientSyncPerHour is how many sync and leave requests one enrolled device may make an hour. The client asks about hourly
	// (sync_every_s), so thirty leaves room for a restart, a retry after an error and a few manual refreshes, and stays far
	// below what a loop with a bug would make; every request counts, a 304 too.
	clientSyncPerHour = 30
	clientSyncWindow  = time.Hour
)

// ClientAPI is what GET <base>/sync and POST <base>/leave call; *access.Service implements it. A Source that does not leaves the
// endpoints to the decoy.
type ClientAPI interface {
	ClientDeviceByKey(ctx context.Context, keyHash []byte) (store.AccessDevice, store.LinkDeviceKeyOwner, error)
	ClientSubscription(ctx context.Context, dev store.AccessDevice, hint access.DeviceHint, caps []string) (access.SubView, error)
	ClientLeaveDevice(ctx context.Context, dev store.AccessDevice) error
}

// A drift between the interface and the service would silently turn the endpoints into the decoy.
var _ ClientAPI = (*access.Service)(nil)

// clientAPIRoute recognizes the tokenless client endpoints (<prefix>/client/sync and /client/leave). They are routed before the
// subscription-token lockout so that a valid device key keeps working from a network whose unknown-token misses are locked out.
func clientAPIRoute(r *http.Request, prefix string) string {
	p := r.URL.Path
	if prefix != "" {
		p = strings.TrimPrefix(p, prefix)
	}
	switch p {
	case "/client/sync":
		return "sync"
	case "/client/leave":
		return "leave"
	}
	return ""
}

// serveClientAPI answers sync and leave. The device key is the only credential; the answers come in this order, and none of
// them is given before the key is known to be a device's: a missing, malformed or unknown key (and a wrong method) is the
// decoy and a miss, as an unknown subscription token is, and the decoy is exactly the one of any other path (no header of
// ours before it); a key that is a device's, live or not, is counted in the budget of the device, 429 beyond it; then the key of
// a removed or blocked device is 401 device_removed or device_blocked; a device enrolled under a link that is not the person's
// current one is 401 link_changed (leave excepted: the key proves the device, and it must be able to remove itself); a request
// from another origin is 403 cross_origin. The owner's client-API switch gates none of it: it closes only discovery and
// enrolment. A key of the right shape is always looked up, locked-out network or not: the lookup is an indexed read, and
// the network behind one exit address (a node, a CGNAT, WARP) must not switch sync off for everyone by missing. Nothing of the
// key, the answer or the address is logged.
func (h *handler) serveClientAPI(w http.ResponseWriter, r *http.Request, route, client string) {
	ctx := r.Context()
	locked := client != "" && h.blocked(ctx, client)
	key, ok := bearerKey(r)
	if !ok || h.client == nil || (route == "sync" && r.Method != http.MethodGet) || (route == "leave" && r.Method != http.MethodPost) {
		h.clientAPIMiss(w, r, client, locked) // no database call for what cannot be a key
		return
	}
	keyHash := sha256.Sum256(key)
	clear(key)
	dev, owner, err := h.client.ClientDeviceByKey(ctx, keyHash[:])
	refusal := ""
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.clientAPIMiss(w, r, client, locked)
		return
	case errors.Is(err, store.ErrDeviceRevoked):
		refusal = "device_removed"
	case errors.Is(err, store.ErrAccessBlocked):
		refusal = "device_blocked"
	case err != nil:
		h.clientAPIFailed(w, "client_api_key_lookup_failed", err)
		return
	}
	// The budget is the device's; a removed or blocked one has no device here, so its key (a hash of it) stands for it.
	budget := "k:" + hex.EncodeToString(keyHash[:16])
	if refusal == "" {
		budget = "d:" + dev.ID
	}
	if retry, admitted := h.clientSyncAdmit(ctx, budget); !admitted {
		tooManyWrites(w, retry)
		return
	}
	if refusal != "" {
		jsonError(w, http.StatusUnauthorized, refusal, "")
		return
	}
	if route == "sync" && linkMoved(dev, owner) {
		jsonError(w, http.StatusUnauthorized, "link_changed", "")
		return
	}
	if h.crossOrigin(r) {
		jsonError(w, http.StatusForbidden, "cross_origin", "")
		return
	}
	if route == "leave" {
		h.serveClientLeave(w, r, dev, keyHash[:], client, locked)
		return
	}
	h.serveClientSync(w, r, dev, keyHash[:], client, locked)
}

// linkMoved says the device was enrolled under a link that is no longer the person's current one.
func linkMoved(dev store.AccessDevice, owner store.LinkDeviceKeyOwner) bool {
	return len(dev.EnrolTokenHash) != len(owner.SubTokenHash) || subtle.ConstantTimeCompare(dev.EnrolTokenHash, owner.SubTokenHash) != 1
}

// crossOrigin applies to sync the rule enrol and leave live by: a browser page of another site is refused. The standard check
// lets a GET through (a page cannot read its answer), but a native client sends neither Origin nor Sec-Fetch-Site, so for the
// client API a GET is held to the rule of an unsafe method too.
func (h *handler) crossOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet {
		unsafe := r.Clone(r.Context())
		unsafe.Method = http.MethodPost
		r = unsafe
	}
	return h.cop.Check(r) != nil
}

func (h *handler) serveClientLeave(w http.ResponseWriter, r *http.Request, dev store.AccessDevice, keyHash []byte, client string, locked bool) {
	if err := h.client.ClientLeaveDevice(r.Context(), dev); err != nil {
		if errors.Is(err, store.ErrNotFound) { // the key stopped being the device's between the lookup and the write
			h.staleKey(w, r, "leave", keyHash, client, locked, err)
			return
		}
		h.clientAPIFailed(w, "client_api_leave_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func (h *handler) serveClientSync(w http.ResponseWriter, r *http.Request, dev store.AccessDevice, keyHash []byte, client string, locked bool) {
	ctx := r.Context()
	set := h.settings(ctx)
	hint := access.DeviceHint{OSVersion: firstHeader(r, "X-Ver-Os"), Agent: r.UserAgent()}
	view, err := h.client.ClientSubscription(ctx, dev, hint, clientCapabilities(r))
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrAccessExists) || errors.Is(err, access.ErrUnknownToken) {
		// Something the snapshot of the request counted on is gone: the device, its key, its link, a profile, the person.
		h.staleKey(w, r, "sync", keyHash, client, locked, err)
		return
	}
	if err != nil {
		h.clientAPIFailed(w, "client_api_sync_failed", err)
		return
	}
	in := clientSyncInput{DeviceID: dev.ID, View: view, Brand: h.brand(ctx), Settings: set, ProbeOn: h.probeOn(ctx)}
	if view.Status == access.StatusActive && !view.AppMistgateOff {
		in.DNS = h.linkPreset(ctx, view)
	}
	answer := buildClientSync(in)
	canonical, err := json.Marshal(answer) // without the caller's ASN: it describes the caller, not the configuration
	if err != nil {
		h.clientAPIFailed(w, "client_api_sync_render_failed", err)
		return
	}
	sum := sha256.Sum256(canonical)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	w.Header()["Cache-Control"] = headerCacheNoStore[:1:1]
	w.Header()["Content-Type"] = headerContentTypeJSON[:1:1]
	w.Header()["X-Content-Type-Options"] = headerNoSniff[:1:1]
	w.Header()["Referrer-Policy"] = headerNoReferrer[:1:1]
	if ifNoneMatch(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := canonical
	if asn := h.directASN(r); asn != 0 {
		answer.ASN = asn
		if body, err = json.Marshal(answer); err != nil {
			h.clientAPIFailed(w, "client_api_sync_render_failed", err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// directASN is the autonomous system of the address the request came from, for a client that says the call was made outside the
// tunnel (X-Mistgate-Direct: 1) and a panel that has a networks table (the VPS edition). It is looked up in process, answered and
// forgotten: never stored, never logged. 0 = unknown, and then the field is absent.
func (h *handler) directASN(r *http.Request) uint32 {
	if h.cfg.ClientASN == nil || strings.TrimSpace(r.Header.Get("X-Mistgate-Direct")) != "1" {
		return 0
	}
	ip := h.cfg.ClientIP(r)
	if !ip.IsValid() {
		return 0
	}
	return h.cfg.ClientASN(ip.Unmap())
}

// staleKey answers a request that failed on something the first lookup of its key did not show. The key is looked up again: a
// device removed or blocked by now gets its 401, a link rotated by now its link_changed, a key that is nobody's now is a miss.
// A key that is still a live device's is never the decoy and never a miss: the cause is elsewhere (a profile deleted or a
// device re-keyed between the snapshot and the write), the answer is 503 retry, and the client asks again after Retry-After.
func (h *handler) staleKey(w http.ResponseWriter, r *http.Request, route string, keyHash []byte, client string, locked bool, cause error) {
	dev, owner, err := h.client.ClientDeviceByKey(r.Context(), keyHash)
	switch {
	case errors.Is(err, store.ErrDeviceRevoked):
		jsonError(w, http.StatusUnauthorized, "device_removed", "")
	case errors.Is(err, store.ErrAccessBlocked):
		jsonError(w, http.StatusUnauthorized, "device_blocked", "")
	case errors.Is(err, store.ErrNotFound):
		h.clientAPIMiss(w, r, client, locked)
	case err != nil:
		h.clientAPIFailed(w, "client_api_key_lookup_failed", err)
	case route == "sync" && linkMoved(dev, owner):
		jsonError(w, http.StatusUnauthorized, "link_changed", "")
	default:
		h.cfg.Log.Warn("client API request asked to retry", "code", "client_api_retry", "route", route, "err", cause)
		w.Header().Set("Retry-After", clientRetryAfter)
		jsonError(w, http.StatusServiceUnavailable, "retry", "")
	}
}

// clientRetryAfter is the seconds a client waits before it repeats a request that was answered 503 retry.
const clientRetryAfter = "5"

// clientAPIMiss is the decoy for a stranger, counted as a miss the way an unknown token is (in the cold-check window when the
// network is locked out already).
func (h *handler) clientAPIMiss(w http.ResponseWriter, r *http.Request, client string, locked bool) {
	if locked {
		h.recordColdMiss(r.Context(), client)
	} else {
		h.miss(r.Context(), client)
	}
	h.decoy.ServeHTTP(w, r)
}

func (h *handler) clientAPIFailed(w http.ResponseWriter, code string, err error) {
	h.cfg.Log.Error("client API request failed", "code", code, "err", err) // the store's errors carry no key
	jsonError(w, http.StatusInternalServerError, "internal", "")
}

// clientSyncAdmit counts one request of the device against its hourly budget. A limiter that does not answer refuses ("later",
// not a free pass).
func (h *handler) clientSyncAdmit(ctx context.Context, deviceID string) (time.Duration, bool) {
	d, err := h.cfg.Limiter.Record(ctx, securitylimit.Window{Name: "subscription-client-sync", Limit: clientSyncPerHour, Span: clientSyncWindow}, "d:"+deviceID)
	if err != nil {
		return clientSyncWindow, false
	}
	return d.RetryAfter, d.Allowed
}

// clientCapabilities are the entries of X-Mistgate-Caps ("v=1; hysteria2=2; gecko=1; awg=2.0,3.1"), trimmed.
func clientCapabilities(r *http.Request) []string {
	var caps []string
	for _, header := range r.Header.Values("X-Mistgate-Caps") {
		for _, item := range strings.Split(header, ";") {
			if item = strings.TrimSpace(item); item != "" {
				caps = append(caps, item)
			}
		}
	}
	return caps
}

// The body of sync. The field order of these structs is the key order of the answer, and the answer is what the ETag is made of,
// so nothing in it may vary between two calls that mean the same: no time of the request, no map, no random value. Only data
// (an expiry, a counter, a key) may change it. Never in it: a node name, the subscription link or its token, another person.

type clientSyncBrand struct {
	Parts   []string `json:"parts"`
	Accent  string   `json:"accent"`
	LogoSVG string   `json:"logo_svg"`
}

type clientSyncNotice struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type clientSyncPlan struct {
	ExpiresUnix   int64  `json:"expires_unix"`
	QuotaBytes    uint64 `json:"quota_bytes"`
	UsedBytes     uint64 `json:"used_bytes"`
	QuotaReset    string `json:"quota_reset"`
	NextResetUnix int64  `json:"next_reset_unix"`
}

// clientSyncDevices are numbers only, the person's own: how many of their devices fill the device limit (the count the limit is
// enforced by, access.SubView.DevicesUsed: the Telegram key is not among them) and the limit itself. A person always has a limit (1 to
// 100, the table refuses anything else), so there is no "none" to express; a 0 would mean none, as quota_bytes 0 means no quota.
// Nothing of any device.
type clientSyncDevices struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

type clientSyncProbe struct {
	Intake bool `json:"intake"`
	V      int  `json:"v"`
}

type clientSyncDebug struct {
	Intake bool `json:"intake"`
}

type clientSyncDNS struct {
	Transport   string               `json:"transport"`
	Servers     []clientDNSEndpoint  `json:"servers"`
	Split       []clientDNSSplitRule `json:"split"`
	SplitDirect bool                 `json:"split_direct"`
	BlockAds    bool                 `json:"block_ads"`
	IPv4Only    bool                 `json:"ipv4_only"`
}

type clientDNSEndpoint struct {
	Transport string   `json:"transport"`
	Address   string   `json:"address"`
	IPs       []string `json:"ips,omitempty"`
}

type clientDNSSplitRule struct {
	Suffixes []string            `json:"suffixes"`
	Geo      []string            `json:"geo"`
	Servers  []clientDNSEndpoint `json:"servers"`
}

// clientSyncPath is one way to a server. Label tells two profiles of one protocol on one server apart: the owner's name of the
// profile (the {profile} of the subscription names), never the node's name, a country or the way ("via"). ViaCountry is the
// country of the entry node of a relay path and is there only on one that has it. "ipv6" says the path goes to the server's IPv6
// address, nothing about the egress. "ipv6_egress" is that, and is always there: true only when the panel knows the exit hands the
// client IPv6 (access.ipv6Egress), so false also where it is not known. For a relay it is the exit's, not the relay's. An AmneziaWG path
// with false has no address_v6.
type clientSyncPath struct {
	ID         string          `json:"id"`
	Protocol   string          `json:"protocol"`
	Label      string          `json:"label,omitempty"`
	Via        string          `json:"via"`
	ViaCountry string          `json:"via_country,omitempty"`
	IPv6       bool            `json:"ipv6"`
	IPv6Egress bool            `json:"ipv6_egress"`
	Settings   json.RawMessage `json:"settings"`
}

// clientSyncTelegram is one tg://proxy link of the person's Telegram proxy key, for the node it names (server_id is the id the
// same node has in servers, when it has one there: a node with only MTProto is no server of the app). The node's IPv6 and relay
// twins carry the same server_id, so they say what they are the way a path does: via ("direct" or "relay"), via_country (the relay's
// entry country, when it has one) and ipv6.
type clientSyncTelegram struct {
	ServerID   string `json:"server_id"`
	Country    string `json:"country"`
	City       string `json:"city"`
	Via        string `json:"via"`
	ViaCountry string `json:"via_country,omitempty"`
	IPv6       bool   `json:"ipv6"`
	Link       string `json:"link"`
}

// clientSyncServer is a node as people see it: a country and a city, and the owner's public label of it when there is one. The
// label is a text the owner wrote for people; the node's name is not sent.
type clientSyncServer struct {
	ID      string           `json:"id"`
	Country string           `json:"country"`
	City    string           `json:"city"`
	Label   string           `json:"label,omitempty"`
	Load    string           `json:"load,omitempty"`
	Paths   []clientSyncPath `json:"paths"`
}

type clientSyncAnswer struct {
	V          int                `json:"v"`
	DeviceID   string             `json:"device_id"`
	Status     string             `json:"status"`
	SyncEveryS int                `json:"sync_every_s"`
	Brand      clientSyncBrand    `json:"brand"`
	SupportURL string             `json:"support_url,omitempty"`
	Notice     *clientSyncNotice  `json:"notice,omitempty"`
	Plan       clientSyncPlan     `json:"plan"`
	Devices    clientSyncDevices  `json:"devices"`
	Probe      clientSyncProbe    `json:"probe"`
	Debug      clientSyncDebug    `json:"debug"`
	ASN        uint32             `json:"asn,omitempty"`
	DNS        *clientSyncDNS     `json:"dns,omitempty"`
	Servers    []clientSyncServer `json:"servers"`
	// Telegram are the person's Telegram proxy links, as the page of the subscription renders them (the node's IPv6 and relay twins
	// too). Absent when the person has none: Telegram switched off for them, no MTProto server in their group, or no access.
	Telegram []clientSyncTelegram `json:"telegram,omitempty"`

	// Reason is why an active person gets no servers although they have access: "app_disabled", the Mistgate app is switched off for
	// them. Absent otherwise.
	Reason string `json:"reason,omitempty"`
}

// clientSyncInput is everything the body is made of.
type clientSyncInput struct {
	DeviceID string
	View     access.SubView
	Brand    instance.Settings
	Settings *adminv1.SubscriptionSettings
	ProbeOn  bool
	DNS      *dns.Preset // the preset of the link apps; nil = none (the field is then absent)
}

// buildClientSync makes the body of an answer. A person without access (disabled, expired, over the quota) gets the status, the
// plan and no servers; an active one gets the servers the view holds, each path with the protocol settings its plugin rendered.
func buildClientSync(in clientSyncInput) clientSyncAnswer {
	v := in.View
	answer := clientSyncAnswer{
		V: 1, DeviceID: in.DeviceID, Status: v.Status, SyncEveryS: enrolSyncEvery,
		Brand: clientSyncBrand{Parts: []string{in.Brand.BrandHead}, Accent: in.Brand.Accent, LogoSVG: in.Brand.LogoSVG},
		Plan: clientSyncPlan{ExpiresUnix: unixOrZero(v.Expires), QuotaBytes: v.Total, UsedBytes: v.Up + v.Down,
			QuotaReset: v.QuotaReset, NextResetUnix: unixOrZero(v.NextReset)},
		Devices: clientSyncDevices{Used: v.DevicesUsed, Limit: max(v.DeviceLimit, 0)},
		Probe:   clientSyncProbe{Intake: in.ProbeOn, V: clientprobe.Version},
		Debug:   clientSyncDebug{Intake: false}, // no intake exists yet
		Servers: []clientSyncServer{},
	}
	// "none" is the contract's word for a period that never ends, and then there is no reset time (the page does the same). The
	// period of a person without a quota is kept: their used counter is zeroed by it, which they see.
	if answer.Plan.QuotaReset == "" || answer.Plan.QuotaReset == access.ResetNone {
		answer.Plan.QuotaReset, answer.Plan.NextResetUnix = access.ResetNone, 0
	}
	if in.Brand.BrandTail != "" {
		answer.Brand.Parts = append(answer.Brand.Parts, in.Brand.BrandTail)
	}
	answer.SupportURL = in.Settings.GetSupportUrl()
	if text := in.Settings.GetAnnouncement(); text != "" {
		sum := sha256.Sum256([]byte(text))
		answer.Notice = &clientSyncNotice{ID: hex.EncodeToString(sum[:]), Text: text}
	}
	if v.Status == access.StatusActive && v.AppMistgateOff {
		answer.Reason = "app_disabled" // access is there, the app is not allowed: the status stays active, the servers are none
	} else if v.Status == access.StatusActive {
		if in.DNS != nil {
			answer.DNS = clientDNSOf(*in.DNS)
		}
		answer.Servers = clientServersOf(v.Servers)
		answer.Telegram = clientTelegramOf(v)
	}
	return answer
}

// clientDNSOf is the DNS preset the person gets, in the shape the Mihomo profile derives it from (dns.Preset.ClientPlan for the
// Mihomo client): the main resolvers with the transport actually used for each, the split rules (only when the preset sends them
// direct), ad blocking and the IPv4-only flag. nil when no resolver can be carried.
func clientDNSOf(p dns.Preset) *clientSyncDNS {
	main, split, _, err := p.ClientPlan(dns.ClientMihomo)
	if err != nil {
		return nil
	}
	out := &clientSyncDNS{Transport: string(p.Transport), Servers: dnsEndpoints(main), Split: []clientDNSSplitRule{},
		SplitDirect: p.SplitDirect, BlockAds: p.BlockAds, IPv4Only: p.IPv4Only}
	for _, rule := range split {
		out.Split = append(out.Split, clientDNSSplitRule{Suffixes: nonNil(rule.Suffixes), Geo: nonNil(rule.Geo), Servers: dnsEndpoints(rule.Endpoints)})
	}
	return out
}

func dnsEndpoints(endpoints []dns.Endpoint) []clientDNSEndpoint {
	out := make([]clientDNSEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		out = append(out, clientDNSEndpoint{Transport: string(endpoint.Kind), Address: endpoint.Address, IPs: endpoint.IPs})
	}
	return out
}

// viaOf is how a path reaches its server, "relay" or "direct" (WARP is the caller's to add: a Telegram link has none), and for a
// relay the country of its entry node ("" when it has none, which leaves the field out).
func viaOf(server access.SubServer) (via, country string) {
	if !strings.HasPrefix(server.PathKey, "relay:") {
		return "direct", ""
	}
	country, _ = strings.CutPrefix(server.Mark, access.MarkVia)
	return "relay", strings.ToUpper(strings.TrimSpace(country))
}

// clientTelegramOf lists the tg://proxy links the view holds for a person whose Telegram app is on; nil when there are none.
func clientTelegramOf(v access.SubView) []clientSyncTelegram {
	if !v.AppTelegram {
		return nil
	}
	var out []clientSyncTelegram
	for _, server := range v.Telegram {
		if server.URI == "" {
			continue
		}
		via, viaCountry := viaOf(server)
		out = append(out, clientSyncTelegram{ServerID: clientOpaqueID("srv", server.NodeID), Country: strings.ToUpper(server.CountryCode),
			City: server.Location, Via: via, ViaCountry: viaCountry, IPv6: server.PathKey == "ipv6", Link: server.URI})
	}
	return out
}

// clientServersOf groups the paths of the view by exit node, in the order of the view. A server is a country and a city, never a
// node name; its id and the ids of its paths are hashes of internal ids (opaque, stable). A node whose paths the client cannot
// use (a protocol without the Mistgate format, a path the client's capabilities leave out) is not listed.
func clientServersOf(servers []access.SubServer) []clientSyncServer {
	byNode := make(map[string]int, len(servers))
	out := make([]clientSyncServer, 0, len(servers))
	loads := make([]int, 0, len(servers))
	for _, server := range servers {
		settings := json.RawMessage(server.URI)
		if !json.Valid(settings) {
			continue
		}
		idx, ok := byNode[server.NodeID]
		if !ok {
			idx = len(out)
			byNode[server.NodeID] = idx
			out = append(out, clientSyncServer{ID: clientOpaqueID("srv", server.NodeID), Country: strings.ToUpper(server.CountryCode),
				City: server.Location, Label: strings.TrimSpace(server.PublicLabel), Paths: []clientSyncPath{}})
			loads = append(loads, -1)
		}
		relay := strings.HasPrefix(server.PathKey, "relay:")
		if !relay && server.LoadPercent != nil && *server.LoadPercent > loads[idx] { // the load of the exit node itself, not of a relay's
			loads[idx] = *server.LoadPercent
		}
		via, viaCountry := viaOf(server)
		if via == "direct" && server.Exit == "warp" {
			via = "warp"
		}
		out[idx].Paths = append(out[idx].Paths, clientSyncPath{ID: clientOpaqueID("pth", server.NodeID, server.InboundID, server.ProfileID, server.PathKey),
			Protocol: server.Protocol, Label: clean(server.Profile), Via: via, ViaCountry: viaCountry, IPv6: server.PathKey == "ipv6", IPv6Egress: server.IPv6Egress, Settings: settings})
	}
	for i, load := range loads {
		if load >= 0 {
			out[i].Load = loadLevel(load)
		}
	}
	return out
}

// clientOpaqueID is prefix_ and the first 12 bytes of a SHA-256 over the length-prefixed parts, in base64url.
func clientOpaqueID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		_, _ = h.Write([]byte(part))
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:12])
}

// ifNoneMatch reports whether an If-None-Match header lists etag (or *). The comparison is weak (W/ is ignored), as RFC 9110
// prescribes for it.
func ifNoneMatch(values []string, etag string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "*" || strings.TrimPrefix(item, "W/") == etag {
				return true
			}
		}
	}
	return false
}
