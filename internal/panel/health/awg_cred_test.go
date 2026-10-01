package health

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The system credential of an AmneziaWG inbound: a peer with its own key pair and an address from the profile's allocator,
// kept in health_probe_cred and never in the tables of devices.

func (e *env) awgProfile(name, settings string) string {
	e.t.Helper()
	return must(e.acc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "awg", Name: name, SettingsJson: settings}))).Msg.Profile.Id
}

func (e *env) awgInbound(profile, nodeID string) string {
	e.t.Helper()
	id := must(e.acc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: profile, NodeId: nodeID}))).Msg.Inbound.Id
	e.exec(`UPDATE inbound SET state = 'active' WHERE id = ?`, id)
	e.s.invalidateSnapshot()
	return id
}

func (e *env) amneziaUser(name, group string, deviceLimit uint32) (id, token string) {
	e.t.Helper()
	u := must(e.acc.CreateUser(e.ctx, connect.NewRequest(&adminv1.CreateUserRequest{Name: name, GroupId: group, DeviceLimit: deviceLimit, Apps: &adminv1.AppToggles{Amnezia: true}}))).Msg.User
	link := must(e.acc.GetSubscriptionLink(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: u.Id}))).Msg.Url
	link = strings.TrimSuffix(link, "/")
	return u.Id, link[strings.LastIndex(link, "/")+1:]
}

// desiredOf is the desired state of a node as cmd/mistgate builds it: the access module's list plus the system credentials.
func (e *env) desiredOf(node string) []statehash.Inbound {
	e.t.Helper()
	ins := must(e.acc.Desired(e.ctx, node))
	out := make([]statehash.Inbound, len(ins))
	for i, x := range ins {
		out[i] = statehash.Inbound(x)
	}
	return e.s.WithProbeCreds(e.ctx, out)
}

// systemCred is the credential of the inbound in a desired state: the one with no user.
func systemCred(t *testing.T, list []statehash.Inbound, inbound string) plugin.UserCred {
	t.Helper()
	for _, in := range list {
		if in.Spec.ID != inbound {
			continue
		}
		for _, c := range in.Creds {
			if c.UserID == "" {
				return c
			}
		}
	}
	t.Fatalf("no system credential for %s in %+v", inbound, list)
	return plugin.UserCred{}
}

