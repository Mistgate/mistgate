package subs_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"gopkg.in/yaml.v3"
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

func TestMihomoAWGRelayTwinUsesItsOwnKeyAndURIListOmitsAWG(t *testing.T) {
	m := newM3Rig(t)
	if _, err := m.st.W.Exec(`UPDATE node SET name = 'de1', address = '192.0.2.10', country_code = 'DE' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at)
		VALUES ('nod_msk1', 'msk1', '198.51.100.20', 'ZZ', 'active', 'relay-forward/1', 2)`); err != nil {
		t.Fatal(err)
	}
	var inboundID string
	if err := m.st.R.QueryRow(`SELECT id FROM inbound WHERE profile_id = ?`, m.awg).Scan(&inboundID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.st.W.Exec(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_awg_msk1', ?, 'nod_msk1', 'udp', 8443, 1, 'active', ?, 1, 1)`, inboundID,
		`{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`); err != nil {
		t.Fatal(err)
	}
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	_, token := m.newUser("alice", nil)
	response := fetch(h, "/"+token, mihomoUA)
	if response.Code != 200 {
		t.Fatalf("Mihomo = %d %s", response.Code, response.Body.String())
	}
	p := parseYAML(t, response.Body.Bytes())
	if len(p.Proxies) != 2 {
		t.Fatalf("Mihomo proxies = %+v, want the direct AWG server and its relay twin", p.Proxies)
	}
	var awgIndices []int
	for i, proxy := range p.Proxies {
		if proxy["type"] == "wireguard" {
			awgIndices = append(awgIndices, i)
		}
	}
	if len(awgIndices) != 2 || awgIndices[1] != awgIndices[0]+1 {
		t.Fatalf("AWG twin is not immediately after its server: %+v", p.Proxies)
	}
	direct, relay := p.Proxies[awgIndices[0]], p.Proxies[awgIndices[1]]
	if direct["server"] != "192.0.2.10" || relay["server"] != "198.51.100.20" || relay["port"] != 8443 ||
		direct["private-key"] == "" || relay["private-key"] == "" || direct["private-key"] == relay["private-key"] ||
		direct["ip"] == "" || relay["ip"] == "" || direct["ip"] == relay["ip"] || direct["mtu"] != relay["mtu"] ||
		!reflect.DeepEqual(direct["amnezia-wg-option"], relay["amnezia-wg-option"]) {
		t.Fatalf("direct and relay AWG proxies = %+v / %+v", direct, relay)
	}
	var autoProxies []string
	var autoType string
	for i := range p.Groups {
		if p.Groups[i].Name == "Auto" {
			autoType, autoProxies = p.Groups[i].Type, p.Groups[i].Proxies
			break
		}
	}
	if autoType != "url-test" || slices.Index(autoProxies, direct["name"].(string))+1 != slices.Index(autoProxies, relay["name"].(string)) {
		t.Fatalf("Auto does not list the direct and relay proxies in order: %+v", p.Groups)
	}

	// Keep the golden stable across the generated profile's random port and keys. The test above compares the real key and
	// obfuscation fields before this summary is masked.
	type goldenProxy struct {
		Name       string `yaml:"name"`
		Type       string `yaml:"type"`
		Server     string `yaml:"server"`
		Port       string `yaml:"port"`
		IP         string `yaml:"ip,omitempty"`
		PrivateKey string `yaml:"private-key,omitempty"`
		MTU        int    `yaml:"mtu,omitempty"`
	}
	type golden struct {
		Proxies []goldenProxy `yaml:"proxies"`
		Auto    []string      `yaml:"auto"`
	}
	clean := golden{}
	for _, proxy := range p.Proxies {
		port := fmt.Sprint(proxy["port"])
		if proxy["type"] == "wireguard" && proxy["server"] == "192.0.2.10" {
			port = "<profile port>"
		}
		entry := goldenProxy{Name: fmt.Sprint(proxy["name"]), Type: fmt.Sprint(proxy["type"]), Server: fmt.Sprint(proxy["server"]), Port: port}
		if proxy["type"] == "wireguard" {
			entry.IP, entry.PrivateKey, entry.MTU = fmt.Sprint(proxy["ip"]), "<private key>", proxy["mtu"].(int)
		}
		clean.Proxies = append(clean.Proxies, entry)
	}
	clean.Auto = autoProxies
	gold, err := yaml.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	goldenFile(t, "awg_relay_mihomo.yaml", string(gold))

	uri := fetch(h, "/"+token, happUA)
	if uri.Code != 200 {
		t.Fatalf("URI list = %d %s", uri.Code, uri.Body.String())
	}
	body := decode(t, uri.Body.String())
	if strings.Contains(strings.ToLower(body), "awg") || strings.Contains(body, "wireguard") || len(strings.Split(body, "\n")) != 1 {
		t.Errorf("URI list should carry only the Hysteria2 server, got %q", body)
	}
}

func TestAWGRelayNetworkFullLeavesTheDirectProxy(t *testing.T) {
	m := newM3Rig(t)
	if _, err := m.st.W.Exec(`UPDATE node SET name = 'de1', address = '192.0.2.10', country_code = 'DE' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at)
		VALUES ('nod_msk1', 'msk1', '198.51.100.20', 'ZZ', 'active', 'relay-forward/1', 2)`); err != nil {
		t.Fatal(err)
	}
	tiny := must(m.svc.CreateProfile(m.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "awg", Name: "tiny", SettingsJson: `{"subnet4":"10.77.0.0/24","subnet6":""}`,
	}))).Msg.Profile
	inbound := must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: tiny.Id, NodeId: "nod_1"}))).Msg.Inbound
	group := must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "alice", ProfileIds: []string{m.profile, tiny.Id}}))).Msg.Group.Id
	if _, err := m.st.W.Exec(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_awg_msk1', ?, 'nod_msk1', 'udp', 8443, 1, 'active', ?, 1, 1)`, inbound.Id,
		`{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`); err != nil {
		t.Fatal(err)
	}
	bobID, _ := m.user("bob", func(c *adminv1.CreateUserRequest) { c.GroupId = group })
	for _, q := range []string{
		`WITH RECURSIVE n(i) AS (VALUES(3) UNION ALL SELECT i + 1 FROM n WHERE i < 254)
			INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at)
			SELECT 'dev_bob_' || i, ?, CAST('hwid-bob-' || i AS BLOB), 0, 0, 1 FROM n`,
		`WITH RECURSIVE n(i) AS (VALUES(3) UNION ALL SELECT i + 1 FROM n WHERE i < 254)
			INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, path, secret_enc, data_json, created_at)
			SELECT 'crd_bob_' || i, 'dev_bob_' || i, ?, 'awg', ?, '', x'01', '{}', 1 FROM n`,
		`WITH RECURSIVE n(i) AS (VALUES(3) UNION ALL SELECT i + 1 FROM n WHERE i < 254)
			INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at)
			SELECT 'crd_bob_' || i, ?, i, 'pub_bob_' || i, 1 FROM n`,
	} {
		var args []any
		switch {
		case strings.Contains(q, "device (id"):
			args = []any{bobID}
		case strings.Contains(q, "device_credential"):
			args = []any{bobID, tiny.Id}
		default:
			args = []any{tiny.Id}
		}
		if _, err := m.st.W.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	aliceID, token := m.user("alice", func(c *adminv1.CreateUserRequest) { c.GroupId = group })
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	response := fetch(h, "/"+token, mihomoUA)
	if response.Code != 200 {
		t.Fatalf("Mihomo = %d %s", response.Code, response.Body.String())
	}
	p := parseYAML(t, response.Body.Bytes())
	var wireguard []map[string]any
	for _, proxy := range p.Proxies {
		if proxy["type"] == "wireguard" {
			wireguard = append(wireguard, proxy)
		}
	}
	if len(wireguard) != 1 || wireguard[0]["server"] != "192.0.2.10" || wireguard[0]["private-key"] == "" {
		t.Fatalf("full path network hid or changed the direct proxy: %+v", wireguard)
	}
	countCreds := func(path string) int {
		t.Helper()
		var n int
		if err := m.st.R.QueryRow(`SELECT count(*) FROM device_credential WHERE user_id = ? AND profile_id = ? AND path = ? AND revoked_at IS NULL`, aliceID, tiny.Id, path).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if direct, path := countCreds(""), countCreds("relay:nod_msk1"); direct != 1 || path != 0 {
		t.Fatalf("direct/path credentials after a full path network = %d/%d, want 1/0", direct, path)
	}
}
