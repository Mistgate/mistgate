package health

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The system credential rides in the desired state of its inbound (no user, no device, rate limited), is made
// once and stays the same, and is gone with the inbound.
func TestSystemCredentialInTheDesiredState(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	in := e.inbound("de1", 443)
	grp := must(e.acc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g", ProfileIds: []string{profileOf(e, in)}}))).Msg.Group.Id
	must(e.acc.CreateUser(e.ctx, connect.NewRequest(&adminv1.CreateUserRequest{Name: "alice", GroupId: grp})))

	desired := func() []statehash.Inbound {
		ins, err := e.acc.Desired(e.ctx, "de1")
		if err != nil {
			t.Fatal(err)
		}
		out := make([]statehash.Inbound, len(ins))
		for i, x := range ins {
			out[i] = statehash.Inbound(x)
		}
		return e.s.WithProbeCreds(e.ctx, out)
	}
	plain := must(e.acc.Desired(e.ctx, "de1"))
	if len(plain[0].Creds) != 1 {
		t.Fatalf("access.Desired already holds %d credentials: the system one must come from health only", len(plain[0].Creds))
	}
	first := desired()
	if len(first) != 1 || len(first[0].Creds) != 2 {
		t.Fatalf("desired: %+v", first)
	}
	var sys *struct{ id, data string }
	for _, c := range first[0].Creds {
		if c.UserID == "" {
			if c.DeviceID != "" || c.RateLimitBps != probeRateLimitBps || !c.ValidUntil.IsZero() {
				t.Fatalf("system credential: %+v", c)
			}
			sys = &struct{ id, data string }{c.CredID, string(c.Data)}
		}
	}
	if sys == nil || !strings.HasPrefix(sys.id, "crd_") || !strings.Contains(sys.data, "auth_sha256") {
		t.Fatalf("no system credential: %+v", first[0].Creds)
	}
	// stable across calls and across a restart of the module (the row, not the cache, is the truth)
	again := New(e.st, e.v, e.s.reg, e.fl, e.s.cfg).WithProbeCreds(e.ctx, must(toStateHashed(e)))
	for _, got := range [][]statehash.Inbound{desired(), again} {
		ok := false
		for _, c := range got[0].Creds {
			ok = ok || c.CredID == sys.id
		}
		if !ok {
			t.Fatal("the system credential changed between calls")
		}
	}
	// the input is not modified: the real users' list keeps its length
	if n := len(must(e.acc.Desired(e.ctx, "de1"))[0].Creds); n != 1 {
		t.Fatalf("a real list was touched: %d", n)
	}

	// the verifier the node gets matches the secret the probe client presents
	secret, err := e.s.probeSecret(e.ctx, &target{in: store.FleetInboundRow{ID: in, Protocol: "hysteria2"}})
	if err != nil || secret == "" {
		t.Fatalf("secret: %q %v", secret, err)
	}
	var v struct {
		Auth string `json:"auth_sha256"`
	}
	if err := json.Unmarshal([]byte(sys.data), &v); err != nil || len(v.Auth) != 64 {
		t.Fatalf("verifier: %s %v", sys.data, err)
	}

	// an inbound that is gone takes its credential along
	if err := e.st.Access().DeleteInbound(e.ctx, in, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ProbeCred(e.ctx, in); err != store.ErrNotFound {
		t.Fatalf("credential outlived its inbound: %v", err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("unexpected error: %v", err))
	}
	return v
}

func toStateHashed(e *env) ([]statehash.Inbound, error) {
	ins, err := e.acc.Desired(e.ctx, "de1")
	out := make([]statehash.Inbound, len(ins))
	for i, x := range ins {
		out[i] = statehash.Inbound(x)
	}
	return out, err
}

func profileOf(e *env, inbound string) string {
	var id string
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT profile_id FROM inbound WHERE id = ?`, inbound).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// Nothing a user or an admin can list, open or fetch mentions the system credential: its id, its secret and its
// verifier stay out of every user-facing view and every subscription.
func TestSystemCredentialNeverAppearsInUserFacingViews(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	in := e.inbound("de1", 443)
	grp := must(e.acc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g", ProfileIds: []string{profileOf(e, in)}}))).Msg.Group.Id
	cu := must(e.acc.CreateUser(e.ctx, connect.NewRequest(&adminv1.CreateUserRequest{Name: "alice", GroupId: grp}))).Msg
	uid := cu.User.Id

	e.s.WithProbeCreds(e.ctx, must(toStateHashed(e))) // the credential exists now
	row := must(e.st.ProbeCred(e.ctx, in))
	secret := must(e.s.probeSecret(e.ctx, &target{in: store.FleetInboundRow{ID: in, Protocol: "hysteria2"}}))
	var v struct {
		Auth string `json:"auth_sha256"`
	}
	json.Unmarshal([]byte(row.DataJSON), &v)
	needles := map[string]string{"cred id": row.CredID, "secret": secret, "verifier": v.Auth, "sealed secret": string(row.SecretEnc)}

	dumps := map[string]string{}
	msg := func(name string, m proto.Message, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		dumps[name] = protojson.Format(m)
	}
	lu, err := e.acc.ListUsers(e.ctx, connect.NewRequest(&adminv1.ListUsersRequest{}))
	msg("ListUsers", lu.Msg, err)
	gu, err := e.acc.GetUser(e.ctx, connect.NewRequest(&adminv1.GetUserRequest{UserId: uid}))
	msg("GetUser", gu.Msg, err)
	gl, err := e.acc.GetSubscriptionLink(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid}))
	msg("GetSubscriptionLink", gl.Msg, err)
	lp, err := e.acc.ListProfiles(e.ctx, connect.NewRequest(&adminv1.ListProfilesRequest{}))
	msg("ListProfiles", lp.Msg, err)
	gp, err := e.acc.GetProfile(e.ctx, connect.NewRequest(&adminv1.GetProfileRequest{ProfileId: profileOf(e, in)}))
	msg("GetProfile", gp.Msg, err)
	lg, err := e.acc.ListGroups(e.ctx, connect.NewRequest(&adminv1.ListGroupsRequest{}))
	msg("ListGroups", lg.Msg, err)

	token := strings.TrimSuffix(gl.Msg.Url, "/")
	token = token[strings.LastIndex(token, "/")+1:]
	sv, err := e.acc.Subscription(e.ctx, token)
	if err != nil || len(sv.Lines) != 1 {
		t.Fatalf("subscription: %+v %v", sv, err)
	}
	b, _ := json.Marshal(sv)
	dumps["Subscription"] = string(b)
	pv, _, err := e.acc.PreviewSubscription(e.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(pv)
	dumps["PreviewSubscription"] = string(b)

	// the health views themselves: alerts, checks and the doctor
	e.report("de1", false, res("disk_space", dWarn, "journald_vacuum"))
	e.round(in, cOK, "")
	la, err := rpc{e.s}.ListAlerts(e.ctx, connect.NewRequest(&adminv1.ListAlertsRequest{}))
	msg("ListAlerts", la.Msg, err)
	gc, err := rpc{e.s}.GetChecks(e.ctx, connect.NewRequest(&adminv1.GetChecksRequest{}))
	msg("GetChecks", gc.Msg, err)
	gd, err := rpc{e.s}.GetDoctor(e.ctx, connect.NewRequest(&adminv1.GetDoctorRequest{}))
	msg("GetDoctor", gd.Msg, err)

	for view, text := range dumps {
		for what, needle := range needles {
			if needle != "" && strings.Contains(text, needle) {
				t.Errorf("%s contains the system credential (%s)", view, what)
			}
		}
	}
	if !strings.Contains(dumps["Subscription"], "hysteria2://") { // the views do render credentials: the absence above means something
		t.Fatalf("the subscription has no line: %s", dumps["Subscription"])
	}
	if len(dumps) < 10 {
		t.Fatalf("only %d views were checked", len(dumps))
	}

	// the user's own credential rows are the only ones the access module knows about
	var n int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM device_credential WHERE id = ?`, row.CredID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the system credential is in device_credential: %d %v", n, err)
	}
	creds, err := e.st.Access().DesiredCreds(e.ctx, "de1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range creds {
		if c.CredID == row.CredID {
			t.Fatal("DesiredCreds of the access module returns the system credential")
		}
	}
}

// Traffic and sessions the node reports under the system credential are skipped by the stats ingest: they
// reach no user, no bucket and no online list.
func TestStatsUnderTheSystemCredentialAreSkipped(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	in := e.inbound("de1", 443)
	e.s.WithProbeCreds(e.ctx, must(toStateHashed(e)))
	row := must(e.st.ProbeCred(e.ctx, in))

	out, err := e.st.IngestStats(e.ctx, store.FleetStatsIn{
		NodeID: "de1", Instance: "i1", Seq: 0, Now: e.clock.Now(), HourStart: e.clock.Now().Unix() - e.clock.Now().Unix()%3600,
		Traffic: []store.FleetTraffic{
			{CredID: row.CredID, InboundID: in, Up: 10, Down: 1000},
			{CredID: "crd_nobody", InboundID: in, Up: 1, Down: 1},
		},
		Sessions: []store.FleetSessionRef{{CredID: row.CredID, InboundID: in}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// the system credential is ignored quietly (Probe), only the really unknown one is an anomaly (Skipped: the panel warns)
	if out.Probe != 1 || out.Skipped != 1 || len(out.Refs) != 0 || len(out.Users) != 0 {
		t.Fatalf("ingest: %+v", out)
	}
	var n int
	for _, q := range []string{`SELECT count(*) FROM traffic_bucket`, `SELECT count(*) FROM node_traffic_hour`} {
		if err := e.st.R.QueryRowContext(e.ctx, q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s = %d %v", q, n, err)
		}
	}
}
