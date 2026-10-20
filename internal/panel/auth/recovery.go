package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/store"
)

const maxRecoveryBody = 4 << 10

var recoveryTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type recoveryRequest struct {
	RecoveryToken string `json:"recoveryToken"`
	Login         string `json:"login"`
}

type recoveryResponse struct {
	AdminDisplayName string `json:"adminDisplayName"`
	Login            string `json:"login"`
	Password         string `json:"password"`
	TOTPURI          string `json:"totpUri"`
	TOTPSecret       string `json:"totpSecret"`
	Created          bool   `json:"created"`
	SessionsEnded    int    `json:"sessionsEnded"`
}

// RecoveryHandler is the public, edge-only endpoint used by the recover screen. The regular Connect auth service stays
// unchanged for the VPS edition.
func (s *Service) RecoveryHandler() http.Handler {
	return http.HandlerFunc(s.recoverLoginHTTP)
}

func (s *Service) recoverLoginHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.RecoveryEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := s.ClientIP(r)
	decision, err := s.lim.Take(r.Context(), authBucket, SourceKey(ip))
	if err != nil || !decision.Allowed {
		writeRecoveryError(w, http.StatusTooManyRequests, "resource_exhausted", "too many attempts, try again later")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRecoveryBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var input recoveryRequest
	if err := dec.Decode(&input); err != nil {
		writeRecoveryError(w, http.StatusBadRequest, "invalid_argument", "invalid recovery request")
		return
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeRecoveryError(w, http.StatusBadRequest, "invalid_argument", "invalid recovery request")
		return
	}
	if !recoveryTokenPattern.MatchString(input.RecoveryToken) {
		writeRecoveryError(w, http.StatusForbidden, "permission_denied", "recovery link is invalid or expired")
		return
	}
	now := s.now()
	tokenHash := hashToken(input.RecoveryToken)
	live, err := s.st.RecoveryTokenLive(r.Context(), tokenHash, now)
	if err != nil {
		s.log.Error("check recovery token", "err", err)
		writeRecoveryError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if !live {
		writeRecoveryError(w, http.StatusForbidden, "permission_denied", "recovery link is invalid or expired")
		return
	}
	resetInput, err := resolveRecoveryResetInput(r.Context(), s.st, input.Login)
	if err != nil {
		var target *ResetTargetError
		if errors.As(err, &target) {
			writeRecoveryError(w, http.StatusBadRequest, "invalid_argument", target.Error())
			return
		}
		if strings.HasPrefix(err.Error(), "invalid recovery login:") {
			writeRecoveryError(w, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		s.log.Error("resolve edge recovery target", "err", err)
		writeRecoveryError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	resetInput.IP = ip
	won, err := s.st.ConsumeRecoveryToken(r.Context(), hashToken(input.RecoveryToken), now)
	if err != nil {
		s.log.Error("consume recovery token", "err", err)
		writeRecoveryError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if !won {
		writeRecoveryError(w, http.StatusForbidden, "permission_denied", "recovery link is invalid or expired")
		return
	}
	result, err := ResetLogin(r.Context(), s.st, s.vault, resetInput, now)
	if err != nil {
		var target *ResetTargetError
		if errors.As(err, &target) {
			writeRecoveryError(w, http.StatusBadRequest, "invalid_argument", target.Error())
			return
		}
		s.log.Error("reset admin login through edge recovery", "err", err)
		writeRecoveryError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	event := Event{Kind: EventPasswordChanged, AdminID: result.Admin.ID, Login: result.Login, Method: "password"}
	if ip.IsValid() {
		event.IP = ip.String()
	}
	s.emit(event)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(recoveryResponse{
		AdminDisplayName: result.Admin.DisplayName,
		Login:            result.Login,
		Password:         result.Password,
		TOTPURI:          result.TOTPURI,
		TOTPSecret:       result.TOTPSecret,
		Created:          result.Created,
		SessionsEnded:    result.SessionsEnded,
	}); err != nil {
		s.log.Error("encode edge recovery response", "err", err)
	}
}

// resolveRecoveryResetInput checks that a live-token request identifies an existing password login or an unambiguous
// passkey-only admin before the token is consumed. An empty login uses the sole admin, or the unique owner when the
// database has several admins; passkey-only targets receive the default "admin" login unless the owner supplied one.
func resolveRecoveryResetInput(ctx context.Context, st *store.Store, requested string) (ResetLoginInput, error) {
	reset := ResetLoginInput{Actor: "edge_recovery"}
	login := ""
	if strings.TrimSpace(requested) != "" {
		var err error
		login, err = normLogin(requested)
		if err != nil {
			return reset, fmt.Errorf("invalid recovery login: %w", err)
		}
	}
	admins, err := st.AdminLogins(ctx)
	if err != nil {
		return reset, err
	}
	if len(admins) == 0 {
		return reset, &ResetTargetError{"no admin yet: run `mistgate setup` and open its link"}
	}

	if login == "" {
		target, ok := soleRecoveryAdmin(admins)
		if !ok {
			return reset, &ResetTargetError{"recovery could not choose an admin; enter the admin login"}
		}
		login = target.Login
		if login == "" {
			login = "admin"
			if err := recoveryLoginAvailable(admins, login, target.ID); err != nil {
				return reset, err
			}
		}
		return ResetLoginInput{Login: login, AdminID: target.ID, Actor: "edge_recovery"}, nil
	}

	for _, admin := range admins {
		if admin.Login == login {
			return ResetLoginInput{Login: login, Actor: "edge_recovery"}, nil
		}
	}
	if len(admins) == 1 && admins[0].Login == "" {
		return ResetLoginInput{Login: login, AdminID: admins[0].ID, Actor: "edge_recovery"}, nil
	}
	if target, ok := soleRecoveryOwner(admins); ok && target.Login == "" {
		return ResetLoginInput{Login: login, AdminID: target.ID, Actor: "edge_recovery"}, nil
	}
	if len(admins) == 1 {
		return reset, &ResetTargetError{fmt.Sprintf("%s signs in as %q: run reset-login %s", admins[0].DisplayName, admins[0].Login, admins[0].Login)}
	}
	return reset, &ResetTargetError{fmt.Sprintf("no admin signs in as %q; to give one a password login, add --admin <id>", login)}
}

func soleRecoveryAdmin(admins []store.AdminLogin) (store.AdminLogin, bool) {
	if len(admins) == 1 {
		return admins[0], true
	}
	return soleRecoveryOwner(admins)
}

func soleRecoveryOwner(admins []store.AdminLogin) (store.AdminLogin, bool) {
	var owner store.AdminLogin
	count := 0
	for _, admin := range admins {
		if admin.Role == store.RoleOwner {
			owner = admin
			count++
		}
	}
	return owner, count == 1
}

func recoveryLoginAvailable(admins []store.AdminLogin, login, adminID string) error {
	for _, admin := range admins {
		if admin.ID != adminID && admin.Login == login {
			return &ResetTargetError{fmt.Sprintf("the login %q belongs to %s (%s), not to %s", login, admin.DisplayName, admin.ID, adminID)}
		}
	}
	return nil
}

func writeRecoveryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}
