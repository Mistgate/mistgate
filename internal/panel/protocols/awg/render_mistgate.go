package awg

import (
	"encoding/json"
	"strconv"
)

type mistgateAWGEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type mistgateAWGSettings struct {
	Version      string              `json:"version"`
	Endpoint     mistgateAWGEndpoint `json:"endpoint"`
	PublicKey    string              `json:"public_key"`
	PrivateKey   string              `json:"private_key"`
	PresharedKey string              `json:"preshared_key"`
	AddressV4    string              `json:"address_v4"`
	AddressV6    string              `json:"address_v6,omitempty"`
	AllowedIPs   []string            `json:"allowed_ips"`
	DNS          []string            `json:"dns"`
	MTU          int                 `json:"mtu"`
	KeepaliveS   int                 `json:"keepalive_s,omitempty"`
	Params       map[string]any      `json:"params"`
}

func (c clientConfig) mistgate() ([]byte, error) {
	// Both ranges whatever the profile's addresses are, as the .conf has them: a client on a dual-stack network must not send its
	// IPv6 around the tunnel (the server drops what has no address to return to). That holds when the exit gives the client no IPv6
	// as well (no address_v6 below): its IPv6 goes into the tunnel, finds no source address and fails at once, and the app uses IPv4.
	allowed := c.allowedIPs()
	params := map[string]any{
		"jc": c.s.Obfuscation.Jc, "jmin": c.s.Obfuscation.Jmin, "jmax": c.s.Obfuscation.Jmax,
		"s1": c.s.Obfuscation.S1, "s2": c.s.Obfuscation.S2, "s3": c.s.Obfuscation.S3, "s4": c.s.Obfuscation.S4,
		"h1": c.s.Obfuscation.H1, "h2": c.s.Obfuscation.H2, "h3": c.s.Obfuscation.H3, "h4": c.s.Obfuscation.H4,
	}
	for i, value := range c.s.Obfuscation.I() {
		if value != "" {
			params["i"+strconv.Itoa(i+1)] = value
		}
	}
	if c.v31() {
		// The set the .conf and the Mihomo proxy of a 3.1 profile carry: the strings only when the profile has them.
		params["header_protection_key"] = c.hpk
		for _, kv := range [][2]string{
			{"content_padding_addition", c.s.Obfuscation.ContentPaddingAddition}, {"rekey_after_time", c.s.Obfuscation.RekeyAfterTime},
			{"rekey_timeout", c.s.Obfuscation.RekeyTimeout}, {"reject_after_time", c.s.Obfuscation.RejectAfterTime},
			{"keepalive_timeout", c.s.Obfuscation.KeepaliveTimeout}, {"max_handshake_attempts", c.s.Obfuscation.MaxHandshakeAttempts},
		} {
			if kv[1] != "" {
				params[kv[0]] = kv[1]
			}
		}
		params["random_trailers"] = c.s.Obfuscation.RandomTrailers
		params["disable_cookies"] = true
	}
	keepalive := 0 // the first number of the profile's range, as the Mihomo proxy has it; none when the profile has none
	if r, err := parseRange(c.s.Obfuscation.PersistentKeepalive, maxUint16); err == nil {
		keepalive = int(r.lo)
	}
	settings := mistgateAWGSettings{
		Version: c.s.Version, Endpoint: mistgateAWGEndpoint{Host: c.host, Port: c.port},
		PublicKey: c.server, PrivateKey: c.priv, PresharedKey: c.psk,
		AddressV4: c.ip4 + "/32", AllowedIPs: allowed, DNS: []string{c.dns[0], c.dns[1]},
		MTU: c.s.MTU, KeepaliveS: keepalive, Params: params,
	}
	if c.ip6 != "" && !c.noIPv6 {
		settings.AddressV6 = c.ip6 + "/128"
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	return b, nil
}
