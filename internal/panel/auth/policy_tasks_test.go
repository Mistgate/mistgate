//go:build !js

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/gen/mistgate/tasks/v1/tasksv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The TasksService: the strings of policy_tasks.go are the generated names; reads and "run now" are the owner's and the
// helpers' and open to operator tokens; creating, changing and deleting a task are the owner's and reach a token only through
// the owner's approved plan, each tool granted exactly its own procedure.
func TestTasksProcedurePolicy(t *testing.T) {
	reads := map[string]string{
		TasksListTasksProcedure:      tasksv1connect.TasksServiceListTasksProcedure,
		TasksGetTaskProcedure:        tasksv1connect.TasksServiceGetTaskProcedure,
		TasksRunTaskNowProcedure:     tasksv1connect.TasksServiceRunTaskNowProcedure,
		TasksListTaskRunsProcedure:   tasksv1connect.TasksServiceListTaskRunsProcedure,
		TasksListCheckToolsProcedure: tasksv1connect.TasksServiceListCheckToolsProcedure,
	}
	writes := map[string]string{
		TasksCreateTaskProcedure: tasksv1connect.TasksServiceCreateTaskProcedure,
		TasksUpdateTaskProcedure: tasksv1connect.TasksServiceUpdateTaskProcedure,
		TasksDeleteTaskProcedure: tasksv1connect.TasksServiceDeleteTaskProcedure,
	}
	for got, want := range reads {
		if got != want {
			t.Errorf("policy constant %q differs from the generated procedure %q", got, want)
		}
		if role := ProcedureRole(want); role != store.RoleHelper {
			t.Errorf("%s: ProcedureRole = %q, want helper", want, role)
		}
		if access, ok := TokenAccess(want); !ok || access != TokenAccessDirect {
			t.Errorf("%s: TokenAccess = %d, %v; want direct", want, access, ok)
		}
	}
	for got, want := range writes {
		if got != want {
			t.Errorf("policy constant %q differs from the generated procedure %q", got, want)
		}
		if role := ProcedureRole(want); role != store.RoleOwner {
			t.Errorf("%s: ProcedureRole = %q, want owner", want, role)
		}
		if access, ok := TokenAccess(want); !ok || access != TokenAccessApproved {
			t.Errorf("%s: TokenAccess = %d, %v; want approved", want, access, ok)
		}
		if NeedsStepUp(want) {
			t.Errorf("%s needs a step-up", want)
		}
	}
	for tool, want := range map[string]string{"task_create": TasksCreateTaskProcedure, "task_update": TasksUpdateTaskProcedure, "task_delete": TasksDeleteTaskProcedure} {
		if got := grantProcedures[tool]; !slices.Equal(got, []string{want}) {
			t.Errorf("%s grants %v, want only %s", tool, got, want)
		}
	}
}

// The scheduled tasks' hidden identity: the operator profile whatever the owner's role, without the assistant being on (a check
// task runs no model); it cannot stand in for the assistant's identity nor the other way round, and a read-only admin's tasks
// get nothing.
func TestScheduledTaskIdentity(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	adminID := "adm_tasks_owner"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin(id,display_name,role,user_handle,created_at) VALUES(?,?,?,?,?)`, adminID, "Owner", store.RoleOwner, []byte("tasks-handle"), clock.Unix()); err != nil {
		t.Fatal(err)
	}
	// the assistant is off
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_settings(id,enabled,audience) VALUES(1,0,1)`); err != nil {
		t.Fatal(err)
	}
	sched, err := st.EnsureScheduledToken(ctx, adminID, *clock)
	if err != nil || sched.ID != store.ScheduledTokenID(adminID) || sched.Profile != store.ProfileOperator || !sched.AssistantOnly {
		t.Fatalf("scheduled identity: %+v %v", sched, err)
	}
	asst, err := st.EnsureAssistantToken(ctx, adminID, store.ProfileAdmin, *clock)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, PrincipalFrom(r.Context()).Name)
		w.WriteHeader(http.StatusNoContent)
	})
	call := func(path string, ctxOf func(context.Context) context.Context) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://panel.invalid"+path, nil)
		req = req.WithContext(ctxOf(req.Context()))
		rec := httptest.NewRecorder()
		s.RequireSession(next).ServeHTTP(rec, req)
		return rec.Code
	}
	scheduled := func(tokenID string) func(context.Context) context.Context {
		return func(c context.Context) context.Context { return WithScheduledTask(c, tokenID, adminID, "Owner") }
	}
	if code := call(adminv1connect.HealthServiceRunChecksNowProcedure, scheduled(sched.ID)); code != http.StatusNoContent {
		t.Fatalf("an operator procedure was refused: %d", code)
	}
	if len(seen) != 1 || seen[0] != "Scheduled tasks of Owner" {
		t.Fatalf("principal: %v", seen)
	}
	// an owner procedure: the operator profile does not reach it
	if code := call(adminv1connect.AuthServiceListAuditProcedure, scheduled(sched.ID)); code != http.StatusForbidden {
		t.Fatalf("an owner procedure answered %d", code)
	}
	// the assistant's row as a scheduled identity, and the scheduled row as the assistant's
	if code := call(adminv1connect.UserServiceListUsersProcedure, scheduled(asst.ID)); code != http.StatusUnauthorized {
		t.Fatalf("the assistant identity ran as a scheduled task: %d", code)
	}
	if code := call(adminv1connect.UserServiceListUsersProcedure, func(c context.Context) context.Context {
		return WithAssistant(c, sched.ID, store.ProfileOperator, adminID, "Owner")
	}); code != http.StatusUnauthorized {
		t.Fatalf("the scheduled identity ran as the assistant: %d", code)
	}
	// a read-only admin's tasks act with nothing
	if _, err = st.W.ExecContext(ctx, `UPDATE admin SET role=? WHERE id=?`, store.RoleReadonly, adminID); err != nil {
		t.Fatal(err)
	}
	if code := call(adminv1connect.UserServiceListUsersProcedure, scheduled(sched.ID)); code != http.StatusUnauthorized {
		t.Fatalf("a read-only admin's task was let in: %d", code)
	}
	if len(seen) != 1 {
		t.Fatalf("refused requests reached the API: %v", seen)
	}
}
