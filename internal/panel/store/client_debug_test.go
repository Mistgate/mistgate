//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var debugNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// keyedDevice makes a device of the user that has a key (a device of the Mistgate app).
func keyedDevice(t *testing.T, s *Store, user, tag string) string {
	t.Helper()
	ctx := context.Background()
	res, err := s.Access().EnsureLinkDevice(ctx, linkDevice(user, tag, HWIDHash(user, strings.Repeat(tag, 16))), 10, linkNow, linkCreds(tag, user))
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.Access().SetLinkDeviceKeyHash(ctx, user, res.Device.ID, nil, deviceHash("key-"+tag), deviceHash("token-"+tag)); err != nil || !changed {
		t.Fatalf("key of %s: %v %v", tag, changed, err)
	}
	return res.Device.ID
}

func debugFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	s := clientDeviceKeyFixture(t)
	return s, keyedDevice(t, s, "usr_alice", "a"), keyedDevice(t, s, "usr_alice", "b")
}

func askDebug(t *testing.T, s *Store, device string, at time.Time) ClientDebugRow {
	t.Helper()
	row, err := s.CreateClientDebug(context.Background(), ClientDebugRow{ID: NewClientDebugID(), DeviceID: device, Actor: "adm_1", Note: "please", Level: "debug"}, at)
	if err != nil {
		t.Fatalf("CreateClientDebug: %v", err)
	}
	return row
}

func debugLines(n int) []ClientDebugLine {
	out := make([]ClientDebugLine, n)
	for i := range out {
		out[i] = ClientDebugLine{AtMs: debugNow.UnixMilli() + int64(i), Level: i % 4, Source: "service", Message: fmt.Sprintf("line %d", i), Attrs: map[string]string{"code": "c"}}
	}
	return out
}

func answer(t *testing.T, s *Store, req ClientDebugRow, lines int, at time.Time) ClientDebugRow {
	t.Helper()
	row, replay, err := s.ReceiveClientDebug(context.Background(), ClientDebugIntake{RequestID: req.ID, UserID: req.UserID, DeviceID: req.DeviceID, App: "Mistgate/0.3.1",
		Lines: debugLines(lines), Bytes: 100 * lines, DroppedKV: 2, FirstMs: 5, LastMs: 9, Now: at})
	if err != nil || replay {
		t.Fatalf("ReceiveClientDebug: replay=%v err=%v", replay, err)
	}
	return row
}

func countLines(t *testing.T, s *Store, id string) int {
	t.Helper()
	return countT(t, s, `SELECT COUNT(*) FROM debug_capture_line WHERE capture_id = ?`, id)
}

