package hysteria2

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	panelhy2 "github.com/mistgate/mistgate/internal/panel/protocols/hysteria2"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The settings the panel plugin produces (DefaultSettings -> BuildInbound -> InboundSpec, a credential from
// IssueCredential) must be accepted by this engine and work with a real client. This is the one place where the
// two halves of the contract meet; a renamed enum value or field on either side fails here.
func TestPanelPluginSpecAppliesOnLoopback(t *testing.T) {
	for _, masq := range []string{"decoy", "none"} {
		t.Run("masquerade_"+masq, func(t *testing.T) {
			r := newRig(t, "")
			p := panelhy2.New()

			raw, err := p.DefaultSettings()
			if err != nil {
				t.Fatal(err)
			}
			var s panelhy2.Settings
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatal(err)
			}
			s.TLSMode, s.SNI, s.Masquerade.Type = "self_signed", serverName, masq
			s.UpMbps, s.DownMbps = 50, 50 // the unit is Mbit/s on both sides
			if raw, err = json.Marshal(s); err != nil {
				t.Fatal(err)
			}
			if errs := p.Validate(raw); len(errs) > 0 {
				t.Fatalf("the plugin rejects its own defaults: %v", errs)
			}
			spec, err := p.BuildInbound(protocols.InboundInput{
				Profile:      protocols.ProfileView{ID: "prf_1", Version: 1, Settings: raw},
				Node:         protocols.NodeView{ID: "nod_1", Name: "de1", Address: "127.0.0.1"},
				PortOverride: uint16(r.udpPort), SpecVersion: 1, Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			spec.ID = "inb_1" // the framework owns the id
			// The panel does not send the node's optional masquerade.tcp_port; keep the test off privileged ports.
			var doc map[string]any
			if err := json.Unmarshal(spec.Settings, &doc); err != nil {
				t.Fatal(err)
			}
			doc["masquerade"].(map[string]any)["tcp_port"] = 0
			if spec.Settings, err = json.Marshal(doc); err != nil {
				t.Fatal(err)
			}

			issued, err := p.IssueCredential(protocols.IssueInput{UserID: "usr_a", DeviceID: "dev_a"})
			if err != nil {
				t.Fatal(err)
			}
			rep, err := r.e.Apply(context.Background(), spec, []plugin.UserCred{
				{CredID: "crd_a", UserID: "usr_a", DeviceID: "dev_a", Data: issued.NodeData},
			})
			if err != nil {
				t.Fatalf("the engine rejects the plugin's spec %s: %v", spec.Settings, err)
			}
			if rep.CredCount != 1 || rep.Cert.PinSHA256 == "" {
				t.Fatalf("report %+v", rep)
			}

			c, _, err := client.NewClient(&client.Config{
				ConnFactory: obfsFactory{s.Obfs.Type, s.Obfs.Password},
				ServerAddr:  &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.udpPort},
				Auth:        issued.Secret,
				TLSConfig:   client.TLSConfig{ServerName: serverName, InsecureSkipVerify: true},
				QUICConfig:  client.QUICConfig{MaxIdleTimeout: 10 * time.Second},
			})
			if err != nil {
				t.Fatalf("a client with the issued secret cannot connect: %v", err)
			}
			defer c.Close()
			if err := echoOnce(c, r.echo, payload(2000)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
