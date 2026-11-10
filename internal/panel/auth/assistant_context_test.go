package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adminv1connect "github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestAssistantMCPRequestFollowsCurrentRoleAndAudience(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	adminID := "adm_assistant_role"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin(id,display_name,role,user_handle,created_at) VALUES(?,?,?,?,?)`, adminID, "Owner", store.RoleOwner, []byte("role-handle"), clock.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_settings(id,enabled,audience) VALUES(1,1,3)`); err != nil {
		t.Fatal(err)
	}
	token, err := st.EnsureAssistantToken(ctx, adminID, store.ProfileAdmin, *clock)
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	})
	call := func(profile string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://panel.invalid"+adminv1connect.UserServiceListUsersProcedure, nil)
		req = req.WithContext(WithAssistant(req.Context(), token.ID, profile, adminID, "Owner"))
		response := httptest.NewRecorder()
		s.RequireSession(next).ServeHTTP(response, req)
		return response.Code
	}
	if code := call(store.ProfileAdmin); code != http.StatusNoContent {
		t.Fatalf("current owner assistant request was rejected: %d", code)
	}
	if _, err = st.W.ExecContext(ctx, `UPDATE admin SET role=? WHERE id=?`, store.RoleHelper, adminID); err != nil {
		t.Fatal(err)
	}
	if code := call(store.ProfileAdmin); code != http.StatusUnauthorized {
		t.Fatalf("old owner identity survived demotion: %d", code)
	}
	updated, err := st.GetAssistantToken(ctx, token.ID)
	if err != nil || !updated.Revoked() {
		t.Fatalf("stale owner identity was not revoked after demotion: %+v %v", updated, err)
	}
	token, err = st.EnsureAssistantToken(ctx, adminID, store.ProfileOperator, *clock)
	if err != nil || token.Revoked() {
		t.Fatalf("new assistant turn did not refresh its hidden identity at the helper profile: %+v %v", token, err)
	}
	if code := call(store.ProfileOperator); code != http.StatusNoContent {
		t.Fatalf("current helper identity was rejected for everyone audience: %d", code)
	}
	if _, err = st.W.ExecContext(ctx, `UPDATE assistant_settings SET audience=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if code := call(store.ProfileOperator); code != http.StatusUnauthorized {
		t.Fatalf("assistant request outside its current audience was accepted: %d", code)
	}
	updated, err = st.GetAssistantToken(ctx, token.ID)
	if err != nil || !updated.Revoked() {
		t.Fatalf("hidden token was not revoked after leaving the audience: %+v %v", updated, err)
	}
	if hits != 2 {
		t.Fatalf("rejected assistant requests reached the panel API: handler hits=%d", hits)
	}
}

func TestAssistantMCPRequestRevokesIdentityAfterAdminRemoval(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	adminID := "adm_assistant_removed"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin(id,display_name,role,user_handle,created_at) VALUES(?,?,?,?,?)`, adminID, "Helper", store.RoleHelper, []byte("removed-handle"), clock.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_settings(id,enabled,audience) VALUES(1,1,1)`); err != nil {
		t.Fatal(err)
	}
	token, err := st.EnsureAssistantToken(ctx, adminID, store.ProfileOperator, *clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.W.ExecContext(ctx, `DELETE FROM admin WHERE id=?`, adminID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://panel.invalid"+adminv1connect.UserServiceListUsersProcedure, nil)
	request = request.WithContext(WithAssistant(request.Context(), token.ID, store.ProfileOperator, adminID, "Helper"))
	response := httptest.NewRecorder()
	s.RequireSession(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("removed admin reached the panel API") })).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("removed admin assistant request was accepted: %d", response.Code)
	}
	updated, err := st.GetAssistantToken(ctx, token.ID)
	if err != nil || !updated.Revoked() || !updated.RevokedAt.After(time.Time{}) {
		t.Fatalf("hidden token was not revoked on admin removal: %+v %v", updated, err)
	}
}

func TestAssistantFollowUpKeepsReadProfileAndRefreshesAfterRoleChange(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	adminID := "adm_followup_cap"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin(id,display_name,role,user_handle,created_at) VALUES(?,?,?,?,?)`, adminID, "Owner", store.RoleOwner, []byte("followup-handle"), clock.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_settings(id,enabled,audience) VALUES(1,1,3)`); err != nil {
		t.Fatal(err)
	}
	token, err := st.EnsureAssistantFollowUpToken(ctx, adminID, store.ProfileAdmin, *clock)
	if err != nil || token.Profile != store.ProfileAdmin {
		t.Fatalf("follow-up token = %+v, %v", token, err)
	}
	hits := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	})
	call := func(path string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://panel.invalid"+path, nil)
		req = req.WithContext(WithAssistantFollowUp(req.Context(), token.ID, token.Profile, adminID, "Owner"))
		response := httptest.NewRecorder()
		s.RequireSession(next).ServeHTTP(response, req)
		return response.Code
	}
	if code := call(OpsListDebugCapturesProcedure); code != http.StatusNoContent {
		t.Fatalf("owner follow-up could not read debug captures: %d", code)
	}
	if code := call(adminv1connect.UserServiceListUsersProcedure); code != http.StatusNoContent {
		t.Fatalf("owner follow-up lost an ordinary read: %d", code)
	}
	if hits != 2 {
		t.Fatalf("owner reads reached the panel API %d times", hits)
	}
	if _, err = st.W.ExecContext(ctx, `UPDATE admin SET role=? WHERE id=?`, store.RoleHelper, adminID); err != nil {
		t.Fatal(err)
	}
	if code := call(OpsListDebugCapturesProcedure); code != http.StatusUnauthorized {
		t.Fatalf("old owner follow-up identity survived demotion: %d", code)
	}
	token, err = st.EnsureAssistantFollowUpToken(ctx, adminID, store.ProfileOperator, *clock)
	if err != nil || token.Profile != store.ProfileOperator {
		t.Fatalf("helper follow-up token = %+v, %v", token, err)
	}
	if code := call(OpsListDebugCapturesProcedure); code != http.StatusForbidden {
		t.Fatalf("helper follow-up reached owner debug reads: %d", code)
	}
	if code := call(adminv1connect.UserServiceListUsersProcedure); code != http.StatusNoContent {
		t.Fatalf("helper follow-up lost its own read access: %d", code)
	}
	if hits != 3 {
		t.Fatalf("helper read reached the panel API %d times", hits)
	}
}
