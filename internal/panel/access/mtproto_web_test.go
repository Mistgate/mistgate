package access

import (
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The Telegram Web Proxy is an MTProto profile in the mode "web": de1 serves it by its own name on 443 and the Fake TLS proxy on
// 8443, fi1 is a relay entry.
type webFix struct {
	*env
	group string
	web   *adminv1.Inbound
	fake  *adminv1.Inbound
}

func newWebFix(t *testing.T) *webFix {
	t.Helper()
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_fi1", "fi1", "fi1.example.com", "active")
	e.sql(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id = 'nod_fi1'`)
	e.sql(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_de1'`)
	web := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: "tg web", SettingsJson: `{"port":443,"mode":"web"}`}))).Msg.Profile
	fake := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: "tg 8443", SettingsJson: `{"port":8443}`}))).Msg.Profile
	return &webFix{env: e, group: e.group("tg", web.Id, fake.Id), web: e.inbound(web.Id, "nod_de1"), fake: e.inbound(fake.Id, "nod_de1")}
}

func TestTelegramWebLinkAndItsTwins(t *testing.T) {
	f := newWebFix(t)
	alice := must(f.s.CreateUser(f.ctx, req(&adminv1.CreateUserRequest{Name: "alice", GroupId: f.group, Apps: &adminv1.AppToggles{Happ: true, Telegram: true}}))).Msg.User

	var web, fake []*adminv1.TelegramProxyLink
	for _, l := range must(f.s.GetTelegramLinks(f.ctx, req(&adminv1.GetTelegramLinksRequest{UserId: alice.Id}))).Msg.Links {
		if l.Web {
			web = append(web, l)
		} else {
			fake = append(fake, l)
		}
	}
	// the Web Proxy: one link, no IPv6 twin (the link names the host, not an address); the Fake TLS proxy of the node keeps its twin
	if len(web) != 1 || web[0].Ipv6 || web[0].Relay || web[0].NodeName != "de1" || web[0].ProfileName != "tg web" {
		t.Fatalf("web links: %+v", web)
	}
	if len(fake) != 2 {
		t.Fatalf("fake tls links: %+v", fake)
	}
	u, err := url.Parse(web[0].Url)
	if err != nil || u.Scheme != "tg" || u.Host != "webproxy" || u.Query().Get("server") != "de1.example.com" || u.Query().Get("port") != "443" {
		t.Fatalf("web link %q: %v", web[0].Url, err)
	}
	secret := u.Query().Get("secret")
	if !strings.HasPrefix(secret, "dd") || len(secret) != 34 {
		t.Fatalf("secret %q", secret)
	}
	if _, err := hex.DecodeString(secret[2:]); err != nil {
		t.Fatal(err)
	}
	// one key of the person for both proxies
	for _, l := range fake {
		if keyOf(t, l.Url) != secret[2:] {
			t.Errorf("the Fake TLS link has another key: %s", l.Url)
		}
	}
	// the node is told the mode, and the key
	var told bool
	for _, in := range must(f.s.Desired(f.ctx, "nod_de1")) {
		if in.Spec.ID == f.web.Id {
			told = true
			if string(in.Spec.Settings) != `{"domains":["de1.example.com"],"fallback":"decoy","mode":"web"}` || in.Spec.Listen.Port != 443 || len(in.Creds) != 1 {
				t.Errorf("the web inbound is told %s, port %d, %d keys", in.Spec.Settings, in.Spec.Listen.Port, len(in.Creds))
			}
		}
	}
	if !told {
		t.Error("the web inbound is not in the desired state")
	}
}

// A relay would carry a copy of the link at another address: the Web Proxy is refused, with the reason the admin reads.
func TestRelayOfTheWebProxyIsRefused(t *testing.T) {
	f := newWebFix(t)
	_, err := f.s.CreateRelay(f.ctx, req(&adminv1.CreateRelayRequest{InboundId: f.web.Id, ViaNodeId: "nod_fi1"}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "relay_unsupported", map[string]string{"reason": "webproxy"})
}

// The web inbound takes the port 443 only: a port override on the node is refused at once.
func TestWebProxyPortOverride(t *testing.T) {
	f := newWebFix(t)
	web := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: "tg web 2", SettingsJson: `{"port":443,"mode":"web"}`}))).Msg.Profile
	if _, err := f.s.CreateInbound(f.ctx, req(&adminv1.CreateInboundRequest{ProfileId: web.Id, NodeId: "nod_fi1", PortOverride: 8443, TlsServerNameOverride: "tg.example.com"})); err == nil {
		t.Error("a port override of 8443 was accepted for the Web Proxy")
	}
	if _, err := f.s.CreateInbound(f.ctx, req(&adminv1.CreateInboundRequest{ProfileId: web.Id, NodeId: "nod_fi1", PortOverride: 443, TlsServerNameOverride: "tg.example.com"})); err != nil {
		t.Errorf("a port override of 443: %v", err)
	}
}
