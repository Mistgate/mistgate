//go:build !js

package vlesshelper

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	xraycore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/socks"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
	vlessout "github.com/xtls/xray-core/proxy/vless/outbound"
	"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	"github.com/xtls/xray-core/transport/internet/tls"
	"github.com/xtls/xray-core/transport/internet/websocket"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestVLESSClientConfigMatchesSubscriptionParameters(t *testing.T) {
	cases := []struct {
		name, security, transport, cdn string
	}{
		{name: "REALITY TCP Vision", security: "reality", transport: "tcp"},
		{name: "REALITY XHTTP", security: "reality", transport: "xhttp"},
		{name: "TLS XHTTP", security: "tls", transport: "xhttp"},
		{name: "TLS WebSocket", security: "tls", transport: "ws"},
		{name: "TLS XHTTP behind CDN", security: "tls", transport: "xhttp", cdn: "cdn.example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := vlessRenderInput(t, tc.security, tc.transport, tc.cdn)
			params, ok := panelvless.ResolveClientParams(input)
			if !ok {
				t.Fatalf("parameter resolution refused the test profile: %+v", panelvless.New().Validate(input.Settings))
			}
			fragment, ok := panelvless.New().Render(input)
			if !ok {
				t.Fatal("URI renderer refused the test profile")
			}
			uri, err := url.Parse(string(fragment.Data))
			if err != nil {
				t.Fatal(err)
			}
			query := uri.Query()
			if uri.User.Username() != params.UUID || uri.Hostname() != params.Host || uri.Port() != strconv.Itoa(int(params.Port)) ||
				query.Get("security") != params.Security || query.Get("sni") != params.SNI || query.Get("type") != params.Transport ||
				query.Get("fp") != params.Fingerprint || query.Get("flow") != params.Flow {
				t.Fatalf("subscription parameters do not match the resolved client: URI=%s params=%+v", uri, params)
			}

			config, err := BuildClientConfig(params, 1087, "socks-user", "socks-password", 8*time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertVLESSConfigMatchesURI(t, config, params, query)
		})
	}
}

