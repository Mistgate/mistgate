package access

import (
	"encoding/json"
	"slices"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// "The same server with and without WARP": TwinProfile makes the profile's twin with the exit flipped.

func (e *env) twin(profileID, egress string, dry bool, port uint32) (*adminv1.TwinProfileResponse, error) {
	r, err := e.s.TwinProfile(e.ctx, req(&adminv1.TwinProfileRequest{ProfileId: profileID, Egress: egress, DryRun: dry, Port: port}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

// settingsOf is a profile's merged settings (secrets in place) as a generic document.
func (e *env) settingsOf(profileID string) map[string]any {
	e.t.Helper()
	p := must(e.st.Access().Profile(e.ctx, profileID))
	raw := must(e.s.mergedSettings(p))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) profileCount() int { return len(must(e.st.Access().Profiles(e.ctx))) }

func errOf(_ *adminv1.TwinProfileResponse, err error) error { return err }

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestTwinHysteria2(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	p := e.profile("files", `{"hop":{"from":20000,"to":30000}}`)
	must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_de1", TlsServerNameOverride: "files.example.com"})))
	e.inbound(p.Id, "nod_nl1")
	g := e.group("default", p.Id)
	e.group("other")
	before := e.profileCount()

	// the dry run is the plan and changes nothing
	plan, err := e.twin(p.Id, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "files · WARP" || plan.Egress != "warp" || plan.Port != 8443 || !plan.HopDropped || plan.Profile != nil ||
		!slices.Equal(plan.GroupIds, []string{g}) || len(plan.NodeIds) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if e.profileCount() != before {
		t.Fatal("a dry run made a profile")
	}

	// the real call, with the planned port
	done, err := e.twin(p.Id, "warp", false, plan.Port)
	if err != nil {
		t.Fatal(err)
	}
	tw := done.Profile
	if tw == nil || tw.Name != "files · WARP" || tw.Protocol != "hysteria2" || tw.NodeCount != 2 || done.Port != 8443 {
		t.Fatalf("twin = %+v", done)
	}
	was, now := e.settingsOf(p.Id), e.settingsOf(tw.Id)
	if now["egress"] != "warp" || was["egress"] == "warp" || now["port"] != float64(8443) || was["port"] != float64(443) {
		t.Errorf("egress/port: twin %v/%v, profile %v/%v", now["egress"], now["port"], was["egress"], was["port"])
	}
	if hop := now["hop"].(map[string]any); hop["from"] != float64(0) || hop["to"] != float64(0) {
		t.Errorf("the twin kept the hop range: %v", hop)
	}
	if now["obfs"].(map[string]any)["password"] == was["obfs"].(map[string]any)["password"] {
		t.Error("the twin got the profile's obfuscation password")
	}
	for _, k := range []string{"sni", "tls_mode", "masquerade", "bbr_profile"} {
		if !jsonEqual(now[k], was[k]) {
			t.Errorf("%s differs: %v vs %v", k, now[k], was[k])
		}
	}
	// every node and the group, the SNI override of the inbound too
	var on []string
	for _, in := range must(e.st.Access().InboundsOfProfile(e.ctx, tw.Id)) {
		on = append(on, in.NodeID)
		if in.NodeID == "nod_de1" && in.TLSServerNameOverride != "files.example.com" {
			t.Errorf("the SNI override was not carried: %q", in.TLSServerNameOverride)
		}
	}
	slices.Sort(on)
	if !slices.Equal(on, []string{"nod_de1", "nod_nl1"}) {
		t.Errorf("twin nodes = %v", on)
	}
	for _, gr := range must(e.s.ListGroups(e.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups {
		if has := slices.Contains(gr.ProfileIds, tw.Id); has != (gr.Id == g) {
			t.Errorf("group %s holds the twin: %v", gr.Name, has)
		}
	}

	// and back: the name without the mark is taken by the profile, so it gets a number; the port is the next free one
	back, err := e.twin(tw.Id, "", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if back.Profile.Name != "files 2" || back.Egress != "direct" || back.Port != 4443 {
		t.Errorf("back = %+v", back)
	}
	// the profile's own exit again is not a twin
	wantCode(t, errOf(e.twin(p.Id, "direct", false, 0)), connect.CodeInvalidArgument)
	wantCode(t, errOf(e.twin(p.Id, "tor", false, 0)), connect.CodeInvalidArgument)
	wantCode(t, errOf(e.twin("prf_nope", "warp", false, 0)), connect.CodeNotFound)
}

// A port another profile of the node listens on, or hops over, is never chosen; a port that was asked for and is taken
// is refused.
func TestTwinPortAvoidsNeighbours(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.profile("main", "")
	e.inbound(p.Id, "nod_de1")
	// "hopper" is on 10000 and forwards 8000-9500, which holds the first candidate (8443)
	h := e.profile("hopper", `{"port":10000,"hop":{"from":8000,"to":9500}}`)
	e.inbound(h.Id, "nod_de1")
	// "taken" listens on the second candidate
	t4 := e.profile("taken", `{"port":4443}`)
	e.inbound(t4.Id, "nod_de1")

	plan, err := e.twin(p.Id, "warp", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Port != 2053 {
		t.Errorf("port = %d, want 2053 (8443 is in a hop range, 4443 is listened on)", plan.Port)
	}
	wantCode(t, errOf(e.twin(p.Id, "warp", false, 4443)), connect.CodeAlreadyExists)
	wantCode(t, errOf(e.twin(p.Id, "warp", false, 8443)), connect.CodeAlreadyExists)
}

// A step that fails leaves nothing behind.
func TestTwinRollsBack(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	p := e.profile("files", "")
	e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_nl1")
	e.group("default", p.Id)
	before := e.profileCount()
	// the second inbound of the twin fails (a node going away, a full disk), after the first one was made
	e.sql(`CREATE TRIGGER twin_boom BEFORE INSERT ON inbound
	       WHEN NEW.profile_id <> '` + p.Id + `' AND (SELECT count(*) FROM inbound WHERE profile_id = NEW.profile_id) >= 1
	       BEGIN SELECT RAISE(ABORT, 'boom'); END`)

	if _, err := e.twin(p.Id, "warp", false, 0); err == nil {
		t.Fatal("the twin was made although a node refused it")
	}
	if e.profileCount() != before {
		t.Errorf("profiles = %d, want %d: the half-made twin stayed", e.profileCount(), before)
	}
	for _, f := range must(e.st.Access().InboundsFull(e.ctx, "")) {
		if f.Profile.ID != p.Id {
			t.Errorf("an inbound of %q stayed", f.Profile.Name)
		}
	}
}

func TestTwinAWG(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	was := e.settingsOf(f.profile)

	plan, err := e.twin(f.profile, "warp", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "awg31 · WARP" || plan.Port < 10000 || plan.Port > 60000 || plan.HopDropped || len(plan.NodeIds) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	done, err := e.twin(f.profile, "warp", false, plan.Port)
	if err != nil {
		t.Fatal(err)
	}
	now := e.settingsOf(done.Profile.Id)
	if now["egress"] != "warp" || now["port"] != float64(plan.Port) || now["version"] != was["version"] {
		t.Errorf("twin settings: egress %v port %v version %v", now["egress"], now["port"], now["version"])
	}
	if now["subnet4"] == was["subnet4"] || now["subnet4"] == "" {
		t.Errorf("the twin shares the client network: %v / %v", now["subnet4"], was["subnet4"])
	}
	wo, no := was["obfuscation"].(map[string]any), now["obfuscation"].(map[string]any)
	if no["header_protection_key"] == wo["header_protection_key"] || no["signature_seed"] == wo["signature_seed"] || no["signature_seed"] == "" {
		t.Errorf("the twin shares a secret or the signature seed: %v / %v", no["header_protection_key"], no["signature_seed"])
	}
	if got := e.profileCount(); got != 2 {
		t.Errorf("profiles = %d", got)
	}
}
