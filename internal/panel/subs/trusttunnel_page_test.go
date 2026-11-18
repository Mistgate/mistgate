package subs_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/pagepass"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	ttplugin "github.com/mistgate/mistgate/internal/panel/protocols/trusttunnel"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/vault"
	ttwire "github.com/mistgate/mistgate/internal/trusttunnel"
)

func trustTunnelPageRig(t *testing.T, tlsMode string) (*enrolEnv, string, string) {
	t.Helper()
	return trustTunnelPageRigWith(t, tlsMode, nil)
}

func trustTunnelPageRigWith(t *testing.T, tlsMode string, mut func(*subs.Config, *store.Store)) (*enrolEnv, string, string) {
	t.Helper()
	e := newEnrolEnvWithRegistry(t, trustTunnelRegistry(t), mut)
	profile := must(e.svc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "trusttunnel", Name: "tt", SettingsJson: `{"port":443,"h3":false,"sni":"de1.example.com","tls_mode":"` + tlsMode + `","egress":"direct"}`,
	}))).Msg.Profile
	must(e.svc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: profile.Id, NodeId: "nod_1"})))
	e.group = must(e.svc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{
		Name: "with tt", ProfileIds: []string{e.profile, profile.Id},
	}))).Msg.Group.Id
	userID, token := e.user("alice", nil)
	e.exec(`UPDATE user SET app_trusttunnel = 1 WHERE id = ?`, userID)
	now := time.Now().UTC()
	deviceID := store.NewID("dev_")
	issued, err := ttplugin.New().IssueCredential(protocols.IssueInput{UserID: userID, DeviceID: deviceID})
	if err != nil {
		t.Fatal(err)
	}
	credentialID := store.NewID("crd_")
	v, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	add := store.TrustTunnelKeyAdd{
		Device: store.AccessDevice{ID: deviceID, UserID: userID, Platform: "other", Model: "TrustTunnel key", Client: "trusttunnel", Key: true},
		Cred: store.AccessCred{ID: credentialID, DeviceID: deviceID, UserID: userID, Protocol: store.TrustTunnelProtocol,
			SecretEnc: v.Seal([]byte(issued.Secret), credentialID), DataJSON: string(issued.NodeData)},
		Limit: 5,
	}
	if err := e.st.Access().AddTrustTunnelKey(e.ctx, add, now); err != nil {
		t.Fatal(err)
	}
	return e, userID, token
}

func (e *enrolEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.ExecContext(e.ctx, query, args...); err != nil {
		e.t.Fatal(err)
	}
}

func TestTrustTunnelLinksAppearInPublicPageData(t *testing.T) {
	e, _, token := trustTunnelPageRig(t, "acme_domain")
	rec := fetch(e.h, "/"+token, chrome)
	if rec.Code != 200 {
		t.Fatalf("page status = %d: %s", rec.Code, rec.Body.String())
	}
	data, _ := pageData(t, rec.Body.String())
	block, ok := data["trust_tunnel"].(map[string]any)
	if !ok {
		t.Fatalf("TrustTunnel page block = %v", data["trust_tunnel"])
	}
	links, _ := block["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("page links = %v, want one", links)
	}
	link := links[0].(map[string]any)
	uri, _ := link["url"].(string)
	decoded, err := ttwire.DecodeDeepLink(uri)
	if err != nil || decoded.Hostname != "de1.example.com" || len(decoded.Addresses) != 1 || decoded.Addresses[0] != "de1.example.com:443" {
		t.Fatalf("page TrustTunnel URL = %q (%+v, %v)", uri, decoded, err)
	}
}

func TestTrustTunnelSelfSignedNoteAppearsInPublicPageData(t *testing.T) {
	e, _, token := trustTunnelPageRig(t, "self_signed")
	rec := fetch(e.h, "/"+token, chrome)
	if rec.Code != 200 {
		t.Fatalf("page status = %d: %s", rec.Code, rec.Body.String())
	}
	data, _ := pageData(t, rec.Body.String())
	block, ok := data["trust_tunnel"].(map[string]any)
	if !ok {
		t.Fatalf("TrustTunnel page block = %v", data["trust_tunnel"])
	}
	links, _ := block["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("page links = %v, want one note-only row", links)
	}
	link := links[0].(map[string]any)
	if link["url"] != "" || link["note"] != "self_signed" {
		t.Fatalf("self-signed page row = %v", link)
	}
}

// The key links are for the person on their own page: not for a locked page (the page password is on), not for an app's fetch of
// the subscription in any format, and not for a link that is nobody's.
func TestTrustTunnelLinksAreOnlyForTheUnlockedPage(t *testing.T) {
	e, _, token := trustTunnelPageRigWith(t, "acme_domain", func(c *subs.Config, _ *store.Store) { c.PageKey = pageKey(t) })

	locked := fetch(e.h, "/"+token, chrome)
	if d, _ := pageData(t, locked.Body.String()); d["locked"] != true || d["trust_tunnel"] != nil || strings.Contains(locked.Body.String(), "tt://") {
		t.Fatalf("the locked page shows the key: locked=%v block=%v", d["locked"], d["trust_tunnel"])
	}
	unlock := call{e.h, t, token}.post("/unlock", string(mustJSON(map[string]string{"password": pagepass.Password(pageKey(t), token, 0)})))
	if unlock.Code != 200 {
		t.Fatalf("unlock = %d %s", unlock.Code, unlock.Body.String())
	}
	open := fetch(e.h, "/"+token, chrome, "Cookie", cookieOf(t, unlock).Name+"="+cookieOf(t, unlock).Value)
	if d, _ := pageData(t, open.Body.String()); d["trust_tunnel"] == nil {
		t.Fatalf("the unlocked page has no key block: %v", d["locked"])
	}

	for _, ua := range []string{happUA, curlUA, "clash.meta/1.19", "sing-box/1.12.0", "Hiddify/2.5"} {
		rec := fetch(e.h, "/"+token, ua)
		body := rec.Body.String()
		if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body)); err == nil {
			body += string(decoded)
		}
		if strings.Contains(body, "tt://") || strings.Contains(strings.ToLower(body), "trusttunnel") {
			t.Errorf("a %q fetch carries the key: %.200s", ua, body)
		}
	}
	if rec := fetch(e.h, "/"+unknown, chrome); strings.Contains(rec.Body.String(), "tt://") || strings.Contains(rec.Body.String(), "trust_tunnel") {
		t.Errorf("an unknown link answers with key data: %.200s", rec.Body.String())
	}
}
