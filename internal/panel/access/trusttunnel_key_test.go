package access

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	trusttunnelv1 "github.com/mistgate/mistgate/gen/mistgate/trusttunnel/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	ttwire "github.com/mistgate/mistgate/internal/trusttunnel"
)

type trustTunnelKeyRig struct {
	e      *env
	userID string
	token  string
	ttID   string
	nodeID string
}

func newTrustTunnelKeyRig(t *testing.T, limit int, tlsMode string) trustTunnelKeyRig {
	t.Helper()
	e := newEnv(t)
	registerTrustTunnelForTest(t, e)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	hy2 := e.profile("hy2", `{"port":8443}`)
	e.inbound(hy2.Id, "nod_de1")
	settings := `{"port":443,"h3":false,"sni":"de1.example.com","tls_mode":"` + tlsMode + `","egress":"direct"}`
	tt := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "trusttunnel", Name: "TrustTunnel", SettingsJson: settings}))).Msg.Profile
	e.inbound(tt.Id, "nod_de1")
	group := e.group("default", hy2.Id, tt.Id)
	created := e.userBare("alice", group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = uint32(limit) })
	token := e.tokenOf(created.User.Id)
	e.sql(`UPDATE user SET app_trusttunnel = 1 WHERE id = ?`, created.User.Id)
	return trustTunnelKeyRig{e: e, userID: created.User.Id, token: token, ttID: tt.Id, nodeID: "nod_de1"}
}

func (r trustTunnelKeyRig) user() store.AccessUser {
	r.e.t.Helper()
	return must(r.e.st.Access().User(r.e.ctx, r.userID))
}

func TestTrustTunnelKeyCountsAgainstDeviceLimitAndRevokeFreesIt(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 1, "acme_domain")
	e := r.e
	if made, err := e.s.issueTrustTunnelKey(e.ctx, "admin", r.user()); err != nil || !made {
		t.Fatalf("issue key = %v, %v", made, err)
	}
	counts, err := e.st.Access().DeviceCounts(e.ctx, []string{r.userID}, e.clock)
	if err != nil || counts[r.userID].Used != 1 {
		t.Fatalf("device count = %+v, %v; want one counted key", counts[r.userID], err)
	}
	if err := e.st.SetSettings(e.ctx, map[string]string{store.SettingLinkDeviceLimit: store.LinkModeEnforce}); err != nil {
		t.Fatal(err)
	}

	view := must(e.s.SubscriptionWith(e.ctx, r.token, SubOptions{}))
	if view.Refusal != RefusalDeviceLimit || len(view.Lines) != 0 {
		t.Fatalf("fetch at a full limit = refusal %q and %d lines, want device_limit and no lines", view.Refusal, len(view.Lines))
	}

	var desiredKey int
	for _, inbound := range must(e.s.Desired(e.ctx, r.nodeID)) {
		if inbound.Spec.Protocol == store.TrustTunnelProtocol {
			desiredKey += len(inbound.Creds)
		}
	}
	if desiredKey != 1 {
		t.Fatalf("desired TrustTunnel credentials = %d, want the user's one key", desiredKey)
	}

	if err := e.s.revokeTrustTunnelKey(e.ctx, "admin", r.userID); err != nil {
		t.Fatal(err)
	}
	for _, inbound := range must(e.s.Desired(e.ctx, r.nodeID)) {
		if inbound.Spec.Protocol == store.TrustTunnelProtocol && len(inbound.Creds) != 0 {
			t.Fatalf("revoked TrustTunnel credential remained in desired state: %+v", inbound.Creds)
		}
	}
	if made, err := e.s.issueTrustTunnelKey(e.ctx, "admin", r.user()); err != nil || !made {
		t.Fatalf("reissue after revoke = %v, %v; the freed key slot should be available", made, err)
	}
	if err := e.s.revokeTrustTunnelKey(e.ctx, "admin", r.userID); err != nil {
		t.Fatal(err)
	}
	view = must(e.s.SubscriptionWith(e.ctx, r.token, SubOptions{}))
	if view.Refusal != "" || len(view.Lines) != 1 || view.Servers[0].Protocol == store.TrustTunnelProtocol {
		t.Fatalf("fetch after revoke = refusal %q, lines %+v, used %d", view.Refusal, view.Servers, view.DevicesUsed)
	}
}

