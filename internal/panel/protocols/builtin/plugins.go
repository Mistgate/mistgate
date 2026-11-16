//go:build !js

package builtin

import (
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/protocols/hysteria2"
	"github.com/mistgate/mistgate/internal/panel/protocols/mtproto"
	"github.com/mistgate/mistgate/internal/panel/protocols/trusttunnel"
	"github.com/mistgate/mistgate/internal/panel/protocols/vless"
)

func plugins() []protocols.Protocol {
	return []protocols.Protocol{hysteria2.New(), vless.New(), awg.New(), mtproto.New(), trusttunnel.New()}
}
