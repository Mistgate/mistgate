//go:build !js

package ops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Reading, downloading and deleting the logs people send for support.

const (
	stateFile = `%USERPROFILE%\AppData\Roaming\Mistgate\state.json`
	// A secret name the panel learned after the bundle was stored: the read cuts it out of what comes back.
	lateSecret = "late-secret-name.example"
)

// seedBundle stores a received bundle of device dev (a person's own: sent unasked) and returns its id.
func seedBundle(t *testing.T, e *env, user, dev string, lines []store.ClientDebugLine) string {
	t.Helper()
	seedKeyedDevice(t, e, user, dev, true)
	in := store.ClientDebugIntake{NewID: store.NewClientDebugID(), UserID: user, DeviceID: dev, App: "Mistgate/0.3.1", Lines: lines, Now: time.Now()}
	for i, l := range lines {
		in.Bytes += 40 + len(l.Message)
		if i == 0 {
			in.FirstMs = l.AtMs
		}
		in.LastMs = l.AtMs
	}
	row, _, err := e.st.ReceiveClientDebug(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func aliceLines() []store.ClientDebugLine {
	return []store.ClientDebugLine{
		{AtMs: 1000, Level: 1, Source: "service", Message: "connecting", Attrs: map[string]string{"path": "pth_q1W2e3R4t5Y6u7I8", "protocol": "hysteria2", "attempt": "1"}},
		{AtMs: 2000, Level: 2, Source: "tunnel", Message: "handshake timed out after 5000 ms", Attrs: map[string]string{"code": "handshake_timeout", "server": "srv_Zz98Yy76Xx54Ww32", "via": "direct", "ms": "5000"}},
		{AtMs: 3000, Level: 3, Source: "service", Message: "cannot write " + stateFile, Attrs: map[string]string{"code": "state_write_failed", "path": stateFile, "bytes": "2048", "net": "wifi"}},
		{AtMs: 4000, Level: 0, Source: "tunnel", Message: "trace for " + strings.ToUpper(lateSecret) + " ended", Attrs: map[string]string{"engine": "sing-box", "phase": "up", "status": "200", "http": "204", "err": "reset by " + lateSecret}},
		{AtMs: 5000, Level: 1, Source: "service", Message: "plain note", Attrs: map[string]string{"stray": "an attribute outside the contract", "state": "ok"}},
	}
}

func getBundle(t *testing.T, e *env, req *opsv1.GetClientDebugRequest) *opsv1.GetClientDebugResponse {
	t.Helper()
	r, err := e.cl.GetClientDebug(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	return r.Msg
}

func seqList(recs []*opsv1.DebugRecord) string {
	var out []string
	for _, r := range recs {
		out = append(out, fmt.Sprint(r.Seq))
	}
	return strings.Join(out, ",")
}

// A bundle is read as it was stored: a file name stays a file name and a path stays a path, the attributes of the contract come with
// the record, and the only thing the read does to the text is cut out the panel's current secret names.
func TestGetClientDebugReadsTheStoredTextNotTheNodeJournalCleaning(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Secrets = append(c.Secrets, lateSecret) })
	id := seedBundle(t, e, "usr_alice", "dev_alice", aliceLines())
	r := getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id})
	if len(r.Records) != 5 || r.NextCursor != 0 {
		t.Fatalf("records = %d, cursor %d", len(r.Records), r.NextCursor)
	}
	if b := r.Bundle; b.Id != id || b.State != "received" || b.Records != 5 || b.UserName != "usr_alice-name" || b.DeviceModel != "Desk-PC" || b.App != "Mistgate/0.3.1" {
		t.Errorf("bundle = %+v", b)
	}
	third := r.Records[2]
	if want := "cannot write " + stateFile; third.Message != want {
		t.Errorf("message = %q, want it as stored (%q)", third.Message, want)
	}
	if third.Attrs["path"] != stateFile {
		t.Errorf("attribute path = %q, want the stored path", third.Attrs["path"])
	}
	for _, bad := range []string{"<host>", "<key>"} {
		if strings.Contains(third.Message, bad) || strings.Contains(third.Attrs["path"], bad) {
			t.Errorf("the node journal's cleaning ran over a bundle: %q", bad)
		}
	}
	// every key the contract names comes through (they are all keys the node journal would have dropped)
	if a := r.Records[1].Attrs; a["server"] != "srv_Zz98Yy76Xx54Ww32" || a["via"] != "direct" || a["ms"] != "5000" || a["code"] != "handshake_timeout" {
		t.Errorf("attributes of the second record = %v", a)
	}
	if a := r.Records[0].Attrs; a["path"] != "pth_q1W2e3R4t5Y6u7I8" || a["protocol"] != "hysteria2" || a["attempt"] != "1" {
		t.Errorf("attributes of the first record = %v", a)
	}
	if a := r.Records[3].Attrs; a["engine"] != "sing-box" || a["phase"] != "up" || a["status"] != "200" || a["http"] != "204" {
		t.Errorf("attributes of the fourth record = %v", a)
	}
	if a := r.Records[2].Attrs; a["bytes"] != "2048" || a["net"] != "wifi" {
		t.Errorf("attributes of the third record = %v", a)
	}
	// an attribute outside the contract's keys is not returned, even if it is stored
	if _, ok := r.Records[4].Attrs["stray"]; ok || r.Records[4].Attrs["state"] != "ok" {
		t.Errorf("attributes of the last record = %v", r.Records[4].Attrs)
	}
	// the panel's current secret names are cut out of text and attributes
	raw := r.String()
	if strings.Contains(strings.ToLower(raw), lateSecret) {
		t.Errorf("the read returned a secret name:\n%s", raw)
	}
	if !strings.Contains(r.Records[3].Message, "<secret>") || r.Records[3].Attrs["err"] != "reset by <secret>" {
		t.Errorf("fourth record = %+v", r.Records[3])
	}
	if strings.Join(r.Sources, ",") != "service,tunnel" {
		t.Errorf("sources = %v", r.Sources)
	}
	if r.Records[0].Level != "info" || r.Records[1].Level != "warn" || r.Records[2].Level != "error" || r.Records[3].Level != "debug" || r.Records[0].TimeUnixMs != 1000 {
		t.Errorf("levels or times: %v", r.Records)
	}
}

