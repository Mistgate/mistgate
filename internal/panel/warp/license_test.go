package warp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// Placeholders, not keys of anyone: the fake Cloudflare decides which of them it takes (harness_test.go, fakeCF.keys).
const (
	keyOK    = "TESTPLUS-AAAAAAAA-BBBBBBBB"
	keyOther = "TESTPLUS-CCCCCCCC-DDDDDDDD"
	keyBad   = "TESTBADK-AAAAAAAA-BBBBBBBB"
	keyLimit = "TESTFULL-AAAAAAAA-BBBBBBBB"
	keyBusy  = "TESTBUSY-AAAAAAAA-BBBBBBBB"
	keyDown  = "TESTDOWN-AAAAAAAA-BBBBBBBB"
	keyOne   = "TESTONEK-AAAAAAAA-BBBBBBBB" // allows one device
)

func (e *env) setKeys(m map[string]string) {
	e.cf.mu.Lock()
	defer e.cf.mu.Unlock()
	for k, v := range m {
		e.cf.keys[k] = v
	}
}

func (e *env) setLicense(node, key string) (*adminv1.WarpAccount, error) {
	r, err := e.rpc().SetWarpLicense(e.ctx, connect.NewRequest(&adminv1.SetWarpLicenseRequest{NodeId: node, LicenseKey: key}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Account, nil
}

func (e *env) reregister(node string) (*adminv1.WarpAccount, error) {
	r, err := e.rpc().RegisterWarp(e.ctx, connect.NewRequest(&adminv1.RegisterWarpRequest{NodeId: node, AcceptTos: true, TosUrlShown: TOSURL, ReplaceExisting: true}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Account, nil
}

func (e *env) account(node string) *adminv1.WarpAccount {
	e.t.Helper()
	g, err := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if err != nil || g.Msg.Account == nil {
		e.t.Fatalf("GetWarp: %v %v", g, err)
	}
	e.noSecrets(g.Msg)
	return g.Msg.Account
}

func (e *env) boundKeys() []string {
	e.cf.mu.Lock()
	defer e.cf.mu.Unlock()
	return append([]string(nil), e.cf.bound...)
}

// auditHas is whether some audit row of the action has the text in its params.
func (e *env) auditHas(action, text string) bool {
	for _, p := range e.auditParams(action) {
		if strings.Contains(p, text) {
			return true
		}
	}
	return false
}

func (e *env) auditParams(action string) []string {
	var out []string
	for _, r := range e.audits() {
		if r.Action == action {
			out = append(out, r.Params)
		}
	}
	return out
}

func TestSetLicenseBindsTheKeyAndShowsPlus(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, keyOK, keyOther)
	e.setKeys(map[string]string{keyOK: "ok"})
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	key := e.privateKeyOf(node)
	if a := e.account(node); a.AccountType != "free" || a.HasLicense {
		t.Fatalf("before: %+v", a)
	}

	a, err := e.setLicense(node, "  "+keyOK+" \n")
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountType != "plus" || !a.HasLicense || a.LicenseError != "" || !a.HasToken {
		t.Errorf("account = %+v", a)
	}
	// One bind, then the device is read back for its type; the key is the one typed, trimmed.
	if calls := e.cf.callLog(); !slices.Equal(calls[len(calls)-2:], []string{"PUT /reg/dev-SECRET-1/account", "GET /reg/dev-SECRET-1"}) {
		t.Errorf("calls = %v", calls)
	}
	if got := e.boundKeys(); !slices.Equal(got, []string{keyOK}) {
		t.Errorf("bound = %v", got)
	}
	if e.steps != 2 { // the registration and this
		t.Errorf("step-up asked %d times", e.steps)
	}
	if sp, _ := e.s.Spec(e.ctx, node); sp == nil || sp.PrivateKey != key {
		t.Errorf("the tunnel's private key changed: %+v", sp)
	}
	row, _ := e.st.WarpAccount(e.ctx, node)
	x, err := e.s.open(row)
	if err != nil || x.PlusKey != keyOK || x.License != "LIC-SECRET-1" || x.Token != "tok-SECRET-1" || row.AccountType != "plus" {
		t.Errorf("stored secrets %v (type %q) %v", x, row.AccountType, err)
	}
	// The key is only a fact in the audit, and nowhere else but the sealed account.
	if got := e.auditParams("warp_license"); len(got) != 1 || !strings.Contains(got[0], `"account_type":"plus"`) || strings.Contains(got[0], "TESTPLUS") {
		t.Errorf("audit = %v", got)
	}
	g, _ := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	e.noSecrets(g.Msg)
	if strings.Contains(e.log.String(), "TESTPLUS") {
		t.Error("the log has the key")
	}
	if !a.HasLicense || g.Msg.Account.AccountType != "plus" {
		t.Errorf("get = %+v", g.Msg.Account)
	}

	// A second key replaces the first.
	e.setKeys(map[string]string{keyOther: "ok"})
	if _, err := e.setLicense(node, keyOther); err != nil {
		t.Fatal(err)
	}
	row, _ = e.st.WarpAccount(e.ctx, node)
	if x, _ := e.s.open(row); x.PlusKey != keyOther {
		t.Errorf("key = %q", x.PlusKey)
	}
	e.noSecrets()
}

func TestSetLicenseRefusals(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, keyBad, keyLimit, keyBusy, keyDown)
	e.setKeys(map[string]string{keyLimit: "limit", keyBusy: "429", keyDown: "down"})
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	before := len(e.cf.callLog())
	for name, c := range map[string]struct {
		key  string
		want connect.Code
		msg  string
	}{
		"not a key at all":          {"hello", connect.CodeInvalidArgument, "license_invalid"},
		"letters Cloudflare lacks":  {"TESTBADK-AAAAAAAA-BBBBBB!!", connect.CodeInvalidArgument, "license_invalid"},
		"empty":                     {"  ", connect.CodeInvalidArgument, "license_invalid"},
		"Cloudflare does not know":  {keyBad, connect.CodeInvalidArgument, "license_invalid"},
		"the key is on all devices": {keyLimit, connect.CodeFailedPrecondition, "license_device_limit"},
		"Cloudflare is busy":        {keyBusy, connect.CodeResourceExhausted, "license_rate_limited"},
		"no answer":                 {keyDown, connect.CodeUnavailable, "cloudflare_unreachable"},
	} {
		_, err := e.setLicense(node, c.key)
		if gotC, gotM := code(err); err == nil || gotC != c.want || gotM != c.msg {
			t.Errorf("%s: %v (%v %q), want %v %q", name, err, gotC, gotM, c.want, c.msg)
		}
		// Nothing about the account changed, and the refusal said nothing of the key.
		if a := e.account(node); a.AccountType != "free" || a.HasLicense || a.LicenseError != "" {
			t.Errorf("%s: account = %+v", name, a)
		}
	}
	// Only the keys that look like keys reached Cloudflare.
	bound := e.boundKeys()
	for _, k := range []string{keyBad, keyLimit, keyBusy, keyDown} {
		if !slices.Contains(bound, k) {
			t.Errorf("%q was not sent to Cloudflare: %v", k, bound)
		}
	}
	for _, k := range bound {
		if k == "hello" || strings.Contains(k, "!") || strings.TrimSpace(k) == "" {
			t.Errorf("%q was sent to Cloudflare", k)
		}
	}
	if n := len(e.cf.callLog()) - before; n < 4 {
		t.Errorf("Cloudflare was asked %d times", n)
	}
	e.noSecrets()
	if strings.Contains(e.log.String(), "TESTBADK") || strings.Contains(e.log.String(), "TESTFULL") {
		t.Error("Cloudflare's text for a refusal carried the key into the log")
	}

	// The device is gone at Cloudflare: the account is revoked, not the key bad.
	e.cf.mu.Lock()
	e.cf.regs["dev-SECRET-1"].deleted = true
	e.cf.mu.Unlock()
	_, err := e.setLicense(node, keyOK)
	e.wantErr(err, connect.CodeFailedPrecondition, "account_revoked")
}

func TestSetLicenseNeedsAnAccountWithAToken(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, keyOK)
	e.setKeys(map[string]string{keyOK: "ok"})
	node := e.addNode("de1")
	_, err := e.setLicense(node, keyOK)
	e.wantErr(err, connect.CodeFailedPrecondition, "no_account")
	_, err = e.setLicense("nod_nope", keyOK)
	e.wantErr(err, connect.CodeNotFound, "node_not_found")
	_, err = e.setLicense("", keyOK)
	e.wantErr(err, connect.CodeInvalidArgument, "node_id_required")

	// An imported profile without wgcf-account.toml has no device id and token to bind a key to.
	conf := strings.Replace(wgcfProfile, "engage.cloudflareclient.com:2408", "162.159.192.1:2408", 1)
	if _, err := e.importProfile(node, conf, ""); err != nil {
		t.Fatal(err)
	}
	_, err = e.setLicense(node, keyOK)
	e.wantErr(err, connect.CodeFailedPrecondition, "no_token")

	// The step-up comes first: a refusal costs no call.
	e.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	_, err = e.setLicense(node, keyOK)
	e.wantErr(err, connect.CodePermissionDenied, "step-up required")
	if n := e.cf.count("PUT"); n != 0 {
		t.Errorf("%d binds", n)
	}
}

func TestSetLicenseOnAnImportedAccountWithAToken(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, keyOK, testPrivKey, "tok-SECRET-imp", "dev-SECRET-imp")
	e.setKeys(map[string]string{keyOK: "ok"})
	e.cf.mu.Lock()
	e.cf.regs["dev-SECRET-imp"] = &fakeReg{id: "dev-SECRET-imp", token: "tok-SECRET-imp", license: "LIC-SECRET-imp", endpoint: "162.159.192.1:0"}
	e.cf.mu.Unlock()
	node := e.addNode("de1")
	conf := strings.Replace(wgcfProfile, "engage.cloudflareclient.com:2408", "162.159.192.1:2408", 1)
	toml := "access_token = 'tok-SECRET-imp'\ndevice_id = 'dev-SECRET-imp'\nlicense_key = 'LIC-SECRET-imp'\nprivate_key = '" + testPrivKey + "'\n"
	r, err := e.importProfile(node, conf, toml)
	if err != nil || r.Account.AccountType != "free" {
		t.Fatalf("import: %+v %v", r, err)
	}
	a, err := e.setLicense(node, keyOK)
	if err != nil || a.AccountType != "plus" || !a.HasLicense || a.Source != adminv1.WarpSource_WARP_SOURCE_IMPORTED {
		t.Fatalf("set: %+v %v", a, err)
	}
	e.noSecrets(r, &adminv1.GetWarpResponse{Account: a})
}

