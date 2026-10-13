package hysteria2

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apernet/quic-go/http3"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestDecoyModesOnHysteriaTCPAndHTTP3(t *testing.T) {
	site := decoy.BuiltinSite(t.TempDir())
	udpPort, tcpPort := freeUDPPort(t), freeTCPPort(t)
	dns := fakeDNS(t, map[string]string{"app.example.test.": "127.0.0.1"})
	resolvers := func() []string { return []string{dns} }
	direct := egress.New(resolvers, egress.AllowPrivate())
	e, err := New(engine.Env{
		Certs: certs.New(t.TempDir()), DNS: resolvers,
		Egress:     func(string) (engine.Egress, error) { return direct, nil },
		Masquerade: func(string) http.Handler { return site.Handler() },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(context.Background())
	spec := plugin.InboundSpec{
		ID: "inb_decoy", Protocol: Protocol, ProfileID: "prf_decoy", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: uint16(udpPort)},
		TLS:    plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: serverName}, Egress: "direct",
		Settings: json.RawMessage(fmt.Sprintf(`{"masquerade":{"tcp_port":%d}}`, tcpPort)),
	}
	if _, err := e.Apply(context.Background(), spec, []plugin.UserCred{cred("a")}); err != nil {
		t.Fatal(err)
	}

	panel, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("panel site")}})
	if err != nil {
		t.Fatal(err)
	}
	own, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("node site")}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		mode, hash, want string
		files            []decoy.File
	}{
		{mode: decoy.ModeBuiltin, want: "Coming soon"},
		{mode: decoy.ModePanel, hash: panel.Hash(), want: "panel site", files: panel.Files()},
		{mode: decoy.ModeOwn, hash: own.Hash(), want: "node site", files: own.Files()},
	}
	tlsConfig := &tls.Config{InsecureSkipVerify: true, ServerName: serverName}
	tcp := &http.Transport{TLSClientConfig: tlsConfig}
	h3 := &http3.Transport{TLSClientConfig: tlsConfig}
	defer tcp.CloseIdleConnections()
	defer h3.Close()
	clients := map[string]struct {
		roundTripper http.RoundTripper
		url          string
	}{
		"tcp":   {roundTripper: tcp, url: fmt.Sprintf("https://127.0.0.1:%d", tcpPort)},
		"http3": {roundTripper: h3, url: fmt.Sprintf("https://127.0.0.1:%d", udpPort)},
	}
	for _, tc := range cases {
		if _, err := site.Apply(tc.mode, tc.hash, tc.files); err != nil {
			t.Fatalf("set %s mode: %v", tc.mode, err)
		}
		for transport, c := range clients {
			client := &http.Client{Transport: c.roundTripper, Timeout: 10 * time.Second}
			resp, err := client.Get(c.url + "/")
			if err != nil {
				t.Fatalf("%s %s GET: %v", tc.mode, transport, err)
			}
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), tc.want) {
				t.Errorf("%s %s = %d %q, err=%v; want %q", tc.mode, transport, resp.StatusCode, body, readErr, tc.want)
			}
		}
	}
}