func TestGetClientDebugFiltersSearchesAndPages(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Secrets = append(c.Secrets, lateSecret) })
	id := seedBundle(t, e, "usr_alice", "dev_alice", aliceLines())
	get := func(req *opsv1.GetClientDebugRequest) *opsv1.GetClientDebugResponse {
		req.Id = id
		return getBundle(t, e, req)
	}
	for name, c := range map[string]struct {
		req  *opsv1.GetClientDebugRequest
		want string
	}{
		"warn and above":         {&opsv1.GetClientDebugRequest{MinLevel: "warn"}, "2,3"},
		"error":                  {&opsv1.GetClientDebugRequest{MinLevel: "ERROR"}, "3"},
		"source":                 {&opsv1.GetClientDebugRequest{Source: "tunnel"}, "2,4"},
		"source and level":       {&opsv1.GetClientDebugRequest{Source: "tunnel", MinLevel: "warn"}, "2"},
		"from":                   {&opsv1.GetClientDebugRequest{FromMs: 3000}, "3,4,5"},
		"to":                     {&opsv1.GetClientDebugRequest{ToMs: 2000}, "1,2"},
		"from and to":            {&opsv1.GetClientDebugRequest{FromMs: 2000, ToMs: 4000}, "2,3,4"},
		"window and level":       {&opsv1.GetClientDebugRequest{FromMs: 2000, ToMs: 4000, MinLevel: "warn"}, "2,3"},
		"empty window":           {&opsv1.GetClientDebugRequest{FromMs: 4001, ToMs: 4999}, ""},
		"message":                {&opsv1.GetClientDebugRequest{Query: "HANDSHAKE"}, "2"},
		"attribute value":        {&opsv1.GetClientDebugRequest{Query: "pth_q1w2"}, "1"},
		"path as stored":         {&opsv1.GetClientDebugRequest{Query: "state.json"}, "3"},
		"the cleaned text":       {&opsv1.GetClientDebugRequest{Query: "<secret>"}, "4"},
		"a removed secret":       {&opsv1.GetClientDebugRequest{Query: lateSecret}, ""},
		"source is not searched": {&opsv1.GetClientDebugRequest{Query: "service", FromMs: 4000}, ""},
	} {
		if got := seqList(get(c.req).Records); got != c.want {
			t.Errorf("%s: records %q, want %q", name, got, c.want)
		}
	}
	page := get(&opsv1.GetClientDebugRequest{Limit: 2})
	if seqList(page.Records) != "1,2" || page.NextCursor != 2 {
		t.Fatalf("page 1 = %s cursor %d", seqList(page.Records), page.NextCursor)
	}
	page = get(&opsv1.GetClientDebugRequest{Limit: 2, AfterSeq: page.NextCursor})
	if seqList(page.Records) != "3,4" || page.NextCursor != 4 {
		t.Fatalf("page 2 = %s cursor %d", seqList(page.Records), page.NextCursor)
	}
	page = get(&opsv1.GetClientDebugRequest{Limit: 2, AfterSeq: page.NextCursor})
	if seqList(page.Records) != "5" || page.NextCursor != 0 {
		t.Fatalf("page 3 = %s cursor %d", seqList(page.Records), page.NextCursor)
	}
	for name, req := range map[string]*opsv1.GetClientDebugRequest{
		"level":      {Id: id, MinLevel: "loud"},
		"long query": {Id: id, Query: strings.Repeat("x", 101)},
		"from":       {Id: id, FromMs: -1},
	} {
		if _, err := e.cl.GetClientDebug(context.Background(), connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A search that has to look through many pages for a rare record finds it, and one past the page limit continues from the cursor.
func TestGetClientDebugSearchLooksThroughManyPages(t *testing.T) {
	e := newEnv(t)
	var lines []store.ClientDebugLine
	for i := 1; i <= 1300; i++ {
		msg := fmt.Sprintf("tick %d", i)
		if i == 1100 || i == 1250 {
			msg = fmt.Sprintf("rare event %d", i)
		}
		lines = append(lines, store.ClientDebugLine{AtMs: int64(i), Level: 1, Source: "service", Message: msg})
	}
	id := seedBundle(t, e, "usr_alice", "dev_alice", lines)
	r := getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id, Query: "rare", Limit: 1})
	if seqList(r.Records) != "1100" || r.NextCursor != 1100 {
		t.Fatalf("first match = %s cursor %d", seqList(r.Records), r.NextCursor)
	}
	r = getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id, Query: "rare", Limit: 1, AfterSeq: r.NextCursor})
	if seqList(r.Records) != "1250" || r.NextCursor != 0 {
		t.Fatalf("second match = %s cursor %d", seqList(r.Records), r.NextCursor)
	}
	if r := getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id, FromMs: 1290}); len(r.Records) != 11 {
		t.Errorf("window at the end holds %d records", len(r.Records))
	}
}

