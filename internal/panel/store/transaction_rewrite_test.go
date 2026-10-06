//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAddPasswordRaceKeepsOneCredential(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	mkAdmin(t, s, "adm_password_race", "Owner")
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, login := range []string{"first", "second"} {
		go func(i int, login string) {
			<-start
			results <- s.AddPassword(ctx, PasswordCred{AdminID: "adm_password_race", Login: login, Hash: fmt.Sprintf("hash-%d", i), TOTPSecret: []byte("sealed")}, t0)
		}(i, login)
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessExists):
			conflicts++
		default:
			t.Fatalf("AddPassword race: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || countT(t, s, `SELECT count(*) FROM admin_password WHERE admin_id = 'adm_password_race'`) != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestResetPasswordLoginCreateFailureKeepsSideEffectsOut(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for _, id := range []string{"adm_login_owner", "adm_login_helper"} {
		if _, err := s.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, 'owner', ?, ?)`, id, id, []byte(id), unix(t0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddPassword(ctx, PasswordCred{AdminID: "adm_login_owner", Login: "taken", Hash: "hash", TOTPSecret: []byte("sealed")}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO session (token_hash, admin_id, created_at, last_seen_at, expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, '', '')`, []byte("helper-session"), "adm_login_helper", unix(t0), unix(t0), unix(t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordLoginFailure(ctx, "taken", t0, 5, time.Hour, time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err := s.ResetPasswordLogin(ctx, PasswordCred{AdminID: "adm_login_helper", Login: "taken", Hash: "new-hash", TOTPSecret: []byte("new-secret")}, true,
		[]string{"taken"}, t0.Add(time.Minute), AuditEntry{Actor: "cli", Action: "reset_login", Result: "ok"})
	if !errors.Is(err, ErrLoginTaken) {
		t.Fatalf("ResetPasswordLogin duplicate login: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM session WHERE admin_id = 'adm_login_helper'`) != 1 ||
		countT(t, s, `SELECT count(*) FROM admin_password WHERE admin_id = 'adm_login_helper'`) != 0 ||
		countT(t, s, `SELECT count(*) FROM audit WHERE action = 'reset_login'`) != 0 ||
		countT(t, s, `SELECT count(*) FROM login_failure WHERE login = 'taken'`) != 1 {
		t.Fatal("failed reset changed sessions, credentials, lockouts, or audit")
	}
}

func TestCreateFirstAdminRaceConsumesSetupOnce(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	token := []byte("setup-race")
	if err := s.PutSetupToken(ctx, token, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			a := Admin{ID: fmt.Sprintf("adm_first_%d", i), DisplayName: "Owner", Role: RoleOwner, UserHandle: []byte(fmt.Sprintf("handle-%d", i))}
			if i == 0 {
				p := Passkey{ID: fmt.Sprintf("pk_first_%d", i), AdminID: a.ID, CredentialID: []byte(fmt.Sprintf("credential-%d", i)), PublicKey: []byte("key")}
				results <- s.CreateFirstAdmin(ctx, token, t0, a, p)
				return
			}
			results <- s.CreateFirstAdminPassword(ctx, token, t0, a, PasswordCred{Login: "first", Hash: "hash", TOTPSecret: []byte("sealed")})
		}(i)
	}
	close(start)
	var successes, closed int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrSetupClosed):
			closed++
		default:
			t.Fatalf("CreateFirstAdmin race: %v", err)
		}
	}
	if successes != 1 || closed != 1 || countT(t, s, `SELECT count(*) FROM admin`) != 1 ||
		countT(t, s, `SELECT count(*) FROM passkey`)+countT(t, s, `SELECT count(*) FROM admin_password`) != 1 {
		t.Fatalf("successes=%d closed=%d admins=%d", successes, closed, countT(t, s, `SELECT count(*) FROM admin`))
	}
}

