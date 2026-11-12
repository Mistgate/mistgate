//go:build !js

package clientapi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestSwitchIsOwnerOnlySteppedUpAndAudited(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "client-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	steps, invalidated := 0, 0
	now := time.Unix(1_800_000_000, 0).UTC()
	s := New(Config{
		Store: st, Now: func() time.Time { return now },
		StepUp:     func(context.Context) error { steps++; return nil },
		Invalidate: func() { invalidated++ },
	})
	r := rpc{s}
	ownerCtx := auth.WithAdmin(ctx, store.Admin{ID: "adm_owner", Role: store.RoleOwner})
	get := connect.NewRequest(&adminv1.GetClientApiRequest{})
	update := func(c context.Context, enabled bool) error {
		_, err := r.UpdateClientApi(c, connect.NewRequest(&adminv1.UpdateClientApiRequest{Enabled: &enabled}))
		return err
	}

	for _, role := range []string{store.RoleOwner, store.RoleHelper, store.RoleReadonly} {
		readCtx := auth.WithAdmin(ctx, store.Admin{ID: "adm_" + role, Role: role})
		got, err := r.GetClientApi(readCtx, get)
		if err != nil || got.Msg.GetSettings().GetEnabled() {
			t.Fatalf("GetClientApi for %s = %+v, %v", role, got, err)
		}
	}
	on := true
	for name, denied := range map[string]context.Context{
		"helper": auth.WithAdmin(ctx, store.Admin{ID: "adm_helper", Role: store.RoleHelper}),
		"token":  auth.WithAdmin(ctx, store.Admin{ID: "token:abc", Role: store.RoleOwner}),
		"mcp":    auth.WithAdmin(ctx, store.Admin{ID: "mcp:abc", Role: store.RoleOwner}),
	} {
		if err := update(denied, on); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("update by %s = %v, want permission denied", name, err)
		}
	}
	if steps != 0 {
		t.Fatalf("refused changes requested step-up %d times", steps)
	}
	if err := update(ownerCtx, on); err != nil {
		t.Fatal(err)
	}
	if steps != 1 || invalidated != 1 {
		t.Fatalf("step-ups %d, invalidations %d; want 1 each", steps, invalidated)
	}
	if got, err := st.Setting(ctx, store.SettingClientAPI); err != nil || got != "1" {
		t.Fatalf("setting = %q, %v", got, err)
	}
	got, err := r.GetClientApi(ownerCtx, get)
	if err != nil || !got.Msg.GetSettings().GetEnabled() {
		t.Fatalf("enabled setting = %+v, %v", got, err)
	}
	if err := update(ownerCtx, false); err != nil {
		t.Fatal(err)
	}
	if steps != 2 || invalidated != 2 {
		t.Fatalf("step-ups %d, invalidations %d; want 2 each", steps, invalidated)
	}
	rows, err := st.R.QueryContext(ctx, `SELECT actor, action, params, result FROM audit WHERE action = 'client_api_update' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotAudits []string
	for rows.Next() {
		var actor, action, params, result string
		if err := rows.Scan(&actor, &action, &params, &result); err != nil {
			t.Fatal(err)
		}
		gotAudits = append(gotAudits, actor+"|"+action+"|"+params+"|"+result)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(gotAudits) != 2 || gotAudits[0] != `adm_owner|client_api_update|{"enabled":true}|ok` || gotAudits[1] != `adm_owner|client_api_update|{"enabled":false}|ok` {
		t.Fatalf("audit rows = %v", gotAudits)
	}
}

func TestSwitchDoesNotChangeWhenStepUpFails(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "client-api-stepup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	steps := 0
	s := New(Config{Store: st, StepUp: func(context.Context) error {
		steps++
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}})
	on := true
	_, err = (rpc{s}).UpdateClientApi(auth.WithAdmin(ctx, store.Admin{ID: "adm_owner", Role: store.RoleOwner}), connect.NewRequest(&adminv1.UpdateClientApiRequest{Enabled: &on}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || steps != 1 {
		t.Fatalf("update error %v, step-ups %d", err, steps)
	}
	if _, err := st.Setting(ctx, store.SettingClientAPI); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("setting after refused step-up = %v, want not found", err)
	}
}

// The card of the owner's switch carries a summary: everybody, the people who have the Mistgate app on (their switch, and the
// subscription link on) and the devices that hold a key. Every role that reads the switch reads it.
func TestReadCarriesTheSummaryOfTheApp(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "client-api-stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := st.Access().CreateGroup(ctx, store.AccessGroup{ID: "grp_a", Name: "family", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i, u := range []store.AccessUser{{AppHapp: true}, {AppHapp: true, AppMistgateOff: true}, {AppHapp: false, AppAmnezia: true}} {
		name := []string{"alice", "bob", "cara"}[i]
		u.ID, u.Name, u.GroupID, u.Status, u.AppAmnezia, u.AllNodes = "usr_"+name, name, "grp_a", "active", true, true
		u.QuotaReset, u.PeriodStart, u.DeviceLimit, u.SubTokenHash, u.SubTokenEnc, u.CreatedAt = "month", now, 5, []byte(name), []byte("sealed"), now
		if err := st.Access().CreateUser(ctx, u, store.AccessDevice{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO device (id, user_id, kind, hwid_hash, first_seen_at, last_seen_at, created_at, key_hash) VALUES
		('dev_keyed', 'usr_alice', 'link', randomblob(32), 1, 1, 1, randomblob(32)), ('dev_plain', 'usr_alice', 'link', randomblob(32), 1, 1, 1, NULL)`); err != nil {
		t.Fatal(err)
	}
	r := rpc{New(Config{Store: st, StepUp: func(context.Context) error { return nil }})}
	for _, role := range []string{store.RoleOwner, store.RoleHelper, store.RoleReadonly} {
		got, err := r.GetClientApi(auth.WithAdmin(ctx, store.Admin{ID: "adm_" + role, Role: role}), connect.NewRequest(&adminv1.GetClientApiRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if s := got.Msg.GetStats(); s.GetPeople() != 3 || s.GetPeopleOn() != 1 || s.GetDevices() != 1 {
			t.Errorf("%s reads stats %v, want 3 people, 1 with the app on, 1 device", role, s)
		}
	}
}
