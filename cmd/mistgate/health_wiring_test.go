package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// HealthService is mounted behind the session with its roles, and the fleet's desired state carries the system
// credential of the synthetic checker next to the users' credentials (and only there).
func TestHealthWiring(t *testing.T) {
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
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

	now := time.Now()
	cookies := map[string]string{}
	for _, role := range []string{"owner", "helper", "readonly"} {
		id, token := "adm_"+role, "session-token-"+role+"-0123456789abcdef"
		if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, ?)`, id, role, role, []byte(id), now.Unix()); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(token))
		if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		cookies[role] = token
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()
	call := func(role, service, method, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.admin.v1."+service+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookies[role]})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// reads: everybody; mute, check now, run doctor: owner and helper; ApplyFix: the owner. A 404 or a
	// precondition answer from the handler shows that the request got past the role check.
	for _, c := range []struct {
		method string
		body   string
		owner  int
		helper int
		readon int
	}{
		{"ListAlerts", `{}`, 200, 200, 200},
		{"GetChecks", `{}`, 200, 200, 200},
		{"GetDoctor", `{}`, 200, 200, 200},
		{"MuteAlert", `{"alertId":"alt_x","durationS":60}`, 404, 404, 403},
		{"RunChecksNow", `{"nodeId":"nod_x"}`, 404, 404, 403},
		{"RunDoctor", `{"nodeId":"nod_x"}`, 404, 404, 403},
		{"ApplyFix", `{"nodeId":"nod_x","fixId":"journald_vacuum","dryRun":true}`, 404, 403, 403},
	} {
		for role, want := range map[string]int{"owner": c.owner, "helper": c.helper, "readonly": c.readon} {
			if code, body := call(role, "HealthService", c.method, c.body); code != want {
				t.Errorf("%s as %s: %d %s, want %d", c.method, role, code, body, want)
			}
		}
	}

	// The desired state of a node with a profile, an inbound and a user: the user's credential and the system one.
	var node struct{ Node struct{ ID string } }
	code, body := call("owner", "NodeService", "CreateEnrollment", `{"name":"de1","address":"node.example.com"}`)
	if code != 200 || json.Unmarshal([]byte(body), &node) != nil {
		t.Fatalf("CreateEnrollment: %d %s", code, body)
	}
	var prof struct{ Profile struct{ ID string } }
	if code, body = call("owner", "ProfileService", "CreateProfile", `{"protocol":"hysteria2","name":"hy2"}`); code != 200 || json.Unmarshal([]byte(body), &prof) != nil {
		t.Fatalf("CreateProfile: %d %s", code, body)
	}
	if code, body = call("owner", "ProfileService", "CreateInbound", `{"profileId":"`+prof.Profile.ID+`","nodeId":"`+node.Node.ID+`"}`); code != 200 {
		t.Fatalf("CreateInbound: %d %s", code, body)
	}
	var grp struct{ Group struct{ ID string } }
	if code, body = call("owner", "GroupService", "CreateGroup", `{"name":"g","profileIds":["`+prof.Profile.ID+`"]}`); code != 200 || json.Unmarshal([]byte(body), &grp) != nil {
		t.Fatalf("CreateGroup: %d %s", code, body)
	}
	if code, body = call("owner", "UserService", "CreateUser", `{"name":"alice","groupId":"`+grp.Group.ID+`"}`); code != 200 {
		t.Fatalf("CreateUser: %d %s", code, body)
	}
	want, err := p.access.Desired(ctx, node.Node.ID)
	if err != nil || len(want) != 1 || len(want[0].Creds) != 1 {
		t.Fatalf("access.Desired: %+v %v", want, err)
	}
	got, err := p.desired(ctx, node.Node.ID)
	if err != nil || len(got) != 1 || len(got[0].Creds) != 2 {
		t.Fatalf("the fleet's desired state: %+v %v", got, err)
	}
	var sys, real int
	for _, c := range got[0].Creds {
		if c.UserID == "" && c.DeviceID == "" {
			sys++
		} else if c.UserID != "" && c.DeviceID != "" {
			real++
		}
	}
	if sys != 1 || real != 1 {
		t.Fatalf("credentials: %d system, %d users': %+v", sys, real, got[0].Creds)
	}
}