func TestRecordLoginFailureRaceCountsEveryFailure(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	const callers = 24
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			_, err := s.RecordLoginFailure(ctx, "race-login", t0, callers+1, time.Hour, time.Minute)
			results <- err
		}()
	}
	close(start)
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("RecordLoginFailure race: %v", err)
		}
	}
	got, err := s.LoginFailures(ctx, "race-login", t0, time.Hour)
	if err != nil || got.Failures != callers {
		t.Fatalf("LoginFailures() = %+v, %v; want %d", got, err, callers)
	}
}

func TestDeletePasskeyRaceKeepsLastMethod(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := firstAdmin(t, s, t0)
	second := Passkey{ID: "pk_second", AdminID: a.ID, CredentialID: []byte("cred-second"), PublicKey: []byte("key")}
	if err := s.AddPasskey(ctx, second, t0); err != nil {
		t.Fatal(err)
	}
	keys, err := s.PasskeysByAdmin(ctx, a.ID)
	if err != nil || len(keys) != 2 {
		t.Fatalf("passkeys before race: %+v %v", keys, err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{keys[0].ID, second.ID} {
		go func(id string) {
			<-start
			results <- s.DeletePasskey(ctx, a.ID, id)
		}(id)
	}
	close(start)
	var removed, retained int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			removed++
		case errors.Is(err, ErrLastMethod):
			retained++
		default:
			t.Fatalf("DeletePasskey race: %v", err)
		}
	}
	keys, err = s.PasskeysByAdmin(ctx, a.ID)
	if err != nil || removed != 1 || retained != 1 || len(keys) != 1 {
		t.Fatalf("removed=%d retained=%d remaining=%d err=%v", removed, retained, len(keys), err)
	}
}

func TestTakeMCPPlanOwnerSecretRaceReturnsOnce(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	mkAdmin(t, s, "adm_secret_race", "Owner")
	token := newTok(t, s, "secret-race", ProfileAdmin)
	p := newPlan(t, s, token.ID, "node_install", true, t0)
	if _, err := s.ApproveMCPPlanWithSecret(ctx, p.ID, "adm_secret_race", []byte("sealed"), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginApply(ctx, p.ID, token.ID, p.Tool, p.ParamsHash, p.ConfirmHash, t0); err != nil {
		t.Fatal(err)
	}
	const callers = 16
	start := make(chan struct{})
	results := make(chan []byte, callers)
	errorsOut := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			secret, err := s.TakeMCPPlanOwnerSecret(ctx, p.ID, token.ID, p.Tool)
			results <- secret
			errorsOut <- err
		}()
	}
	close(start)
	var got int
	for range callers {
		secret, err := <-results, <-errorsOut
		if err == nil && string(secret) == "sealed" {
			got++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatalf("TakeMCPPlanOwnerSecret race: %q %v", secret, err)
		}
	}
	if got != 1 {
		t.Fatalf("secret returned %d times, want once", got)
	}
}

