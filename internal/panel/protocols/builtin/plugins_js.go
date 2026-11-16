//go:build js

package builtin

import (
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/protocols/hysteria2"
	"github.com/mistgate/mistgate/internal/panel/protocols/mtproto"
	"github.com/mistgate/mistgate/internal/panel/protocols/vless"
)

// TrustTunnel is not in the Cloudflare edition's first version: its Workers have no room for the plugin, the tt:// links and the
// key code (about 60 KB in the link Worker alone, past its size limit), and the edge's own checks of it do not exist yet.
func plugins() []protocols.Protocol {
	return []protocols.Protocol{hysteria2.New(), vless.New(), awg.New(), mtproto.New()}
}