func TestClientDebugOneOpenRequestPerDevice(t *testing.T) {
	ctx := context.Background()
	s, a, b := debugFixture(t)
	first := askDebug(t, s, a, debugNow)
	if first.State != "open" || first.UserID != "usr_alice" || first.Note != "please" || first.Level != "debug" || first.Unasked ||
		!first.ExpiresAt.Equal(debugNow.Add(ClientDebugRequestTTL)) || !strings.HasPrefix(first.ID, "dbr_") {
		t.Fatalf("new request: %+v", first)
	}
	if _, err := s.CreateClientDebug(ctx, ClientDebugRow{ID: NewClientDebugID(), DeviceID: a, Actor: "adm_1", Level: "info"}, debugNow.Add(time.Hour)); !errors.Is(err, ErrClientDebugOpen) {
		t.Fatalf("a second open request: %v", err)
	}
	askDebug(t, s, b, debugNow) // another device is its own
	// a device with no key, a revoked one and an unknown one
	plain, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "c", HWIDHash("usr_alice", "cccccccccccccccc")), 10, linkNow, linkCreds("c", "usr_alice"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{plain.Device.ID, "dev_missing"} {
		if _, err := s.CreateClientDebug(ctx, ClientDebugRow{ID: NewClientDebugID(), DeviceID: id, Level: "info"}, debugNow); !errors.Is(err, ErrNotFound) {
			t.Errorf("request for %s: %v", id, err)
		}
	}
	// after 72 hours the request has run out: it reads as expired, is not offered, and the next request replaces it
	late := debugNow.Add(ClientDebugRequestTTL)
	if got, _ := s.ClientDebug(ctx, first.ID, late); got.State != "expired" {
		t.Errorf("state at the deadline = %s", got.State)
	}
	if _, ok, err := s.OpenClientDebug(ctx, a, late); err != nil || ok {
		t.Errorf("open request at the deadline: %v %v", ok, err)
	}
	if _, ok, err := s.OpenClientDebug(ctx, a, late.Add(-time.Second)); err != nil || !ok {
		t.Errorf("open request a second before the deadline: %v %v", ok, err)
	}
	second := askDebug(t, s, a, late)
	if got, _ := s.ClientDebug(ctx, first.ID, late); got.State != "expired" || got.ClosedAt.IsZero() {
		t.Errorf("the replaced request: %+v", got)
	}
	if open, ok, _ := s.OpenClientDebug(ctx, a, late); !ok || open.ID != second.ID {
		t.Errorf("open request = %+v %v, want the second", open, ok)
	}
	// a revoked device gets no request
	if _, err := s.Access().RevokeDevice(ctx, b, debugNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateClientDebug(ctx, ClientDebugRow{ID: NewClientDebugID(), DeviceID: b, Level: "info"}, late.Add(100*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("request for a revoked device: %v", err)
	}
}

func TestClientDebugReceiveStoresLinesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, a, _ := debugFixture(t)
	req := askDebug(t, s, a, debugNow)
	at := debugNow.Add(time.Hour)
	got := answer(t, s, req, 18, at)
	if got.State != "received" || got.Records != 18 || got.Bytes != 1800 || got.DroppedKV != 2 || got.FirstMs != 5 || got.LastMs != 9 || got.App != "Mistgate/0.3.1" || !got.ClosedAt.Equal(at) {
		t.Fatalf("received row: %+v", got)
	}
	if n := countLines(t, s, req.ID); n != 18 {
		t.Fatalf("lines = %d", n)
	}
	lines, sources, err := s.DebugLines(ctx, req.ID, DebugLineFilter{Limit: 5})
	if err != nil || len(lines) != 6 || lines[0].Seq != 1 || lines[0].Message != "line 0" || len(sources) != 1 || sources[0] != "service" {
		t.Fatalf("lines read: %d %+v %v %v", len(lines), lines, sources, err)
	}
	// a second delivery of the same request: acknowledged with the stored row, nothing written
	again, replay, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{RequestID: req.ID, UserID: req.UserID, DeviceID: a, Lines: debugLines(3), Now: at.Add(time.Minute)})
	if err != nil || !replay || again.Records != 18 {
		t.Fatalf("repeat: %+v replay=%v %v", again, replay, err)
	}
	if n := countLines(t, s, req.ID); n != 18 {
		t.Errorf("a repeat changed the lines: %d", n)
	}
	// the request is closed for everything else
	if _, err := s.CancelClientDebug(ctx, req.ID, at); !errors.Is(err, ErrClientDebugClosed) {
		t.Errorf("cancel after receipt: %v", err)
	}
	if _, err := s.DeclineClientDebug(ctx, a, req.ID, at); !errors.Is(err, ErrClientDebugNoRequest) {
		t.Errorf("decline after receipt: %v", err)
	}
	if _, ok, _ := s.OpenClientDebug(ctx, a, at); ok {
		t.Error("an answered request is still offered")
	}
	// the GetDebugCapture of the nodes finds none of it
	if _, err := s.DebugCapture(ctx, req.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DebugCapture(%s) = %v, want ErrNotFound", req.ID, err)
	}
	if _, ok, err := s.AppendDebugLines(ctx, "nod_1", req.ID, debugLinesIn(1), nil, at); err != nil || ok {
		t.Errorf("a node's append into a client bundle: ok=%v %v", ok, err)
	}
}