func (e *env) cnt(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.R.QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func nodeData(t *testing.T, c plugin.UserCred) awg.NodeData {
	t.Helper()
	var nd awg.NodeData
	if err := json.Unmarshal(c.Data, &nd); err != nil {
		t.Fatal(err)
	}
	return nd
}

func TestAWGSystemCredentialLifecycle(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("de2", "hetzner", true)
	pid := e.awgProfile("awg", "")
	in1, in2 := e.awgInbound(pid, "de1"), e.awgInbound(pid, "de2")
	grp := must(e.acc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g", ProfileIds: []string{pid}}))).Msg.Group.Id
	alice, token := e.amneziaUser("alice", grp, 1)

	// made with the inbound, by the desired state: the access module alone never mentions it
	if n := len(must(e.acc.Desired(e.ctx, "de1"))[0].Creds); n != 0 {
		t.Fatalf("access.Desired holds %d credentials: the system one must come from health only", n)
	}
	first := e.desiredOf("de1")
	sys := systemCred(t, first, in1)
	if !strings.HasPrefix(sys.CredID, "crd_") || sys.DeviceID != "" || sys.RateLimitBps != probeRateLimitBps || !sys.ValidUntil.IsZero() {
		t.Fatalf("system credential: %+v", sys)
	}
	row := must(e.st.ProbeCred(e.ctx, in1))
	if row.CredID != sys.CredID || row.AWGIdx != 2 {
		t.Fatalf("row: %+v", row)
	}
	nd := nodeData(t, sys)
	server := first[0].Spec.Tunnel
	a4 := netip.MustParsePrefix(nd.AllowedIPs[0])
	if len(nd.AllowedIPs) != 2 || a4.Addr().String() != "10.66.4.2" || a4.Bits() != 32 || !server.AddrV4.Masked().Contains(a4.Addr()) || a4.Addr() == server.AddrV4.Addr() ||
		netip.MustParsePrefix(nd.AllowedIPs[1]).Addr().String() != "fd66:66:0:1::2" {
		t.Fatalf("addresses %v, server %v", nd.AllowedIPs, server.AddrV4)
	}
	// its own key pair: the sealed secret opens to the private key of the public key the node got
	secret := must(e.s.probeSecret(e.ctx, &target{in: store.FleetInboundRow{ID: in1, Protocol: "awg"}}))
	var ps struct {
		Priv string `json:"priv"`
		PSK  string `json:"psk"`
		IP4  string `json:"ip4"`
		IP6  string `json:"ip6"`
	}
	if err := json.Unmarshal([]byte(secret), &ps); err != nil {
		t.Fatal(err)
	}
	raw := must(base64.StdEncoding.DecodeString(ps.Priv))
	if pub := must(ecdh.X25519().NewPrivateKey(raw)).PublicKey().Bytes(); base64.StdEncoding.EncodeToString(pub) != nd.PublicKey || ps.PSK != nd.PSK || ps.IP4 != "10.66.4.2" {
		t.Fatalf("the secret does not belong to the verifier: %+v vs %+v", ps, nd)
	}
	if strings.Contains(string(row.SecretEnc), ps.Priv) || strings.Contains(string(row.SecretEnc), nd.PublicKey) {
		t.Fatal("the secret is not sealed")
	}
	if strings.Contains(string(sys.Data), ps.Priv) {
		t.Fatal("the private key went to the node")
	}

	// stable across calls and across a restart of the module
	again := New(e.st, e.v, e.s.reg, e.fl, e.s.cfg).WithProbeCreds(e.ctx, must(toStateHashed(e)))
	for _, got := range [][]statehash.Inbound{e.desiredOf("de1"), again} {
		if c := systemCred(t, got, in1); c.CredID != sys.CredID || string(c.Data) != string(sys.Data) {
			t.Fatal("the system credential changed between calls")
		}
	}
	// the input is not modified: the real users' list keeps its length
	if n := len(must(e.acc.Desired(e.ctx, "de1"))[0].Creds); n != 0 {
		t.Fatalf("a real list was touched: %d", n)
	}

	// IPAM: the user's device takes the next address, never the probe's, and the probe is not a device of the limit
	dev := must(e.acc.CreateAwgDevice(e.ctx, connect.NewRequest(&adminv1.CreateAwgDeviceRequest{UserId: alice, ProfileId: pid, Platform: "ios", Label: "phone"}))).Msg
	if dev.Device.Address != "10.66.4.3, fd66:66:0:1::3" {
		t.Fatalf("the device got %q, the probe holds .2", dev.Device.Address)
	}
	if _, err := e.acc.CreateAwgDevice(e.ctx, connect.NewRequest(&adminv1.CreateAwgDeviceRequest{UserId: alice, ProfileId: pid, Platform: "ios", Label: "tablet"})); err == nil ||
		!strings.Contains(err.Error(), "device_limit: 1/1") {
		t.Fatalf("the second device: %v (the limit counts the one device, not the probes)", err)
	}
	// the other inbound of the profile has a credential of its own, with its own key and address
	sys2 := systemCred(t, e.desiredOf("de2"), in2)
	nd2 := nodeData(t, sys2)
	if sys2.CredID == sys.CredID || nd2.PublicKey == nd.PublicKey || nd2.PSK == nd.PSK || nd2.AllowedIPs[0] != "10.66.4.4/32" {
		t.Fatalf("second inbound: %+v vs %+v", nd2, nd)
	}
	// the device is valid on both inbounds of the profile; the probes only on their own
	if c := e.cnt(`SELECT count(*) FROM device_credential WHERE profile_id = ?`, pid); c != 1 {
		t.Fatalf("device credentials of the profile: %d", c)
	}

	// hidden: no table of devices, peers or credentials has it, and no view shows it
	if n := e.cnt(`SELECT count(*) FROM device_credential WHERE id IN (?, ?)`, sys.CredID, sys2.CredID); n != 0 {
		t.Fatalf("the system credential is in device_credential: %d", n)
	}
	if n := e.cnt(`SELECT count(*) FROM awg_peer WHERE public_key IN (?, ?)`, nd.PublicKey, nd2.PublicKey); n != 0 {
		t.Fatalf("the system credential is in awg_peer: %d", n)
	}
	if e.cnt(`SELECT count(*) FROM awg_peer`) != 1 || e.cnt(`SELECT count(*) FROM device`) != 1 {
		t.Fatal("one device, one peer: the probes are neither")
	}
	creds, err := e.st.Access().DesiredCreds(e.ctx, "de1")
	if err != nil || len(creds) != 1 || creds[0].CredID == sys.CredID {
		t.Fatalf("DesiredCreds of the access module: %+v %v", creds, err)
	}
	needles := map[string]string{"cred id": sys.CredID, "cred id 2": sys2.CredID, "public key": nd.PublicKey, "public key 2": nd2.PublicKey,
		"psk": nd.PSK, "psk 2": nd2.PSK, "private key": ps.Priv, "secret": secret, "sealed": string(row.SecretEnc),
		"address": "10.66.4.2/32", "address 2": "10.66.4.4/32"}
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
	gu, err := e.acc.GetUser(e.ctx, connect.NewRequest(&adminv1.GetUserRequest{UserId: alice}))
	msg("GetUser", gu.Msg, err)
	lp, err := e.acc.ListProfiles(e.ctx, connect.NewRequest(&adminv1.ListProfilesRequest{}))
	msg("ListProfiles", lp.Msg, err)
	gp, err := e.acc.GetProfile(e.ctx, connect.NewRequest(&adminv1.GetProfileRequest{ProfileId: pid}))
	msg("GetProfile", gp.Msg, err)
	lg, err := e.acc.ListGroups(e.ctx, connect.NewRequest(&adminv1.ListGroupsRequest{}))
	msg("ListGroups", lg.Msg, err)
	msg("CreateAwgDevice", dev, nil) // the user's own configs
	sv, err := e.acc.Subscription(e.ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sv)
	dumps["Subscription"] = string(b)
	pv, _, err := e.acc.PreviewSubscription(e.ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(pv)
	dumps["PreviewSubscription"] = string(b)
	e.round(in1, cOK, "")
	la, err := rpc{e.s}.ListAlerts(e.ctx, connect.NewRequest(&adminv1.ListAlertsRequest{}))
	msg("ListAlerts", la.Msg, err)
	gc, err := rpc{e.s}.GetChecks(e.ctx, connect.NewRequest(&adminv1.GetChecksRequest{}))
	msg("GetChecks", gc.Msg, err)
	gd, err := rpc{e.s}.GetDoctor(e.ctx, connect.NewRequest(&adminv1.GetDoctorRequest{}))
	msg("GetDoctor", gd.Msg, err)
	for view, text := range dumps {
		for what, needle := range needles {
			if strings.Contains(text, needle) {
				t.Errorf("%s contains the system credential (%s)", view, what)
			}
		}
	}
	if !strings.Contains(dumps["CreateAwgDevice"], "[Interface]") || !strings.Contains(dumps["CreateAwgDevice"], "10.66.4.3") { // the views do render credentials
		t.Fatalf("the device's own config is not in the dump: %s", dumps["CreateAwgDevice"])
	}

	// traffic and sessions under it are the checker's: no user, no bucket, no online list, no anomaly
	out, err := e.st.IngestStats(e.ctx, store.FleetStatsIn{
		NodeID: "de1", Instance: "i1", Seq: 0, Now: e.clock.Now(), HourStart: e.clock.Now().Unix() - e.clock.Now().Unix()%3600,
		Traffic:  []store.FleetTraffic{{CredID: sys.CredID, InboundID: in1, Up: 10, Down: 1000}},
		Sessions: []store.FleetSessionRef{{CredID: sys.CredID, InboundID: in1}},
	})
	if err != nil || out.Probe != 1 || out.Skipped != 0 || len(out.Refs) != 0 || len(out.Users) != 0 {
		t.Fatalf("ingest: %+v %v", out, err)
	}
	if n := e.cnt(`SELECT count(*) FROM traffic_bucket`) + e.cnt(`SELECT count(*) FROM node_traffic_hour`); n != 0 {
		t.Fatalf("the probe's traffic was counted: %d", n)
	}

	// gone with its inbound, and its address is free again for the next device
	if err := e.st.Access().DeleteInbound(e.ctx, in1, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ProbeCred(e.ctx, in1); err != store.ErrNotFound {
		t.Fatalf("credential outlived its inbound: %v", err)
	}
	bob, _ := e.amneziaUser("bob", grp, 1)
	if d := must(e.acc.CreateAwgDevice(e.ctx, connect.NewRequest(&adminv1.CreateAwgDeviceRequest{UserId: bob, ProfileId: pid, Platform: "ios"}))).Msg.Device; d.Address != "10.66.4.2, fd66:66:0:1::2" {
		t.Fatalf("the freed address was not reused: %q", d.Address)
	}
	// the other inbound keeps its own
	if c := systemCred(t, e.desiredOf("de2"), in2); c.CredID != sys2.CredID {
		t.Fatal("the credential of the other inbound changed")
	}
}

// A network with no free address gives the probe no credential and the node no peer to refuse: the round is skipped, the
// inbound's real users are untouched, and nothing panics.
func TestAWGSystemCredentialWithAFullNetwork(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	pid := e.awgProfile("tiny", `{"subnet4":"10.77.0.0/24","subnet6":""}`)
	in := e.awgInbound(pid, "de1")
	grp := must(e.acc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g", ProfileIds: []string{pid}}))).Msg.Group.Id
	var fill []string
	for i := range 3 {
		id, _ := e.amneziaUser(fmt.Sprintf("fill%d", i), grp, 100)
		fill = append(fill, id)
	}
	max := must(awg.MaxPeerIndex(json.RawMessage(`{"version":"3.1","port":10001,"mtu":1280,"egress":"direct","subnet4":"10.77.0.0/24","subnet6":"","obfuscation":{}}`)))
	if max != 254 {
		t.Fatalf("max %d", max)
	}
	add := func(i int) error {
		_, err := e.st.Access().AddAWGDevice(e.ctx, store.AWGDeviceAdd{Device: store.AccessDevice{ID: fmt.Sprintf("dev_f%d", i), UserID: fill[i/100]}, ProfileID: pid, MaxIdx: max, Limit: 100}, e.clock.Now(),
			func(idx int) (store.AccessCred, string, error) {
				return store.AccessCred{ID: fmt.Sprintf("crd_f%d", i), Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, fmt.Sprintf("pub%d", i), nil
			})
		return err
	}
	for i := range 253 { // every index 2..254
		if err := add(i); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	got := e.desiredOf("de1")
	if len(got) != 1 || len(got[0].Creds) != 253 { // the devices only: the probe has no address to take
		t.Fatalf("a credential was made without an address: %d inbounds, %d credentials", len(got), len(got[0].Creds))
	}
	for _, c := range got[0].Creds {
		if c.UserID == "" {
			t.Fatalf("a system credential in a full network: %+v", c)
		}
	}
	if _, err := e.st.ProbeCred(e.ctx, in); err != store.ErrNotFound {
		t.Fatalf("row: %v", err)
	}
	sn := must(e.s.snapshot(e.ctx))
	if _, skipped := e.s.attempt(e.ctx, sn.targets[in]); !skipped {
		t.Fatal("a round without a credential was reported as a failure of the node")
	}
}
