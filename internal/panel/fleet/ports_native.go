//go:build !js

package fleet

import (
	"github.com/mistgate/mistgate/internal/tcpcheck"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

const panelSendsUDP = true

// The panel sends the probes of a port check itself when no node can (the VPS panel is the sender of last resort).
var (
	defaultSendUDP  = udpcheck.Send
	defaultProbeTCP = tcpcheck.Probe
)
