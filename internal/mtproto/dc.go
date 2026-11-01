package mtproto

// DCEndpoint is one Telegram datacenter's reachable addresses.
type DCEndpoint struct {
	IPv4 string
	IPv6 string
}

// DefaultDCs is the dial map. Telemt omits 203 and falls back to DC 2 for it;
// including it here is deliberate (telemt issue #19).
var DefaultDCs = map[int16]DCEndpoint{
	1:   {IPv4: "149.154.175.50:443", IPv6: "[2001:b28:f23d:f001::a]:443"},
	2:   {IPv4: "149.154.167.51:443", IPv6: "[2001:67c:04e8:f002::a]:443"},
	3:   {IPv4: "149.154.175.100:443", IPv6: "[2001:b28:f23d:f003::a]:443"},
	4:   {IPv4: "149.154.167.91:443", IPv6: "[2001:67c:04e8:f004::a]:443"},
	5:   {IPv4: "149.154.171.5:443", IPv6: "[2001:b28:f23f:f005::a]:443"},
	203: {IPv4: "91.105.192.100:443", IPv6: "[2a0a:f280:0203:000a:5000:0000:0000:0100]:443"},
}

// AbsDC maps a signed media DC id to its datacenter number.
func AbsDC(dc int16) int16 {
	if dc < 0 {
		return -dc
	}
	return dc
}

// DefaultDCAddress returns the address of dc. The sign is ignored: a negative
// id is the media variant of the same datacenter.
func DefaultDCAddress(dc int16, preferIPv6 bool) (string, bool) {
	ep, ok := DefaultDCs[AbsDC(dc)]
	if !ok {
		return "", false
	}
	if preferIPv6 && ep.IPv6 != "" {
		return ep.IPv6, true
	}
	if ep.IPv4 != "" {
		return ep.IPv4, true
	}
	return ep.IPv6, ep.IPv6 != ""
}