func TestBindTelegramChatRaceKeepsOneOwner(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for _, id := range []string{"adm_chat_one", "adm_chat_two"} {
		if _, err := s.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, 'owner', ?, ?)`, id, id, []byte(id), unix(t0)); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, adminID := range []string{"adm_chat_one", "adm_chat_two"} {
		go func(adminID string) {
			<-start
			results <- s.BindTelegramChat(ctx, adminID, 1234, t0)
		}(adminID)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("BindTelegramChat race: %v", err)
		}
	}
	links, err := s.TelegramLinks(ctx)
	if err != nil || len(links) != 1 || links[0].ChatID != 1234 {
		t.Fatalf("TelegramLinks() = %+v, %v; want one owner", links, err)
	}
}

func TestRevokeAPITokenRacePreservesFirstRevocation(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	mkAdmin(t, s, "adm_revoke_race", "Owner")
	token := newTok(t, s, "revoke-race", ProfileAdmin)
	const callers = 16
	start := make(chan struct{})
	results := make(chan APIToken, callers)
	errorsOut := make(chan error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got, err := s.RevokeAPIToken(ctx, token.ID, fmt.Sprintf("adm_%d", i), t0.Add(time.Duration(i)*time.Second))
			results <- got
			errorsOut <- err
		}(i)
	}
	close(start)
	wg.Wait()
	var first APIToken
	for range callers {
		got, err := <-results, <-errorsOut
		if err != nil || !got.Revoked() {
			t.Fatalf("RevokeAPIToken race: %+v %v", got, err)
		}
		if first.ID == "" {
			first = got
		} else if got.RevokedAt != first.RevokedAt || got.RevokedBy != first.RevokedBy {
			t.Fatalf("revocation changed between callers: first=%+v got=%+v", first, got)
		}
	}
}

func rewriteAccessGroup(t *testing.T, s *Store, id, name string) {
	t.Helper()
	if err := s.Access().CreateGroup(context.Background(), AccessGroup{ID: id, Name: name, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
}

func rewriteAccessUser(t *testing.T, s *Store, id, name, groupID string) {
	t.Helper()
	u := AccessUser{ID: id, Name: name, GroupID: groupID, Status: "active", AppHapp: true, AppAmnezia: true,
		AllNodes: true, QuotaReset: "month", PeriodStart: t0, DeviceLimit: 5, SubTokenHash: []byte(id + "-hash"),
		SubTokenEnc: []byte("sealed"), CreatedAt: t0}
	if err := s.Access().CreateUser(context.Background(), u, AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCreateUserRaceKeepsUniqueName(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	rewriteAccessGroup(t, s, "grp_create_race", "Create race")
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			u := AccessUser{ID: fmt.Sprintf("usr_create_race_%d", i), Name: "same name", GroupID: "grp_create_race",
				Status: "active", AppHapp: true, AppAmnezia: true, AllNodes: true, QuotaReset: "month", PeriodStart: t0,
				DeviceLimit: 5, SubTokenHash: []byte(fmt.Sprintf("hash-%d", i)), SubTokenEnc: []byte("sealed"), CreatedAt: t0}
			results <- s.Access().CreateUser(ctx, u, AccessDevice{}, nil)
		}(i)
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessExists):
			conflicts++
		default:
			t.Fatalf("CreateUser race: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || countT(t, s, `SELECT count(*) FROM user WHERE name = 'same name'`) != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestAddDeviceRaceKeepsOneImplicitDevice(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	rewriteAccessGroup(t, s, "grp_device_race", "Device race")
	rewriteAccessUser(t, s, "usr_device_race", "Device user", "grp_device_race")
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			dev := AccessDevice{ID: fmt.Sprintf("dev_implicit_race_%d", i), UserID: "usr_device_race", Implicit: true, CreatedAt: t0}
			results <- s.Access().AddDevice(ctx, dev, nil)
		}(i)
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessExists):
			conflicts++
		default:
			t.Fatalf("AddDevice race: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || countT(t, s, `SELECT count(*) FROM device WHERE user_id = 'usr_device_race' AND hwid_hash IS NULL AND revoked_at IS NULL`) != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestAddCredsRaceKeepsOneLiveProtocol(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	rewriteAccessGroup(t, s, "grp_creds_race", "Credential race")
	rewriteAccessUser(t, s, "usr_creds_race", "Credential user", "grp_creds_race")
	dev := AccessDevice{ID: "dev_creds_race", UserID: "usr_creds_race", CreatedAt: t0}
	if err := s.Access().AddDevice(ctx, dev, nil); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			cred := AccessCred{ID: fmt.Sprintf("crd_creds_race_%d", i), DeviceID: dev.ID, UserID: dev.UserID,
				Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: t0}
			results <- s.Access().AddCreds(ctx, []AccessCred{cred})
		}(i)
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessExists):
			conflicts++
		default:
			t.Fatalf("AddCreds race: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || countT(t, s, `SELECT count(*) FROM device_credential WHERE device_id = 'dev_creds_race' AND revoked_at IS NULL`) != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestUpdateUserRaceKeepsUniqueName(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := s.Access()
	rewriteAccessGroup(t, s, "grp_update_race", "Update race")
	rewriteAccessUser(t, s, "usr_update_race_a", "First", "grp_update_race")
	rewriteAccessUser(t, s, "usr_update_race_b", "Second", "grp_update_race")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"usr_update_race_a", "usr_update_race_b"} {
		go func(id string) {
			u, err := a.User(ctx, id)
			if err != nil {
				results <- err
				return
			}
			u.Name = "Shared"
			<-start
			results <- a.UpdateUser(ctx, u, false)
		}(id)
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessExists):
			conflicts++
		default:
			t.Fatalf("UpdateUser race: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || countT(t, s, `SELECT count(*) FROM user WHERE name = 'Shared'`) != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestUpdateProfileRaceUsesExpectedVersion(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := s.Access()
	if err := a.CreateProfile(ctx, AccessProfile{ID: "prf_update_race", Protocol: "hysteria2", Name: "Original", SettingsJSON: `{}`, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"First update", "Second update"} {
		go func(name string) {
			<-start
			_, err := a.UpdateProfile(ctx, AccessProfile{ID: "prf_update_race", Name: name, SettingsJSON: `{}`}, 1, false, false, t0)
			results <- err
		}(name)
	}
	close(start)
	var successes, stale int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessVersion):
			stale++
		default:
			t.Fatalf("UpdateProfile race: %v", err)
		}
	}
	got, err := a.Profile(ctx, "prf_update_race")
	if err != nil || got.Version != 2 || successes != 1 || stale != 1 {
		t.Fatalf("profile=%+v successes=%d stale=%d err=%v", got, successes, stale, err)
	}
}

func TestCreateInboundRaceKeepsUniqueProfileNode(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := s.Access()
	if err := a.CreateProfile(ctx, AccessProfile{ID: "prf_inbound_race", Protocol: "hysteria2", Name: "Inbound race", SettingsJSON: `{}`, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_inbound_race', 'Inbound race', '203.0.113.12', 'active', ?)`, unix(t0))
	in := AccessInbound{ID: "inb_inbound_race", ProfileID: "prf_inbound_race", NodeID: "nod_inbound_race", Enabled: true, CreatedAt: t0}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- a.CreateInbound(ctx, in)
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessExists):
			conflicts++
		default:
			t.Fatalf("CreateInbound race: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || countT(t, s, `SELECT count(*) FROM inbound WHERE profile_id = 'prf_inbound_race' AND node_id = 'nod_inbound_race'`) != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestCreateGroupRaceKeepsUniqueName(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			g := AccessGroup{ID: fmt.Sprintf("grp_create_race_%d", i), Name: "Same group", CreatedAt: t0}
			results <- s.Access().CreateGroup(ctx, g)
		}(i)
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessExists):
			conflicts++
		default:
			t.Fatalf("CreateGroup race: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || countT(t, s, `SELECT count(*) FROM user_group WHERE name = 'Same group'`) != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestUpdateGroupRaceKeepsUniqueName(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := s.Access()
	rewriteAccessGroup(t, s, "grp_update_race_a", "First group")
	rewriteAccessGroup(t, s, "grp_update_race_b", "Second group")
	start := make(chan struct{})
	results := make(chan error, 2)
	shared := "Shared group"
	for _, id := range []string{"grp_update_race_a", "grp_update_race_b"} {
		go func(id string) {
			<-start
			results <- a.UpdateGroup(ctx, id, &shared, nil, nil, nil)
		}(id)
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAccessExists):
			conflicts++
		default:
			t.Fatalf("UpdateGroup race: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || countT(t, s, `SELECT count(*) FROM user_group WHERE name = 'Shared group'`) != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestRevokeDeviceRaceRevokesOnce(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	rewriteAccessGroup(t, s, "grp_revoke_race", "Revoke race")
	rewriteAccessUser(t, s, "usr_revoke_race", "Revoke user", "grp_revoke_race")
	dev := AccessDevice{ID: "dev_revoke_race", UserID: "usr_revoke_race", CreatedAt: t0}
	cred := AccessCred{ID: "crd_revoke_race", DeviceID: dev.ID, UserID: dev.UserID, Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: t0}
	if err := s.Access().AddDevice(ctx, dev, []AccessCred{cred}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		userID string
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			userID, err := s.Access().RevokeDevice(ctx, dev.ID, t0.Add(time.Minute))
			results <- result{userID: userID, err: err}
		}()
	}
	close(start)
	var successes, missing int
	for range 2 {
		got := <-results
		switch {
		case got.err == nil && got.userID == dev.UserID:
			successes++
		case errors.Is(got.err, ErrNotFound):
			missing++
		default:
			t.Fatalf("RevokeDevice race: %+v", got)
		}
	}
	if successes != 1 || missing != 1 || countT(t, s, `SELECT count(*) FROM device WHERE id = 'dev_revoke_race' AND revoked_at IS NOT NULL`) != 1 {
		t.Fatalf("successes=%d missing=%d", successes, missing)
	}
}

