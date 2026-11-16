//go:build !js

package subs

import (
	"strings"

	"github.com/mistgate/mistgate/internal/panel/access"
)

func pageTrustTunnelOf(v access.SubView, lang string) *pageTrustTunnel {
	tt := &pageTrustTunnel{Links: []pageTrustTunnelLink{}}
	labels := serverLabels(v, lang)
	for _, srv := range v.TrustTunnel {
		if srv.URI == "" && srv.Note == "" {
			continue
		}
		link := pageTrustTunnelLink{
			Label: labels[srv.NodeID] + markText(srv.Mark, lang), CountryCode: strings.ToUpper(strings.TrimSpace(srv.CountryCode)),
			Place: place(srv.Location), HasIPv6: srv.IPv6Egress, Note: srv.Note, URL: srv.URI,
		}
		if cc, ok := strings.CutPrefix(srv.Mark, access.MarkVia); ok {
			link.Relay, link.Via = true, cc
		}
		tt.Links = append(tt.Links, link)
	}
	return tt
}
