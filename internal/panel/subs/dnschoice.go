package subs

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The pick of a DNS on the public page: per server (for the AmneziaWG keys), or one for the apps that take the link. Under the
// subscription link, POST, JSON in and out, like the device calls:
//
//	POST <link>/dns   {"server":"<servers[].id>","preset":"<servers[].dns.options[]>"}   "" = the node's default
//	200 {"server": <the servers[] item as it is now>, "stale_devices": ["<amnezia.devices[].id>", ...]}
//
//	POST <link>/dns   {"link":true,"preset":"<dns.link.options[]>"}   "" = what the link apps get without a pick
//	200 {"link": <dns.link as it is now>, "stale_devices": []}
//
// It goes through the same start as the device calls (enter, then admitWrite: page password, token, cross-origin, the
// owner's switch, the user's status, the hourly budget of writes of the token, shared with the devices; a token the handler
// knows is refused for a cross-origin call, the owner's switch and a used-up budget before its view is built) and then:
//
//	400 bad_request | 413 too_large | 415 unsupported_media_type   the body is not one small JSON object of these fields, or
//	                                                                names both a server and the link, or neither
//	404 not_found                                                   the server is none of the person's
//	409 not_allowed                                                 the server (or the offer for the link apps) has no such preset
//	403 dns_disabled                                                the owner switched the choice off (UserPageOptions.allow_dns_choice)
//
// stale_devices are the person's AmneziaWG devices with a key on this server that holds an older DNS: they fetch their
// configs again (the key itself does not change). The apps that take the link get the new DNS with their next refresh; a
// pick for them never makes a key stale.

// PageDNS is what the call does; *access.Service implements it. A Source that does not leaves the endpoint off.
type PageDNS interface {
	SetPageDNS(ctx context.Context, userID string, c access.PageDNSChoice) error
}

type dnsAnswer struct {
	Server       *pageNode    `json:"server,omitempty"`
	Link         *pageDNSLink `json:"link,omitempty"`
	StaleDevices []string     `json:"stale_devices"`
}

func (h *handler) serveDNS(w http.ResponseWriter, r *http.Request, token, client string, now time.Time) {
	ctx := r.Context()
	v, st, ok := h.enter(w, r, token, client, now, h.pdns != nil, subsettings.DNSChoice, "dns_disabled")
	if !ok || !h.admitWrite(r.Context(), w, token, v, true) { // a user without access has no servers to pick on
		return
	}
	var in struct {
		Server string `json:"server"`
		Link   bool   `json:"link"`
		Preset string `json:"preset"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Link == (in.Server != "") { // one target: a server or the link apps
		jsonError(w, http.StatusBadRequest, "bad_request", "")
		return
	}
	if !in.Link && !slices.ContainsFunc(v.Nodes, func(n access.SubNode) bool { return n.ID == in.Server }) {
		jsonError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if err := h.pdns.SetPageDNS(ctx, v.UserID, access.PageDNSChoice{NodeID: in.Server, Link: in.Link, PresetID: in.Preset}); err != nil {
		writeAccessError(w, err)
		return
	}
	st.drop() // the cached views still carry the old DNS (the Mihomo profile's AmneziaWG proxies, the dns of the link apps)
	v, _, err := h.identify(ctx, token, true)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if in.Link {
		link := pageLinkDNS(v)
		writeJSON(w, http.StatusOK, dnsAnswer{Link: &link, StaleDevices: []string{}})
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
		writeJSON(w, http.StatusOK, dnsAnswer{Server: &s, StaleDevices: stale})
		return
	}
	jsonError(w, http.StatusNotFound, "not_found", "") // the server went away between the write and the read
}
