package warp

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// A wgcf-profile.conf of the shape wgcf v2.3.0 writes (the key is a test value, not an account's).
const (
	testPrivKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	testPeerKey = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
)

var wgcfProfile = `[Interface]
PrivateKey = ` + testPrivKey + `
Address = 172.16.0.2/32
Address = 2001:db8:110::2/128
DNS = 1.1.1.1
DNS = 1.0.0.1
MTU = 1280

[Peer]
PublicKey = ` + testPeerKey + `
AllowedIPs = 0.0.0.0/0
AllowedIPs = ::/0
Endpoint = engage.cloudflareclient.com:2408
`

const wgcfAccount = `access_token = 'tok-SECRET-import'
device_id = 'dev-SECRET-import'
license_key = 'LIC-SECRET-import'
private_key = '` + testPrivKey + `'
`

func TestParseProfile(t *testing.T) {
	p, err := parseProfile(wgcfProfile)
	if err != nil {
		t.Fatal(err)
	}
	if p.privateKey != testPrivKey || p.addressV4 != "172.16.0.2/32" || p.addressV6 != "2001:db8:110::2/128" ||
		p.mtu != 1280 || p.peerPublicKey != testPeerKey || p.endpointHost != "engage.cloudflareclient.com" || p.endpointPort != 2408 {
		t.Errorf("profile = %+v", p)
	}

	// Comma separated addresses, a bare address, a literal endpoint, no MTU, CRLF line ends, a bracketed v6 endpoint.
	p, err = parseProfile(strings.ReplaceAll("[Interface]\nPrivateKey="+testPrivKey+"\nAddress = 172.16.0.2, 2606:4700::1/128\n[Peer]\nPublicKey = "+testPeerKey+"\nEndpoint = [2606:4700:d0::a29f:c001]:500\n", "\n", "\r\n"))
	if err != nil || p.addressV4 != "172.16.0.2/32" || p.addressV6 != "2606:4700::1/128" || p.mtu != 1280 || p.endpointHost != "2606:4700:d0::a29f:c001" || p.endpointPort != 500 {
		t.Errorf("profile = %+v, %v", p, err)
	}
	// An endpoint without a port is the WARP default.
	p, err = parseProfile("[Interface]\nPrivateKey=" + testPrivKey + "\nAddress=172.16.0.2/32\n[Peer]\nPublicKey=" + testPeerKey + "\nEndpoint=162.159.192.1\n")
	if err != nil || p.endpointPort != 2408 {
		t.Errorf("profile = %+v, %v", p, err)
	}

	secretBad := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZQ==" // 31 bytes
	for name, text := range map[string]string{
		"empty":         "",
		"no private":    strings.Replace(wgcfProfile, "PrivateKey = "+testPrivKey, "", 1),
		"short private": strings.Replace(wgcfProfile, testPrivKey, secretBad, 1),
		"no v4":         strings.Replace(wgcfProfile, "Address = 172.16.0.2/32\n", "", 1),
		"bad address":   strings.Replace(wgcfProfile, "172.16.0.2/32", "172.16.0.999/32", 1),
		"bad peer":      strings.Replace(wgcfProfile, testPeerKey, "AAAA", 1),
		"no peer":       wgcfProfile[:strings.Index(wgcfProfile, "[Peer]")],
		"no endpoint":   strings.Replace(wgcfProfile, "Endpoint = engage.cloudflareclient.com:2408\n", "", 1),
		"bad port":      strings.Replace(wgcfProfile, ":2408", ":99999", 1),
		"bad mtu":       strings.Replace(wgcfProfile, "MTU = 1280", "MTU = 9", 1),
		"huge":          wgcfProfile + strings.Repeat("#", maxImportText),
	} {
		_, err := parseProfile(text)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), testPrivKey) || strings.Contains(err.Error(), secretBad) {
			t.Errorf("%s: the error quotes a key: %v", name, err)
		}
	}
}

func TestParseAccount(t *testing.T) {
	a, err := parseAccount(wgcfAccount)
	if err != nil || a.accessToken != "tok-SECRET-import" || a.deviceID != "dev-SECRET-import" || a.licenseKey != "LIC-SECRET-import" || a.privateKey != testPrivKey {
		t.Errorf("account = %+v, %v", a, err)
	}
	a, err = parseAccount("# wgcf\n[section]\naccess_token = \"t\"\ndevice_id = d\nother = 1\n")
	if err != nil || a.accessToken != "t" || a.deviceID != "d" {
		t.Errorf("double quotes and bare values: %+v %v", a, err)
	}
	if _, err := parseAccount("access_token = 'x'\n"); err == nil {
		t.Error("a token without a device id accepted")
	}
	if _, err := parseAccount(strings.Repeat("a", maxImportText+1)); err == nil {
		t.Error("huge file accepted")
	}
}