func debugLinesIn(n int) []DebugLineIn {
	out := make([]DebugLineIn, n)
	for i := range out {
		out[i] = DebugLineIn{AtMs: 1, Level: 1, Source: "agent", Message: "x"}
	}
	return out
}

func TestClientDebugReceiveRefusals(t *testing.T) {
	ctx := context.Background()
	s, a, b := debugFixture(t)
	req := askDebug(t, s, a, debugNow)
	intake := func(id, device string, at time.Time) error {
		_, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{RequestID: id, UserID: "usr_alice", DeviceID: device, Lines: debugLines(2), Now: at})
		return err
	}
	if err := intake(req.ID, b, debugNow); !errors.Is(err, ErrClientDebugNoRequest) { // another device's request
		t.Errorf("foreign device: %v", err)
	}
	if err := intake("dbr_unknown", a, debugNow); !errors.Is(err, ErrClientDebugNoRequest) {
		t.Errorf("unknown id: %v", err)
	}
	if err := intake(req.ID, a, debugNow.Add(ClientDebugRequestTTL)); !errors.Is(err, ErrClientDebugNoRequest) { // run out
		t.Errorf("expired: %v", err)
	}
	if _, err := s.CancelClientDebug(ctx, req.ID, debugNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := intake(req.ID, a, debugNow.Add(2*time.Minute)); !errors.Is(err, ErrClientDebugNoRequest) { // cancelled while the person was sending
		t.Errorf("cancelled: %v", err)
	}
	if n := countLines(t, s, req.ID); n != 0 {
		t.Errorf("refused deliveries left %d lines", n)
	}
	if _, err := s.CancelClientDebug(ctx, "dbr_unknown", debugNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel of an unknown id: %v", err)
	}
	// declined
	req2 := askDebug(t, s, a, debugNow.Add(time.Hour))
	if _, err := s.DeclineClientDebug(ctx, b, req2.ID, debugNow.Add(time.Hour)); !errors.Is(err, ErrClientDebugNoRequest) {
		t.Errorf("decline by another device: %v", err)
	}
	row, err := s.DeclineClientDebug(ctx, a, req2.ID, debugNow.Add(time.Hour))
	if err != nil || row.State != "declined" {
		t.Fatalf("decline: %+v %v", row, err)
	}
	if _, err := s.DeclineClientDebug(ctx, a, req2.ID, debugNow.Add(time.Hour)); !errors.Is(err, ErrClientDebugNoRequest) {
		t.Errorf("decline twice: %v", err)
	}
	if err := intake(req2.ID, a, debugNow.Add(time.Hour)); !errors.Is(err, ErrClientDebugNoRequest) {
		t.Errorf("receipt after a refusal: %v", err)
	}
}

