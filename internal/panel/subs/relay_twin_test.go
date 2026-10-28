package subs_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The twin of a relay in every format (cascade plan §7), against the real access module: the exit's server on de1 (DE) and,
// next to it, the same server through the relay on fi1 (FI), which forwards port 8443. The files in testdata are what each
// format answers; the credential of the device is the one random value and is masked.

type relayRig struct {
	*rig
	exit, group string
}

func newRelayRig(t *testing.T) *relayRig {
	t.Helper()
	r := newRig(t, "/k3xq8")
	for _, q := range []string{
		`UPDATE node SET country_code = 'DE' WHERE id = 'nod_1'`,
		`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at) VALUES ('nod_2', 'fi1', 'fi1.example.com', 'FI', 'active', 'relay-forward/1', 2)`,
	} {
		if _, err := r.st.W.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	p := must(r.svc.CreateProfile(r.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "hysteria2", Name: "Hysteria2 premium", SettingsJson: `{"port":4443,"hop":{"from":20000,"to":30000},"obfs":{"type":"salamander","password":"golden-obfs-password"}}`,
	}))).Msg.Profile
	in := must(r.svc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_1"}))).Msg.Inbound
	if _, err := r.st.W.Exec(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_1', ?, 'nod_2', 'udp', 8443, 1, 'active', ?, 1, 1)`, in.Id, `{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`); err != nil {
		t.Fatal(err)
	}
	g := must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "relayed", ProfileIds: []string{p.Id}}))).Msg.Group.Id
	return &relayRig{rig: r, exit: in.Id, group: g}
}

func (m *relayRig) token(name string, nodes ...string) string {
	_, tok := m.user(name, func(c *adminv1.CreateUserRequest) {
		c.GroupId = m.group
		if len(nodes) > 0 {
			c.Nodes = &adminv1.NodeSelection{NodeIds: nodes}
		}
	})
	return tok
}

var secretOf = regexp.MustCompile(`hysteria2://([^@:]+)@`)

// golden compares got with testdata/<name>; -update rewrites it (the flag of the package's other golden tests).
func goldenFile(t *testing.T, name, got string) {
	t.Helper()
	if strings.HasSuffix(name, ".json") {
		var b bytes.Buffer
		if json.Indent(&b, []byte(got), "", "  ") == nil {
			got = b.String() + "\n"
		}
	}
	path := filepath.Join("testdata", name)
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestRelayTwinInEveryFormat(t *testing.T) {
	m := newRelayRig(t)
	tok := m.token("alice")

	list := fetch(m.h, "/"+tok, happUA)
	body := decode(t, list.Body.String())
	secret := secretOf.FindStringSubmatch(body)
	if list.Code != 200 || secret == nil {
		t.Fatalf("list: %d %q", list.Code, body)
	}
	masked := func(s string) string { return strings.ReplaceAll(s, secret[1], "<secret>") }
	lines := strings.Split(body, "\n")
	if len(lines) != 2 {
		t.Fatalf("the list has %d lines, want the server and its twin:\n%s", len(lines), body)
	}
	// The names Happ shows (30 units at most): the exit's own and its twin through FI, the load not yet known.
	var names []string
	for _, l := range lines {
		_, frag, _ := strings.Cut(l, "#")
		n, err := url.PathUnescape(frag)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if names[0] != "\U0001F1E9\U0001F1EA DE · Hysteria2 premium" || names[1] != "\U0001F1E9\U0001F1EA DE · HY2 premium · via FI" {
		t.Errorf("names: %q", names)
	}
	// The twin is the exit's line with the entry's address and its one port: no change of obfs, SNI or credential.
	exitURI, _, _ := strings.Cut(lines[0], "#")
	twin, _, _ := strings.Cut(lines[1], "#")
	if want := strings.Replace(exitURI, "@de1.example.com:4443/", "@fi1.example.com:8443/", 1); twin != want || twin == exitURI {
		t.Errorf("twin\n%s\nwant\n%s", twin, want)
	}
	goldenFile(t, "relay_list.golden", masked(strings.Join(lines, "\n"))+"\n")

	for _, c := range []struct{ file, path, ua string }{
		{"relay_mihomo.yaml", "/" + tok, mihomoUA},
		{"relay_xray.json", "/" + tok + "?format=xray-json", happUA},
		{"relay_singbox.json", "/" + tok, sfaUA},
	} {
		rec := fetch(m.h, c.path, c.ua)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.file, rec.Code, rec.Body.String())
		}
		goldenFile(t, c.file, masked(rec.Body.String()))
	}
}

// A person who is not allowed the entry node gets the exit's server alone, and one who is allowed gets both.
func TestRelayTwinOnlyForThePeopleOfTheEntry(t *testing.T) {
	m := newRelayRig(t)
	count := func(tok string) int {
		return len(strings.Split(decode(t, fetch(m.h, "/"+tok, happUA).Body.String()), "\n"))
	}
	if n := count(m.token("only-exit", "nod_1")); n != 1 {
		t.Errorf("a person without the entry: %d servers", n)
	}
	if n := count(m.token("both", "nod_1", "nod_2")); n != 2 {
		t.Errorf("a person with the entry: %d servers", n)
	}
	// The relay fails its path check: the twin is gone from the next subscription of everyone.
	if _, err := m.st.W.Exec(`UPDATE relay SET check_json = ? WHERE id = 'rly_1'`, `{"at":2,"leg":{"status":"ok"},"path":{"status":"fail","code":"broken"},"ok":false,"stage":"path"}`); err != nil {
		t.Fatal(err)
	}
	if n := count(m.token("late", "nod_1", "nod_2")); n != 1 {
		t.Errorf("a relay that failed its check: %d servers", n)
	}
}