func (e *env) importProfile(node, conf, acct string) (*adminv1.ImportWarpResponse, error) {
	r, err := e.rpc().ImportWarp(e.ctx, connect.NewRequest(&adminv1.ImportWarpRequest{NodeId: node, ProfileConf: conf, AccountToml: acct}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func TestImport(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, testPrivKey, "tok-SECRET-import", "dev-SECRET-import", "LIC-SECRET-import")
	node := e.addNode("de1")
	e.dns["engage.cloudflareclient.com"] = []netip.Addr{netip.MustParseAddr("2606:4700:d0::a29f:c001"), netip.MustParseAddr("162.159.192.1")}

	resp, err := e.importProfile(node, wgcfProfile, wgcfAccount)
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Account
	if a.Source != adminv1.WarpSource_WARP_SOURCE_IMPORTED || !a.Enabled || !a.HasToken || a.EndpointV4 != "162.159.192.1" ||
		a.AddressV4 != "172.16.0.2/32" || a.Mtu != 1280 || a.PeerPublicKey != testPeerKey || a.TosUrl != "" || a.TosAcceptedBy != "" ||
		a.TosAcceptedUnix != 0 || a.UseReserved || len(a.Ports) != 4 || a.Ports[0] != 2408 {
		t.Errorf("account = %+v", a)
	}
	if n := len(e.cf.callLog()); n != 0 {
		t.Errorf("an import called Cloudflare %d times", n)
	}
	if e.kicks != 1 {
		t.Errorf("recompute asked %d times", e.kicks)
	}
	row, _ := e.st.WarpAccount(e.ctx, node)
	x, err := e.s.open(row)
	if err != nil || x.PrivateKey != testPrivKey || x.Token != "tok-SECRET-import" || x.RegID != "dev-SECRET-import" || x.License != "LIC-SECRET-import" {
		t.Errorf("secrets %v %v", x, err)
	}
	sp, _ := e.s.Spec(e.ctx, node)
	if sp.PrivateKey != testPrivKey || sp.EndpointV4 != "162.159.192.1" || sp.AddressV6 == "" {
		t.Errorf("spec = %+v", sp)
	}
	var audited bool
	for _, r := range e.audits() {
		if r.Action == "warp_import" {
			audited = true
			if r.Actor != "adm_test" || !strings.Contains(r.Params, node) || !strings.Contains(r.Params, `"with_token":"true"`) {
				t.Errorf("audit = %+v", r)
			}
		}
	}
	if !audited {
		t.Errorf("audit = %v", e.auditActions())
	}
	g, _ := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	e.noSecrets(resp, g.Msg)

	// One account per node.
	_, err = e.importProfile(node, wgcfProfile, "")
	e.wantErr(err, connect.CodeFailedPrecondition, "account_exists")
}

func TestImportWithoutToken(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, testPrivKey)
	node := e.addNode("de1")
	conf := strings.Replace(wgcfProfile, "engage.cloudflareclient.com:2408", "162.159.192.1:500", 1)
	resp, err := e.importProfile(node, conf, "")
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Account
	if a.HasToken || a.EndpointV4 != "162.159.192.1" || len(a.Ports) != 4 || a.Ports[0] != 500 || a.Ports[1] != 2408 || a.Ports[3] != 4500 {
		t.Errorf("account = %+v", a)
	}
	// Nothing remote can be done for it: refresh has no token; delete is local only.
	_, err = e.rpc().RefreshWarp(e.ctx, connect.NewRequest(&adminv1.RefreshWarpRequest{NodeId: node}))
	e.wantErr(err, connect.CodeFailedPrecondition, "no_token")
	d, err := e.rpc().DeleteWarp(e.ctx, connect.NewRequest(&adminv1.DeleteWarpRequest{NodeId: node, ConfirmName: "de1"}))
	if err != nil || d.Msg.RemoteDeleted || len(e.cf.callLog()) != 0 {
		t.Errorf("delete: %v %v, calls %v", d, err, e.cf.callLog())
	}
	e.noSecrets(resp)
}

func TestImportRefusals(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, testPrivKey, "tok-SECRET-import")
	node := e.addNode("de1")
	old := e.addNode("old", "doctor/1")
	e.dns["v6only.example.com"] = []netip.Addr{netip.MustParseAddr("2606:4700:d0::a29f:c001")}

	withEndpoint := func(ep string) string { return strings.Replace(wgcfProfile, "engage.cloudflareclient.com:2408", ep, 1) }
	for name, c := range map[string]struct {
		node, conf, acct string
		want             connect.Code
	}{
		"not a profile":         {node, "hello", "", connect.CodeInvalidArgument},
		"name does not resolve": {node, wgcfProfile, "", connect.CodeInvalidArgument}, // e.dns has no entry yet
		"v6 only name":          {node, withEndpoint("v6only.example.com:2408"), "", connect.CodeInvalidArgument},
		"v6 literal":            {node, withEndpoint("[2606:4700:d0::a29f:c001]:2408"), "", connect.CodeInvalidArgument},
		"loopback":              {node, withEndpoint("127.0.0.1:2408"), "", connect.CodeInvalidArgument},
		"unspecified":           {node, withEndpoint("0.0.0.0:2408"), "", connect.CodeInvalidArgument},
		"multicast":             {node, withEndpoint("224.0.0.1:2408"), "", connect.CodeInvalidArgument},
		"account of another":    {node, withEndpoint("162.159.192.1:2408"), strings.Replace(wgcfAccount, testPrivKey, "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU=", 1), connect.CodeInvalidArgument},
		"half an account":       {node, withEndpoint("162.159.192.1:2408"), "access_token = 'x'", connect.CodeInvalidArgument},
		"agent too old":         {old, withEndpoint("162.159.192.1:2408"), "", connect.CodeFailedPrecondition},
		"no node":               {"nod_nope", withEndpoint("162.159.192.1:2408"), "", connect.CodeNotFound},
	} {
		_, err := e.importProfile(c.node, c.conf, c.acct)
		if got, _ := code(err); got != c.want {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), testPrivKey) {
			t.Errorf("%s: the error quotes the key", name)
		}
	}
	if _, err := e.st.WarpAccount(e.ctx, node); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused import left an account: %v", err)
	}
	e.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	_, err := e.importProfile(node, withEndpoint("162.159.192.1:2408"), "")
	e.wantErr(err, connect.CodePermissionDenied, "step-up required")
	e.noSecrets()
}