func TestClientDebugKeepsThreeBundlesPerDeviceWithTheirLines(t *testing.T) {
	ctx := context.Background()
	s, a, b := debugFixture(t)
	other := answer(t, s, askDebug(t, s, b, debugNow), 4, debugNow)
	var ids []string
	for i := range 5 { // the clock does not move: the newest is still the one just received
		req := askDebug(t, s, a, debugNow)
		answer(t, s, req, 3, debugNow)
		ids = append(ids, req.ID)
		_ = i
	}
	rows, err := s.ClientDebugList(ctx, "", debugNow)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, r := range rows {
		if r.DeviceID == a {
			kept[r.ID] = true
		}
	}
	if len(kept) != 3 || !kept[ids[4]] {
		t.Fatalf("kept for the device: %v, want 3 including the newest %s", kept, ids[4])
	}
	for _, id := range ids {
		want := 0
		if kept[id] {
			want = 3
		}
		if n := countLines(t, s, id); n != want {
			t.Errorf("lines of %s = %d, want %d", id, n, want)
		}
	}
	if n := countLines(t, s, other.ID); n != 4 {
		t.Errorf("another device's bundle lost lines: %d", n)
	}
	// older bundles go first
	s2, a2, _ := debugFixture(t)
	var old []string
	for i := range 4 {
		req := askDebug(t, s2, a2, debugNow.Add(time.Duration(i)*time.Hour))
		answer(t, s2, req, 1, debugNow.Add(time.Duration(i)*time.Hour))
		old = append(old, req.ID)
	}
	if _, err := s2.ClientDebug(ctx, old[0], debugNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("the oldest bundle survived: %v", err)
	}
	for _, id := range old[1:] {
		if _, err := s2.ClientDebug(ctx, id, debugNow); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
}

func TestClientDebugSentUnaskedOncePerTenMinutes(t *testing.T) {
	ctx := context.Background()
	s, a, b := debugFixture(t)
	send := func(device string, at time.Time) (ClientDebugRow, error) {
		if err := s.ClientDebugAdmitUnasked(ctx, device, at); err != nil {
			return ClientDebugRow{}, err
		}
		row, replay, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: device, App: "Mistgate/0.3.1", Lines: debugLines(4), Bytes: 40, Now: at})
		if replay {
			t.Error("an unasked bundle is a replay")
		}
		return row, err
	}
	first, err := send(a, debugNow)
	if err != nil || !first.Unasked || first.State != "received" || first.Actor != "" || first.Records != 4 || !strings.HasPrefix(first.ID, "dbr_") {
		t.Fatalf("unasked bundle: %+v %v", first, err)
	}
	if n := countLines(t, s, first.ID); n != 4 {
		t.Errorf("lines = %d", n)
	}
	var rate *ClientDebugRateError
	if _, err := send(a, debugNow.Add(9*time.Minute+59*time.Second)); !errors.As(err, &rate) || rate.After != time.Second {
		t.Fatalf("a second within ten minutes: %v", err)
	}
	if _, err := send(b, debugNow.Add(time.Minute)); err != nil { // the limit is the device's
		t.Errorf("another device: %v", err)
	}
	// the same gate inside the write: a delivery that skipped the pre-check is refused as well
	if _, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: a, Lines: debugLines(1), Now: debugNow.Add(5 * time.Minute)}); !errors.As(err, &rate) {
		t.Errorf("unchecked second delivery: %v", err)
	}
	if _, err := send(a, debugNow.Add(ClientDebugUnaskedEvery)); err != nil {
		t.Fatalf("after ten minutes: %v", err)
	}
	// an open request stays open while the person sends on their own, and the unasked bundles count in the three
	req := askDebug(t, s, a, debugNow.Add(20*time.Minute))
	for i := 2; i < 5; i++ {
		if _, err := send(a, debugNow.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := s.OpenClientDebug(ctx, a, debugNow.Add(6*time.Hour)); !ok {
		t.Error("an unasked bundle closed the request")
	}
	if n := countT(t, s, `SELECT COUNT(*) FROM client_debug WHERE device_id = ? AND state = 'received'`, a); n != ClientDebugPerDevice {
		t.Errorf("bundles of the device = %d", n)
	}
	answer(t, s, req, 1, debugNow.Add(7*time.Hour))
	if n := countT(t, s, `SELECT COUNT(*) FROM client_debug WHERE device_id = ? AND state = 'received'`, a); n != ClientDebugPerDevice {
		t.Errorf("bundles of the device after the answer = %d", n)
	}
}

func TestClientDebugPanelCeilingRefusesUnaskedOnly(t *testing.T) {
	ctx := context.Background()
	s, a, b := debugFixture(t)
	if _, err := s.W.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO client_debug (id, user_id, device_id, requested_at, expires_at, state, closed_at) SELECT 'dbr_fill' || i, 'usr_alice', 'dev_fill' || i, 1, 1, 'received', 1 FROM n`, ClientDebugMaxBundles-1); err != nil {
		t.Fatal(err)
	}
	unasked := func(device string, at time.Time) error {
		if err := s.ClientDebugAdmitUnasked(ctx, device, at); err != nil {
			return err
		}
		_, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: device, Lines: debugLines(1), Now: at})
		return err
	}
	if err := unasked(a, debugNow); err != nil { // the 200th
		t.Fatalf("the last free place: %v", err)
	}
	if err := unasked(b, debugNow); !errors.Is(err, ErrClientDebugFull) {
		t.Fatalf("past the ceiling: %v", err)
	}
	if _, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: b, Lines: debugLines(1), Now: debugNow}); !errors.Is(err, ErrClientDebugFull) {
		t.Errorf("unchecked delivery past the ceiling: %v", err)
	}
	// the owner's request is answered all the same, and it counts
	req := askDebug(t, s, b, debugNow)
	answer(t, s, req, 2, debugNow)
	if n := countT(t, s, `SELECT COUNT(*) FROM client_debug WHERE state = 'received'`); n != ClientDebugMaxBundles+1 {
		t.Errorf("bundles = %d", n)
	}
}

func TestClientDebugSimultaneousDeliveriesStoreOnce(t *testing.T) {
	ctx := context.Background()
	s, a, _ := debugFixture(t)
	req := askDebug(t, s, a, debugNow)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var replays, stored int
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, replay, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{RequestID: req.ID, UserID: "usr_alice", DeviceID: a, Lines: debugLines(50), Now: debugNow.Add(time.Minute)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				t.Errorf("delivery: %v", err)
			case replay:
				replays++
			default:
				stored++
			}
		}()
	}
	wg.Wait()
	if stored != 1 || replays != 3 {
		t.Errorf("stored %d, replays %d", stored, replays)
	}
	if n := countLines(t, s, req.ID); n != 50 {
		t.Errorf("lines = %d", n)
	}
}

func TestPruneClientDebugByAgeAndOrphans(t *testing.T) {
	ctx := context.Background()
	s, a, b := debugFixture(t)
	fresh := answer(t, s, askDebug(t, s, a, debugNow), 2, debugNow)
	old := answer(t, s, askDebug(t, s, a, debugNow.Add(-8*24*time.Hour)), 2, debugNow.Add(-8*24*time.Hour))
	declined := askDebug(t, s, b, debugNow.Add(-9*24*time.Hour))
	gone := answer(t, s, askDebug(t, s, b, debugNow), 2, debugNow)
	if _, err := s.W.ExecContext(ctx, `DELETE FROM device WHERE id = ?`, b); err != nil {
		t.Fatal(err)
	}
	// a node capture's line, which the sweep must leave alone
	if _, err := s.W.ExecContext(ctx, `INSERT INTO debug_capture_line (capture_id, seq, at_ms, level, source, message) VALUES ('req_node', 1, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneClientDebug(ctx, debugNow.Add(-ClientDebugKeep))
	if err != nil || n != 3 {
		t.Fatalf("pruned %d rows, %v; want the old one, the old request and the orphan", n, err)
	}
	for _, id := range []string{old.ID, declined.ID, gone.ID} {
		if _, err := s.ClientDebug(ctx, id, debugNow); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s survived: %v", id, err)
		}
		if c := countLines(t, s, id); c != 0 {
			t.Errorf("%s left %d lines", id, c)
		}
	}
	if _, err := s.ClientDebug(ctx, fresh.ID, debugNow); err != nil || countLines(t, s, fresh.ID) != 2 {
		t.Errorf("the fresh bundle did not survive: %v", err)
	}
	if countLines(t, s, "req_node") != 1 {
		t.Error("a node capture's line went")
	}
	noStrayLines(t, s)
}

func TestClientDebugListNamesAndOrder(t *testing.T) {
	ctx := context.Background()
	s, a, b := debugFixture(t)
	if _, err := s.W.ExecContext(ctx, `UPDATE device SET model = 'Desk-PC', platform = 'windows' WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	first := askDebug(t, s, a, debugNow)
	second := askDebug(t, s, b, debugNow.Add(time.Minute))
	rows, err := s.ClientDebugList(ctx, "usr_alice", debugNow)
	if err != nil || len(rows) != 2 || rows[0].ID != second.ID || rows[1].ID != first.ID {
		t.Fatalf("list: %+v %v", rows, err)
	}
	if rows[1].UserName != "alice" || rows[1].DeviceModel != "Desk-PC" || rows[1].DevicePlatform != "windows" || rows[1].ActorName != "adm_1" {
		t.Errorf("names: %+v", rows[1])
	}
	if rows, _ := s.ClientDebugList(ctx, "usr_nobody", debugNow); len(rows) != 0 {
		t.Errorf("another person's list: %d", len(rows))
	}
}

// noStrayLines: every way a bundle goes (an eviction, the sweep) takes its lines in the same batch, so no line of a bundle is left
// without its row. The sweep does not look for such lines (it would scan every line of the panel under the write lock).
func noStrayLines(t *testing.T, s *Store) {
	t.Helper()
	if n := countT(t, s, `SELECT COUNT(*) FROM debug_capture_line WHERE capture_id >= 'dbr_' AND capture_id < 'dbr`+"`"+`' AND capture_id NOT IN (SELECT id FROM client_debug)`); n != 0 {
		t.Errorf("%d lines of bundles that are gone", n)
	}
}

// One person with four devices, one after the other (leaving and coming back as a new device): three bundles sent unasked are kept
// for the person, the oldest go with their lines.
func TestClientDebugKeepsThreeUnaskedBundlesPerPerson(t *testing.T) {
	ctx := context.Background()
	s, _, _ := debugFixture(t)
	var devices []string
	for _, tag := range []string{"c", "d", "e", "f"} {
		devices = append(devices, keyedDevice(t, s, "usr_alice", tag))
	}
	var ids []string
	for i, d := range devices {
		at := debugNow.Add(time.Duration(i) * time.Minute)
		row, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: d, Lines: debugLines(2), Bytes: 200, Now: at})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.ID)
	}
	if _, err := s.ClientDebug(ctx, ids[0], debugNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("the oldest bundle of the person survived: %v", err)
	}
	for _, id := range ids[1:] {
		if _, err := s.ClientDebug(ctx, id, debugNow); err != nil || countLines(t, s, id) != 2 {
			t.Errorf("%s: %v", id, err)
		}
	}
	if n := countT(t, s, `SELECT COUNT(*) FROM client_debug WHERE user_id = 'usr_alice' AND unasked = 1`); n != 3 {
		t.Errorf("unasked bundles of the person = %d", n)
	}
	// another person is not pushed out
	rewriteAccessUser(t, s, "usr_bob", "bob", "grp_link")
	bob := keyedDevice(t, s, "usr_bob", "g")
	if _, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_bob", DeviceID: bob, Lines: debugLines(1), Now: debugNow.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if n := countT(t, s, `SELECT COUNT(*) FROM client_debug WHERE unasked = 1`); n != 4 {
		t.Errorf("unasked bundles in all = %d, want 3 of alice and 1 of bob", n)
	}
	noStrayLines(t, s)
}

// Bundles sent unasked never push out one the owner asked for (L-2): three of them in a row leave it where it was.
func TestClientDebugUnaskedBundlesDoNotPushOutARequestedOne(t *testing.T) {
	ctx := context.Background()
	s, a, _ := debugFixture(t)
	asked := answer(t, s, askDebug(t, s, a, debugNow), 5, debugNow)
	for i := range 5 {
		at := debugNow.Add(time.Duration(i+1) * ClientDebugUnaskedEvery)
		if _, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: a, Lines: debugLines(1), Now: at}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ClientDebug(ctx, asked.ID, debugNow); err != nil || countLines(t, s, asked.ID) != 5 {
		t.Fatalf("the requested bundle was pushed out: %v", err)
	}
	if n := countT(t, s, `SELECT COUNT(*) FROM client_debug WHERE device_id = ? AND unasked = 1`, a); n != 3 {
		t.Errorf("unasked bundles of the device = %d, want 3", n)
	}
	noStrayLines(t, s)
}

// The bundles weigh at most ClientDebugMaxBytes together: a bundle sent unasked past it is refused, an answer to the owner's request is not.
func TestClientDebugWeightCeilingRefusesUnaskedOnly(t *testing.T) {
	ctx := context.Background()
	s, a, b := debugFixture(t)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO client_debug (id, user_id, device_id, requested_at, expires_at, state, closed_at, bytes) VALUES ('dbr_heavy', 'usr_alice', 'dev_heavy', 1, 1, 'received', 1, ?)`,
		ClientDebugMaxBytes-1000); err != nil {
		t.Fatal(err)
	}
	if err := s.ClientDebugAdmitUnasked(ctx, a, debugNow); err != nil {
		t.Fatalf("admitted below the ceiling: %v", err)
	}
	// a bundle that fits in what is left, and one that does not
	if _, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: a, Lines: debugLines(1), Bytes: 2000, Now: debugNow}); !errors.Is(err, ErrClientDebugFull) {
		t.Fatalf("a bundle past the weight ceiling: %v", err)
	}
	if _, _, err := s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: a, Lines: debugLines(1), Bytes: 1000, Now: debugNow}); err != nil {
		t.Fatalf("a bundle that fits: %v", err)
	}
	if err := s.ClientDebugAdmitUnasked(ctx, b, debugNow); !errors.Is(err, ErrClientDebugFull) {
		t.Fatalf("admission at the ceiling: %v", err)
	}
	req := askDebug(t, s, b, debugNow)
	answer(t, s, req, 3, debugNow) // 300 bytes more than the ceiling: the owner asked
	if s.ClientDebugAdmitUnasked(ctx, b, debugNow) == nil {
		t.Error("the ceiling opened again")
	}
}

