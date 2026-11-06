//go:build !js

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"filippo.io/age"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestDownloadStreamsRecipientArchiveAndRemovesStage(t *testing.T) {
	s, _, identity := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	w := httptest.NewRecorder()
	request := downloadRequestFor("owner", store.RoleOwner, `{"passphrase":"ignored"}`)
	s.download(w, request)

	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	filename := w.Header().Get("X-Mistgate-Filename")
	if filename == "" || !strings.HasPrefix(filename, "mistgate-backup-mistgate-") || !strings.HasSuffix(filename, ".tar.gz.age") {
		t.Fatalf("download filename header = %q", filename)
	}
	_, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || params["filename"] != filename {
		t.Fatalf("Content-Disposition = %q, params %v, err %v", w.Header().Get("Content-Disposition"), params, err)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Mistgate-Filename") {
		t.Errorf("download filename is not exposed to fetch: %q", got)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := RestoreEncryptedArchive(context.Background(), strings.NewReader(w.Body.String()), []age.Identity{identity}, restored); err != nil {
		t.Fatalf("restore streamed response: %v", err)
	}
	assertDownloadAudit(t, s, filename, int64(w.Body.Len()), "recipient", "ignored")
	assertNoDownloadStage(t, s.dataDir)
}

func TestDownloadUsesScryptWithoutRecoveryRecipient(t *testing.T) {
	s, _, _ := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	settings, err := s.st.PanelBackupSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.AgeRecipient = ""
	if err := s.st.SavePanelBackupSettings(context.Background(), settings, time.Now()); err != nil {
		t.Fatal(err)
	}
	const passphrase = "long enough passphrase"
	w := httptest.NewRecorder()
	s.download(w, downloadRequestFor("owner", store.RoleOwner, `{"passphrase":"long enough passphrase"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d: %s", w.Code, w.Body.String())
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := RestoreEncryptedArchive(context.Background(), strings.NewReader(w.Body.String()), []age.Identity{identity}, restored); err != nil {
		t.Fatalf("restore passphrase download: %v", err)
	}
	assertDownloadAudit(t, s, w.Header().Get("X-Mistgate-Filename"), int64(w.Body.Len()), "passphrase", passphrase)
	assertNoDownloadStage(t, s.dataDir)
}

func TestDownloadRemovesStageWhenRequestIsCancelledMidStream(t *testing.T) {
	s, _, _ := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := downloadRequestFor("owner", store.RoleOwner, `{}`).WithContext(auth.WithAdmin(ctx, store.Admin{ID: "owner", Role: store.RoleOwner}))
	w := &cancelAfterFirstWrite{ResponseRecorder: httptest.NewRecorder(), ctx: ctx, cancel: cancel}
	s.download(w, request)
	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Fatalf("cancelled stream status/body = %d/%d bytes", w.Code, w.Body.Len())
	}
	assertNoDownloadStage(t, s.dataDir)
}

func TestDownloadReturnsBusyWhileAnotherDownloadStreams(t *testing.T) {
	s, _, _ := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	blocked := &blockingResponseWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		s.download(blocked, downloadRequestFor("owner", store.RoleOwner, `{}`))
		close(finished)
	}()
	select {
	case <-blocked.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first download did not start streaming")
	}
	busy := httptest.NewRecorder()
	s.download(busy, downloadRequestFor("owner", store.RoleOwner, `{}`))
	if busy.Code != http.StatusConflict || !strings.Contains(busy.Body.String(), `"code":"backup_busy"`) {
		t.Fatalf("concurrent download = %d %s", busy.Code, busy.Body.String())
	}
	close(blocked.release)
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the first download did not finish after release")
	}
	assertNoDownloadStage(t, s.dataDir)
}

func TestDownloadRequiresOwnerStepUpAndPassphraseWhenNeeded(t *testing.T) {
	s, _, _ := newTestService(t)
	s.stepUp = func(context.Context) error { return errors.New("confirm it is you first (step-up required)") }
	stepUp := httptest.NewRecorder()
	s.download(stepUp, downloadRequestFor("owner", store.RoleOwner, `{}`))
	// Connect's error JSON, as the RPCs answer it, so the admin's step-up guard handles it
	if stepUp.Code != http.StatusForbidden || !strings.Contains(stepUp.Body.String(), `"code":"permission_denied"`) {
		t.Errorf("download without step-up = %d %s", stepUp.Code, stepUp.Body.String())
	}

	s.stepUp = func(context.Context) error { return nil }
	nonOwner := httptest.NewRecorder()
	s.download(nonOwner, downloadRequestFor("helper", store.RoleHelper, `{}`))
	if nonOwner.Code != http.StatusForbidden || !strings.Contains(nonOwner.Body.String(), `"code":"owner_required"`) {
		t.Errorf("non-owner download = %d %s", nonOwner.Code, nonOwner.Body.String())
	}
	token := httptest.NewRecorder()
	s.download(token, downloadRequestFor("token:example", store.RoleOwner, `{}`))
	if token.Code != http.StatusForbidden || !strings.Contains(token.Body.String(), `"code":"owner_required"`) {
		t.Errorf("token download = %d %s", token.Code, token.Body.String())
	}

	settings, err := s.st.PanelBackupSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.AgeRecipient = ""
	if err := s.st.SavePanelBackupSettings(context.Background(), settings, time.Now()); err != nil {
		t.Fatal(err)
	}
	short := httptest.NewRecorder()
	s.download(short, downloadRequestFor("owner", store.RoleOwner, `{"passphrase":"too-short"}`))
	if short.Code != http.StatusBadRequest || !strings.Contains(short.Body.String(), `"code":"passphrase_too_short"`) {
		t.Errorf("short passphrase download = %d %s", short.Code, short.Body.String())
	}
	s.edge = true
	edge := httptest.NewRecorder()
	s.download(edge, downloadRequestFor("owner", store.RoleOwner, `{}`))
	if edge.Code != http.StatusNotImplemented || !strings.Contains(edge.Body.String(), `"code":"unavailable_in_edition"`) {
		t.Errorf("edge download = %d %s", edge.Code, edge.Body.String())
	}
	assertNoDownloadStage(t, s.dataDir)
}

func TestDownloadFilenameSanitizesBrandHostAndDevVersion(t *testing.T) {
	created := time.Date(2026, time.November, 6, 14, 37, 52, 0, time.FixedZone("test", 3*60*60))
	if got, want := downloadFilename(" ΩDD.Brand__!! ", "ignored.example", "0.1.0-dev", created), "mistgate-backup-dd-brand-0.1.0-dev-2026-11-06-1137.tar.gz.age"; got != want {
		t.Errorf("odd brand filename = %q, want %q", got, want)
	}
	if got, want := downloadFilename("", "Admin.Example", "0.1.0-dev", created), "mistgate-backup-admin-example-0.1.0-dev-2026-11-06-1137.tar.gz.age"; got != want {
		t.Errorf("empty brand filename = %q, want %q", got, want)
	}
	if got, want := downloadFilename("___", "", "dev", created), "mistgate-backup-panel-dev-2026-11-06-1137.tar.gz.age"; got != want {
		t.Errorf("empty panel fallback filename = %q, want %q", got, want)
	}
	if got := sanitizeFilenamePart(buildinfo.Version, true); got == "" {
		t.Error("the running dev version should have a filename part")
	}
}

func downloadRequestFor(id, role, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, downloadRoute, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	ctx := auth.WithAdmin(request.Context(), store.Admin{ID: id, Role: role, DisplayName: "Owner"})
	return request.WithContext(ctx)
}

func assertNoDownloadStage(t *testing.T, dataDir string) {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagePrefix) {
			t.Errorf("download stage remains after request: %s", entry.Name())
		}
	}
}

func assertDownloadAudit(t *testing.T, s *Service, filename string, size int64, encryption, passphrase string) {
	t.Helper()
	rows, err := s.st.ListAudit(context.Background(), "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Action != "backup.download" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(row.Params), &fields); err != nil {
			t.Fatalf("download audit params %q: %v", row.Params, err)
		}
		if fields["filename"] != filename || fields["encryption"] != encryption || fields["completed"] != true || fields["size_bytes"] != float64(size) || row.Result != "ok" {
			t.Errorf("download audit fields = %v", fields)
		}
		if passphrase != "" && strings.Contains(row.Params, passphrase) {
			t.Error("audit event contains the passphrase")
		}
		return
	}
	t.Fatal("backup.download audit event was not recorded")
}

type cancelAfterFirstWrite struct {
	*httptest.ResponseRecorder
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (w *cancelAfterFirstWrite) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.ResponseRecorder.Write(data)
	w.once.Do(w.cancel)
	return n, err
}

type blockingResponseWriter struct {
	*httptest.ResponseRecorder
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingResponseWriter) Write(data []byte) (int, error) {
	w.once.Do(func() {
		close(w.started)
		<-w.release
	})
	return w.ResponseRecorder.Write(data)
}

// The step-up refusal is the RPCs' own: PERMISSION_DENIED with the StepUpRequired detail, which the admin's guard reads.
func TestDownloadStepUpRefusalCarriesTheDetail(t *testing.T) {
	s, _, _ := newTestService(t)
	s.stepUp = func(context.Context) error {
		e := connect.NewError(connect.CodePermissionDenied, errors.New("confirm it is you first (step-up required)"))
		if d, err := connect.NewErrorDetail(&adminv1.StepUpRequired{Passkey: true, Totp: true}); err == nil {
			e.AddDetail(d)
		}
		return e
	}
	rec := httptest.NewRecorder()
	s.download(rec, downloadRequestFor("owner", store.RoleOwner, `{}`))
	body := rec.Body.String()
	if rec.Code != http.StatusForbidden || !strings.Contains(body, `"code":"permission_denied"`) || !strings.Contains(body, "mistgate.admin.v1.StepUpRequired") {
		t.Errorf("step-up refusal = %d %s", rec.Code, body)
	}
}
