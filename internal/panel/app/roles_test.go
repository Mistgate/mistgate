//go:build !js

package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	nodeprovision "github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	roleAdminPrefix = "/admin-4f2a/"
	roleSubPrefix   = "/sub-9c1d/"
	roleLinkPrefix  = "/link-7b3e5a9d1c2f4e6a/"
)

// roleConfig is the configuration every role of one edge installation gets: the same database and keys, the same instance.
func roleConfig(t *testing.T, remote *edgeTickRemote) Config {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	vlt, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc, err := auth.New(st, auth.Config{RPID: "example.com", Origins: []string{"https://example.com"}, Vault: vlt}, log)
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Store: st, Vault: vlt, Auth: authSvc, Clock: time.Now, Logger: log,
		Instance: InstanceConfig{
			PublicURL: "https://example.com", AdminPrefix: roleAdminPrefix, RPID: "example.com",
			RPOrigins: []string{"https://example.com"}, AgentSNI: "agent.example.com", SubPrefix: roleSubPrefix, LinkPrefix: roleLinkPrefix,
		},
		DataDir: t.TempDir(), MasterKey: key, Remote: remote,
		AfterResponse: func(work func()) { work() },
		SSHClient:     nodeprovision.NewClient(),
	}
}

type edgeRoles struct {
	whole, public, link, jobs, admin, provision *Panel
}

func buildEdgeRoles(t *testing.T) (edgeRoles, *edgeTickRemote) {
	t.Helper()
	remote := &edgeTickRemote{}
	c := roleConfig(t, remote)
	publicConfig := c
	publicConfig.Auth = nil // the public Worker builds no auth service
	var r edgeRoles
	for _, step := range []struct {
		name  string
		build func(Config) (*Panel, error)
		cfg   Config
		into  **Panel
	}{
		{"Build", Build, c, &r.whole},
		{"BuildPublic", BuildPublic, publicConfig, &r.public},
		{"BuildLink", BuildLink, c, &r.link},
		{"BuildJobs", BuildJobs, c, &r.jobs},
		{"BuildAdmin", BuildAdmin, c, &r.admin},
		{"BuildProvision", BuildProvision, c, &r.provision},
	} {
		p, err := step.build(step.cfg)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		*step.into = p
	}
	return r, remote
}

// The edge runs the panel as several Workers, each built by its own function. Build is the whole panel, and it is what the
// VPS runs: every service and every handler of Build must belong to some role, so a service wired into Build and forgotten in
// a role (or the other way round) fails here and not in production.
func TestEdgeRolesCoverBuild(t *testing.T) {
	r, remote := buildEdgeRoles(t)
	roles := map[string]*Panel{"public": r.public, "link": r.link, "jobs": r.jobs, "admin": r.admin, "provision": r.provision}

	// Services: every service Build wires is held by some role.
	whole := reflect.ValueOf(*r.whole)
	for i := 0; i < whole.NumField(); i++ {
		field := whole.Type().Field(i)
		if !field.IsExported() || field.Type.Kind() != reflect.Pointer || whole.Field(i).IsNil() || field.Name == "Server" {
			continue
		}
		held := false
		for _, p := range roles {
			held = held || !reflect.ValueOf(*p).Field(i).IsNil()
		}
		if !held {
			t.Errorf("Panel.%s is wired by Build and by no role", field.Name)
		}
	}

	// Admin handlers: the admin and the Provisioner together serve what Build does, and the Provisioner only installs.
	union := func(pick func(Surface) []string, ps ...*Panel) []string {
		var out []string
		for _, p := range ps {
			for _, s := range pick(p.Surface) {
				if !slices.Contains(out, s) {
					out = append(out, s)
				}
			}
		}
		slices.Sort(out)
		return out
	}
	api := func(s Surface) []string { return s.AdminAPI }
	pages := func(s Surface) []string { return s.AdminPages }
	mounts := func(s Surface) []string { return s.PublicMounts }
	if got, want := union(api, r.admin, r.provision), union(api, r.whole); !slices.Equal(got, want) {
		t.Errorf("admin API of the admin and the Provisioner = %v, Build = %v", got, want)
	}
	if got, want := union(pages, r.admin, r.provision), union(pages, r.whole); !slices.Equal(got, want) {
		t.Errorf("admin pages of the admin and the Provisioner = %v, Build = %v", got, want)
	}
	if got := union(api, r.provision); len(got) != 1 || got[0] != "/mistgate.admin.v1.ProvisioningService/" {
		t.Errorf("the Provisioner serves %v, want the ProvisioningService alone", got)
	}
	if r.admin.Surface.MCP != r.whole.Surface.MCP || r.admin.Surface.Preview != r.whole.Surface.Preview || !r.admin.Surface.Preview {
		t.Errorf("admin MCP/preview = %v/%v, Build = %v/%v", r.admin.Surface.MCP, r.admin.Surface.Preview, r.whole.Surface.MCP, r.whole.Surface.Preview)
	}

	// The public mount is the public Worker's alone, and the link is the link Worker's alone.
	if got, want := union(mounts, r.public), union(mounts, r.whole); len(want) != 1 || !slices.Equal(got, want) {
		t.Errorf("public mounts of the public role = %v, Build = %v", got, want)
	}
	for name, p := range roles {
		if name != "public" && len(p.Surface.PublicMounts) != 0 {
			t.Errorf("role %s mounts %v: only the public role serves subscriptions", name, p.Surface.PublicMounts)
		}
		if name != "link" && p.Surface.Link != "" {
			t.Errorf("role %s serves the agent link %q: only the link role does", name, p.Surface.Link)
		}
		if name != "admin" && name != "provision" && (len(p.Surface.AdminAPI) != 0 || len(p.Surface.AdminPages) != 0) {
			t.Errorf("role %s serves admin handlers %v %v", name, p.Surface.AdminAPI, p.Surface.AdminPages)
		}
	}
	if r.link.Surface.Link != r.whole.Surface.Link || r.link.Surface.Link == "" {
		t.Errorf("link prefix of the link role = %q, Build = %q", r.link.Surface.Link, r.whole.Surface.Link)
	}

	// The scheduled work: the jobs role runs the whole tick (sweeps, node-down events, the safety-net fan-out).
	now := time.Date(2026, 10, 9, 12, 10, 0, 0, time.UTC)
	addExpiredAlice(t, r.jobs, now)
	nodeID := addSilentNodeA(t, r.jobs, now)
	if err := r.jobs.EdgeTick(context.Background(), now); err != nil {
		t.Fatalf("EdgeTick of the jobs role: %v", err)
	}
	var status string
	if err := r.jobs.Store.R.QueryRowContext(context.Background(), `SELECT status FROM user WHERE name = 'alice'`).Scan(&status); err != nil || status != "expired" {
		t.Errorf("user status after the jobs tick = %q (%v), want expired", status, err)
	}
	assertNodeDownCount(t, r.jobs.Store, nodeID, 1)
	if remote.pokeCount() == 0 {
		t.Error("the jobs tick poked no node")
	}
}