func TestImportAsksCloudflareForTheType(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, testPrivKey, "tok-SECRET-imp", "dev-SECRET-imp", "LIC-SECRET-imp")
	e.cf.mu.Lock()
	e.cf.regs["dev-SECRET-imp"] = &fakeReg{id: "dev-SECRET-imp", token: "tok-SECRET-imp", license: "LIC-SECRET-imp", endpoint: "162.159.192.1:0", kind: "unlimited"}
	e.cf.mu.Unlock()
	conf := strings.Replace(wgcfProfile, "engage.cloudflareclient.com:2408", "162.159.192.1:2408", 1)
	toml := "access_token = 'tok-SECRET-imp'\ndevice_id = 'dev-SECRET-imp'\nlicense_key = 'LIC-SECRET-imp'\nprivate_key = '" + testPrivKey + "'\n"

	node := e.addNode("de1")
	r, err := e.importProfile(node, conf, toml)
	if err != nil {
		t.Fatal(err)
	}
	if r.Account.AccountType != "plus" || r.Account.HasLicense {
		t.Errorf("account = %+v", r.Account) // plus by Cloudflare's word; the panel holds no key of the owner's
	}
	row, _ := e.st.WarpAccount(e.ctx, node)
	if row.AccountType != "plus" {
		t.Errorf("stored type %q", row.AccountType)
	}
	e.noSecrets(r)

	// Cloudflare not answering for the device does not fail the import.
	other := e.addNode("de2")
	r, err = e.importProfile(other, conf, strings.ReplaceAll(toml, "dev-SECRET-imp", "dev-SECRET-gone"))
	if err != nil || r.Account.AccountType != "" || !r.Account.HasToken {
		t.Errorf("import with an unknown device: %+v %v", r, err)
	}
	e.noSecrets(r)
}

