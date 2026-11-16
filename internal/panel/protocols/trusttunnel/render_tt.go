package trusttunnel

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
	ttwire "github.com/mistgate/mistgate/internal/trusttunnel"
)

func renderTTLink(in protocols.RenderInput) (plugin.Fragment, bool) {
	if in.Inbound.TLSMode == plugin.TLSSelfSigned {
		return plugin.Skipped(plugin.NoteSelfSigned)
	}
	if in.Inbound.TLSMode != plugin.TLSAcmeDomain {
		return plugin.Fragment{}, false
	}
	username, password := protocols.MaskedSecret, protocols.MaskedSecret
	if !in.MaskSecrets {
		var ok bool
		username, password, ok = strings.Cut(in.Secret, ":")
		if !ok || username == "" || password == "" || strings.Contains(password, ":") {
			return plugin.Fragment{}, false
		}
	}
	host := strings.Trim(in.Inbound.Node.Address, "[]")
	if !validHost(host) || !validHost(in.Inbound.TLSServerName) || in.Inbound.Port == 0 {
		return plugin.Fragment{}, false
	}
	dns := make([]string, 0, min(2, len(in.DNS)))
	for _, upstream := range in.DNS {
		ip, err := netip.ParseAddr(upstream)
		if err == nil && ip.Is4() {
			dns = append(dns, ip.String())
			if len(dns) == 2 {
				break
			}
		}
	}
	link, err := ttwire.EncodeDeepLink(ttwire.DeepLink{
		Hostname:     in.Inbound.TLSServerName,
		Addresses:    []string{net.JoinHostPort(host, strconv.Itoa(int(in.Inbound.Port)))},
		HasIPv6:      !in.NoIPv6Egress,
		Username:     username,
		Password:     password,
		Name:         clipName(in.DisplayName),
		DNSUpstreams: dns,
	})
	if err != nil {
		return plugin.Fragment{}, false
	}
	return plugin.Fragment{Format: plugin.FormatTTLink, Name: in.DisplayName, Data: []byte(link)}, true
}

// maxLinkName keeps a server name from pushing the link past its size limit, which would leave the server with no link at all.
const maxLinkName = 120

// clipName cuts a name to maxLinkName bytes at a character boundary.
func clipName(name string) string {
	if len(name) <= maxLinkName {
		return name
	}
	cut := maxLinkName
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}
