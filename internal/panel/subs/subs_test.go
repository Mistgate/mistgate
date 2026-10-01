package subs_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const decoyBody = "<html>decoy 404</html>"

var decoy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(decoyBody))
})

type rig struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	svc     *access.Service
	h       http.Handler
	group   string
	profile string
	clock   time.Time
}

func newRig(t *testing.T, prefix string) *rig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, _ := vault.New(make([]byte, vault.KeySize))
	svc, err := access.New(st, v, builtin.Registry(), nil, nil, access.Config{SubscriptionBaseURL: "https://sub.example.com" + prefix})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, ctx: ctx, st: st, svc: svc, h: subs.Handler(svc, decoy, subs.Config{Prefix: prefix, Title: "Example VPN"}), clock: time.Now()}
	if _, err := st.W.Exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_1', 'de1', 'de1.example.com', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	prof := must(svc.CreateProfile(ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "p"}))).Msg.Profile
	must(svc.CreateInbound(ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: prof.Id, NodeId: "nod_1"})))
	r.profile = prof.Id
	r.group = must(svc.CreateGroup(ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g", ProfileIds: []string{prof.Id}}))).Msg.Group.Id
	return r
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func (r *rig) user(name string, mut func(*adminv1.CreateUserRequest)) (id, token string) {
	m := &adminv1.CreateUserRequest{Name: name, GroupId: r.group}
	if mut != nil {
		mut(m)
	}
	resp := must(r.svc.CreateUser(r.ctx, connect.NewRequest(m))).Msg
	return resp.User.Id, resp.SubscriptionUrl[strings.LastIndex(resp.SubscriptionUrl, "/")+1:]
}

func (r *rig) get(method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func decode(t *testing.T, body string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("body is not base64: %v", err)
	}
	return string(b)
}

func TestActiveUser(t *testing.T) {
	r := newRig(t, "/k3xq8")
	_, token := r.user("alice", func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 5000; m.TermDays = 30 })
	rec := r.get("GET", "/k3xq8/"+token)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	h := rec.Header()
	if h.Get("Cache-Control") != "no-store" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", h)
	}
	if title := h.Get("Profile-Title"); !strings.HasPrefix(title, "base64:") || decode(t, strings.TrimPrefix(title, "base64:")) != "Example VPN" {
		t.Errorf("profile-title = %q", title)
	}
	exp := time.Now().AddDate(0, 0, 30).Unix()
	var e int64
	var up, down, total uint64
	if _, err := sscanf(h.Get("Subscription-Userinfo"), &up, &down, &total, &e); err != nil || up != 0 || down != 0 || total != 5000 || e < exp-5 || e > exp+5 {
		t.Errorf("subscription-userinfo = %q (%v)", h.Get("Subscription-Userinfo"), err)
	}
	if h.Get("Profile-Update-Interval") != "12" {
		t.Errorf("interval = %q", h.Get("Profile-Update-Interval"))
	}
	lines := strings.Split(decode(t, rec.Body.String()), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "hysteria2://") || !strings.Contains(lines[0], "@de1.example.com:443/?obfs=salamander&obfs-password=") {
		t.Errorf("lines = %q", lines)
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Error("the token leaked into the body")
	}

	// HEAD works (net/http drops the body on a real server); other methods get the decoy.
	if rec := r.get("HEAD", "/k3xq8/"+token); rec.Code != 200 {
		t.Errorf("HEAD: %d", rec.Code)
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "OPTIONS"} {
		if rec := r.get(m, "/k3xq8/"+token); rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Errorf("%s answered %d %q", m, rec.Code, rec.Body.String())
		}
	}
}

// sscanf parses "upload=..; download=..; total=..; expire=.." (the format is part of the contract).
func sscanf(s string, up, down, total *uint64, exp *int64) (int, error) {
	parts := strings.Split(s, "; ")
	if len(parts) != 4 {
		return 0, errors.New("want 4 fields")
	}
	var vals [4]int64
	for i, key := range []string{"upload=", "download=", "total=", "expire="} {
		v, ok := strings.CutPrefix(parts[i], key)
		if !ok {
			return i, errors.New("missing " + key)
		}
		var n int64
		for _, c := range v {
			if c < '0' || c > '9' {
				return i, errors.New("not a number: " + v)
			}
			n = n*10 + int64(c-'0')
		}
		vals[i] = n
	}
	*up, *down, *total, *exp = uint64(vals[0]), uint64(vals[1]), uint64(vals[2]), vals[3]
	return 4, nil
}