func TestReregisterCarriesTheKey(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, keyOK, keyOther, keyLimit)
	e.setKeys(map[string]string{keyOK: "ok", keyOther: "ok"})
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	if _, err := e.setLicense(node, keyOK); err != nil {
		t.Fatal(err)
	}

	// A re-registration binds the same key to the new device on its own.
	a, err := e.reregister(node)
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountType != "plus" || !a.HasLicense || a.LicenseError != "" {
		t.Errorf("account = %+v", a)
	}
	if got := e.boundKeys(); !slices.Equal(got, []string{keyOK, keyOK}) {
		t.Errorf("bound = %v", got)
	}
	if !e.cf.reg(1).deleted || e.cf.reg(2).deleted {
		t.Error("the old device is not the one deleted")
	}
	if got := e.auditParams("warp_reregister"); len(got) != 1 || !strings.Contains(got[0], `"license":"applied"`) {
		t.Errorf("audit = %v", got)
	}

	// Cloudflare refuses it for the new device (the key is on all the devices it allows): the re-registration still
	// goes through, the account is free, the card says why and the key is kept.
	e.setKeys(map[string]string{keyOK: "limit"})
	a, err = e.reregister(node)
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountType != "free" || !a.HasLicense || a.LicenseError != "license_device_limit" || !a.HasToken {
		t.Errorf("account after a refusal = %+v", a)
	}
	if sp, _ := e.s.Spec(e.ctx, node); sp == nil || !sp.Enabled {
		t.Errorf("spec = %+v", sp)
	}
	if !e.auditHas("warp_reregister", `"license":"license_device_limit"`) {
		t.Errorf("audit = %v", e.auditParams("warp_reregister"))
	}
	if card := e.account(node); card.LicenseError != "license_device_limit" || card.AccountType != "free" {
		t.Errorf("card = %+v", card)
	}

	// The owner gives another key: the account is plus again and the refusal is gone.
	a, err = e.setLicense(node, keyOther)
	if err != nil || a.AccountType != "plus" || a.LicenseError != "" {
		t.Fatalf("set: %+v %v", a, err)
	}

	// A node that never had a key asks Cloudflare for none.
	plain := e.addNode("de2")
	if _, err := e.register(plain); err != nil {
		t.Fatal(err)
	}
	puts := e.cf.count("PUT")
	if a, err := e.reregister(plain); err != nil || a.HasLicense || a.AccountType != "free" || e.cf.count("PUT") != puts {
		t.Errorf("plain re-registration: %+v %v, binds %d -> %d", a, err, puts, e.cf.count("PUT"))
	}
	if !e.auditHas("warp_reregister", `"license":"none"`) {
		t.Errorf("audit = %v", e.auditParams("warp_reregister"))
	}
	e.noSecrets()
	if strings.Contains(e.log.String(), "TESTPLUS") || strings.Contains(e.log.String(), "TESTFULL") {
		t.Error("the log has a key")
	}
}

