//go:build !js

package mcp

import (
	"strings"
	"testing"
)

// The apps of a user are three toggles (Happ link, AmneziaVPN keys, Telegram proxy). A change names only the ones it wants to
// change: the panel replaces all three on an UpdateUser, so the plan must carry the current values of the others, or an agent
// that changes one app would silently switch the Telegram proxy (and its key) off.

func TestUserUpdateAppsKeepWhatIsNotNamed(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)

	// switching Telegram on touches Telegram alone
	p := planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "apps": map[string]any{"telegram": true}})
	if f := factOf(t, p, "apps"); f.Code != "change" || f.Params["from"] != "both" || f.Params["to"] != "both+telegram" || !strings.Contains(f.Value, "Telegram proxy") {
		t.Errorf("apps fact: %+v", f)
	}
	applyOf(t, s, "user_update", p.ConfirmToken)
	if got := e.w.updateReq[len(e.w.updateReq)-1].GetApps(); !got.GetHapp() || !got.GetAmnezia() || !got.GetTelegram() {
		t.Errorf("request apps: %+v", got)
	}

	// a user who has Telegram on keeps it when another app changes, or when the apps are not named at all
	e.w.users["usr_alice"].User.Apps.Telegram = true
	p = planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "apps": map[string]any{"amnezia": false}})
	if f := factOf(t, p, "apps"); f.Params["from"] != "both+telegram" || f.Params["to"] != "happ+telegram" {
		t.Errorf("apps fact: %+v", f)
	}
	applyOf(t, s, "user_update", p.ConfirmToken)
	if got := e.w.updateReq[len(e.w.updateReq)-1].GetApps(); !got.GetHapp() || got.GetAmnezia() || !got.GetTelegram() {
		t.Errorf("request apps after switching AmneziaVPN off: %+v", got)
	}
	p = planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "quota_bytes": 1 << 30})
	applyOf(t, s, "user_update", p.ConfirmToken)
	if got := e.w.updateReq[len(e.w.updateReq)-1]; got.Apps != nil {
		t.Errorf("a change that names no app sends apps: %+v", got.Apps)
	}

	// and it can be switched off again, alone
	p = planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "apps": map[string]any{"telegram": false}})
	if f := factOf(t, p, "apps"); f.Params["from"] != "both+telegram" || f.Params["to"] != "both" {
		t.Errorf("apps fact: %+v", f)
	}
	applyOf(t, s, "user_update", p.ConfirmToken)
	if got := e.w.updateReq[len(e.w.updateReq)-1].GetApps(); !got.GetHapp() || !got.GetAmnezia() || got.GetTelegram() {
		t.Errorf("request apps after Telegram off: %+v", got)
	}

	// Telegram comes in addition: the result still needs Happ or AmneziaVPN
	e.w.users["usr_alice"].User.Apps.Amnezia = false
	out := mustFail(t, s, "user_update_plan", map[string]any{"user_id": "usr_alice", "apps": map[string]any{"happ": false}})
	if !strings.Contains(out, "at least one of happ and amnezia") {
		t.Errorf("refusal: %s", out)
	}
}

func TestUserCreateAppsDefaults(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	for i, c := range []struct {
		apps map[string]any
		want [3]bool
	}{
		{nil, [3]bool{}}, // not named: the panel's default is sent as nothing
		{map[string]any{"telegram": true}, [3]bool{true, true, true}},
		{map[string]any{"happ": true, "amnezia": false, "telegram": true}, [3]bool{true, false, true}},
		{map[string]any{"amnezia": false}, [3]bool{true, false, false}},
	} {
		args := map[string]any{"name": "zoe" + string(rune('a'+i)), "group_id": "grp_1"}
		if c.apps != nil {
			args["apps"] = c.apps
		}
		p := planOf(t, s, "user_create", args)
		applyOf(t, s, "user_create", p.ConfirmToken)
		got := e.w.createReq[len(e.w.createReq)-1].Apps
		if c.apps == nil {
			if got != nil {
				t.Errorf("no apps named, but %+v was sent", got)
			}
			continue
		}
		if [3]bool{got.GetHapp(), got.GetAmnezia(), got.GetTelegram()} != c.want {
			t.Errorf("%v: sent %+v, want %v", c.apps, got, c.want)
		}
	}
}

func TestUsersSearchShowsTelegram(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	e.w.users["usr_alice"].User.Apps.Telegram = true
	out := mustOK(t, s, "users_search", map[string]any{"query": "alice"})
	v := decode[UsersV](t, out)
	if len(v.Users) != 1 || !v.Users[0].Telegram {
		t.Errorf("users: %+v", v.Users)
	}
	if !strings.Contains(out, `"app_telegram":true`) {
		t.Errorf("the view does not carry app_telegram: %s", out)
	}
}

func TestProtocolsListMarksExperimental(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	v := decode[protocolsListV](t, mustOK(t, s, "protocols_list", map[string]any{}))
	got := map[string]bool{}
	apps := map[string][]string{}
	for _, p := range v.Protocols {
		got[p.ID], apps[p.ID] = p.Experimental, p.Apps
	}
	if !got["mtproto"] || got["hysteria2"] || len(got) != 2 || len(apps["mtproto"]) != 1 || apps["mtproto"][0] != "telegram" {
		t.Errorf("experimental %v, apps %v", got, apps)
	}
}