// The first read of a bundle is audit-logged, once; the next pages, filters and searches are not, nor is a read by another call.
func TestGetClientDebugAuditsOnlyTheFirstRead(t *testing.T) {
	e := newEnv(t)
	id := seedBundle(t, e, "usr_alice", "dev_alice", aliceLines())
	other := seedBundle(t, e, "usr_bob", "dev_bob", aliceLines())
	reads := func() int { return countRows(t, e, `SELECT COUNT(*) FROM audit WHERE action = 'client_debug_read'`) }
	if reads() != 0 {
		t.Fatal("an audit row before any read")
	}
	list := func() *opsv1.ClientDebugInfo {
		l, err := e.cl.ListClientDebug(context.Background(), connect.NewRequest(&opsv1.ListClientDebugRequest{UserId: "usr_alice"}))
		if err != nil || len(l.Msg.Items) != 1 {
			t.Fatalf("list: %v %v", l, err)
		}
		return l.Msg.Items[0]
	}
	if list().ReadUnix != 0 {
		t.Error("read_unix set before a read")
	}
	first := getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id, Limit: 2})
	if first.Bundle.ReadUnix == 0 || list().ReadUnix == 0 {
		t.Errorf("read_unix after the first read: %d, list %d", first.Bundle.ReadUnix, list().ReadUnix)
	}
	getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id, AfterSeq: 2})
	getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id, Query: "plain"})
	getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id, MinLevel: "error"})
	if reads() != 1 {
		t.Errorf("audit rows for the reads of one bundle = %d, want 1", reads())
	}
	var params string
	if err := e.st.R.QueryRow(`SELECT params FROM audit WHERE action = 'client_debug_read'`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(params, id) || !strings.Contains(params, "usr_alice") || !strings.Contains(params, "dev_alice") || strings.Contains(params, "state.json") {
		t.Errorf("audit params = %s", params)
	}
	getBundle(t, e, &opsv1.GetClientDebugRequest{Id: other})
	if reads() != 2 {
		t.Errorf("another bundle's first read: %d audit rows, want 2", reads())
	}
}