func TestStatusesGetEmptyListWithHeaders(t *testing.T) {
	r := newRig(t, "/k3xq8")
	limited, limTok := r.user("limited", func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 1000 })
	expired, expTok := r.user("expired", func(m *adminv1.CreateUserRequest) { m.TermDays = 1 })
	disabled, disTok := r.user("disabled", nil)
	_, okTok := r.user("fine", nil)

	r.st.W.Exec(`UPDATE user SET used_bytes = 1000 WHERE id = ?`, limited)
	r.st.W.Exec(`UPDATE user SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).Unix(), expired)
	r.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, disabled)
	r.st.W.Exec(`INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down) VALUES (?, 'nod_1', 'hysteria2', ?, 250, 750)`,
		limited, time.Now().Unix()/3600*3600)
	if err := r.svc.Recompute(r.ctx, []string{limited, expired, disabled}); err != nil {
		t.Fatal(err)
	}

	// No server, only the entry that names the reason (it never connects: nothing listens on 0.0.0.0:1).
	for name, want := range map[string]struct{ token, note string }{
		"limited":  {limTok, "Traffic used up, resets on "},
		"expired":  {expTok, "Subscription ended on "},
		"disabled": {disTok, "Access paused"},
	} {
		rec := r.get("GET", "/k3xq8/"+want.token)
		lines := strings.Split(decode(t, rec.Body.String()), "\n")
		if rec.Code != 200 || len(lines) != 1 || !strings.HasPrefix(lines[0], "hysteria2://off@0.0.0.0:1/#") {
			t.Errorf("%s: status %d lines %q", name, rec.Code, lines)
		}
		if note := decodeQueryName(t, lines[0]); !strings.HasPrefix(note, want.note) {
			t.Errorf("%s: the entry says %q, want %q…", name, note, want.note)
		}
		if rec.Header().Get("Profile-Title") == "" || rec.Header().Get("Subscription-Userinfo") == "" || rec.Header().Get("Profile-Update-Interval") != "1" {
			t.Errorf("%s: headers %v", name, rec.Header())
		}
	}
	rec := r.get("GET", "/k3xq8/"+limTok)
	if got := rec.Header().Get("Subscription-Userinfo"); got != "upload=250; download=750; total=1000; expire=0" {
		t.Errorf("limited userinfo = %q", got)
	}
	if rec := r.get("GET", "/k3xq8/"+okTok); decode(t, rec.Body.String()) == "" {
		t.Error("the active user got nothing")
	}
}

func TestUnknownGetsDecoy(t *testing.T) {
	r := newRig(t, "/k3xq8")
	_, token := r.user("alice", nil)
	wrong := token[:len(token)-1] + map[bool]string{true: "A", false: "B"}[token[len(token)-1] != 'A']
	for name, path := range map[string]string{
		"wrong token":   "/k3xq8/" + wrong,
		"too short":     "/k3xq8/abc",
		"bad chars":     "/k3xq8/" + strings.Repeat("!", 43),
		"no token":      "/k3xq8/",
		"bare prefix":   "/k3xq8",
		"extra segment": "/k3xq8/" + token + "/x",
		"wrong prefix":  "/other/" + token,
		"root":          "/",
	} {
		rec := r.get("GET", path)
		if rec.Code != 404 || rec.Body.String() != decoyBody || rec.Header().Get("Subscription-Userinfo") != "" || rec.Header().Get("Profile-Title") != "" {
			t.Errorf("%s: %d %q %v", name, rec.Code, rec.Body.String(), rec.Header())
		}
	}
}

func TestNoPrefixMount(t *testing.T) {
	// Mounted behind a mux that already stripped the prefix: the request path is just /<token>.
	r := newRig(t, "/k3xq8")
	_, token := r.user("alice", nil)
	if rec := r.get("GET", "/"+token); rec.Code != 200 {
		t.Errorf("stripped mount: %d", rec.Code)
	}
	// A handler without any prefix works the same.
	r2 := newRig(t, "")
	_, token2 := r2.user("alice", nil)
	if rec := r2.get("GET", "/"+token2); rec.Code != 200 {
		t.Errorf("no prefix: %d", rec.Code)
	}
}

func TestRotatedLinkDies(t *testing.T) {
	r := newRig(t, "/k3xq8")
	id, old := r.user("alice", nil)
	must(r.svc.GetSubscriptionLink(r.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: id, Rotate: true})))
	if rec := r.get("GET", "/k3xq8/"+old); rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Errorf("old link: %d", rec.Code)
	}
}

func TestStoreErrorIsNotADecoy(t *testing.T) {
	h := subs.Handler(failing{}, decoy, subs.Config{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/"+strings.Repeat("a", 43), nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
}

type failing struct{}

func (failing) Subscription(context.Context, string) (access.SubView, error) {
	return access.SubView{}, errors.New("boom")
}
