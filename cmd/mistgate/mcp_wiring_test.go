package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// The MCP endpoint on a real panel (loopback only): real token store, real auth, real services. The chain a dangerous change
// takes, plan, owner's approval with a step-up, apply; what a token cannot reach; what the audit log says.

type bearerClient struct{ secret string }

func (b bearerClient) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.secret)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPEndToEnd(t *testing.T) {
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
	for role, age := range map[string]time.Duration{"owner": 0, "staleowner": time.Hour} {
		id, token := "adm_"+role, "session-token-"+role+"-0123456789abcdef"
		if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, 'owner', ?, ?)`, id, role, []byte(id), now.Unix()); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(token))
		if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: id, CreatedAt: now.Add(-age), LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		cookies[role] = token
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()
	public := httptest.NewServer(p.srv.Public())
	defer public.Close()

	mkToken := func(name, profile string) (id, secret string) {
		t.Helper()
		secret = auth.NewTokenSecret()
		sum := sha256.Sum256([]byte(secret))
		id = store.NewID("tok_")
		if err := st.CreateAPIToken(ctx, store.APIToken{
			ID: id, Name: name, Profile: profile, Hint: secret[len(secret)-4:], RatePerMin: 600, CreatedBy: "adm_owner",
			CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		}, sum[:]); err != nil {
			t.Fatal(err)
		}
		return id, secret
	}
	session := func(secret string) *sdk.ClientSession {
		t.Helper()
		c := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil)
		s, err := c.Connect(ctx, &sdk.StreamableClientTransport{
			Endpoint: admin.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerClient{secret}}, DisableStandaloneSSE: true, MaxRetries: -1,
		}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	tool := func(s *sdk.ClientSession, name string, args map[string]any) (string, bool) {
		t.Helper()
		r, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var sb strings.Builder
		for _, c := range r.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		return sb.String(), r.IsError
	}
	ok := func(s *sdk.ClientSession, name string, args map[string]any) string {
		t.Helper()
		out, isErr := tool(s, name, args)
		if isErr {
			t.Fatalf("%s: %s", name, out)
		}
		return out
	}
	rpc := func(cookie, secret, service, method, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.admin.v1."+service+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookies[cookie]})
		}
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	opID, opSecret := mkToken("ops", store.ProfileOperator)
	adID, adSecret := mkToken("boss", store.ProfileAdmin)
	_, roSecret := mkToken("looker", store.ProfileReadonly)
	op, ad, ro := session(opSecret), session(adSecret), session(roSecret)

	count := func(s *sdk.ClientSession) int {
		n := 0
		for _, err := range s.Tools(ctx, nil) {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
		return n
	}
	if a, b, c := count(ro), count(op), count(ad); a != 50 || b != 105 || c != 210 {
		t.Errorf("tools per profile: readonly %d, operator %d, admin %d", a, b, c)
	}
	// an operator token neither sees nor calls a fleet tool
	if _, err := op.CallTool(ctx, &sdk.CallToolParams{Name: "rollout_start_plan", Arguments: map[string]any{}}); err == nil {
		t.Error("an operator token called rollout_start_plan")
	}

	// -- reads on the real services
	if out := ok(ro, "fleet_status", map[string]any{}); !strings.Contains(out, `"nodes_total":0`) {
		t.Errorf("fleet_status: %s", out)
	}
	// the kl!ck reports are off by default: a readonly token reads the switch and no nodes
	if out := ok(ro, "klick_reports", map[string]any{}); !strings.Contains(out, `"enabled":false`) || !strings.Contains(out, `"nodes":[]`) {
		t.Errorf("klick_reports: %s", out)
	}
	// the client catalogue on the real access module and the real token policy: a readonly token reads the four counts of an empty
	// panel; the table itself is not an MCP tool
	if out := ok(ro, "outdated_devices", map[string]any{}); !strings.Contains(out, `"total":0`) || !strings.Contains(out, `"state":"below_minimum"`) || !strings.Contains(out, `"devices":[]`) {
		t.Errorf("outdated_devices: %s", out)
	}
	// relays on the real access module and the real token policy: a readonly token reads (none yet); the check and the changes
	// are the admin profile's; no token reaches the procedures over /api
	if out := ok(ro, "relays_list", map[string]any{}); !strings.Contains(out, `"relays":[]`) {
		t.Errorf("relays_list: %s", out)
	}
	if out, isErr := tool(ad, "relay_check", map[string]any{"relay_id": "rly_none"}); !isErr || !strings.Contains(out, "not_found") {
		t.Errorf("relay_check of a relay that does not exist: %v %s", isErr, out)
	}
	if out, isErr := tool(ad, "relay_plan", map[string]any{"action": "delete", "relay_id": "rly_none"}); !isErr || !strings.Contains(out, "no such relay") {
		t.Errorf("relay_plan of a relay that does not exist: %v %s", isErr, out)
	}
	for _, secret := range []string{roSecret, opSecret, adSecret} {
		if code, _ := rpc("", secret, "RelayService", "ListRelays", `{}`); code != 403 {
			t.Errorf("ListRelays over the API with a token: %d", code)
		}
	}
	if out := ok(ad, "updates_status", map[string]any{}); !strings.Contains(out, `"status":"missing"`) {
		t.Errorf("updates_status: %s", out)
	}
	if out := ok(ad, "audit_search", map[string]any{}); !strings.Contains(out, `"entries"`) {
		t.Errorf("audit_search: %s", out)
	}

	// -- an operator makes users through plan and apply (the real access module), without anybody's approval
	code, body := rpc("owner", "", "GroupService", "CreateGroup", `{"name":"everyone"}`)
	if code != 200 {
		t.Fatalf("CreateGroup: %d %s", code, body)
	}
	groupID := decodeJSON[struct {
		Group struct{ ID string } `json:"group"`
	}](t, body).Group.ID
	var ids []string
	for _, name := range []string{"u1", "u2", "u3", "u4"} {
		pl := decodeJSON[mcp.PlanOut](t, ok(op, "user_create_plan", map[string]any{"name": name, "group_id": groupID, "term_days": 30}))
		if pl.NeedsApproval {
			t.Fatalf("user_create needs the owner: %+v", pl)
		}
		ap := decodeJSON[mcp.ApplyOut](t, ok(op, "user_create_apply", map[string]any{"confirm_token": pl.ConfirmToken}))
		if !strings.Contains(ap.Result, "usr_") || strings.Contains(ap.Result, "/"+in.SubPrefix[1:]) {
			t.Fatalf("create result: %q", ap.Result)
		}
		ids = append(ids, ap.Result[strings.Index(ap.Result, "usr_"):][:strings.IndexAny(ap.Result[strings.Index(ap.Result, "usr_"):], ")")])
	}
	var users struct {
		Users []struct{ ID, Name, Status string } `json:"users"`
	}
	if err := json.Unmarshal([]byte(ok(ro, "users_search", map[string]any{})), &users); err != nil || len(users.Users) != 4 {
		t.Fatalf("users: %v %+v", err, users)
	}

	// -- disabling four at once waits for the owner: plan, the owner's approval with a step-up, apply
	dis := decodeJSON[mcp.PlanOut](t, ok(op, "user_disable_plan", map[string]any{"user_ids": ids, "reason": "cleanup"}))
	if !dis.NeedsApproval || len(dis.Danger) != 1 || dis.Danger[0] != "bulk" {
		t.Fatalf("plan: %+v", dis)
	}
	if out, isErr := tool(op, "user_disable_apply", map[string]any{"confirm_token": dis.ConfirmToken}); !isErr || !strings.Contains(out, "waiting for the owner") {
		t.Errorf("apply before approval: %v %s", isErr, out)
	}
	// the owner's inbox
	code, body = rpc("owner", "", "ApprovalService", "ListApprovals", `{}`)
	if code != 200 || !strings.Contains(body, dis.PlanID) || !strings.Contains(body, "APPROVAL_STATE_AWAITING") || !strings.Contains(body, "cleanup") {
		t.Fatalf("ListApprovals: %d %s", code, body)
	}
	// a token cannot reach the approval service or the token service, whatever its profile
	for _, s := range []string{opSecret, adSecret, roSecret} {
		for _, c := range [][2]string{{"ApprovalService", "Approve"}, {"ApprovalService", "ListApprovals"}, {"ApiTokenService", "CreateApiToken"}, {"ApiTokenService", "ListApiTokens"}} {
			if code, body := rpc("", s, c[0], c[1], `{"id":"`+dis.PlanID+`"}`); code != 403 {
				t.Errorf("a token called %s/%s: %d %s", c[0], c[1], code, body)
			}
		}
	}
	// approving needs a fresh step-up
	if code, body := rpc("staleowner", "", "ApprovalService", "Approve", `{"id":"`+dis.PlanID+`"}`); code != 403 || !strings.Contains(body, "step-up") {
		t.Errorf("approve with a stale session: %d %s", code, body)
	}
	if out, isErr := tool(op, "user_disable_apply", map[string]any{"confirm_token": dis.ConfirmToken}); !isErr {
		t.Errorf("applied after a refused approval: %s", out)
	}
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+dis.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	ap := decodeJSON[mcp.ApplyOut](t, ok(op, "user_disable_apply", map[string]any{"confirm_token": dis.ConfirmToken}))
	if ap.Status != "applied" || !strings.Contains(ap.Result, "4 users disabled") {
		t.Errorf("apply: %+v", ap)
	}
	if err := json.Unmarshal([]byte(ok(ro, "users_search", map[string]any{"filter": ""})), &users); err != nil {
		t.Fatal(err)
	}
	for _, u := range users.Users {
		if u.Status != "disabled" {
			t.Errorf("%s is %s", u.Name, u.Status)
		}
	}
	// Enabling three users is within the bulk threshold and their owner-approved disable spent no unattended budget.
	rej := decodeJSON[mcp.PlanOut](t, ok(op, "user_enable_plan", map[string]any{"user_ids": ids[:3]}))
	if rej.NeedsApproval {
		t.Fatalf("enabling three users needs the owner: %+v", rej)
	}
	ok(op, "user_enable_apply", map[string]any{"confirm_token": rej.ConfirmToken})
	dis2 := decodeJSON[mcp.PlanOut](t, ok(op, "user_disable_plan", map[string]any{"user_ids": ids}))
	if code, body := rpc("owner", "", "ApprovalService", "Reject", `{"id":"`+dis2.PlanID+`"}`); code != 200 {
		t.Fatalf("Reject: %d %s", code, body)
	}
	if out, isErr := tool(op, "user_disable_apply", map[string]any{"confirm_token": dis2.ConfirmToken}); !isErr || !strings.Contains(out, "rejected by the owner") {
		t.Errorf("apply after a reject: %s", out)
	}

	// -- the hourly cap on what a token changes without the owner, on the real limiter and the real principal. Three disables
	// of three users fit under ten units (the owner-approved four above spent none), the fourth asks for no more than three
	// users and still waits for the owner, and the same token is refused whole over /api.
	// A token of its own, so that the earlier calls do not eat into its request-rate burst.
	_, capSecret := mkToken("capped", store.ProfileOperator)
	capped, capRPC := session(capSecret), capSecret
	for i := 1; i <= 3; i++ {
		if i > 1 {
			if code, body := rpc("owner", "", "UserService", "SetUsersEnabled", `{"user_ids":["`+ids[0]+`","`+ids[1]+`","`+ids[2]+`"],"enabled":true}`); code != 200 {
				t.Fatalf("owner resetting users before disable %d: %d %s", i, code, body)
			}
		}
		pl := decodeJSON[mcp.PlanOut](t, ok(capped, "user_disable_plan", map[string]any{"user_ids": ids[:3]}))
		if pl.NeedsApproval {
			t.Fatalf("disable plan %d of three users needs the owner: %+v", i, pl)
		}
		ok(capped, "user_disable_apply", map[string]any{"confirm_token": pl.ConfirmToken})
	}
	if code, body := rpc("owner", "", "UserService", "SetUsersEnabled", `{"user_ids":["`+ids[0]+`","`+ids[1]+`","`+ids[2]+`"],"enabled":true}`); code != 200 {
		t.Fatalf("owner resetting users before over-limit plan: %d %s", code, body)
	}
	over := decodeJSON[mcp.PlanOut](t, ok(capped, "user_disable_plan", map[string]any{"user_ids": ids[:3]}))
	if !over.NeedsApproval || len(over.Danger) != 1 || over.Danger[0] != "user_change_limit" {
		t.Fatalf("the fourth disable plan of three users: %+v", over)
	}
	if code, body := rpc("", capRPC, "UserService", "SetUsersEnabled", `{"user_ids":["`+ids[0]+`","`+ids[1]+`"],"enabled":false}`); code != 429 ||
		!strings.Contains(body, "token_user_change_limit") || !strings.Contains(body, "used=9") {
		t.Errorf("a disable over /api past the cap: %d %s", code, body)
	}
	// An owner's session has no budget.
	if code, body := rpc("owner", "", "UserService", "SetUsersEnabled", `{"user_ids":["`+ids[0]+`"],"enabled":false}`); code != 200 {
		t.Errorf("an owner disabling a user: %d %s", code, body)
	}
	re := decodeJSON[mcp.PlanOut](t, ok(capped, "user_enable_plan", map[string]any{"user_ids": ids}))
	// Enabling gives access back: it waits for the owner only because it names many people, never because of the
	// token's hourly limit, even with that limit spent.
	if re.NeedsApproval || len(re.Danger) != 0 {
		t.Fatalf("enabling people with the user-change limit spent: %+v", re)
	}
	ok(capped, "user_enable_apply", map[string]any{"confirm_token": re.ConfirmToken})

	// -- a fleet change through the owner's grant. The real UpdateService answers 400 "no release key" in this test build, which
	// is its own answer: it was reached, past the allow-list, the role check and its step-up, only because the plan is approved.
	if out, isErr := tool(ad, "rollout_start_plan", map[string]any{"node_ids": []string{"de1"}}); !isErr || !strings.Contains(out, "not trusted") {
		t.Errorf("rollout_start_plan: %v %s", isErr, out)
	}
	// Over /api the same procedure is closed to every token, whatever its profile.
	for _, s := range []string{opSecret, adSecret} {
		if code, body := rpc("", s, "UpdateService", "StartRollout", `{}`); code != 403 {
			t.Errorf("StartRollout over /api with a token: %d %s", code, body)
		}
	}
	grants := 0
	grantFor := func(tool string, approve bool) string {
		t.Helper()
		grants++
		confirm := "cf_" + strings.Repeat("G", 42) + string(rune(64+grants))
		sum := sha256.Sum256([]byte(confirm))
		pl := store.MCPPlan{
			TokenID: adID, Tool: tool, ParamsJSON: `{}`, NeedsApproval: true, Status: store.PlanAwaiting, ConfirmHash: sum[:],
			CreatedAt: time.Now(), Summary: "test",
		}
		pl.ID = store.NewID("pln_")
		if err := st.CreateMCPPlan(ctx, pl, 20, 50); err != nil {
			t.Fatal(err)
		}
		if approve {
			if _, err := st.DecideMCPPlan(ctx, pl.ID, "adm_owner", true, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		return confirm
	}
	// not approved: refused by the plan state, the API is not reached
	if out, isErr := tool(ad, "rollout_start_apply", map[string]any{"confirm_token": grantFor("rollout_start", false)}); !isErr || !strings.Contains(out, "waiting for the owner") {
		t.Errorf("unapproved: %s", out)
	}
	// approved: the call goes through the grant and the real handler answers
	if out, isErr := tool(ad, "rollout_start_apply", map[string]any{"confirm_token": grantFor("rollout_start", true)}); !isErr || !strings.Contains(out, "no release key") {
		t.Errorf("approved: %v %s", isErr, out)
	}
	// a grant is for its own procedure: the approved plan of a rollback does not open a rollout (here: no such plan tool
	// for the stored name, so the apply of another tool cannot even find it)
	if out, isErr := tool(ad, "rollout_cancel_apply", map[string]any{"confirm_token": grantFor("rollout_start", true)}); !isErr || !strings.Contains(out, "unknown confirm token") {
		t.Errorf("cross-tool: %s", out)
	}

	// -- node_fix's plan makes the dry run of ApplyFix under the planning grant. This panel has a node that never connected, so
	// the doctor says no, but its answer shows the call passed the token layer (a refusal would say "not allowed for this token").
	if code, body := rpc("owner", "", "NodeService", "CreateEnrollment", `{"name":"de1","address":"203.0.113.10","countryCode":"DE"}`); code != 200 {
		t.Fatalf("CreateEnrollment: %d %s", code, body)
	}
	out, isErr := tool(ad, "node_fix_plan", map[string]any{"node": "de1", "fix_id": "journald_vacuum"})
	if !isErr || !strings.Contains(out, "node_offline") || strings.Contains(out, "not allowed") || strings.Contains(out, "approval") || strings.Contains(out, "unauthenticated") {
		t.Errorf("node_fix_plan: %v %s", isErr, out)
	}
	t.Logf("node_fix_plan on a node that never connected: %s", out)

	// -- the library of decoy sites on the real services, the real token policy and the real fleet: a draft is saved by the token
	// at once and is on no node; the panel itself refuses a draft call on a ready site; putting a site on a node and a new version
	// of a site that stands on one wait for the owner and then reach the node.
	// tokens of their own, so that the earlier calls do not eat into the request-rate burst
	_, lopSecret := mkToken("libops", store.ProfileOperator)
	_, ladSecret := mkToken("libboss", store.ProfileAdmin)
	_, lroSecret := mkToken("libreader", store.ProfileReadonly)
	lop, lad, lro := session(lopSecret), session(ladSecret), session(lroSecret)
	const siteMarker = "Marker-DECOY-4417"
	index := map[string]any{"path": "index.html", "content": "<title>Bakery</title><p>" + siteMarker}
	if out, isErr := tool(lop, "decoy_site_draft_save", map[string]any{"name": "Bakery", "note": "a small neighbourhood bakery", "files": []any{index, map[string]any{"path": "404.html", "content": "<p>gone"}}}); isErr {
		t.Fatalf("decoy_site_draft_save: %s", out)
	} else if d := decodeJSON[mcp.DecoyDraftV](t, out); d.State != "draft" || d.Version != 1 || d.Files != 2 {
		t.Fatalf("draft = %+v", d)
	}
	sites := decodeJSON[mcp.DecoySitesV](t, ok(lro, "decoy_sites_list", map[string]any{}))
	if len(sites.Sites) != 1 || sites.Sites[0].State != "draft" || sites.Sites[0].Source != "mcp" || sites.Sites[0].MadeBy != "libops" || sites.Sites[0].NodeN != 0 {
		t.Fatalf("decoy_sites_list: %+v", sites)
	}
	siteID := sites.Sites[0].ID
	// no plan was made for the draft, and no token reaches the library over /api except to read
	for _, p := range [][2]string{{"CreateDecoySite", `{}`}, {"AddDecoySiteVersion", `{}`}, {"DownloadDecoySite", `{}`}, {"AssignDecoySite", `{"siteId":"` + siteID + `","nodeIds":["x"],"dryRun":true}`}, {"DeleteDecoySite", `{}`}} {
		for _, s := range []string{lopSecret, ladSecret} {
			req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.decoysite.v1.DecoySiteService/"+p[0], strings.NewReader(p[1]))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+s)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Errorf("%s over /api with a token: %d", p[0], resp.StatusCode)
			}
		}
	}
	// the owner's plan puts the draft on de1 in one step; it waits for the owner
	assign := decodeJSON[mcp.PlanOut](t, ok(lad, "decoy_site_assign_plan", map[string]any{"site_id": siteID, "nodes": []string{"de1"}, "mark_ready": true}))
	if !assign.NeedsApproval {
		t.Fatalf("assign plan: %+v", assign)
	}
	if out, isErr := tool(lad, "decoy_site_assign_apply", map[string]any{"confirm_token": assign.ConfirmToken}); !isErr || !strings.Contains(out, "waiting for the owner") {
		t.Errorf("assign before the approval: %v %s", isErr, out)
	}
	if got := decodeJSON[mcp.DecoySiteGetV](t, ok(lro, "decoy_site_get", map[string]any{"site_id": siteID})); got.Site.State != "draft" || len(got.Nodes) != 0 {
		t.Fatalf("the plan changed the site: %+v", got.Site)
	}
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+assign.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	if ap := decodeJSON[mcp.ApplyOut](t, ok(lad, "decoy_site_assign_apply", map[string]any{"confirm_token": assign.ConfirmToken})); ap.Status != "applied" {
		t.Fatalf("assign apply: %+v", ap)
	}
	on := decodeJSON[mcp.DecoySiteGetV](t, ok(lro, "decoy_site_get", map[string]any{"site_id": siteID}))
	if on.Site.State != "ready" || len(on.Nodes) != 1 || on.Nodes[0].Node != "de1" {
		t.Fatalf("after the assignment: %+v", on)
	}
	nodes, err := st.Nodes(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var de1 store.NodeRow
	for _, n := range nodes {
		if n.Name == "de1" {
			de1 = n
		}
	}
	if de1.DecoyMode != "own" || de1.DecoySiteID != siteID || de1.DecoyBundleHash == "" {
		t.Fatalf("node de1 = mode %q site %q bundle %q", de1.DecoyMode, de1.DecoySiteID, de1.DecoyBundleHash)
	}
	// a ready site is not a draft: the panel refuses the draft tool whatever the tool does, and says what to do
	if out, isErr := tool(lop, "decoy_site_draft_save", map[string]any{"site_id": siteID, "files": []any{index}}); !isErr || !strings.Contains(out, "decoy_site_not_draft") || !strings.Contains(out, "decoy_site_version_plan") {
		t.Errorf("draft tool on a ready site: %v %s", isErr, out)
	}
	if out, isErr := tool(lop, "decoy_site_draft_save", map[string]any{"name": "Other", "note": "n", "files": []any{index}}); isErr {
		t.Errorf("a second draft: %s", out)
	}
	// a new version of the site on the node: the operator's plan, the owner's approval, then the node's bundle changes
	ver := decodeJSON[mcp.PlanOut](t, ok(lop, "decoy_site_version_plan", map[string]any{"site_id": siteID, "change_files": []any{map[string]any{"path": "menu.html", "content": "<p>menu"}}}))
	if !ver.NeedsApproval || !strings.Contains(ver.Summary, "1 node") {
		t.Fatalf("version plan: %+v", ver)
	}
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+ver.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	if ap := decodeJSON[mcp.ApplyOut](t, ok(lop, "decoy_site_version_apply", map[string]any{"confirm_token": ver.ConfirmToken})); ap.Status != "applied" {
		t.Fatalf("version apply: %+v", ap)
	}
	if after, err := st.Node(ctx, de1.ID); err != nil || after.DecoyBundleHash == de1.DecoyBundleHash || after.DecoyMode != "own" {
		t.Fatalf("node de1 after the new version: %+v %v", after, err)
	}
	if got := decodeJSON[mcp.DecoySiteGetV](t, ok(lro, "decoy_site_get", map[string]any{"site_id": siteID})); got.Site.Version != 2 || len(got.Files) != 3 {
		t.Fatalf("after the new version: %+v", got)
	}
	if file := decodeJSON[mcp.DecoyFileV](t, ok(lro, "decoy_site_file", map[string]any{"site_id": siteID, "path": "index.html"})); !strings.Contains(file.Text, siteMarker) {
		t.Fatalf("decoy_site_file: %+v", file)
	}
	// the audit log names the site and the hash, never the text
	decoyAudit, err := st.ListAudit(ctx, "", 0, 400)
	if err != nil {
		t.Fatal(err)
	}
	var decoyRows int
	for _, r := range decoyAudit {
		if strings.HasPrefix(r.Action, "decoy_site.") || strings.HasPrefix(r.Action, "mcp_") {
			decoyRows++
		}
		if strings.Contains(r.Params, siteMarker) || strings.Contains(r.Params, "menu</p>") {
			t.Errorf("audit row %s carries the text of a site: %s", r.Action, r.Params)
		}
	}
	if decoyRows == 0 {
		t.Error("no audit rows for the library")
	}

	// -- the connection counters on the real services: a node that never connected does not send them, and a person nobody
	// connected for has no rows; a readonly token reads both
	if out := ok(ro, "node_connections", map[string]any{"node": "de1"}); !strings.Contains(out, `"supported":false`) || !strings.Contains(out, `"inbounds":[]`) {
		t.Errorf("node_connections: %s", out)
	}
	if out := ok(ro, "fleet_connections", map[string]any{"hours": 168}); !strings.Contains(out, `"nodes":[`) || !strings.Contains(out, `"node_name":"de1"`) || !strings.Contains(out, `"supported":false`) {
		t.Errorf("fleet_connections: %s", out)
	}
	journal := decodeJSON[mcp.NodeJournalV](t, ok(ro, "node_journal", map[string]any{"node": "de1"}))
	if len(journal.Entries) != 0 || len(journal.NodesWithoutJournal) != 1 || journal.NodesWithoutJournal[0] == "" {
		t.Errorf("node_journal through the real services: %+v", journal)
	}
	if out := ok(ro, "user_connection", map[string]any{"user_id": ids[0], "hours": 168, "by_hour": true}); !strings.Contains(out, `"rows":[]`) || !strings.Contains(out, `"nodes_without_counters":[]`) {
		t.Errorf("user_connection: %s", out)
	}
	if out, isErr := tool(ro, "user_connection", map[string]any{"user_id": "usr_missing"}); !isErr || !strings.Contains(out, "not found") {
		t.Errorf("user_connection of an unknown user: %v %s", isErr, out)
	}

	// -- the subscription page's app list: any token reads it; an operator's change waits for the owner and then saves through
	// the real SubscriptionService; an owner's save in between makes the apply refuse instead of overwriting it.
	if out := ok(ro, "subscription_settings_get", map[string]any{}); !strings.Contains(out, `"name":"Happ"`) {
		t.Errorf("subscription_settings_get: %s", out)
	}
	op2ID, op2Secret := mkToken("ops2", store.ProfileOperator)
	op2 := session(op2Secret)
	if code, body := rpc("", op2Secret, "SubscriptionService", "UpdateSubscriptionSettings", `{}`); code != 403 {
		t.Errorf("UpdateSubscriptionSettings over /api with a token: %d %s", code, body)
	}
	const tmpl = "myclient://add?url={url_enc}&name={name_enc}"
	sub := decodeJSON[mcp.PlanOut](t, ok(op2, "subscription_app_upsert_plan", map[string]any{
		"platform": "windows", "name": "My Client", "kind": "happ", "download_url": "https://example.com/myclient.exe", "add_link_template": tmpl,
	}))
	if !sub.NeedsApproval || len(sub.Danger) != 1 || sub.Danger[0] != "user_page" {
		t.Fatalf("subscription_app_upsert_plan: %+v", sub)
	}
	if code, body := rpc("owner", "", "ApprovalService", "ListApprovals", `{}`); code != 200 || !strings.Contains(body, tmpl) {
		t.Errorf("the owner's card lacks the full template: %d %s", code, body)
	}
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+sub.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	if ap := decodeJSON[mcp.ApplyOut](t, ok(op2, "subscription_app_upsert_apply", map[string]any{"confirm_token": sub.ConfirmToken})); ap.Status != "applied" {
		t.Errorf("subscription_app_upsert_apply: %+v", ap)
	}
	code, body = rpc("owner", "", "SubscriptionService", "GetSubscriptionSettings", `{}`)
	if code != 200 || !strings.Contains(body, tmpl) {
		t.Fatalf("the app was not saved: %d %s", code, body)
	}
	stale := decodeJSON[mcp.PlanOut](t, ok(op2, "subscription_app_remove_plan", map[string]any{"platform": "windows", "name": "my client"}))
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+stale.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	var got struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	got.Settings["title"] = "Saved by the owner"
	edit, _ := json.Marshal(map[string]any{"settings": got.Settings})
	if code, body := rpc("owner", "", "SubscriptionService", "UpdateSubscriptionSettings", string(edit)); code != 200 {
		t.Fatalf("the owner's save: %d %s", code, body)
	}
	if out, isErr := tool(op2, "subscription_app_remove_apply", map[string]any{"confirm_token": stale.ConfirmToken}); !isErr || !strings.Contains(out, "changed after the plan") {
		t.Errorf("apply over the owner's save: %v %s", isErr, out)
	}
	if code, body := rpc("owner", "", "SubscriptionService", "GetSubscriptionSettings", `{}`); code != 200 || !strings.Contains(body, tmpl) || !strings.Contains(body, "Saved by the owner") {
		t.Errorf("after the refused apply: %d %s", code, body)
	}

	// -- an own Mihomo template: the operator plans it (a helper's role: only the owner's approval of this plan lets the save change a
	// template), the owner's card names it without the text, the real save checks and stores it, the readonly token reads it in windows, and no audit row holds the text.
	const tplMarker = "# marker-template-text-4471"
	tplText := "mixed-port: 7890\n" + tplMarker + "\nproxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\nrules:\n  - MATCH,Proxy\n"
	tp := decodeJSON[mcp.PlanOut](t, ok(op2, "subscription_mihomo_template_set_plan", map[string]any{"name": "ROUTER", "yaml": tplText, "group_ids": []string{groupID}}))
	if !tp.NeedsApproval || len(tp.Danger) != 1 || tp.Danger[0] != "user_page" {
		t.Fatalf("subscription_mihomo_template_set_plan: %+v", tp)
	}
	if code, body := rpc("owner", "", "ApprovalService", "ListApprovals", `{}`); code != 200 || !strings.Contains(body, "ROUTER") || strings.Contains(body, "marker-template-text") {
		t.Errorf("the owner's card for the template: %d %s", code, body)
	}
	if out, isErr := tool(op2, "subscription_mihomo_template_set_plan", map[string]any{"name": "ROUTER", "yaml": tplText + "# [redacted]\n"}); !isErr || !strings.Contains(out, "template_has_redaction_markers") {
		t.Errorf("a text with a marker of the scrubber: %v %s", isErr, out)
	}
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+tp.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	if ap := decodeJSON[mcp.ApplyOut](t, ok(op2, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": tp.ConfirmToken})); ap.Status != "applied" {
		t.Errorf("subscription_mihomo_template_set_apply: %+v", ap)
	}
	if code, body := rpc("owner", "", "SubscriptionService", "GetSubscriptionSettings", `{}`); code != 200 || !strings.Contains(body, "marker-template-text") {
		t.Errorf("the template was not saved: %d %s", code, body)
	}
	if out := ok(ro, "subscription_settings_get", map[string]any{}); !strings.Contains(out, `"mihomo_templates":[{"name":"ROUTER"`) || strings.Contains(out, "marker-template-text") {
		t.Errorf("the list of templates: %s", out)
	}
	if out := ok(ro, "subscription_mihomo_template_get", map[string]any{"name": "router"}); !strings.Contains(out, "marker-template-text") || !strings.Contains(out, `"bytes":`+strconv.Itoa(len(tplText))) {
		t.Errorf("the read of the template: %s", out)
	}

	// The templates are the owner's: the operator's other plans carry them as they are stored and apply without the owner's grant of this
	// tool; the save over the API as a helper is refused in the subs tests (a token has no way to it but a plan).
	tx := decodeJSON[mcp.PlanOut](t, ok(op2, "subscription_texts_set_plan", map[string]any{"announcement": "Maintenance tonight"}))
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+tx.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	if ap := decodeJSON[mcp.ApplyOut](t, ok(op2, "subscription_texts_set_apply", map[string]any{"confirm_token": tx.ConfirmToken})); ap.Status != "applied" {
		t.Errorf("subscription_texts_set_apply with a template stored: %+v", ap)
	}
	if code, body := rpc("owner", "", "SubscriptionService", "GetSubscriptionSettings", `{}`); code != 200 || !strings.Contains(body, "Maintenance tonight") || !strings.Contains(body, "marker-template-text") {
		t.Errorf("after the operator's text change: %d %s", code, body)
	}

	// The routing profiles of Happ and INCY are the owner's too, by the same rule: the operator plans, the apply waits for the owner,
	// and the save passes only under the owner's approval of this tool's plan.
	const routingMarker = "marker-routing-profile-5532"
	routingText := `{"Name":"` + routingMarker + `","DirectSites":["domain:example.ru"]}`
	rp := decodeJSON[mcp.PlanOut](t, ok(op2, "subscription_routing_set_plan", map[string]any{"name": routingMarker, "profile": routingText, "group_ids": []string{groupID}}))
	if !rp.NeedsApproval {
		t.Fatalf("subscription_routing_set_plan: %+v", rp)
	}
	if out, isErr := tool(op2, "subscription_routing_set_apply", map[string]any{"confirm_token": rp.ConfirmToken}); !isErr || !strings.Contains(out, "waiting for the owner") {
		t.Errorf("routing apply before the approval: %v %s", isErr, out)
	}
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+rp.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	if ap := decodeJSON[mcp.ApplyOut](t, ok(op2, "subscription_routing_set_apply", map[string]any{"confirm_token": rp.ConfirmToken})); ap.Status != "applied" {
		t.Errorf("subscription_routing_set_apply: %+v", ap)
	}
	if code, body := rpc("owner", "", "SubscriptionService", "GetSubscriptionSettings", `{}`); code != 200 || !strings.Contains(body, routingMarker) {
		t.Errorf("the routing profile was not saved: %d %s", code, body)
	}
	// another plan of the operator carries the stored profile unchanged
	tx2 := decodeJSON[mcp.PlanOut](t, ok(op2, "subscription_texts_set_plan", map[string]any{"announcement": "Maintenance over"}))
	if code, body := rpc("owner", "", "ApprovalService", "Approve", `{"id":"`+tx2.PlanID+`"}`); code != 200 {
		t.Fatalf("Approve: %d %s", code, body)
	}
	if ap := decodeJSON[mcp.ApplyOut](t, ok(op2, "subscription_texts_set_apply", map[string]any{"confirm_token": tx2.ConfirmToken})); ap.Status != "applied" {
		t.Errorf("subscription_texts_set_apply with a routing profile stored: %+v", ap)
	}
	if code, body := rpc("owner", "", "SubscriptionService", "GetSubscriptionSettings", `{}`); code != 200 || !strings.Contains(body, "Maintenance over") || !strings.Contains(body, routingMarker) {
		t.Errorf("after the operator's text change: %d %s", code, body)
	}

	// -- the audit trail: the plan, the apply and the module's own rows carry the token as mcp:<id>
	rows, err := st.ListAudit(ctx, "", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Source+"/"+r.Action+"/"+r.Actor] = true
		if strings.Contains(r.Params, "cf_") || strings.Contains(r.Params, opSecret) || strings.Contains(r.Params, op2Secret) {
			t.Errorf("audit row %s carries a secret: %s", r.Action, r.Params)
		}
		if strings.Contains(r.Params, "marker-template-text") {
			t.Errorf("audit row %s carries the text of a template: %s", r.Action, r.Params)
		}
	}
	for _, want := range []string{"mcp/mcp_plan/mcp:" + opID, "mcp/mcp_apply/mcp:" + opID, "mcp/mcp_apply/mcp:" + adID, "panel/approval_approve/adm_owner",
		"mcp/subscription_settings_update/mcp:" + op2ID} {
		if !seen[want] {
			t.Errorf("no audit row %s; have %v", want, keys(seen))
		}
	}

	// -- CreateUser over /api: a token of any profile that may call it gets the user, never the link or the page password
	// (they are the user's credentials); a signed-in admin gets both.
	for i, s := range []string{opSecret, adSecret} {
		code, body := rpc("", s, "UserService", "CreateUser", fmt.Sprintf(`{"name":"script%d","groupId":%q}`, i, groupID))
		if code != 200 || !strings.Contains(body, `"user"`) || strings.Contains(body, "subscriptionUrl") || strings.Contains(body, "pagePassword") ||
			strings.Contains(body, strings.Trim(in.SubPrefix, "/")) {
			t.Errorf("CreateUser with a token: %d %s", code, body)
		}
	}
	if code, body := rpc("owner", "", "UserService", "CreateUser", fmt.Sprintf(`{"name":"clicked","groupId":%q}`, groupID)); code != 200 ||
		!strings.Contains(body, `"subscriptionUrl":"https://example.com`+strings.TrimRight(in.SubPrefix, "/")+"/") || !strings.Contains(body, `"pagePassword"`) {
		t.Errorf("CreateUser signed in: %d %s", code, body)
	}

	// -- /mcp is only where the admin API is: the public listener answers with the decoy
	resp, err := http.Post(public.URL+"/mcp", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 || strings.Contains(string(b), "jsonrpc") {
		t.Errorf("public /mcp: %d %.80s", resp.StatusCode, b)
	}

	// -- `mistgate mcp`: the stdio proxy in front of the same endpoint, with the token in a file
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(roSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var proxyErr bytes.Buffer
	pctx, pcancel := context.WithCancel(ctx)
	defer pcancel()
	go func() {
		runMCP([]string{"--url", admin.URL + "/", "--token-file", tokenFile}, inR, outW, &proxyErr)
		outW.Close()
	}()
	pc := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	ps, err := pc.Connect(pctx, &sdk.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect through mistgate mcp: %v (%s)", err, proxyErr.String())
	}
	defer ps.Close()
	if n := count(ps); n != 50 {
		t.Errorf("tools through the proxy: %d", n)
	}
	if out := ok(ps, "fleet_status", map[string]any{}); !strings.Contains(out, `"nodes_total"`) {
		t.Errorf("fleet_status through the proxy: %s", out)
	}
	if strings.Contains(proxyErr.String(), roSecret) {
		t.Error("the proxy printed the token")
	}

	// -- a revoked token stops at its very next request, and its open plans are cancelled
	open := decodeJSON[mcp.PlanOut](t, ok(op, "user_enable_plan", map[string]any{"user_ids": ids[:1]}))
	if _, err := st.RevokeAPIToken(ctx, opID, "adm_owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := op.CallTool(ctx, &sdk.CallToolParams{Name: "user_enable_apply", Arguments: map[string]any{"confirm_token": open.ConfirmToken}}); err == nil {
		t.Error("a revoked token applied")
	}
	if got, err := st.GetMCPPlan(ctx, open.PlanID); err != nil || got.Status != store.PlanCancelled {
		t.Errorf("open plan of a revoked token: %+v %v", got.Status, err)
	}
}

func decodeJSON[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