// Two reads at once: only the one that wrote read_at writes the audit row.
func TestMarkClientDebugReadIsOnceForTwoCallers(t *testing.T) {
	e := newEnv(t)
	id := seedBundle(t, e, "usr_alice", "dev_alice", aliceLines())
	ctx := context.Background()
	first, err := e.st.MarkClientDebugRead(ctx, id, "adm_1", time.Now())
	second, err2 := e.st.MarkClientDebugRead(ctx, id, "adm_2", time.Now())
	if err != nil || err2 != nil || !first || second {
		t.Fatalf("first %v/%v second %v/%v", first, err, second, err2)
	}
	var by string
	if err := e.st.R.QueryRow(`SELECT read_by FROM client_debug WHERE id = ?`, id).Scan(&by); err != nil || by != "adm_1" {
		t.Errorf("read_by = %q, %v", by, err)
	}
}

// What a request holds is told apart: an open one has no logs, an unknown id and a node capture's id are not found.
func TestGetClientDebugErrorsAndTheTwoKindsOfLinesDoNotMeet(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := seedBundle(t, e, "usr_alice", "dev_alice", aliceLines())
	seedCapture(t, e, "req_c1", "done")
	seedKeyedDevice(t, e, "usr_bob", "dev_bob", true)
	open, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{DeviceId: "dev_bob"}))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		id   string
		want connect.Code
	}{
		"unknown":            {"dbr_missing", connect.CodeNotFound},
		"empty":              {"", connect.CodeNotFound},
		"a node capture":     {"req_c1", connect.CodeNotFound},
		"an open request":    {open.Msg.Request.Id, connect.CodeFailedPrecondition},
		"export, a capture":  {"req_c1", connect.CodeNotFound},
		"export, an open id": {open.Msg.Request.Id, connect.CodeFailedPrecondition},
	} {
		var err error
		if strings.HasPrefix(name, "export") {
			var s *connect.ServerStreamForClient[opsv1.ExportClientDebugResponse]
			if s, err = e.cl.ExportClientDebug(ctx, connect.NewRequest(&opsv1.ExportClientDebugRequest{Id: c.id})); err == nil {
				for s.Receive() {
				}
				err = s.Err()
			}
		} else {
			_, err = e.cl.GetClientDebug(ctx, connect.NewRequest(&opsv1.GetClientDebugRequest{Id: c.id}))
		}
		if connect.CodeOf(err) != c.want {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if n := countRows(t, e, `SELECT COUNT(*) FROM audit WHERE action IN ('client_debug_read', 'client_debug_downloaded')`); n != 0 {
		t.Errorf("%d audit rows for reads that returned nothing", n)
	}
	// the node's own read sees the node's five lines and none of the bundle's, whatever the seqs
	r, err := e.cl.GetDebugCapture(ctx, connect.NewRequest(&opsv1.GetDebugCaptureRequest{CaptureId: "req_c1"}))
	if err != nil || len(r.Msg.Records) != 5 {
		t.Fatalf("node capture read: %v %v", r, err)
	}
	for _, rec := range r.Msg.Records {
		if strings.Contains(rec.Message, "handshake timed out") || strings.Contains(rec.Message, "plain note") {
			t.Errorf("a record of a person's bundle in a node capture: %+v", rec)
		}
	}
	if b := getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id}); len(b.Records) != 5 {
		t.Errorf("bundle read: %d records", len(b.Records))
	}
	for _, rec := range getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id}).Records {
		if strings.Contains(rec.Message, "canary") || strings.Contains(rec.Message, "boom") {
			t.Errorf("a record of a node capture in a bundle: %+v", rec)
		}
	}
}