func TestDeleteInboundRaceDeletesOnce(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	_, inboundID := fixtureInbound(t, s, "delete_race")
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- s.Access().DeleteInbound(ctx, inboundID, t0, nil)
		}()
	}
	close(start)
	var successes, missing int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrNotFound):
			missing++
		default:
			t.Fatalf("DeleteInbound race: %v", err)
		}
	}
	if successes != 1 || missing != 1 || countT(t, s, `SELECT count(*) FROM inbound WHERE id = ?`, inboundID) != 0 {
		t.Fatalf("successes=%d missing=%d", successes, missing)
	}
}

func TestDeleteProfileAndGroupKeepInUseGuards(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	_, _ = fixtureInbound(t, s, "profile_in_use")
	if err := s.Access().DeleteProfile(ctx, "prf_profile_in_use", t0); !errors.Is(err, ErrAccessInUse) {
		t.Fatalf("DeleteProfile with inbound: %v", err)
	}
	rewriteAccessGroup(t, s, "grp_in_use", "In use")
	rewriteAccessGroup(t, s, "grp_move_to", "Move to")
	rewriteAccessUser(t, s, "usr_in_group", "Group user", "grp_in_use")
	if err := s.Access().DeleteGroup(ctx, "grp_in_use", ""); !errors.Is(err, ErrAccessInUse) {
		t.Fatalf("DeleteGroup with user: %v", err)
	}
	if err := s.Access().DeleteGroup(ctx, "grp_in_use", "grp_move_to"); err != nil {
		t.Fatalf("DeleteGroup moving user: %v", err)
	}
	u, err := s.Access().User(ctx, "usr_in_group")
	if err != nil || u.GroupID != "grp_move_to" {
		t.Fatalf("moved user = %+v, %v", u, err)
	}
}

