package subs

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The pick of a DNS per server, on the public page. Under the subscription link, POST, JSON in and out, like the device calls:
//
//	POST <link>/dns   {"server":"<servers[].id>","preset":"<servers[].dns.options[]>"}   "" = the node's default
//	200 {"server": <the servers[] item as it is now>, "stale_devices": ["<amnezia.devices[].id>", ...]}
//
// It goes through the same start as the device calls (enter, then admitWrite: page password, token, cross-origin, the
// owner's switch, the user's status, the hourly budget of writes of the token, shared with the devices) and then:
//
//	400 bad_request | 413 too_large | 415 unsupported_media_type   the body is not one small JSON object of these two fields
//	404 not_found                                                   the server is none of the person's
//	409 not_allowed                                                 the server does not offer this preset
//	403 dns_disabled                                                the owner switched the choice off (UserPageOptions.allow_dns_choice)
//
// stale_devices are the person's AmneziaWG devices with a key on this server that holds an older DNS: they fetch their
// configs again (the key itself does not change). The apps that take the link get the new DNS with their next refresh.

// PageDNS is what the call does; *access.Service implements it. A Source that does not leaves the endpoint off.
type PageDNS interface {
	SetPageDNS(ctx context.Context, userID string, c access.PageDNSChoice) error
}

type dnsAnswer struct {
	Server       pageNode `json:"server"`
	StaleDevices []string `json:"stale_devices"`
}

func (h *handler) serveDNS(w http.ResponseWriter, r *http.Request, token, client string, now time.Time) {
	ctx := r.Context()
	v, st, ok := h.enter(w, r, token, client, now, h.pdns != nil, subsettings.DNSChoice, "dns_disabled")
	if !ok || !h.admitWrite(w, st, v, true, now) { // a user without access has no servers to pick on
		return
	}
	var in struct {
		Server string `json:"server"`
		Preset string `json:"preset"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Server == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "")
		return
	}
	if !slices.ContainsFunc(v.Nodes, func(n access.SubNode) bool { return n.ID == in.Server }) {
		jsonError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if err := h.pdns.SetPageDNS(ctx, v.UserID, access.PageDNSChoice{NodeID: in.Server, PresetID: in.Preset}); err != nil {
		writeAccessError(w, err)
		return
	}
	st.drop() // the cached views still carry the old DNS (the Mihomo profile's AmneziaWG proxies)
	v, _, err := h.identify(ctx, token, now)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	b := h.brand(ctx)
	for _, s := range pageServers(v, h.settings(ctx), pageLang(r, b.Language)) {
		if s.ID != in.Server {
			continue
		}
		stale := []string{}
		if s.DNS != nil {
			stale = s.DNS.KeysToRefresh
		}
		writeJSON(w, http.StatusOK, dnsAnswer{Server: s, StaleDevices: stale})
		return
	}
	jsonError(w, http.StatusNotFound, "not_found", "") // the server went away between the write and the read
}