func exportBundle(t *testing.T, e *env, id string) (string, error) {
	t.Helper()
	s, err := e.cl.ExportClientDebug(context.Background(), connect.NewRequest(&opsv1.ExportClientDebugRequest{Id: id}))
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	for s.Receive() {
		out.Write(s.Msg().Data)
	}
	return out.String(), s.Err()
}

// The download is the same output as the read, as NDJSON: not HTML-escaped, secrets cut out, only the contract's attribute keys, one
// audit row each time, and it needs a fresh sign-in.
func TestExportClientDebugNeedsStepUpIsAuditedAndHoldsTheSameRecords(t *testing.T) {
	stepUp := errors.New("step-up required")
	var denied atomic.Bool
	denied.Store(true)
	e := newEnv(t, func(c *Config) {
		c.Secrets = append(c.Secrets, lateSecret)
		c.StepUp = func(context.Context) error {
			if denied.Load() {
				return connect.NewError(connect.CodePermissionDenied, stepUp)
			}
			return nil
		}
	})
	id := seedBundle(t, e, "usr_alice", "dev_alice", aliceLines())
	downloads := func() int {
		return countRows(t, e, `SELECT COUNT(*) FROM audit WHERE action = 'client_debug_downloaded'`)
	}
	if _, err := exportBundle(t, e, id); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("export without a fresh sign-in: %v", err)
	}
	if downloads() != 0 {
		t.Error("an audit row for a refused download")
	}
	denied.Store(false)
	out, err := exportBundle(t, e, id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(out), lateSecret) {
		t.Errorf("download:\n%s", out)
	}
	if !strings.Contains(out, `"msg":"cannot write %USERPROFILE%\\AppData\\Roaming\\Mistgate\\state.json"`) || !strings.Contains(out, `<secret>`) {
		t.Errorf("download lacks the stored text:\n%s", out)
	}
	read := getBundle(t, e, &opsv1.GetClientDebugRequest{Id: id})
	sc := bufio.NewScanner(strings.NewReader(out))
	n := 0
	for sc.Scan() {
		var line exportedLine
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&line); err != nil {
			t.Fatalf("line %d: %v: %s", n+1, err, sc.Text())
		}
		rec := read.Records[n]
		if line.Seq != rec.Seq || line.Ts != rec.TimeUnixMs || line.Lvl != rec.Level || line.Src != rec.Source || line.Msg != rec.Message || fmt.Sprint(line.KV) != fmt.Sprint(rec.Attrs) {
			t.Errorf("line %d = %+v, the read has %+v", n+1, line, rec)
		}
		n++
	}
	if n != 5 {
		t.Errorf("download holds %d lines, want 5", n)
	}
	if _, err := exportBundle(t, e, id); err != nil {
		t.Fatal(err)
	}
	if downloads() != 2 {
		t.Errorf("audit rows for two downloads = %d", downloads())
	}
	var params string
	if err := e.st.R.QueryRow(`SELECT params FROM audit WHERE action = 'client_debug_downloaded'`).Scan(&params); err != nil || !strings.Contains(params, id) || strings.Contains(params, "state.json") {
		t.Errorf("audit params = %s, %v", params, err)
	}
}

