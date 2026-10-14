//go:build js

package fleet

import (
	"context"
	"errors"

	"github.com/mistgate/mistgate/internal/tcpcheck"
)

const panelSendsUDP = false

// A Worker has no sockets: it is never the sender of a port check (panelSendsUDP), so nothing calls these. They stand in
// for udpcheck.Send and tcpcheck.Probe, which would put the TLS client and the HTTP probe into panel.wasm.
var errNoPanelSender = errors.New("the panel sends no port probes in this edition")

func defaultSendUDP(context.Context, string, []uint16, [8]byte, int, int, int) (string, int, error) {
	return "", 0, errNoPanelSender
}

func defaultProbeTCP(context.Context, string, []tcpcheck.Target) (string, map[uint16]string, error) {
	return "", nil, errNoPanelSender
}