func vlessRenderInput(t *testing.T, security, transport, cdn string) protocols.RenderInput {
	t.Helper()
	raw, err := panelvless.New().DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var settings panelvless.Settings
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Security, settings.Transport = security, transport
	settings.Reality.ServerNames = []string{"reality.example.net"}
	settings.Reality.ShortIDs = []string{"0102030405060708"}
	settings.XHTTP.Path = "/probe-path"
	settings.XHTTP.XPaddingBytes = "120-360"
	if security == "tls" {
		settings.TLSMode = "acme_domain"
		settings.SNI = "origin.example.net"
		if cdn != "" {
			settings.CDN = &panelvless.CDNSettings{Host: cdn, Port: 9443}
		}
		if transport == "ws" {
			settings.WS = &panelvless.WSSettings{Path: "/socket", Host: "ws.example.net"}
		}
	} else {
		settings.XHTTP.Host = "cover.example.net"
	}
	raw, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	privateBytes := make([]byte, 32)
	for i := range privateBytes {
		privateBytes[i] = byte(i + 1)
	}
	private, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(struct {
		PublicKey string `json:"public_key"`
	}{PublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	serverName := "origin.example.net"
	if security == "reality" {
		serverName = "reality.example.net"
	}
	return protocols.RenderInput{
		Format: plugin.FormatURIList, Settings: raw, InboundPublic: public,
		Inbound: protocols.InboundView{ID: "inb_test", Node: protocols.NodeView{ID: "nod_test", Name: "node", Address: "node.example.org"},
			Port: 8443, TLSServerName: serverName, TLSMode: plugin.TLSAcmeDomain},
		DeviceID: "crd_probe_1", Secret: "35b78532-7b51-4ea4-9c02-4e8f3a51d103",
	}
}

func assertVLESSConfigMatchesURI(t *testing.T, config *xraycore.Config, params panelvless.ClientParams, query url.Values) {
	t.Helper()
	if len(config.Inbound) != 1 || len(config.Outbound) != 1 {
		t.Fatalf("client config has the wrong number of handlers: %d/%d", len(config.Inbound), len(config.Outbound))
	}
	inMessage, err := config.Inbound[0].ProxySettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	inbound, ok := inMessage.(*socks.ServerConfig)
	if !ok || inbound.AuthType != socks.AuthType_PASSWORD || inbound.UdpEnabled || inbound.Accounts["socks-user"] != "socks-password" {
		t.Fatalf("unexpected SOCKS inbound: %T %+v", inMessage, inMessage)
	}
	receiverMessage, err := config.Inbound[0].ReceiverSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	receiver, ok := receiverMessage.(*proxyman.ReceiverConfig)
	if !ok || receiver.Listen == nil || net.IP(receiver.Listen.GetIp()).String() != "127.0.0.1" ||
		len(receiver.PortList.GetRange()) != 1 || receiver.PortList.GetRange()[0].GetFrom() != 1087 || receiver.PortList.GetRange()[0].GetTo() != 1087 {
		t.Fatalf("unexpected loopback SOCKS receiver: %T %+v", receiverMessage, receiverMessage)
	}
	policyMessage, err := config.App[3].GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	clientPolicy, ok := policyMessage.(*policy.Config)
	if !ok || clientPolicy.Level[0].Timeout.GetHandshake().GetValue() != 8 || clientPolicy.Level[0].Timeout.GetConnectionIdle().GetValue() != 8 {
		t.Fatalf("unexpected helper timeouts: %T %+v", policyMessage, policyMessage)
	}
	outMessage, err := config.Outbound[0].ProxySettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	out, ok := outMessage.(*vlessout.Config)
	if !ok || out.Vnext == nil || out.Vnext.User == nil {
		t.Fatalf("unexpected VLESS outbound config: %T %+v", outMessage, outMessage)
	}
	if got := vlessAddressText(out.Vnext.Address); got != params.Host || out.Vnext.Port != uint32(params.Port) {
		t.Fatalf("outbound endpoint %s:%d, want %s:%d", got, out.Vnext.Port, params.Host, params.Port)
	}
	accountMessage, err := out.Vnext.User.Account.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	account, ok := accountMessage.(*xrayvless.Account)
	if !ok || account.Id != params.UUID || account.Flow != query.Get("flow") {
		t.Fatalf("outbound account %T %+v does not match subscription UUID/flow", accountMessage, accountMessage)
	}
	senderMessage, err := config.Outbound[0].SenderSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	sender, ok := senderMessage.(*proxyman.SenderConfig)
	if !ok || sender.StreamSettings == nil {
		t.Fatalf("unexpected VLESS sender config: %T %+v", senderMessage, senderMessage)
	}
	stream := sender.StreamSettings
	wantTransport := map[string]string{"tcp": "tcp", "xhttp": "splithttp", "ws": "websocket"}[params.Transport]
	if stream.ProtocolName != wantTransport {
		t.Fatalf("transport %q, want %q", stream.ProtocolName, wantTransport)
	}
	if len(stream.SecuritySettings) != 1 {
		t.Fatalf("security settings: %+v", stream.SecuritySettings)
	}
	securityMessage, err := stream.SecuritySettings[0].GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	switch security := securityMessage.(type) {
	case *tls.Config:
		if params.Security != "tls" || security.ServerName != params.SNI || security.Fingerprint != params.Fingerprint || !slices.Equal(security.NextProtocol, params.ALPN) {
			t.Fatalf("TLS client settings %+v do not match URI/default ALPN %+v", security, params)
		}
	case *reality.Config:
		if params.Security != "reality" || security.ServerName != query.Get("sni") || security.Fingerprint != query.Get("fp") ||
			base64.RawURLEncoding.EncodeToString(security.PublicKey) != query.Get("pbk") || hex.EncodeToString(security.ShortId) != query.Get("sid") || security.SpiderX != query.Get("spx") {
			t.Fatalf("REALITY client settings %+v do not match URI %v", security, query)
		}
	default:
		t.Fatalf("unexpected VLESS security config: %T", securityMessage)
	}
	if params.Transport == "xhttp" {
		if len(stream.TransportSettings) != 1 {
			t.Fatalf("XHTTP transport settings: %+v", stream.TransportSettings)
		}
		message, err := stream.TransportSettings[0].Settings.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		xhttp, ok := message.(*splithttp.Config)
		if !ok || xhttp.Path != query.Get("path") || xhttp.Host != query.Get("host") || xhttp.Mode != query.Get("mode") ||
			xhttp.XPaddingBytes == nil || xhttp.XPaddingBytes.From != 120 || xhttp.XPaddingBytes.To != 360 {
			t.Fatalf("XHTTP config %T %+v does not match URI %v", message, message, query)
		}
	}
	if params.Transport == "ws" {
		message, err := stream.TransportSettings[0].Settings.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		ws, ok := message.(*websocket.Config)
		if !ok || ws.Path != query.Get("path") || ws.Host != query.Get("host") {
			t.Fatalf("WebSocket config %T %+v does not match URI %v", message, message, query)
		}
	}
}

func vlessAddressText(address *xnet.IPOrDomain) string {
	if address == nil {
		return ""
	}
	if domain := address.GetDomain(); domain != "" {
		return domain
	}
	return net.IP(address.GetIp()).String()
}