func TestAutoReregisterCarriesTheKey(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, keyOK)
	e.setKeys(map[string]string{keyOK: "ok"})
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	if _, err := e.setLicense(node, keyOK); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rpc().UpdateWarpRegistrationParams(e.ctx, connect.NewRequest(&adminv1.UpdateWarpRegistrationParamsRequest{
		Params: &adminv1.WarpRegistrationParams{AutoReregister: true}})); err != nil {
		t.Fatal(err)
	}
	if err := e.s.NeedsAttention(e.ctx, node, "revoked"); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.s.AutoReregister(e.ctx, node); !ok || err != nil {
		t.Fatalf("auto re-registration: %v %v", ok, err)
	}
	if a := e.account(node); a.AccountType != "plus" || !a.HasLicense || a.LicenseError != "" {
		t.Errorf("account = %+v", a)
	}
	if got := e.boundKeys(); !slices.Equal(got, []string{keyOK, keyOK}) {
		t.Errorf("bound = %v", got)
	}

	// And a refusal does not undo it.
	e.setKeys(map[string]string{keyOK: "limit"})
	e.now = e.now.Add(2 * time.Hour)
	if err := e.s.NeedsAttention(e.ctx, node, "revoked"); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.s.AutoReregister(e.ctx, node); !ok || err != nil {
		t.Fatalf("auto re-registration: %v %v", ok, err)
	}
	if a := e.account(node); a.AccountType != "free" || !a.HasLicense || a.LicenseError != "license_device_limit" {
		t.Errorf("account = %+v", a)
	}
	e.noSecrets()
}

