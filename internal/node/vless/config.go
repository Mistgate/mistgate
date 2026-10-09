package vless

import (
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
	vlessin "github.com/xtls/xray-core/proxy/vless/inbound"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	_ "github.com/xtls/xray-core/transport/internet/tcp"

	"github.com/mistgate/mistgate/internal/plugin"
)

func buildCoreConfig(spec plugin.InboundSpec, cfg settings, creds *credentialIndex, tracker *tcpConnTracker) *core.Config {
	clients := make([]*protocol.User, 0, len(creds.byID))
	for _, id := range sortedCredentialIDs(creds) {
		cs := creds.byID[id]
		clients = append(clients, userConfig(cs, cfg.flow))
	}

	stream := &internet.StreamConfig{
		ProtocolName: "tcp",
		SecurityType: serial.GetMessageType(&reality.Config{}),
		SecuritySettings: []*serial.TypedMessage{serial.ToTypedMessage(&reality.Config{
			Dest: cfg.reality.target, Type: "tcp", ServerNames: append([]string(nil), cfg.reality.serverNames...),
			PrivateKey: append([]byte(nil), cfg.reality.privateKey...), ShortIds: cloneByteSlices(cfg.reality.shortIDs),
		})},
	}
	if tracker != nil {
		stream.Tcpmasks = []*serial.TypedMessage{serial.ToTypedMessage(newTCPConnMaskConfig(tracker.key.inboundID, tracker.key.generation))}
	}
	if cfg.transport == "xhttp" {
		stream.ProtocolName = "splithttp"
		stream.TransportSettings = []*internet.TransportConfig{{
			ProtocolName: "splithttp",
			Settings: serial.ToTypedMessage(&splithttp.Config{
				Path: cfg.xhttp.path, Host: cfg.xhttp.host, Mode: "auto",
				XPaddingBytes: &splithttp.RangeConfig{From: int32(cfg.xhttp.xPaddingFrom), To: int32(cfg.xhttp.xPaddingTo)},
			}),
		}}
	}

	receiver := &proxyman.ReceiverConfig{
		PortList:       &net.PortList{Range: []*net.PortRange{net.SinglePortRange(net.Port(spec.Listen.Port))}},
		StreamSettings: stream,
		SniffingSettings: &proxyman.SniffingConfig{
			Enabled: true, DestinationOverride: []string{"http", "tls", "quic"},
		},
	}
	if BindIP != nil {
		receiver.Listen = net.NewIPOrDomain(net.IPAddress(BindIP))
	}

	return &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(&policy.Config{Level: map[uint32]*policy.Policy{0: {Buffer: &policy.Policy_Buffer{Connection: 64 << 10}}}}),
		},
		Inbound: []*core.InboundHandlerConfig{{
			Tag:              spec.ID,
			ReceiverSettings: serial.ToTypedMessage(receiver),
			ProxySettings:    serial.ToTypedMessage(&vlessin.Config{Clients: clients, Decryption: "none"}),
		}},
	}
}

func userConfig(cs *credState, flow string) *protocol.User {
	return &protocol.User{Email: cs.id, Account: serial.ToTypedMessage(&xrayvless.Account{Id: cs.uuid.String(), Flow: flow})}
}

func cloneByteSlices(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i := range in {
		out[i] = append([]byte(nil), in[i]...)
	}
	return out
}
