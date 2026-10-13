package vless

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	websocketcfg "github.com/xtls/xray-core/transport/internet/websocket"

	"github.com/mistgate/mistgate/internal/plugin"
)

const testPrivateKey = "aGSYystUbf59_9_6LKRxD27rmSW_-2_nyd9YG_Gwbks"

func settingsSpec(transport, security string) plugin.InboundSpec {
	return plugin.InboundSpec{
		ID: "node-a", Protocol: Protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: 28443},
		Settings: json.RawMessage(`{"transport":"` + transport + `","security":"` + security + `","reality":{"target":"de1.example.com:443","server_names":["de1.example.com"],"short_ids":["6ba85179"],"private_key":"` + testPrivateKey + `"},"xhttp":{"path":"/q8x2kd7w","host":"edge.example.com","mode":"packet-up","x_padding_bytes":"100-400"}}`),
	}
}

func TestTLSCoreConfigUsesUnixWithoutReality(t *testing.T) {
	for _, transport := range []string{"xhttp", "ws"} {
		t.Run(transport, func(t *testing.T) {
			spec := tlsSettingsSpec(transport, "packet-up", "")
			cfg, err := parseSpec(spec)
			if err != nil {
				t.Fatal(err)
			}
			config := buildCoreConfig(spec, cfg, nil, nil, "@mistgate-vless-tls-node-a")
			message, err := config.Inbound[0].ReceiverSettings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			receiver := message.(*proxyman.ReceiverConfig)
			if receiver.PortList != nil || receiver.Listen == nil {
				t.Fatalf("TLS receiver must listen on its Unix socket: %+v", receiver)
			}
			stream := receiver.StreamSettings
			if stream.SecurityType != "" || len(stream.SecuritySettings) != 0 {
				t.Fatalf("TLS front must terminate TLS outside Xray: %+v", stream)
			}
			wantProtocol := "splithttp"
			if transport == "ws" {
				wantProtocol = "websocket"
			}
			if stream.ProtocolName != wantProtocol {
				t.Fatalf("transport=%q, want %q", stream.ProtocolName, wantProtocol)
			}
			transportSettings, err := stream.TransportSettings[0].Settings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			switch value := transportSettings.(type) {
			case *splithttp.Config:
				if transport != "xhttp" || value.Path != "/q8x2kd7w" || value.Host != "" {
					t.Fatalf("XHTTP TLS origin settings = %+v", value)
				}
			case *websocketcfg.Config:
				if transport != "ws" || value.Path != "/q8x2kd7w" || value.Host != "" {
					t.Fatalf("WebSocket TLS origin settings = %+v", value)
				}
			default:
				t.Fatalf("unexpected transport settings %T", transportSettings)
			}
		})
	}
}

func tlsSettingsSpec(transport, mode, cdnHost string) plugin.InboundSpec {
	wsPath := ""
	if transport == "ws" {
		wsPath = "/q8x2kd7w"
	}
	return plugin.InboundSpec{
		ID: "node-a", Protocol: Protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: 28443},
		TLS:      plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "de1.example.com"},
		Settings: json.RawMessage(`{"transport":"` + transport + `","security":"tls","flow":"","cdn":{"host":"` + cdnHost + `"},"xhttp":{"path":"/q8x2kd7w","host":"","mode":"` + mode + `","x_padding_bytes":"100-400"},"ws":{"path":"` + wsPath + `","host":""}}`),
	}
}

