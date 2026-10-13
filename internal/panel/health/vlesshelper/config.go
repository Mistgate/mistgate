//go:build !js

// Package vlesshelper contains the Xray client used by the panel's short-lived VLESS health helper.
package vlesshelper

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	xraycore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/socks"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
	vlessout "github.com/xtls/xray-core/proxy/vless/outbound"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	xraytls "github.com/xtls/xray-core/transport/internet/tls"
	websocketcfg "github.com/xtls/xray-core/transport/internet/websocket"

	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
)

// BuildClientConfig builds the resolved subscription client and its loopback-only, password-protected SOCKS inbound.
func BuildClientConfig(p panelvless.ClientParams, socksPort uint16, username, password string, handshakeTimeout time.Duration, testPin []byte) (*xraycore.Config, error) {
	if p.Host == "" || p.Port == 0 || p.UUID == "" || p.SNI == "" {
		return nil, errors.New("incomplete VLESS client parameters")
	}
	if socksPort == 0 || username == "" || password == "" || len(username) > 255 || len(password) > 255 {
		return nil, errors.New("invalid VLESS helper SOCKS settings")
	}
	if handshakeTimeout <= 0 {
		return nil, errors.New("invalid VLESS helper handshake timeout")
	}
	var address xnet.Address = xnet.DomainAddress(p.Host)
	if ip := net.ParseIP(p.Host); ip != nil {
		address = xnet.IPAddress(ip)
	}

	stream, err := vlessStreamSettings(p, testPin)
	if err != nil {
		return nil, err
	}
	account := &xrayvless.Account{Id: p.UUID, Flow: p.Flow}
	outbound := &vlessout.Config{Vnext: &protocol.ServerEndpoint{
		Address: xnet.NewIPOrDomain(address), Port: uint32(p.Port),
		User: &protocol.User{Email: "mistgate-health", Account: serial.ToTypedMessage(account)},
	}}
	seconds := uint32(math.Ceil(handshakeTimeout.Seconds()))
	if seconds == 0 {
		seconds = 1
	}
	return &xraycore.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(&policy.Config{Level: map[uint32]*policy.Policy{
				0: {
					Timeout: &policy.Policy_Timeout{
						Handshake:      &policy.Second{Value: seconds},
						ConnectionIdle: &policy.Second{Value: seconds},
					},
					Buffer: &policy.Policy_Buffer{Connection: 64 << 10},
				},
			}}),
		},
		Inbound: []*xraycore.InboundHandlerConfig{{
			Tag: "vless-health-socks",
			ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
				PortList: &xnet.PortList{Range: []*xnet.PortRange{xnet.SinglePortRange(xnet.Port(socksPort))}},
				Listen:   xnet.NewIPOrDomain(xnet.IPAddress(net.ParseIP("127.0.0.1"))),
			}),
			ProxySettings: serial.ToTypedMessage(&socks.ServerConfig{
				AuthType: socks.AuthType_PASSWORD, Accounts: map[string]string{username: password}, UdpEnabled: false,
			}),
		}},
		Outbound: []*xraycore.OutboundHandlerConfig{{
			Tag:            "vless-health",
			ProxySettings:  serial.ToTypedMessage(outbound),
			SenderSettings: serial.ToTypedMessage(&proxyman.SenderConfig{StreamSettings: stream}),
		}},
	}, nil
}

func vlessStreamSettings(p panelvless.ClientParams, testPin []byte) (*internet.StreamConfig, error) {
	stream := &internet.StreamConfig{ProtocolName: "tcp"}
	switch p.Transport {
	case "tcp":
	case "xhttp":
		padding := strings.Split(p.XHTTPPadding, "-")
		if len(padding) != 2 {
			return nil, errors.New("invalid VLESS XHTTP padding")
		}
		from, errFrom := strconv.ParseInt(padding[0], 10, 32)
		to, errTo := strconv.ParseInt(padding[1], 10, 32)
		if errFrom != nil || errTo != nil {
			return nil, errors.New("invalid VLESS XHTTP padding")
		}
		stream.ProtocolName = "splithttp"
		stream.TransportSettings = []*internet.TransportConfig{{
			ProtocolName: "splithttp",
			Settings: serial.ToTypedMessage(&splithttp.Config{
				Path: p.XHTTPPath, Host: p.ClientHost, Mode: p.XHTTPMode,
				XPaddingBytes: &splithttp.RangeConfig{From: int32(from), To: int32(to)},
			}),
		}}
	case "ws":
		stream.ProtocolName = "websocket"
		stream.TransportSettings = []*internet.TransportConfig{{
			ProtocolName: "websocket",
			Settings:     serial.ToTypedMessage(&websocketcfg.Config{Path: p.WSPath, Host: p.ClientHost}),
		}}
	default:
		return nil, errors.New("unsupported VLESS transport")
	}

	switch p.Security {
	case "reality":
		publicKey, err := base64.RawURLEncoding.DecodeString(p.PublicKey)
		if err != nil || len(publicKey) != 32 {
			return nil, errors.New("invalid VLESS REALITY public key")
		}
		shortID, err := hex.DecodeString(p.ShortID)
		if err != nil {
			return nil, errors.New("invalid VLESS REALITY short id")
		}
		stream.SecurityType = serial.GetMessageType(&reality.Config{})
		stream.SecuritySettings = []*serial.TypedMessage{serial.ToTypedMessage(&reality.Config{
			Fingerprint: p.Fingerprint, ServerName: p.SNI, PublicKey: publicKey, ShortId: shortID, SpiderX: p.SpiderX,
		})}
	case "tls":
		tlsConfig := &xraytls.Config{ServerName: p.SNI, Fingerprint: p.Fingerprint, NextProtocol: append([]string(nil), p.ALPN...)}
		if len(testPin) != 0 {
			tlsConfig.AllowInsecure = true
			tlsConfig.VerifyPeerCertByName = []string{p.SNI}
			tlsConfig.PinnedPeerCertSha256 = [][]byte{append([]byte(nil), testPin...)}
		}
		stream.SecurityType = serial.GetMessageType(&xraytls.Config{})
		stream.SecuritySettings = []*serial.TypedMessage{serial.ToTypedMessage(tlsConfig)}
	default:
		return nil, errors.New("unsupported VLESS security")
	}
	return stream, nil
}
