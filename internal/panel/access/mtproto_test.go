package access

import (
	"net/url"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestGetTelegramLinks(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "mtproto", Name: "tg 443", SettingsJson: `{"port":443}`,
	}))).Msg.Profile
	e.inbound(p.Id, "nod_de1")
	g := e.group("tg", p.Id)
	u := e.userBare("alice", g, func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: true}
	})
	r := must(e.s.GetTelegramLinks(e.ctx, req(&adminv1.GetTelegramLinksRequest{UserId: u.User.Id})))
	if len(r.Msg.Links) != 1 {
		t.Fatalf("links = %+v", r.Msg.Links)
	}
	link := r.Msg.Links[0]
	if link.NodeName != "de1" || link.ProfileName != "tg 443" {
		t.Fatalf("link facts = %+v", link)
	}
	uri, err := url.Parse(link.Url)
	if err != nil || uri.Scheme != "tg" || uri.Host != "proxy" {
		t.Fatalf("link = %q, %v", link.Url, err)
	}
	secret := uri.Query().Get("secret")
	if !strings.HasPrefix(secret, "ee") || len(secret) != 2+32+2*len("www.google.com") {
		t.Fatalf("secret = %q", secret)
	}

	off := e.userBare("bob", g, func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: false}
	})
	r = must(e.s.GetTelegramLinks(e.ctx, req(&adminv1.GetTelegramLinksRequest{UserId: off.User.Id})))
	if len(r.Msg.Links) != 0 {
		t.Fatalf("links with Telegram off = %+v", r.Msg.Links)
	}
}