func TestOpenAlertRaceKeepsOneActiveAlert(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := HealthAlert{Kind: "node_down", Severity: 2, NodeID: "nod_alert_race", Subject: "agent", TitleKey: "title", WhyKey: "why"}
	start := make(chan struct{})
	type result struct {
		alert    HealthAlert
		reopened bool
		err      error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			opened, reopened, err := s.OpenAlert(ctx, a, time.Hour, t0)
			results <- result{alert: opened, reopened: reopened, err: err}
		}()
	}
	close(start)
	var first HealthAlert
	for range 2 {
		got := <-results
		if got.err != nil || got.reopened {
			t.Fatalf("OpenAlert race: %+v reopened=%v err=%v", got.alert, got.reopened, got.err)
		}
		if first.ID == "" {
			first = got.alert
		} else if got.alert.ID != first.ID {
			t.Fatalf("callers opened different alerts: %q and %q", first.ID, got.alert.ID)
		}
	}
	if countT(t, s, `SELECT count(*) FROM health_alert WHERE kind = ? AND node_id = ? AND subject = ? AND resolved_at = 0`, a.Kind, a.NodeID, a.Subject) != 1 {
		t.Fatal("the alert race left other than one active row")
	}
}

func TestPutDoctorRaceKeepsOneCheckRow(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	nodeID, _ := fixtureInbound(t, s, "doctor_race")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, status := range []int{1, 2} {
		go func(status int) {
			<-start
			results <- s.PutDoctor(ctx, nodeID, []DoctorRow{{CheckID: "resolver", Status: status}}, false, t0)
		}(status)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("PutDoctor race: %v", err)
		}
	}
	if countT(t, s, `SELECT count(*) FROM doctor_result WHERE node_id = ? AND check_id = 'resolver'`, nodeID) != 1 {
		t.Fatal("the doctor report race duplicated a check row")
	}
}