// A bundle of more than one page is downloaded whole, in order.
func TestExportClientDebugSendsEveryPage(t *testing.T) {
	e := newEnv(t)
	var lines []store.ClientDebugLine
	for i := 1; i <= 1234; i++ {
		lines = append(lines, store.ClientDebugLine{AtMs: int64(i), Level: 1, Source: "service", Message: fmt.Sprintf("line %d", i)})
	}
	id := seedBundle(t, e, "usr_alice", "dev_alice", lines)
	out, err := exportBundle(t, e, id)
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(rows) != 1234 || !strings.HasPrefix(rows[0], `{"seq":1,`) || !strings.HasPrefix(rows[1233], `{"seq":1234,`) {
		t.Errorf("%d lines, first %.40s", len(rows), rows[0])
	}
}

func TestDeleteClientDebug(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := seedBundle(t, e, "usr_alice", "dev_alice", aliceLines())
	other := seedBundle(t, e, "usr_bob", "dev_bob", aliceLines())
	seedCapture(t, e, "req_c1", "done")
	lineRows := func(id string) int {
		return countRows(t, e, `SELECT COUNT(*) FROM debug_capture_line WHERE capture_id = '`+id+`'`)
	}
	del := func(id string) error {
		_, err := e.cl.DeleteClientDebug(ctx, connect.NewRequest(&opsv1.DeleteClientDebugRequest{Id: id}))
		return err
	}
	// an open request is cancelled, not deleted
	seedKeyedDevice(t, e, "usr_carol", "dev_carol", true)
	open, err := e.cl.RequestClientDebug(ctx, connect.NewRequest(&opsv1.RequestClientDebugRequest{DeviceId: "dev_carol"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := del(open.Msg.Request.Id); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("delete of an open request: %v", err)
	}
	if err := del("dbr_missing"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("delete of an unknown id: %v", err)
	}
	if err := del("req_c1"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("delete of a node capture: %v", err)
	}
	if lineRows("req_c1") != 5 {
		t.Error("the node capture lost its lines")
	}
	// a bundle goes with its lines, and only its own
	if lineRows(id) != 5 {
		t.Fatalf("seeded lines = %d", lineRows(id))
	}
	if err := del(id); err != nil {
		t.Fatal(err)
	}
	if lineRows(id) != 0 || countRows(t, e, `SELECT COUNT(*) FROM client_debug WHERE id = '`+id+`'`) != 0 {
		t.Error("the bundle or its lines are still there")
	}
	if lineRows(other) != 5 || lineRows("req_c1") != 5 {
		t.Error("another bundle's or a node capture's lines went")
	}
	if _, err := e.cl.GetClientDebug(ctx, connect.NewRequest(&opsv1.GetClientDebugRequest{Id: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("read after the delete: %v", err)
	}
	if err := del(id); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("second delete: %v", err)
	}
	var params string
	if err := e.st.R.QueryRow(`SELECT params FROM audit WHERE action = 'client_debug_deleted'`).Scan(&params); err != nil || !strings.Contains(params, id) || !strings.Contains(params, `"records":"5"`) {
		t.Errorf("audit params = %s, %v", params, err)
	}
	// a closed request (cancelled, or one that ran out) can be deleted
	if _, err := e.cl.CancelClientDebug(ctx, connect.NewRequest(&opsv1.CancelClientDebugRequest{Id: open.Msg.Request.Id})); err != nil {
		t.Fatal(err)
	}
	if err := del(open.Msg.Request.Id); err != nil {
		t.Errorf("delete of a cancelled request: %v", err)
	}
	late := store.NewClientDebugID()
	if _, err := e.st.W.ExecContext(ctx, `INSERT INTO client_debug (id, user_id, device_id, actor, requested_at, expires_at, state) VALUES (?, 'usr_carol', 'dev_carol', 'adm_1', 1, 2, 'open')`, late); err != nil {
		t.Fatal(err)
	}
	if err := del(late); err != nil {
		t.Errorf("delete of a request that ran out: %v", err)
	}
}