// Every role answers the decoy, the same page as for any unknown path, on the secret paths of the surfaces other Workers
// serve: a request that reaches the wrong Worker learns nothing.
func TestEdgeRolesAnswerTheDecoyOnForeignSurfaces(t *testing.T) {
	r, _ := buildEdgeRoles(t)
	get := func(p *Panel, path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "https://example.com"+path, nil)
		rec := httptest.NewRecorder()
		p.Handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	foreign := map[string][]string{
		"admin":     {roleSubPrefix + "any-token", roleSubPrefix + "any-token/json", roleLinkPrefix + "link/nod_x"},
		"public":    {roleAdminPrefix, roleAdminPrefix + "api/mistgate.admin.v1.NodeService/ListNodes", roleLinkPrefix + "link/nod_x"},
		"link":      {roleAdminPrefix, roleSubPrefix + "any-token", roleSubPrefix + "any-token/json"},
		"provision": {roleSubPrefix + "any-token", roleLinkPrefix + "link/nod_x"},
	}
	panels := map[string]*Panel{"admin": r.admin, "public": r.public, "link": r.link, "provision": r.provision}
	for name, paths := range foreign {
		p := panels[name]
		wantCode, wantBody := get(p, "/nothing-here/")
		if wantCode != http.StatusNotFound {
			t.Fatalf("%s: unknown path = %d, want the decoy 404", name, wantCode)
		}
		for _, path := range paths {
			if code, body := get(p, path); code != wantCode || body != wantBody {
				t.Errorf("%s answers %s with %d (%d bytes), want the decoy (%d, %d bytes)", name, path, code, len(body), wantCode, len(wantBody))
			}
		}
	}
}

// A person created through the admin Worker is served by the public Worker, which builds its own access service over the
// same database; no other Worker answers the link.
func TestEdgeSubscriptionIsServedByThePublicRoleAlone(t *testing.T) {
	r, _ := buildEdgeRoles(t)
	ctx := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner, DisplayName: "alice"})
	group, err := r.admin.Access.CreateGroup(ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "family"}))
	if err != nil {
		t.Fatal(err)
	}
	user, err := r.admin.Access.CreateUser(ctx, connect.NewRequest(&adminv1.CreateUserRequest{Name: "bob", GroupId: group.Msg.Group.Id}))
	if err != nil {
		t.Fatal(err)
	}
	link := user.Msg.SubscriptionUrl
	if !strings.HasPrefix(link, "https://example.com"+roleSubPrefix) {
		t.Fatalf("subscription link %q does not start with the subscription prefix", link)
	}
	get := func(p *Panel) (int, string) {
		req := httptest.NewRequest(http.MethodGet, link, nil)
		req.Header.Set("User-Agent", "Happ/2.0")
		rec := httptest.NewRecorder()
		p.Handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := get(r.public); code != http.StatusOK {
		t.Fatalf("public role answers the person's link with %d (%d bytes)", code, len(body))
	}
	for name, p := range map[string]*Panel{"admin": r.admin, "link": r.link, "provision": r.provision} {
		if c, _ := get(p); c != http.StatusNotFound {
			t.Errorf("%s role answers the person's link with %d", name, c)
		}
	}
}