func TestRollupDailyRaceIsIdempotent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	_, inboundID := fixtureInbound(t, s, "rollup_race")
	day := t0.Unix() - t0.Unix()%86400 - 86400
	if err := s.InsertSample(ctx, CheckSample{InboundID: inboundID, At: time.Unix(day+10, 0), Status: 1, LatencyMS: 50}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- s.RollupDaily(ctx, t0)
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("RollupDaily race: %v", err)
		}
	}
	if countT(t, s, `SELECT count(*) FROM health_check_daily WHERE inbound_id = ?`, inboundID) != 1 {
		t.Fatal("the daily rollup race duplicated a day")
	}
}

func TestCreateNodeProvisionJobRaceKeepsOneReservation(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, name := range []string{"node-race-a", "node-race-b"} {
		go func(i int, name string) {
			<-start
			job := NodeProvisionJob{ID: fmt.Sprintf("job_race_%d", i), NodeID: "nod_provision_race", Name: name,
				Address: "203.0.113.10", SSHHost: "203.0.113.10", SSHPort: 22, HostFingerprint: "SHA256:example",
				Secret: []byte("sealed"), CreatedBy: "admin", CreatedAt: t0, UpdatedAt: t0}
			results <- s.CreateNodeProvisionJob(ctx, job)
		}(i, name)
	}
	close(start)
	var created, conflict int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			created++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("CreateNodeProvisionJob race: %v", err)
		}
	}
	if created != 1 || conflict != 1 || countT(t, s, `SELECT count(*) FROM node_provision_job WHERE node_id = 'nod_provision_race'`) != 1 {
		t.Fatalf("created=%d conflicts=%d", created, conflict)
	}
}

func TestRequeueNodeProvisionJobsRaceWritesOneEvent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedProvisionJob(t, s, "job_requeue_race", "nod_requeue_race", "node-requeue-race", "running", t0)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- s.RequeueNodeProvisionJobs(ctx, t0.Add(time.Minute))
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("RequeueNodeProvisionJobs race: %v", err)
		}
	}
	state, err := s.NodeProvisionJobState(ctx, "job_requeue_race")
	if err != nil || state != "queued" || countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_requeue_race'`) != 1 {
		t.Fatalf("state=%q events=%d err=%v", state, countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_requeue_race'`), err)
	}
}

func TestRequestCancelNodeProvisionJobRaceChangesOnce(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedProvisionJob(t, s, "job_cancel_race", "nod_cancel_race", "node-cancel-race", "queued", t0)
	start := make(chan struct{})
	type result struct {
		state   string
		changed bool
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			state, changed, err := s.RequestCancelNodeProvisionJob(ctx, "job_cancel_race", t0.Add(time.Minute))
			results <- result{state, changed, err}
		}()
	}
	close(start)
	var changed int
	for range 2 {
		got := <-results
		if got.err != nil || got.state != "cancelled" {
			t.Fatalf("RequestCancelNodeProvisionJob race: %+v", got)
		}
		if got.changed {
			changed++
		}
	}
	if changed != 1 || countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_cancel_race'`) != 1 {
		t.Fatalf("changed=%d events=%d", changed, countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_cancel_race'`))
	}
}

func TestFinishCancelledNodeProvisionJobRaceFinishesOnce(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedProvisionJob(t, s, "job_finish_race", "nod_finish_race", "node-finish-race", "cancel_requested", t0)
	start := make(chan struct{})
	type result struct {
		finished bool
		err      error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			finished, err := s.FinishCancelledNodeProvisionJob(ctx, "job_finish_race", t0.Add(time.Minute))
			results <- result{finished, err}
		}()
	}
	close(start)
	var success, noChange int
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("FinishCancelledNodeProvisionJob race: %v", got.err)
		}
		if got.finished {
			success++
		} else {
			noChange++
		}
	}
	if success != 1 || noChange != 1 || countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_finish_race'`) != 1 {
		t.Fatalf("success=%d noChange=%d events=%d", success, noChange, countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_finish_race'`))
	}
}