func TestSettingsSchemaToEngineAndOrder(t *testing.T) {
	first := plugin.InboundSpec{
		ID: "node-a", Protocol: Protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: 28443},
		Settings: json.RawMessage(`{"transport":"xhttp","security":"reality","flow":"ignored","reality":{"target":"de1.example.com:443","server_names":["de1.example.com"],"short_ids":["6ba85179"],"private_key":"` + testPrivateKey + `"},"xhttp":{"path":"/q8x2kd7w","host":"edge.example.com","mode":"stream-up","x_padding_bytes":"120-360"},"ws":{"path":"/legacy","host":"legacy.example.com"},"unknown":"ignored"}`),
	}
	second := first
	second.Settings = json.RawMessage(`{"ws":{"host":"legacy.example.com","path":"/legacy"},"xhttp":{"x_padding_bytes":"120-360","mode":"stream-up","host":"edge.example.com","path":"/q8x2kd7w"},"reality":{"private_key":"` + testPrivateKey + `","short_ids":["6ba85179"],"server_names":["de1.example.com"],"target":"de1.example.com:443"},"flow":"a different ignored flow","security":"reality","transport":"xhttp","another_unknown":true}`)

	got, err := parseSpec(first)
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := parseSpec(second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, reordered) {
		t.Fatalf("field order changed parsed settings:\n%+v\n%+v", got, reordered)
	}
	if got.flow != "" || got.wsPath != "/legacy" || got.wsHost != "legacy.example.com" {
		t.Fatalf("parsed derived/legacy fields: flow=%q ws=%q %q", got.flow, got.wsPath, got.wsHost)
	}

	creds, err := buildIndex(first.ID, nil, []plugin.UserCred{{CredID: "alice", Data: json.RawMessage(`{"id":"66ad4540-b58c-4ad2-9926-ea63445a9b57"}`)}}, time.Now, func(d time.Duration, f func()) stoppable { return time.AfterFunc(d, f) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	coreConfig := buildCoreConfig(first, got, creds, nil, "")
	receiverMessage, err := coreConfig.Inbound[0].ReceiverSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	receiver := receiverMessage.(*proxyman.ReceiverConfig)
	if receiver.PortList.Range[0].From != uint32(first.Listen.Port) || receiver.StreamSettings.ProtocolName != "splithttp" {
		t.Fatalf("receiver settings lost listen/transport: %+v", receiver)
	}
	realityMessage, err := receiver.StreamSettings.SecuritySettings[0].GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	realityConfig := realityMessage.(*reality.Config)
	if realityConfig.Dest != "de1.example.com:443" || realityConfig.ServerNames[0] != "de1.example.com" || hex.EncodeToString(realityConfig.ShortIds[0]) != "6ba8517900000000" || base64.RawURLEncoding.EncodeToString(realityConfig.PrivateKey) != testPrivateKey {
		t.Fatalf("REALITY config differs from schema: %+v", realityConfig)
	}
	transportMessage, err := receiver.StreamSettings.TransportSettings[0].Settings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	transportConfig := transportMessage.(*splithttp.Config)
	if transportConfig.Path != "/q8x2kd7w" || transportConfig.Host != "edge.example.com" || transportConfig.Mode != "auto" || transportConfig.XPaddingBytes.From != 120 || transportConfig.XPaddingBytes.To != 360 {
		t.Fatalf("XHTTP server config differs from schema and auto-mode requirement: %+v", transportConfig)
	}
	accountMessage, err := userConfig(creds.byID["alice"], got.flow).Account.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	account := accountMessage.(*xrayvless.Account)
	if account.GetFlow() != "" {
		t.Fatalf("XHTTP flow must be derived as empty, got %q", account.GetFlow())
	}
}

func TestSettingsValidationAndTLSR3(t *testing.T) {
	t.Run("TCP derives Vision", func(t *testing.T) {
		cfg, err := parseSpec(settingsSpec("tcp", "reality"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.flow != "xtls-rprx-vision" {
			t.Fatalf("flow=%q", cfg.flow)
		}
	})

	for _, tt := range []struct {
		name string
		edit func(*plugin.InboundSpec)
		want string
	}{
		{"port 80", func(s *plugin.InboundSpec) { s.Listen.Port = 80 }, "port 80"},
		{"non TCP listener", func(s *plugin.InboundSpec) { s.Listen.Network = "udp" }, "listen network must be tcp"},
		{"TLS without certificate mode", func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{"transport":"xhttp","security":"tls"}`) }, "TLS requires"},
		{"WebSocket requires TLS", func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{"transport":"ws","security":"reality"}`) }, "ws requires tls"},
		{"unknown transport", func(s *plugin.InboundSpec) { s.Settings = json.RawMessage(`{"transport":"grpc","security":"reality"}`) }, "transport"},
		{"invalid XHTTP padding", func(s *plugin.InboundSpec) {
			s.Settings = json.RawMessage(strings.Replace(string(s.Settings), `100-400`, `0-400`, 1))
		}, "xhttp.x_padding_bytes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := settingsSpec("xhttp", "reality")
			tt.edit(&spec)
			_, err := parseSpec(spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseSpec error=%v, want containing %q", err, tt.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		edit func(*plugin.InboundSpec)
		want string
	}{
		{"xhttp TLS direct", func(s *plugin.InboundSpec) { *s = tlsSettingsSpec("xhttp", "stream-up", "") }, ""},
		{"xhttp TLS behind CDN", func(s *plugin.InboundSpec) { *s = tlsSettingsSpec("xhttp", "packet-up", "cdn.example.org") }, ""},
		{"WebSocket TLS", func(s *plugin.InboundSpec) {
			*s = tlsSettingsSpec("ws", "", "cdn.example.org")
			s.TLS.Mode = plugin.TLSSelfSigned
		}, ""},
		{"TLS TCP forbidden", func(s *plugin.InboundSpec) { *s = tlsSettingsSpec("tcp", "", "cdn.example.org") }, "tcp requires reality"},
		{"self-signed needs CDN", func(s *plugin.InboundSpec) {
			*s = tlsSettingsSpec("xhttp", "auto", "")
			s.TLS.Mode = plugin.TLSSelfSigned
		}, "self_signed requires cdn.host"},
		{"TLS needs certificate", func(s *plugin.InboundSpec) { *s = tlsSettingsSpec("xhttp", "auto", ""); s.TLS = plugin.TLS{} }, "TLS requires a certificate mode and server name"},
		{"CDN host must be plain", func(s *plugin.InboundSpec) { *s = tlsSettingsSpec("xhttp", "auto", "https://cdn.example.org") }, "cdn.host"},
		{"CDN host labels must be valid", func(s *plugin.InboundSpec) { *s = tlsSettingsSpec("xhttp", "auto", "cdn.-bad.example.org") }, "cdn.host"},
		{"TLS rejects client CDN port", func(s *plugin.InboundSpec) {
			*s = tlsSettingsSpec("xhttp", "auto", "cdn.example.org")
			s.Settings = json.RawMessage(strings.Replace(string(s.Settings), `"cdn":{"host":"cdn.example.org"}`, `"cdn":{"host":"cdn.example.org","port":443}`, 1))
		}, "cdn.port is a client setting"},
		{"TLS rejects REALITY fields", func(s *plugin.InboundSpec) {
			*s = tlsSettingsSpec("xhttp", "auto", "")
			s.Settings = json.RawMessage(strings.Replace(string(s.Settings), `{"transport":`, `{"reality":{"target":"example.org:443"},"transport":`, 1))
		}, "reality settings require"},
		{"XHTTP rejects WS settings", func(s *plugin.InboundSpec) {
			*s = tlsSettingsSpec("xhttp", "auto", "")
			s.Settings = json.RawMessage(strings.Replace(string(s.Settings), `"path":"","host":""`, `"path":"/ws","host":""`, 1))
		}, "ws settings require"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := settingsSpec("xhttp", "reality")
			tc.edit(&spec)
			got, err := parseSpec(spec)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("parseSpec: %v", err)
				}
				if got.security != "tls" || (got.transport == "tcp") {
					t.Fatalf("parsed settings = %+v", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseSpec error=%v, want containing %q", err, tc.want)
			}
		})
	}
}
