//go:build !js

package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The owner's side of the logs people send for support.

func seedKeyedDevice(t *testing.T, e *env, user, device string, withKey bool) {
	t.Helper()
	key := "NULL"
	if withKey {
		key = "randomblob(32)"
	}
	for _, q := range []string{
		`INSERT OR IGNORE INTO user_group (id, name, created_at) VALUES ('grp_c', 'g', 1)`,
		`INSERT OR IGNORE INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('` + user + `', '` + user + `-name', 'grp_c', 1, randomblob(32), x'02', 1)`,
		`INSERT INTO device (id, user_id, platform, model, first_seen_at, last_seen_at, created_at, key_hash) VALUES ('` + device + `', '` + user + `', 'windows', 'Desk-PC', 1, 1, 1, ` + key + `)`,
	} {
		if _, err := e.st.W.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestClientDebugRequestCancelAndList(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	seedKeyedDevice(t, e, "usr_alice", "dev_alice", true)
	seedKeyedDevice(t, e, "usr_bob", "dev_bob", true)
	seedKeyedDevice(t, e, "usr_carol", "dev_nokey", false)

	code := func(err error) connect.Code { return connect.CodeOf(err) }
	if _, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{DeviceId: "dev_alice", Level: "trace"})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("bad level: %v", err)
	}
	if _, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("no device: %v", err)
	}
	for _, id := range []string{"dev_missing", "dev_nokey"} {
		if _, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{DeviceId: id})); code(err) != connect.CodeNotFound {
			t.Errorf("%s: %v", id, err)
		}
	}

	note := "Please send\x00 the logs‮\n" + strings.Repeat("x", 300)
	r, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{DeviceId: "dev_alice", Note: note, Level: "debug"}))
	if err != nil {
		t.Fatal(err)
	}
	got := r.Msg.Request
	if got.State != "open" || got.Level != "debug" || got.DeviceId != "dev_alice" || got.UserId != "usr_alice" || got.UserName != "usr_alice-name" ||
		got.DevicePlatform != "windows" || got.DeviceModel != "Desk-PC" || got.Unasked || got.ExpiresUnix-got.RequestedUnix != 72*3600 || got.ClosedUnix != 0 ||
		!strings.HasPrefix(got.Id, "dbr_") {
		t.Fatalf("request = %+v", got)
	}
	if n := len([]rune(got.Note)); n != store.ClientDebugNoteRunes || strings.ContainsAny(got.Note, "\x00‮\n") || !strings.HasPrefix(got.Note, "Please send  the logs") {
		t.Errorf("note (%d characters) = %q", n, got.Note)
	}
	if _, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{DeviceId: "dev_alice"})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("second request for the device: %v", err)
	}
	// the default level, and another device is its own
	if b, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{DeviceId: "dev_bob"})); err != nil || b.Msg.Request.Level != "info" || b.Msg.Request.Note != "" {
		t.Fatalf("bob's request: %+v %v", b, err)
	}
	var params string
	if err := e.st.R.QueryRow(`SELECT params FROM audit WHERE action = 'client_debug_requested' AND params LIKE '%dev_alice%'`).Scan(&params); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if !strings.Contains(params, got.Id) || !strings.Contains(params, `"level":"debug"`) || !strings.Contains(params, "expires") || strings.Contains(params, "Please send") {
		t.Errorf("audit params: %s", params)
	}

	// a bundle a person sent on their own is listed with the requests
	now := time.Now()
	if _, _, err := e.st.ReceiveClientDebug(ctx, store.ClientDebugIntake{NewID: store.NewClientDebugID(), UserID: "usr_alice", DeviceID: "dev_alice", App: "Mistgate/0.3.1",
		Lines: []store.ClientDebugLine{{AtMs: 5, Level: 1, Source: "service", Message: "x"}}, Bytes: 60, FirstMs: 5, LastMs: 5, Now: now}); err != nil {
		t.Fatal(err)
	}
	all, err := e.cl.ListClientDebug(ctx, connect.NewRequest(&opsv1.ListClientDebugRequest{}))
	if err != nil || len(all.Msg.Items) != 3 {
		t.Fatalf("list: %v %v", all, err)
	}
	mine, err := e.cl.ListClientDebug(ctx, connect.NewRequest(&opsv1.ListClientDebugRequest{UserId: "usr_alice"}))
	if err != nil || len(mine.Msg.Items) != 2 {
		t.Fatalf("list of alice: %v %v", mine, err)
	}
	var sent, asked *opsv1.ClientDebugInfo
	for _, it := range mine.Msg.Items {
		if it.Unasked {
			sent = it
		} else {
			asked = it
		}
	}
	if sent == nil || sent.State != "received" || sent.Records != 1 || sent.App != "Mistgate/0.3.1" || sent.Actor != "" || sent.ClosedUnix == 0 || sent.Id == got.Id {
		t.Errorf("bundle sent unasked: %+v", sent)
	}
	if asked == nil || asked.Id != got.Id || asked.State != "open" {
		t.Errorf("request: %+v", asked)
	}
	if none, err := e.cl.ListClientDebug(ctx, connect.NewRequest(&opsv1.ListClientDebugRequest{UserId: "usr_nobody"})); err != nil || len(none.Msg.Items) != 0 {
		t.Errorf("list of nobody: %v %v", none, err)
	}

	// cancel
	c, err := e.cl.CancelClientDebug(ctx, connect.NewRequest(&opsv1.CancelClientDebugRequest{Id: got.Id}))
	if err != nil || c.Msg.Request.State != "cancelled" || c.Msg.Request.ClosedUnix == 0 {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	if _, err := e.cl.CancelClientDebug(ctx, connect.NewRequest(&opsv1.CancelClientDebugRequest{Id: got.Id})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("second cancel: %v", err)
	}
	if _, err := e.cl.CancelClientDebug(ctx, connect.NewRequest(&opsv1.CancelClientDebugRequest{Id: sent.Id})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("cancel of a bundle: %v", err)
	}
	if _, err := e.cl.CancelClientDebug(ctx, connect.NewRequest(&opsv1.CancelClientDebugRequest{Id: "dbr_missing"})); code(err) != connect.CodeNotFound {
		t.Errorf("cancel of an unknown id: %v", err)
	}
	if n := countRows(t, e, `SELECT COUNT(*) FROM audit WHERE action = 'client_debug_cancelled'`); n != 1 {
		t.Errorf("cancel audit rows = %d", n)
	}
	// the device can be asked again
	if _, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{DeviceId: "dev_alice"})); err != nil {
		t.Errorf("request after a cancel: %v", err)
	}
}

// A bundle's lines are not readable through the node capture API: GetDebugCapture looks in debug_capture only.
func TestClientLogsAreNotReadableThroughTheNodeCaptureAPI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	seedKeyedDevice(t, e, "usr_alice", "dev_alice", true)
	row, _, err := e.st.ReceiveClientDebug(ctx, store.ClientDebugIntake{NewID: store.NewClientDebugID(), UserID: "usr_alice", DeviceID: "dev_alice",
		Lines: []store.ClientDebugLine{{AtMs: 5, Level: 1, Source: "service", Message: "line of a person"}}, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.cl.GetDebugCapture(ctx, connect.NewRequest(&opsv1.GetDebugCaptureRequest{CaptureId: row.ID}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetDebugCapture(%s) = %v, want NotFound", row.ID, err)
	}
	if lines, err := e.cl.ListDebugCaptures(ctx, connect.NewRequest(&opsv1.ListDebugCapturesRequest{})); err != nil || len(lines.Msg.Captures) != 0 {
		t.Errorf("ListDebugCaptures shows %v, %v", lines, err)
	}
}

func countRows(t *testing.T, e *env, q string) int {
	t.Helper()
	var n int
	if err := e.st.R.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