func TestClaimNodeProvisionJobRaceHasOneWinner(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedProvisionJob(t, s, "job_claim_race", "nod_claim_race", "node-claim-race", "queued", t0)
	start := make(chan struct{})
	type result struct {
		job NodeProvisionJob
		ok  bool
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			job, ok, err := s.ClaimNodeProvisionJob(ctx, t0.Add(time.Minute))
			results <- result{job, ok, err}
		}()
	}
	close(start)
	var claimed int
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("ClaimNodeProvisionJob race: %v", got.err)
		}
		if got.ok {
			claimed++
			if got.job.ID != "job_claim_race" || got.job.State != "running" {
				t.Fatalf("claimed job: %+v", got.job)
			}
		}
	}
	if claimed != 1 || countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_claim_race'`) != 1 {
		t.Fatalf("claimed=%d events=%d", claimed, countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_claim_race'`))
	}
}

func TestUpdateNodeProvisionJobFromStateRaceUsesCAS(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedProvisionJob(t, s, "job_state_race", "nod_state_race", "node-state-race", "running", t0)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, code := range []string{"failed_a", "failed_b"} {
		go func(code string) {
			<-start
			results <- s.updateNodeProvisionJobFromState(ctx, "job_state_race", "running", "failed", "failed", code, []byte{}, code, t0.Add(time.Minute))
		}(code)
	}
	close(start)
	var changed, conflict int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			changed++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("updateNodeProvisionJobFromState race: %v", err)
		}
	}
	if changed != 1 || conflict != 1 || countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_state_race'`) != 1 {
		t.Fatalf("changed=%d conflict=%d", changed, conflict)
	}
}

func TestCompleteNodeProvisionJobRaceStoresOneAccess(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	nodeID, _ := fixtureInbound(t, s, "complete_race")
	seedProvisionJob(t, s, "job_complete_race", nodeID, "node-complete-race", "running", t0)
	access := NodeServerAccess{NodeID: nodeID, NodeName: "node-complete-race", SSHHost: "203.0.113.10", SSHPort: 22,
		SSHUser: "user1", HostFingerprint: "SHA256:example", Password: []byte("sealed")}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- s.CompleteNodeProvisionJob(ctx, "job_complete_race", access, t0.Add(time.Minute))
		}()
	}
	close(start)
	var complete, missing int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			complete++
		case errors.Is(err, ErrNotFound):
			missing++
		default:
			t.Fatalf("CompleteNodeProvisionJob race: %v", err)
		}
	}
	if complete != 1 || missing != 1 || countT(t, s, `SELECT count(*) FROM node_server_access WHERE node_id = ?`, nodeID) != 1 ||
		countT(t, s, `SELECT count(*) FROM node_provision_event WHERE job_id = 'job_complete_race'`) != 1 {
		t.Fatalf("complete=%d missing=%d", complete, missing)
	}
}

func TestAddNodeToRunningRolloutRaceKeepsContiguousStages(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	rollout := RolloutRow{ID: "rol_add_race", Status: RolloutRunning, ToVersion: "v2", ToBuilt: 2, Manifest: []byte("m"), Signature: []byte("s"), BatchSize: 1, CreatedAt: t0}
	if err := s.CreateRollout(ctx, rollout, []StepRow{{NodeID: "nod_before", NodeName: "before", Stage: 0, State: StepPending}}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"nod_add_a", "nod_add_b"} {
		go func(id string) {
			<-start
			results <- s.AddNodeToRunningRollout(ctx, rollout.ID, rollout.ToVersion, rollout.ToBuilt, 0, StepRow{NodeID: id, NodeName: id})
		}(id)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("AddNodeToRunningRollout race: %v", err)
		}
	}
	steps, err := s.RolloutSteps(ctx, rollout.ID)
	if err != nil || len(steps) != 3 {
		t.Fatalf("steps: %+v %v", steps, err)
	}
	for i, step := range steps {
		if step.Stage != i {
			t.Fatalf("non-contiguous stages: %+v", steps)
		}
	}
}

