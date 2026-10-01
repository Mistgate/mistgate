package awg

import (
	"encoding/json"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Minimum client versions per protocol version: THE place to update when a client release changes what it
// understands. The versions are repository TAGS, not store versions, which lag behind.
// They are shown next to every config because an old AmneziaVPN
// does not refuse a config with keys it does not know: it drops them, and the handshake then fails silently.
var (
	minClients31 = []protocols.ClientReq{
		{Client: plugin.ClientAmnezia, App: "AmneziaVPN", Min: "5.0.1.5"},
		{Client: plugin.ClientAmnezia, App: "AmneziaWG Android", Min: "v3.1.20260814"},
		{Client: plugin.ClientAmnezia, App: "AmneziaWG Windows", Min: "3.1.0"},
		{Client: plugin.ClientAmnezia, App: "AmneziaWG Apple", Min: "v3.1.3"},
		{Client: plugin.ClientMihomo, App: "Mihomo", Min: "v1.19.30"},
	}
	// 2.0: AmneziaWG apps are listed as one entry: 2.0.0 is the first release of each of them (Apple: v2.0.x).
	minClients20 = []protocols.ClientReq{
		{Client: plugin.ClientAmnezia, App: "AmneziaVPN", Min: "4.8.12.9"},
		{Client: plugin.ClientAmnezia, App: "AmneziaWG", Min: "2.0.0"},
		{Client: plugin.ClientMihomo, App: "Mihomo", Min: "v1.19.14"},
	}
)

// MinClients implements protocols.ClientRequirements. Unparsable settings give the 3.1 list, the stricter one.
func (*Protocol) MinClients(raw json.RawMessage) []protocols.ClientReq {
	if s, errs := parse(raw); errs == nil && s.Version == Version20 {
		return append([]protocols.ClientReq(nil), minClients20...)
	}
	return append([]protocols.ClientReq(nil), minClients31...)
}