func TestTrustTunnelKeySwitchOffUpdateRevokesKey(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	if _, err := r.e.s.issueTrustTunnelKey(r.e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	apps := &adminv1.AppToggles{Happ: true, Amnezia: true, TrustTunnel: new(false)}
	if _, err := r.e.s.UpdateUser(r.e.ctx, req(&adminv1.UpdateUserRequest{UserId: r.userID, Apps: apps})); err != nil {
		t.Fatal(err)
	}
	if r.user().AppTrustTunnel {
		t.Fatal("UpdateUser left the TrustTunnel switch on")
	}
	if _, _, err := r.e.st.Access().TrustTunnelKey(r.e.ctx, r.userID); err != store.ErrNotFound {
		t.Fatalf("switch-off left a live key: %v", err)
	}
	for _, in := range must(r.e.s.Desired(r.e.ctx, r.nodeID)) {
		if in.Spec.Protocol == store.TrustTunnelProtocol && len(in.Creds) != 0 {
			t.Fatalf("switch-off left credentials in desired state: %+v", in.Creds)
		}
	}
}

func TestTrustTunnelKeyAuditContainsNoCredentialOrLink(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	if _, err := r.e.s.issueTrustTunnelKey(r.e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	_, cred, err := r.e.st.Access().TrustTunnelKey(r.e.ctx, r.userID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := r.e.s.vault.Open(cred.SecretEnc, cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows := must(r.e.st.ListAuditQuery(r.e.ctx, store.AuditQuery{Actions: []string{"trusttunnel_key_issue"}, Limit: 10}))
	if len(rows) != 1 {
		t.Fatalf("issue audit rows = %d, want one", len(rows))
	}
	username, password, _ := strings.Cut(string(secret), ":")
	if strings.Contains(rows[0].Params, username) || strings.Contains(rows[0].Params, password) || strings.Contains(rows[0].Params, "tt://") || strings.Contains(rows[0].Params, "password") {
		t.Fatalf("TrustTunnel credential appeared in audit params: %s", rows[0].Params)
	}
}

func TestTrustTunnelKeyRPCLifecycle(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	s := trustTunnelRPC{r.e.s}
	got, err := s.GetTrustTunnelLinks(r.e.ctx, req(&trusttunnelv1.GetTrustTunnelLinksRequest{UserId: r.userID}))
	if err != nil || got.Msg.HasKey || len(got.Msg.Links) != 0 {
		t.Fatalf("initial GetTrustTunnelLinks = %+v, %v", got, err)
	}
	issued, err := s.IssueTrustTunnelKey(r.e.ctx, req(&trusttunnelv1.IssueTrustTunnelKeyRequest{UserId: r.userID}))
	if err != nil || issued.Msg.TrustTunnel == nil || !issued.Msg.TrustTunnel.HasKey || len(issued.Msg.TrustTunnel.Links) != 1 || issued.Msg.TrustTunnel.Links[0].Url == "" {
		t.Fatalf("IssueTrustTunnelKey = %+v, %v", issued, err)
	}
	if decoded, err := ttwire.DecodeDeepLink(issued.Msg.TrustTunnel.Links[0].Url); err != nil || decoded.Hostname != "de1.example.com" {
		t.Fatalf("issued RPC URL = %+v, %v", decoded, err)
	}
	revoked, err := s.RevokeTrustTunnelKey(r.e.ctx, req(&trusttunnelv1.RevokeTrustTunnelKeyRequest{UserId: r.userID}))
	if err != nil || revoked.Msg.TrustTunnel == nil || revoked.Msg.TrustTunnel.HasKey || len(revoked.Msg.TrustTunnel.Links) != 0 {
		t.Fatalf("RevokeTrustTunnelKey = %+v, %v", revoked, err)
	}
}

func TestTrustTunnelLinksRequireAnActivePersonAndSwitch(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	e := r.e
	if _, err := e.s.issueTrustTunnelKey(e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	links, err := e.s.trustTunnelKeyLinks(e.ctx, r.user())
	if err != nil || !links.HasKey || len(links.Links) != 1 || links.Links[0].URI == "" {
		t.Fatalf("active TrustTunnel links = %+v, %v", links, err)
	}

	e.sql(`UPDATE user SET disabled = 1 WHERE id = ?`, r.userID)
	links, err = e.s.trustTunnelKeyLinks(e.ctx, r.user())
	if err != nil || !links.HasKey || len(links.Links) != 0 {
		t.Fatalf("disabled user's links = %+v, %v; want retained key and no URLs", links, err)
	}

	e.sql(`UPDATE user SET disabled = 0, expires_at = ? WHERE id = ?`, e.clock.Add(-time.Second).Unix(), r.userID)
	links, err = e.s.trustTunnelKeyLinks(e.ctx, r.user())
	if err != nil || !links.HasKey || len(links.Links) != 0 {
		t.Fatalf("expired user's links = %+v, %v; want retained key and no URLs", links, err)
	}

	e.sql(`UPDATE user SET app_trusttunnel = 0, expires_at = NULL WHERE id = ?`, r.userID)
	if err := e.s.revokeTrustTunnelKey(e.ctx, "admin", r.userID); err != nil {
		t.Fatal(err)
	}
	links, err = e.s.trustTunnelKeyLinks(e.ctx, r.user())
	if err != nil || links.HasKey || len(links.Links) != 0 {
		t.Fatalf("switch-off links = %+v, %v; want no key and no URLs", links, err)
	}
}

func TestTrustTunnelSelfSignedKeyCarriesSkipNote(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "self_signed")
	if _, err := r.e.s.issueTrustTunnelKey(r.e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	links, err := r.e.s.trustTunnelKeyLinks(r.e.ctx, r.user())
	if err != nil || !links.HasKey || len(links.Links) != 1 {
		t.Fatalf("self-signed result = %+v, %v", links, err)
	}
	if links.Links[0].URI != "" || links.Links[0].Note != plugin.NoteSelfSigned {
		t.Fatalf("self-signed server = %+v, want no URL and %q note", links.Links[0], plugin.NoteSelfSigned)
	}
}

func TestTrustTunnelKeyLinksUsePersonIPv6AndDNS(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	e := r.e
	e.sql(`UPDATE node SET client_ipv6 = 1 WHERE id = ?`, r.nodeID)
	e.sql(`INSERT INTO doctor_result (node_id, check_id, status, params_json, received_unix) VALUES (?, 'ipv6', 1, ?, 1)`,
		r.nodeID, `{"ipv6":"yes","connect":"ok","src":"2001:db8::5","addrs":"2001:db8::5/64"}`)
	preset := must(e.s.dns.CreateDnsPreset(e.ctx, req(&adminv1.CreateDnsPresetRequest{Name: "TrustTunnel test DNS", Servers: []*adminv1.DnsServer{
		{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "192.0.2.53"},
		{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "192.0.2.54"},
	}}))).Msg.Preset
	e.sql(`UPDATE user SET dns_preset_id = ? WHERE id = ?`, preset.Id, r.userID)
	if _, err := e.s.issueTrustTunnelKey(e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	links, err := e.s.trustTunnelKeyLinks(e.ctx, r.user())
	if err != nil || len(links.Links) != 1 {
		t.Fatalf("links = %+v, %v", links, err)
	}
	decoded, err := ttwire.DecodeDeepLink(links.Links[0].URI)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.HasIPv6 || len(decoded.DNSUpstreams) != 2 || decoded.DNSUpstreams[0] != "192.0.2.53" || decoded.DNSUpstreams[1] != "192.0.2.54" {
		t.Fatalf("decoded person's IPv6/DNS = %+v", decoded)
	}
}

func TestTrustTunnelKeyLinksIncludeOnlyTCPRelayTwins(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	e := r.e
	e.node("nod_nl1", "nl1", "192.0.2.22", "active")
	e.node("nod_msk1", "msk1", "192.0.2.23", "active")
	e.sql(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id IN (?, ?)`, "nod_nl1", "nod_msk1")
	var inboundID string
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT id FROM inbound WHERE profile_id = ? AND node_id = ?`, r.ttID, r.nodeID).Scan(&inboundID); err != nil {
		t.Fatal(err)
	}
	createRelay := func(entry string, port uint32) string {
		t.Helper()
		relay := must(e.s.CreateRelay(e.ctx, req(&adminv1.CreateRelayRequest{InboundId: inboundID, ViaNodeId: entry, Port: port}))).Msg.Relay
		e.sql(`UPDATE relay SET enabled = 1, state = 'active' WHERE id = ?`, relay.Id)
		check := store.RelayCheck{At: e.clock.Unix(), Leg: store.RelayStage{Status: "ok"}, Path: store.RelayStage{Status: "ok"}}
		check.Settle()
		if err := e.st.SetRelayCheck(e.ctx, relay.Id, check); err != nil {
			t.Fatal(err)
		}
		return relay.Id
	}
	createRelay("nod_nl1", 8443)
	udpRelay := createRelay("nod_msk1", 9443)
	e.sql(`UPDATE relay SET network = 'udp' WHERE id = ?`, udpRelay)
	if _, err := e.s.issueTrustTunnelKey(e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	links, err := e.s.trustTunnelKeyLinks(e.ctx, r.user())
	if err != nil || len(links.Links) != 2 {
		t.Fatalf("links = %+v, %v; want one direct path and one TCP relay twin", links, err)
	}
	var direct, twin *SubServer
	for i := range links.Links {
		if strings.HasPrefix(links.Links[i].Mark, MarkVia) {
			twin = &links.Links[i]
		} else {
			direct = &links.Links[i]
		}
	}
	if direct == nil || twin == nil || !strings.Contains(twin.PathKey, "nod_nl1") || strings.Contains(twin.PathKey, "nod_msk1") {
		t.Fatalf("direct/twin = %+v / %+v", direct, twin)
	}
	decoded, err := ttwire.DecodeDeepLink(twin.URI)
	if err != nil || decoded.Hostname != "de1.example.com" || len(decoded.Addresses) != 1 || decoded.Addresses[0] != "192.0.2.22:8443" {
		t.Fatalf("relay twin decoded = %+v, %v", decoded, err)
	}
	if direct.Protocol != store.TrustTunnelProtocol || strings.Contains(direct.URI, "192.0.2.22:8443") {
		t.Fatalf("direct link = %+v", direct)
	}
}

func TestTrustTunnelKeyNeverEntersPlainSubscriptionFormats(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	if _, err := r.e.s.issueTrustTunnelKey(r.e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	for _, format := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox} {
		view := must(r.e.s.SubscriptionWith(r.e.ctx, r.token, SubOptions{Format: format, NoPageData: true}))
		body := strings.Join(view.Lines, "\n")
		if format == plugin.FormatURIList {
			body = base64.StdEncoding.EncodeToString([]byte(body))
			decoded, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				t.Fatalf("decode base64 URI list: %v", err)
			}
			body = string(decoded)
		}
		if strings.Contains(body, "tt://") || strings.Contains(strings.ToLower(body), "trusttunnel") {
			t.Errorf("format %s contains the official key: %s", format, body)
		}
		for _, server := range view.Servers {
			if server.Protocol == store.TrustTunnelProtocol {
				t.Errorf("format %s listed a TrustTunnel server: %+v", format, server)
			}
		}
	}
}

func TestTrustTunnelKeyIssueAtLimitReturnsDeviceLimit(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 1, "acme_domain")
	if _, err := r.e.s.Subscription(r.e.ctx, r.token); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.s.issueTrustTunnelKey(r.e.ctx, "admin", r.user()); err == nil || !strings.HasPrefix(err.Error(), "failed_precondition: device_limit: 1/1") {
		t.Fatalf("issue at the device limit = %v, want device_limit: 1/1", err)
	}
	if _, _, err := r.e.st.Access().TrustTunnelKey(r.e.ctx, r.userID); err != store.ErrNotFound {
		t.Fatalf("limit refusal left a key: %v", err)
	}
}

func TestTrustTunnelKeyIssuedAtUsesUTC(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 3, "acme_domain")
	if _, err := r.e.s.issueTrustTunnelKey(r.e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	links, err := r.e.s.trustTunnelKeyLinks(r.e.ctx, r.user())
	if err != nil || links.IssuedAt.Location() != time.UTC {
		t.Fatalf("key issue time = %v, %v", links.IssuedAt, err)
	}
}

// The person's Mistgate app holds a TrustTunnel credential of its own on its device: that is not the key, and the key is made
// beside it. Both reach the node, each with its own device.
func TestTrustTunnelKeyIsMadeBesideTheMistgateAppCredential(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	e := r.e
	appKey := sha256.Sum256([]byte("a key of the app"))
	if _, err := e.s.EnrolLinkDevice(e.ctx, r.userID, r.token, tablet(), appKey[:]); err != nil {
		t.Fatal(err)
	}
	dev, _, err := e.s.ClientDeviceByKey(e.ctx, appKey[:])
	if err != nil {
		t.Fatal(err)
	}
	must(e.s.ClientSubscription(e.ctx, dev, tablet(), []string{"v=1", "trusttunnel=1"}))
	if made, err := e.s.issueTrustTunnelKey(e.ctx, "admin", r.user()); err != nil || !made {
		t.Fatalf("issue beside the app's credential = %v, %v; want a key made", made, err)
	}
	var devices []string
	for _, in := range must(e.s.Desired(e.ctx, r.nodeID)) {
		if in.Spec.Protocol == store.TrustTunnelProtocol {
			for _, c := range in.Creds {
				devices = append(devices, c.DeviceID)
			}
		}
	}
	if len(devices) != 2 {
		t.Fatalf("TrustTunnel credentials on the node = %v, want the app device's and the key's", devices)
	}
	if err := e.s.revokeTrustTunnelKey(e.ctx, "admin", r.userID); err != nil {
		t.Fatal(err)
	}
	var left int
	for _, in := range must(e.s.Desired(e.ctx, r.nodeID)) {
		if in.Spec.Protocol == store.TrustTunnelProtocol {
			left += len(in.Creds)
		}
	}
	if left != 1 {
		t.Fatalf("after the revoke %d TrustTunnel credentials remain, want the app device's alone", left)
	}
}

// A person who cannot use the service has no credential on the nodes and no link, whatever the key; the key is kept for when
// they can again. A person who is deleted takes the key with them.
func TestTrustTunnelKeyFollowsThePersonOnTheNodes(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 5, "acme_domain")
	e := r.e
	if _, err := e.s.issueTrustTunnelKey(e.ctx, "admin", r.user()); err != nil {
		t.Fatal(err)
	}
	onNode := func() int {
		n := 0
		for _, in := range must(e.s.Desired(e.ctx, r.nodeID)) {
			if in.Spec.Protocol == store.TrustTunnelProtocol {
				n += len(in.Creds)
			}
		}
		return n
	}
	if got := onNode(); got != 1 {
		t.Fatalf("credentials on the node of an active person = %d, want 1", got)
	}
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{r.userID}, Enabled: false})))
	if got := onNode(); got != 0 {
		t.Fatalf("credentials on the node of a disabled person = %d, want 0", got)
	}
	if links := must(trustTunnelRPC{e.s}.GetTrustTunnelLinks(e.ctx, req(&trusttunnelv1.GetTrustTunnelLinksRequest{UserId: r.userID}))).Msg; !links.HasKey || len(links.Links) != 0 {
		t.Fatalf("links of a disabled person = %+v, want the key kept and no link", links)
	}
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{r.userID}, Enabled: true})))
	if got := onNode(); got != 1 {
		t.Fatalf("credentials on the node of a re-enabled person = %d, want 1", got)
	}
	// An expired term takes the status with it (the sweep sets it; ExtendUsers brings it back).
	e.sql(`UPDATE user SET expires_at = ?, status = 'expired' WHERE id = ?`, e.clock.Add(-time.Hour).Unix(), r.userID)
	if got := onNode(); got != 0 {
		t.Fatalf("credentials on the node of an expired person = %d, want 0", got)
	}
	e.sql(`UPDATE user SET expires_at = NULL, status = 'active' WHERE id = ?`, r.userID)
	must(e.s.DeleteUsers(e.ctx, req(&adminv1.DeleteUsersRequest{UserIds: []string{r.userID}})))
	if got := onNode(); got != 0 {
		t.Fatalf("credentials on the node of a deleted person = %d, want 0", got)
	}
}
