//go:build !js

package vless

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
	"github.com/xtls/xray-core/transport/internet/websocket"

	"github.com/mistgate/mistgate/internal/panel/protocols/jsoncheck"
	"github.com/mistgate/mistgate/internal/plugin"
)

// checkWithCores asks the real cores: Xray's own config builder in process, and every sing-box binary found (the
// check is skipped without one: set SINGBOX_BIN, or download into ~/.cache/mistgate-tests/).
func checkWithCores(t *testing.T, format plugin.ClientFormat, outbound []byte) {
	t.Helper()
	switch format {
	case plugin.FormatXrayJSON:
		jsoncheck.XrayBuild(t, jsoncheck.XrayConfig(t, outbound))
	case plugin.FormatSingbox:
		if !jsoncheck.SingboxCheck(t, jsoncheck.SingboxConfig(t, outbound)) {
			t.Log("no sing-box binary: set SINGBOX_BIN; the sing-box check is skipped")
		}
	}
}

func xrayBuilt(t *testing.T, row string) (data []byte, stream *internet.StreamConfig) {
	t.Helper()
	for _, r := range jsonRows {
		if r.file == row {
			f, ok := New().Render(r.in(t, plugin.FormatXrayJSON))
			if !ok {
				t.Fatalf("%s refused", row)
			}
			return f.Data, jsoncheck.XrayStreams(t, jsoncheck.XrayConfig(t, f.Data))[0]
		}
	}
	t.Fatalf("no row %s", row)
	return nil, nil
}

// What Xray's config builder makes of the fragments is what the matrix says: the REALITY block, the XHTTP fields
// (and `extra` gives the same object as the direct xPaddingBytes key), the TLS names and ALPN, the WebSocket path.
func TestXrayCoreReadsTheVlessFields(t *testing.T) {
	data, st := xrayBuilt(t, "reality_xhttp_full")
	s := st
	sh := jsoncheck.XrayTransport(t, s, "splithttp").(*splithttp.Config)
	if sh.Path != "/q8x2kd7w" || sh.Host != "cover.example.org" || sh.Mode != "packet-up" || sh.XPaddingBytes.From != 120 || sh.XPaddingBytes.To != 360 {
		t.Errorf("splithttp = %v", sh)
	}
	rc := jsoncheck.XraySecurity(t, s).(*reality.Config)
	if rc.ServerName != "second.example.org" || rc.Fingerprint != "firefox" || len(rc.PublicKey) != 32 || len(rc.ShortId) != 8 || rc.ShortId[0] != 0xab || rc.ShortId[1] != 0 || rc.SpiderX != "/57999bb8" {
		t.Errorf("reality = %v", rc)
	}

	// The same object through `extra`, as the share link carries it: conf.SplitHTTPConfig.Build takes everything but
	// host, path and mode from there, so the core's result is the same message.
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	x := doc["streamSettings"].(map[string]any)["xhttpSettings"].(map[string]any)
	delete(x, "xPaddingBytes")
	x["extra"] = map[string]any{"xPaddingBytes": "120-360"}
	viaExtra, _ := json.Marshal(doc)
	sh2 := jsoncheck.XrayTransport(t, jsoncheck.XrayStreams(t, jsoncheck.XrayConfig(t, viaExtra))[0], "splithttp").(*splithttp.Config)
	if !proto.Equal(sh, sh2) {
		t.Errorf("extra and the direct key differ: %v / %v", sh, sh2)
	}

	// Default padding: not written, the core's own default applies.
	_, st = xrayBuilt(t, "reality_xhttp")
	sh = jsoncheck.XrayTransport(t, st, "splithttp").(*splithttp.Config)
	if sh.Mode != "auto" || sh.XPaddingBytes.From != 0 || sh.XPaddingBytes.To != 0 {
		t.Errorf("default splithttp = %v", sh)
	}

	_, st = xrayBuilt(t, "tls_cdn_ws")
	s = st
	tc := jsoncheck.XraySecurity(t, s).(*xtls.Config)
	ws := jsoncheck.XrayTransport(t, s, "websocket").(*websocket.Config)
	if tc.ServerName != "cdn.example.org" || tc.Fingerprint != "chrome" || len(tc.NextProtocol) != 1 || tc.NextProtocol[0] != "http/1.1" || tc.AllowInsecure || ws.Path != "/ws" || ws.Host != "cdn.example.org" {
		t.Errorf("tls ws = %v %v", tc, ws)
	}

	_, st = xrayBuilt(t, "tls_direct_xhttp")
	tc = jsoncheck.XraySecurity(t, st).(*xtls.Config)
	if tc.ServerName != "origin.example.com" || len(tc.NextProtocol) != 2 || tc.NextProtocol[0] != "h2" {
		t.Errorf("tls xhttp = %v", tc)
	}
}