// The ceilings hold when many devices bring a bundle at once: the guards are in the write, not only in the check before it.
func TestClientDebugCeilingHoldsUnderRace(t *testing.T) {
	ctx := context.Background()
	s, _, _ := debugFixture(t)
	var devices []string
	for i := range 8 {
		devices = append(devices, keyedDevice(t, s, "usr_alice", fmt.Sprintf("r%d", i)))
	}
	if _, err := s.W.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO client_debug (id, user_id, device_id, requested_at, expires_at, state, closed_at) SELECT 'dbr_fill' || i, 'usr_other', 'dev_fill' || i, 1, 1, 'received', 1 FROM n`, ClientDebugMaxBundles-2); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, d := range devices {
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if s.ClientDebugAdmitUnasked(ctx, d, debugNow) == nil {
					_, _, _ = s.ReceiveClientDebug(ctx, ClientDebugIntake{NewID: NewClientDebugID(), UserID: "usr_alice", DeviceID: d, Lines: debugLines(20), Now: debugNow})
				}
			}()
		}
	}
	wg.Wait()
	// the person keeps three unasked bundles; 198 filler rows are other people's: the ceiling is not passed
	if n := countT(t, s, `SELECT COUNT(*) FROM client_debug WHERE state = 'received'`); n > ClientDebugMaxBundles {
		t.Errorf("received bundles = %d, past the ceiling of %d", n, ClientDebugMaxBundles)
	}
	if n := countT(t, s, `SELECT COUNT(*) FROM client_debug WHERE user_id = 'usr_alice' AND unasked = 1`); n != 2 && n != 3 {
		t.Errorf("unasked bundles of the person = %d", n)
	}
	noStrayLines(t, s)
}
