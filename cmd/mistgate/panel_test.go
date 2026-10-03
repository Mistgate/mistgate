package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func TestAgentAddress(t *testing.T) {
	for _, c := range []struct{ flag, listen, public, want string }{
		{"10.0.0.5:9000", "127.0.0.1:8082", "https://example.com", "10.0.0.5:9000"}, // the flag wins
		{"", "127.0.0.1:8082", "http://localhost:8080", "127.0.0.1:8082"},           // a concrete agent listener
		{"", "0.0.0.0:8082", "https://example.com", "example.com:443"},              // a wildcard is not dialable
		{"", ":8082", "https://example.com:8443", "example.com:8443"},
		{"", "", "https://example.com", "example.com:443"},
		{"", "", "", ""},
	} {
		if got := agentAddress(c.flag, c.listen, c.public); got != c.want {
			t.Errorf("agentAddress(%q, %q, %q) = %q, want %q", c.flag, c.listen, c.public, got, c.want)
		}
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// Every admin service is mounted behind the session, the subscription endpoint answers under its secret
// prefix, the enrollment install command carries the flags mistgate-node enroll takes, and no secret path or
// token reaches the log.
func TestPanelWiring(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dbPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key, err := vault.LoadKey(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	vlt, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	var logs lockedBuf
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	in := instance{
		PublicURL: "https://example.com", AdminPrefix: "/", AdminListen: "127.0.0.1:8081", RPID: "localhost",
		RPOrigins: []string{"http://localhost:8081"}, AgentSNI: newAgentSNI("example.com"), SubPrefix: newSecretPrefix(),
	}
	authSvc, err := auth.New(st, auth.Config{RPID: in.RPID, Origins: in.RPOrigins, Vault: vlt}, log)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newPanel(st, vlt, authSvc, panelOpts{in: in, panelAddr: agentAddress("", "", in.PublicURL), title: "Test", dataDir: dir, masterKey: key}, log)
	if err != nil {
		t.Fatal(err)
	}

	// A signed-in admin, made directly in the store.
	const sessionToken = "session-token-for-the-wiring-test-0123456789"
	now := time.Now()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_t', 'test', 'owner', x'0102030405', ?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(sessionToken))
	if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: "adm_t", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()
	call := func(service, method, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.admin.v1."+service+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sessionToken})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, c := range [][2]string{
		{"NodeService", "ListNodes"}, {"FleetService", "Overview"}, {"ProfileService", "ListProfiles"},
		{"UserService", "ListUsers"}, {"GroupService", "ListGroups"},
	} {
		if code, body := call(c[0], c[1], `{}`); code != 200 {
			t.Errorf("%s/%s: %d %s", c[0], c[1], code, body)
		}
	}
	// The AWG and WARP services: the generator of the editor, the WARP service (owner only: this admin is the owner), and the device
	// service, whose methods want an argument, so it is enough that they are mounted (an unknown service answers 404).
	for _, c := range [][2]string{{"AwgService", "ListMimicryPresets"}, {"WarpService", "GetWarpRegistrationParams"}} {
		if code, body := call(c[0], c[1], `{}`); code != 200 {
			t.Errorf("%s/%s: %d %s", c[0], c[1], code, body)
		}
	}
	if code, _ := call("DeviceService", "RenameDevice", `{}`); code == 404 {
		t.Error("DeviceService is not mounted")
	}
	if code, body := call("ProfileService", "ListProtocols", `{}`); code != 200 || !strings.Contains(body, "hysteria2") {
		t.Errorf("hysteria2 is not registered: %d %s", code, body)
	}
	if code, _ := call("NoSuchService", "List", `{}`); code != 404 {
		t.Errorf("an unknown service: %d, want 404 (so the 200s above prove the services are mounted)", code)
	}

	// A group and a user: the subscription link points under the secret prefix and works there.
	code, body := call("GroupService", "CreateGroup", `{"name":"g1"}`)
	var grp struct{ Group struct{ ID string } }
	if code != 200 || json.Unmarshal([]byte(body), &grp) != nil || grp.Group.ID == "" {
		t.Fatalf("CreateGroup: %d %s", code, body)
	}
	code, body = call("UserService", "CreateUser", `{"name":"alice","groupId":"`+grp.Group.ID+`"}`)
	var usr struct {
		User            struct{ ID string }
		SubscriptionURL string
		PagePassword    string
	}
	if code != 200 || json.Unmarshal([]byte(body), &usr) != nil {
		t.Fatalf("CreateUser: %d %s", code, body)
	}
	if !strings.HasPrefix(usr.SubscriptionURL, "https://example.com"+strings.TrimRight(in.SubPrefix, "/")+"/") {
		t.Fatalf("subscription url %q", usr.SubscriptionURL)
	}
	u, _ := url.Parse(usr.SubscriptionURL)
	token := u.Path[strings.LastIndex(u.Path, "/")+1:]
	public := p.srv.Public()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		public.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	sub := get(u.Path)
	if _, err := base64.StdEncoding.DecodeString(sub.Body.String()); err != nil || sub.Code != 200 || sub.Header().Get("Subscription-Userinfo") == "" {
		t.Errorf("subscription: %d %q %v", sub.Code, sub.Body.String(), sub.Header())
	}
	// The page password is wired to the panel's master key: the one the admin sees (create, then the link RPC) is the one the
	// public unlock accepts, and a wrong one is refused. Apps never need it: the subscription above was served without one.
	var link struct{ URL, PagePassword string }
	if code, body := call("UserService", "GetSubscriptionLink", `{"userId":"`+usr.User.ID+`"}`); code != 200 || json.Unmarshal([]byte(body), &link) != nil || link.PagePassword == "" || link.PagePassword != usr.PagePassword {
		t.Fatalf("GetSubscriptionLink: %d %s (create said %q)", code, body, usr.PagePassword)
	}
	unlock := func(pw string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", u.Path+"/unlock", strings.NewReader(`{"password":"`+pw+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		public.ServeHTTP(rec, req)
		return rec
	}
	if rec := unlock("aaaa-bbbb"); rec.Code != 401 {
		t.Errorf("a wrong page password: %d %s", rec.Code, rec.Body.String())
	}
	if rec := unlock(link.PagePassword); rec.Code != 200 || !strings.Contains(rec.Header().Get("Set-Cookie"), "mg_page=") {
		t.Errorf("the right page password: %d %s %v", rec.Code, rec.Body.String(), rec.Header())
	}
	// An unknown token and a path elsewhere on the site are the same page.
	unknown, elsewhere := get(in.SubPrefix+strings.Repeat("a", len(token))), get("/nothing/here")
	if unknown.Code != 404 || unknown.Body.String() != elsewhere.Body.String() {
		t.Errorf("unknown token: %d %q, elsewhere: %d %q", unknown.Code, unknown.Body.String(), elsewhere.Code, elsewhere.Body.String())
	}

	// One link fetched from many networks raises the "link shared" event: the subs handler is
	// wired to the store's event table.
	for i := 0; i < 9; i++ {
		req := httptest.NewRequest("GET", u.Path, nil)
		req.RemoteAddr = fmt.Sprintf("198.51.%d.7:4000", 100+i)
		public.ServeHTTP(httptest.NewRecorder(), req)
	}
	var shared int
	for deadline := time.Now().Add(5 * time.Second); shared == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		st.R.QueryRowContext(ctx, `SELECT count(*) FROM event WHERE code = ?`, subs.EventSharedSuspect).Scan(&shared)
	}
	if shared != 1 {
		t.Errorf("subscription_shared_suspect events: %d, want 1 (is subs.Config.Events wired?)", shared)
	}

	// The install command: exactly the flags `mistgate-node enroll` and `install` take, the binary run from /root.
	code, body = call("NodeService", "CreateEnrollment", `{"name":"de1","address":"node.example.com"}`)
	var enr struct{ InstallCommand string }
	if code != 200 || json.Unmarshal([]byte(body), &enr) != nil {
		t.Fatalf("CreateEnrollment: %d %s", code, body)
	}
	all := strings.Fields(enr.InstallCommand)
	f := []string{}
	if len(all) == 17 {
		f = all[4:14]
	}
	if len(all) != 17 || strings.Join(all[:4], " ") != "chmod +x /root/mistgate-node &&" || strings.Join(all[14:], " ") != "&& /root/mistgate-node install" ||
		f[0] != "/root/mistgate-node" || f[1] != "enroll" || f[2] != "--panel" || f[3] != "example.com:443" || f[4] != "--sni" || f[5] != in.AgentSNI ||
		f[6] != "--ca-sha256" || len(f[7]) != 64 || f[8] != "--token" || f[9] == "" {
		t.Fatalf("install command %q", enr.InstallCommand)
	}

	// Nothing secret was logged: neither the subscription prefix or token, nor the session or enrollment token.
	out := logs.String()
	for name, secret := range map[string]string{
		"subscription prefix": strings.Trim(in.SubPrefix, "/"), "subscription token": token,
		"session token": sessionToken, "enrollment token": f[9], "agent SNI": strings.SplitN(in.AgentSNI, ".", 2)[0],
	} {
		if strings.Contains(out, secret) {
			t.Errorf("the %s is in the log:\n%s", name, out)
		}
	}
}