func TestLicenseFailureClasses(t *testing.T) {
	api := func(status int, msg string) error { return &APIError{Status: status, Message: msg} }
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"no answer":           {errors.New("cloudflare request timed out"), "cloudflare_unreachable"},
		"rate limit":          {api(http.StatusTooManyRequests, "Too many requests"), "license_rate_limited"},
		"device limit":        {api(http.StatusBadRequest, "Maximum number of devices reached"), "license_device_limit"},
		"device limit, other": {api(http.StatusForbidden, "device limit exceeded"), "license_device_limit"},
		"bad key":             {api(http.StatusBadRequest, "Invalid license key"), "license_invalid"},
		"bad key, 404":        {api(http.StatusNotFound, "License not found"), "license_invalid"},
		"bad key, no words":   {api(http.StatusBadRequest, ""), "license_invalid"},
		"token refused":       {api(http.StatusUnauthorized, "Invalid token"), "account_revoked"},
		"device gone":         {api(http.StatusNotFound, "Registration not found"), "account_revoked"},
		"blocked address":     {api(http.StatusForbidden, ""), "license_rejected"},
		"server error":        {api(http.StatusBadGateway, ""), "license_rejected"},
	} {
		if got := licenseFailure(c.err); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	for in, want := range map[string]string{"free": "free", "FREE": "free", "unlimited": "plus", "limited": "plus", "plus": "plus", "team": "team", "": ""} {
		if got := normalAccountType(in); got != want {
			t.Errorf("account type %q -> %q, want %q", in, got, want)
		}
	}
}

// A key that allows one device is held by the old device until it is deleted: the re-registration, by hand or on its own, takes the
// old device away first and only then binds the key to the new one, so the account is plus again and not refused for the limit.
func TestReregisterFreesTheKeyBeforeBindingIt(t *testing.T) {
	e := newEnv(t)
	e.secret = append(e.secret, keyOne)
	e.setKeys(map[string]string{keyOne: "one"})
	node := e.addNode("de1")
	if _, err := e.register(node); err != nil {
		t.Fatal(err)
	}
	if a, err := e.setLicense(node, keyOne); err != nil || a.AccountType != "plus" {
		t.Fatalf("set: %+v %v", a, err)
	}

	a, err := e.reregister(node)
	if err != nil || a.AccountType != "plus" || !a.HasLicense || a.LicenseError != "" {
		t.Fatalf("by hand: %+v %v", a, err)
	}
	if !e.cf.reg(1).deleted || e.cf.reg(2).deleted {
		t.Error("the old device is not the one deleted")
	}
	if card := e.account(node); card.AccountType != "plus" || card.LicenseError != "" {
		t.Errorf("card = %+v", card)
	}
	row, err := e.st.WarpAccount(e.ctx, node)
	if x, oerr := e.s.open(row); err != nil || oerr != nil || x.PlusKey != keyOne || x.LicenseError != "" || row.AccountType != "plus" {
		t.Errorf("stored: %+v %v %v", row, err, oerr)
	}

	if _, err := e.rpc().UpdateWarpRegistrationParams(e.ctx, connect.NewRequest(&adminv1.UpdateWarpRegistrationParamsRequest{
		Params: &adminv1.WarpRegistrationParams{AutoReregister: true}})); err != nil {
		t.Fatal(err)
	}
	if err := e.s.NeedsAttention(e.ctx, node, "revoked"); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.s.AutoReregister(e.ctx, node); !ok || err != nil {
		t.Fatalf("auto re-registration: %v %v", ok, err)
	}
	if a := e.account(node); a.AccountType != "plus" || !a.HasLicense || a.LicenseError != "" {
		t.Errorf("auto: %+v", a)
	}
	if !e.cf.reg(2).deleted || e.cf.reg(3).deleted {
		t.Error("the old device is not the one deleted after the automatic re-registration")
	}
	e.noSecrets()
}
