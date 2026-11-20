package subs_test

import (
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The Telegram Web Proxy is an MTProto profile in the mode "web". Its link goes to the person's page and to the app's sync with
// the mark web, beside the Fake TLS link of the same node and the same key; the apps' links carry no mark at all.
func TestTelegramWebLinkIsMarkedOnThePageAndInSync(t *testing.T) {
	e := newSyncEnv(t, nil)
	var profiles []string
	for _, p := range []struct{ name, settings string }{{"tg 8443", `{"port":8443}`}, {"tg web", `{"port":443,"mode":"web"}`}} {
		pr := must(e.svc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: p.name, SettingsJson: p.settings}))).Msg.Profile
		must(e.svc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: pr.Id, NodeId: "nod_1"})))
		profiles = append(profiles, pr.Id)
	}
	group := must(e.svc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "all", ProfileIds: append([]string{e.profile}, profiles...)}))).Msg.Group.Id
	_, tok := e.personIn("alice", group, true, true)
	_, noTelegramTok := e.personIn("bob", group, false, true)
	device := e.enrolDevice(tok, hwidA)

	sync := e.mustSync(device.Key)
	if len(sync.Telegram) != 2 || strings.Count(sync.raw, `"web":true`) != 1 || strings.Contains(sync.raw, `"web":false`) {
		t.Fatalf("sync: %s", sync.raw)
	}
	var web, fake []string
	for _, l := range sync.Telegram {
		if l.Web {
			web = append(web, l.Link)
		} else {
			fake = append(fake, l.Link)
		}
	}
	if len(web) != 1 || len(fake) != 1 {
		t.Fatalf("web %v, fake tls %v", web, fake)
	}
	wu, err := url.Parse(web[0])
	if err != nil || wu.Scheme != "tg" || wu.Host != "webproxy" || wu.Query().Get("server") != "de1.example.com" || wu.Query().Get("port") != "443" || !strings.HasPrefix(wu.Query().Get("secret"), "dd") {
		t.Fatalf("web link %q: %v", web[0], err)
	}
	if fu, err := url.Parse(fake[0]); err != nil || fu.Host != "proxy" || fu.Query().Get("secret")[2:34] != wu.Query().Get("secret")[2:] {
		t.Errorf("the Fake TLS link %q is not of the same key as the web link", fake[0])
	}

	// the page: the same two links, the mark on the web one only
	page, _ := pageData(t, fetch(e.h, "/"+tok, chrome).Body.String())
	var marked int
	for _, l := range page["telegram"].(map[string]any)["links"].([]any) {
		m := l.(map[string]any)
		if isWeb, _ := m["web"].(bool); isWeb {
			marked++
			if m["url"] != web[0] {
				t.Errorf("the page's web link %v is not the sync's", m["url"])
			}
		} else if _, has := m["web"]; has {
			t.Errorf("a link that is not web has the key: %v", m)
		}
	}
	if marked != 1 {
		t.Errorf("%d web links on the page, want 1", marked)
	}

	// a person without Telegram sees neither
	if strings.Contains(fetch(e.h, "/"+noTelegramTok, chrome).Body.String(), "tg://") {
		t.Error("a person without Telegram has a link")
	}
}
