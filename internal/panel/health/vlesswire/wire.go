// Package vlesswire holds the messages between the panel and its VLESS health helper process. It deliberately imports
// no Xray package: the panel process only needs the wire format, the Xray client lives in vlesshelper.
package vlesswire

import (
	"time"

	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
)

// Request is the one-shot input (one JSON line on stdin). TestTLSPin is only set by loopback tests.
type Request struct {
	Client           panelvless.ClientParams `json:"client"`
	SOCKSUsername    string                  `json:"socks_username"`
	SOCKSPassword    string                  `json:"socks_password"`
	HandshakeTimeout time.Duration           `json:"handshake_timeout"`
	HardTimeout      time.Duration           `json:"hard_timeout"`
	TestTLSPin       []byte                  `json:"test_tls_pin,omitempty"`
}

// Ready is the one JSON line the helper prints on stdout once its loopback SOCKS5 listener accepts connections.
type Ready struct {
	Port uint16 `json:"port"`
}

// TunnelMarker is the Xray info line ("proxy/vless/outbound: tunneling request to ...") the helper forwards once the VLESS
// request went out over an established transport. Seen together with a silent end of stream it means the server accepted
// the transport and then dropped the connection, which is how a wrong or revoked UUID looks from the client.
const TunnelMarker = "tunneling request"
