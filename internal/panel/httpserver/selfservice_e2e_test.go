package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// The self-service endpoints of the user page behind the real public listener: the mount strips the secret prefix and
// answers every 404 and 405 with the decoy's own page, so the status is the only message of "not found".
func TestSelfServiceThroughThePublicListener(t *testing.T) {
	var inner atomic.Pointer[http.Handler]
	e := newTestEnv(t, func(c *Config) {
		c.PublicMounts = map[string]http.Handler{subPrefix: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*inner.Load()).ServeHTTP(w, r) })}
	})
	ctx := context.Background()
	v, _ := vault.New(make([]byte, vault.KeySize))
	svc, err := access.New(e.st, v, builtin.Registry(), nil, nil, access.Config{SubscriptionBaseURL: "https://sub.example.com" + strings.TrimSuffix(subPrefix, "/")})
	if err != nil {
		t.Fatal(err)
	}
	h := subs.Handler(svc, NewDecoy(""), subs.Config{Title: "Example VPN", MaxWritesPerHour: 7})
	inner.Store(&h)

	if _, err := e.st.W.Exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_1', 'de1', 'de1.example.com', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	prof, err := svc.CreateProfile(ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "awg", Name: "awg31"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateInbound(ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: prof.Msg.Profile.Id, NodeId: "nod_1"})); err != nil {
		t.Fatal(err)
	}
	grp, err := svc.CreateGroup(ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g", ProfileIds: []string{prof.Msg.Profile.Id}}))
	if err != nil {
		t.Fatal(err)
	}
	usr, err := svc.CreateUser(ctx, connect.NewRequest(&adminv1.CreateUserRequest{Name: "alice", GroupId: grp.Msg.Group.Id}))
	if err != nil {
		t.Fatal(err)
	}
	url := usr.Msg.SubscriptionUrl
	token := url[strings.LastIndex(url, "/")+1:]
	base := e.public.URL + subPrefix

	post := func(path, body string, hdr ...string) response {
		t.Helper()
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return response{resp.StatusCode, resp.Header, string(b)}
	}

	// Add: JSON through the mount, not cacheable, and no key in the headers.
	r := post(token+"/devices", `{"profile_id":"`+prof.Msg.Profile.Id+`","platform":"android","label":"Pixel"}`)
	var add struct {
		Device  struct{ ID string }
		Configs []struct{ Conf string }
	}
	if err := json.Unmarshal([]byte(r.body), &add); r.status != 200 || err != nil || !strings.HasPrefix(add.Device.ID, "dev_") || len(add.Configs) != 1 ||
		!strings.Contains(add.Configs[0].Conf, "[Interface]") || r.header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
		t.Fatalf("add: %d %v %v %.200s", r.status, r.header, err, r.body)
	}

	// "Not found" is the decoy page whatever the cause: an unknown token, an unknown device, a path of nobody's.
	want := do(t, http.MethodGet, e.public.URL, "/"+strings.Repeat("z", 43), nil)
	if want.status != 404 {
		t.Fatalf("decoy: %d", want.status)
	}
	for name, got := range map[string]response{
		"unknown token":  post(strings.Repeat("u", 43)+"/devices", `{}`),
		"unknown device": post(token+"/devices/dev_nope/configs", ""),
		"revoked twice": func() response {
			post(token+"/devices/"+add.Device.ID+"/revoke", "")
			return post(token+"/devices/"+add.Device.ID+"/revoke", "")
		}(),
	} {
		if got.status != 404 || got.body != want.body || strings.Contains(got.body, "not_found") {
			t.Errorf("%s: %d %q (want the decoy: %d %q)", name, got.status, got.body, want.status, want.body)
		}
	}

	// Other refusals keep their JSON: they are about a token that works.
	if r := post(token+"/devices", `{}`, "Sec-Fetch-Site", "cross-site"); r.status != 403 || !strings.Contains(r.body, "cross_origin") {
		t.Errorf("cross-site: %d %s", r.status, r.body)
	}
	if r := post(token+"/devices", `{"profile_id":"`+prof.Msg.Profile.Id+`","platform":"toaster"}`); r.status != 400 || !strings.Contains(r.body, `"error":"invalid"`) {
		t.Errorf("bad platform: %d %s", r.status, r.body)
	}
	// Five writes were spent (add, a device that is not there, revoke, revoke, a bad platform): two more fit, the third
	// is over the budget and answers 429 with Retry-After (not swapped for the decoy). The attempt with an unknown
	// token and the cross-site one did not count.
	var last response
	for i := 0; i < 3; i++ {
		last = post(token+"/devices/"+add.Device.ID+"/configs", "")
	}
	if last.status != 429 || last.header.Get("Retry-After") == "" || !strings.Contains(last.body, "too_many_requests") {
		t.Errorf("budget: %d %v %s", last.status, last.header, last.body)
	}
}
