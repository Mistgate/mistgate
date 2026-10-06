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