func TestCreateRolloutRaceKeepsOneActiveRollout(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"rol_race_a", "rol_race_b"} {
		go func(id string) {
			<-start
			r := RolloutRow{ID: id, Status: RolloutRunning, ToVersion: "v2", ToBuilt: 2, Manifest: []byte("m"), Signature: []byte("s"), BatchSize: 1, CreatedAt: t0}
			results <- s.createRollout(ctx, r, nil, nil, nil)
		}(id)
	}
	close(start)
	var created, conflict int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			created++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("createRollout race: %v", err)
		}
	}
	if created != 1 || conflict != 1 || countT(t, s, `SELECT count(*) FROM update_rollout WHERE status IN ('running', 'paused')`) != 1 {
		t.Fatalf("created=%d conflict=%d", created, conflict)
	}
}

func TestReplaceWarpAccountRaceKeepsAnAccount(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	nodeID, _ := fixtureInbound(t, s, "warp_race")
	base := WarpAccountRow{NodeID: nodeID, Source: WarpImported, SecretEnc: []byte("old"), PeerPublicKey: "old", CreatedAt: t0, UpdatedAt: t0}
	if err := s.CreateWarpAccount(ctx, base); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, key := range []string{"new_a", "new_b"} {
		go func(key string) {
			<-start
			next := base
			next.SecretEnc, next.PeerPublicKey, next.UpdatedAt = []byte(key), key, t0.Add(time.Minute)
			results <- s.ReplaceWarpAccount(ctx, next)
		}(key)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("ReplaceWarpAccount race: %v", err)
		}
	}
	got, err := s.WarpAccount(ctx, nodeID)
	if err != nil || (got.PeerPublicKey != "new_a" && got.PeerPublicKey != "new_b") || countT(t, s, `SELECT count(*) FROM warp_account WHERE node_id = ?`, nodeID) != 1 {
		t.Fatalf("account after replace race: %+v %v", got, err)
	}
}

func TestAwgPrepareTxCASHandlesConcurrentFinish(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	nodeID, _ := fixtureInbound(t, s, "awg_prepare_race")
	if err := s.SetAwgPrepare(ctx, nodeID, AwgPrepareRow{Want: true}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		switched bool
		err      error
	}
	results := make(chan result, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			switched, err := s.AwgPrepareFinish(ctx, nodeID, true, t0.Unix()+int64(i+1), "", "")
			results <- result{switched, err}
		}(i)
	}
	close(start)
	var switched int
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("AwgPrepareFinish race: %v", got.err)
		}
		if got.switched {
			switched++
		}
	}
	var backend string
	if err := s.R.QueryRowContext(ctx, `SELECT awg_backend FROM node WHERE id = ?`, nodeID).Scan(&backend); err != nil {
		t.Fatal(err)
	}
	prepared, err := s.Node(ctx, nodeID)
	if err != nil || switched != 1 || backend != "kernel" || prepared.AwgPrepare().State != AwgPrepareDone {
		t.Fatalf("switches=%d backend=%q state=%+v err=%v", switched, backend, prepared.AwgPrepare(), err)
	}
}

func seedProvisionJob(t *testing.T, s *Store, id, nodeID, name, state string, created time.Time) {
	t.Helper()
	_, err := s.W.ExecContext(context.Background(), `INSERT INTO node_provision_job (
		id, node_id, name, address, ssh_host, ssh_port, host_fingerprint, secret, state, phase, created_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, nodeID, name, "203.0.113.10", "203.0.113.10", 22,
		"SHA256:example", []byte("sealed"), state, state, "admin", unix(created), unix(created))
	if err != nil {
		t.Fatalf("seed provision job %s: %v", id, err)
	}
}
