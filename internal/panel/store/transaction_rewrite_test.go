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
