//go:build !js

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/update"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func ownerContext(id, role string) context.Context {
	return auth.WithAdmin(context.Background(), store.Admin{ID: id, Role: role, DisplayName: "Owner"})
}

func restoreArchiveFixture(t *testing.T, s *Service, recipient age.Recipient, version string, created time.Time) []byte {
	t.Helper()
	ctx := context.Background()
	seedRestoreCounts(t, s.st, s.vault)
	head, tail := "North", "star"
	if _, err := instance.Update(ctx, s.st, instance.Patch{BrandHead: &head, BrandTail: &tail}); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.st.SnapshotDatabase(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := CreateEncryptedArchiveWithRecipient(ctx, s.dataDir, snapshot, s.masterKey, version, recipient, created, &archive); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DELETE FROM user`, `DELETE FROM user_group`, `DELETE FROM profile`, `DELETE FROM node`} {
		if _, err := s.st.W.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_current', 'Current', 'now.example', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func seedRestoreCounts(t *testing.T, st *store.Store, v *vault.Vault) {
	t.Helper()
	queries := []string{
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_backup', 'Backup', 'backup.example', 'active', 1)`,
		`INSERT INTO user_group (id, name, created_at) VALUES ('grp_backup', 'Backup group', 1)`,
		`INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_backup', 'hysteria2', 'Backup profile', '{}', 1, 1)`,
	}
	for _, query := range queries {
		if _, err := st.W.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.W.ExecContext(context.Background(), `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"usr_backup", "Backup user", "grp_backup", 1, []byte{1}, v.Seal([]byte("backup-token"), "usr_backup"), 1); err != nil {
		t.Fatal(err)
	}
}

func uploadRestoreArchive(t *testing.T, s *Service, archive []byte, fileName string) string {
	t.Helper()
	s.stepUp = func(context.Context) error { return nil }
	request := httptest.NewRequest(http.MethodPost, auth.BackupUploadProcedure, bytes.NewReader(archive))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Mistgate-Filename", fileName)
	request = request.WithContext(ownerContext("owner", store.RoleOwner))
	recorder := httptest.NewRecorder()
	s.download(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("archive upload = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.UploadID == "" {
		t.Fatalf("upload response %q: %v", recorder.Body.String(), err)
	}
	return response.UploadID
}

type blockingUploadReader struct {
	data    []byte
	started chan struct{}
	release chan struct{}
	once    sync.Once
	sent    bool
}

func (r *blockingUploadReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	return copy(p, r.data), nil
}

func TestRestoreArchiveWorkAllowsOnlyOneUploadAndPreparation(t *testing.T) {
	s, _, identity := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	reader := &blockingUploadReader{data: []byte("archive bytes"), started: make(chan struct{}), release: make(chan struct{})}
	first := httptest.NewRequest(http.MethodPost, auth.BackupUploadProcedure, reader)
	first.ContentLength = -1
	first.Header.Set("Content-Type", "application/octet-stream")
	first = first.WithContext(ownerContext("owner", store.RoleOwner))
	firstResponse := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.upload(firstResponse, first)
		close(done)
	}()
	<-reader.started
	second := httptest.NewRequest(http.MethodPost, auth.BackupUploadProcedure, strings.NewReader("second archive"))
	second.Header.Set("Content-Type", "application/octet-stream")
	second = second.WithContext(ownerContext("owner", store.RoleOwner))
	secondResponse := httptest.NewRecorder()
	s.upload(secondResponse, second)
	if secondResponse.Code != http.StatusConflict || !strings.Contains(secondResponse.Body.String(), "an archive transfer or restore preparation is already running") {
		t.Fatalf("parallel upload = %d %s", secondResponse.Code, secondResponse.Body.String())
	}
	_, err := (rpc{s}).PrepareRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.PrepareRestoreRequest{
		R2ObjectKey: "mistgate/backups/v1/backup.tar.gz.age", IdentityFile: []byte(identity.String()),
	}))
	if connect.CodeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), "an archive transfer or restore preparation is already running") {
		t.Fatalf("parallel R2 prepare = %v", err)
	}
	close(reader.release)
	<-done
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first upload = %d %s", firstResponse.Code, firstResponse.Body.String())
	}
}

func TestRestoreStageBudgetIncludesRetainedUploadAndRestoreStages(t *testing.T) {
	dataDir := t.TempDir()
	for _, stageName := range []string{uploadStagePrefix + "retained", restoreStagePrefix + strings.Repeat("a", 32)} {
		stage := filepath.Join(dataDir, stageName)
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "archive-or-data"), []byte("1234"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if allowed, err := restoreStageBudgetAllows(dataDir, 3, 10); err != nil || allowed {
		t.Fatalf("aggregate stage budget allowed 8 retained + 3 reserved bytes: %v, %v", allowed, err)
	}
	if allowed, err := restoreStageBudgetAllows(dataDir, 2, 10); err != nil || !allowed {
		t.Fatalf("aggregate stage budget rejected 8 retained + 2 reserved bytes: %v, %v", allowed, err)
	}
}

func prepareUploadedRestore(t *testing.T, s *Service, archive []byte, fileName string, identity []byte, passphrase string) *adminv1.PrepareRestoreResponse {
	t.Helper()
	response, err := (rpc{s}).PrepareRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.PrepareRestoreRequest{
		UploadId: uploadRestoreArchive(t, s, archive, fileName), IdentityFile: identity, Passphrase: passphrase,
	}))
	if err != nil {
		t.Fatalf("prepare restore: %v", err)
	}
	return response.Msg
}

func assertNoRestoreStages(t *testing.T, dataDir string) {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), restoreStagePrefix) || strings.HasPrefix(entry.Name(), uploadStagePrefix) {
			t.Errorf("restore or upload stage remains: %s", entry.Name())
		}
	}
}

func TestPrepareRestoreFromUploadWithRecipientAndPassphrase(t *testing.T) {
	created := time.Date(2026, time.November, 5, 9, 7, 6, 0, time.FixedZone("test", 3*60*60))
	for _, tc := range []struct {
		name       string
		passphrase string
	}{{name: "recipient"}, {name: "passphrase", passphrase: "restore-only passphrase 62f01e"}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, identity := newTestService(t)
			var recipient age.Recipient
			var identityFile []byte
			if tc.passphrase == "" {
				recipient, identityFile = identity.Recipient(), []byte(identity.String())
			} else {
				r, err := age.NewScryptRecipient(tc.passphrase)
				if err != nil {
					t.Fatal(err)
				}
				recipient = r
			}
			archive := restoreArchiveFixture(t, s, recipient, "v2.3.4", created)
			response := prepareUploadedRestore(t, s, archive, "backup-name.tar.gz.age", identityFile, tc.passphrase)
			if response.Summary.PanelName != "Northstar" || response.Summary.MadeByVersion != "v2.3.4" || response.Summary.CreatedUnix != created.Unix() ||
				response.Summary.BackupNodes != 1 || response.Summary.BackupUsers != 1 || response.Summary.BackupProfiles != 1 ||
				response.Summary.CurrentNodes != 1 || response.Summary.CurrentUsers != 0 || response.Summary.CurrentProfiles != 0 {
				t.Fatalf("restore summary = %+v", response.Summary)
			}
			s.restore.mu.Lock()
			record := s.restore.restores[response.RestoreId]
			s.restore.mu.Unlock()
			if response.RestoreId == "" || record.stageDir == "" {
				t.Fatal("prepared restore stage was not registered")
			}
			t.Cleanup(func() { _ = os.RemoveAll(record.stageDir) })
		})
	}
}

func TestPrepareRestoreFromR2UsesTheConfiguredFakeClient(t *testing.T) {
	s, api, identity := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	archive := restoreArchiveFixture(t, s, identity.Recipient(), "v1.2.3", time.Now().UTC())
	api.lastPut = archive
	response, err := (rpc{s}).PrepareRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.PrepareRestoreRequest{
		R2ObjectKey: "mistgate/backups/v1/backup.tar.gz.age", IdentityFile: []byte(identity.String()),
	}))
	if err != nil || response.Msg.Summary.BackupNodes != 1 || api.getCalls != 1 {
		t.Fatalf("R2 prepare = %+v, gets %d, err %v", response, api.getCalls, err)
	}
	s.restore.mu.Lock()
	record := s.restore.restores[response.Msg.RestoreId]
	s.restore.mu.Unlock()
	if record.stageDir == "" {
		t.Fatal("R2 prepare did not retain the verified stage")
	}
	t.Cleanup(func() { _ = os.RemoveAll(record.stageDir) })
	for _, entry := range mustReadDir(t, s.dataDir) {
		if strings.HasPrefix(entry.Name(), uploadStagePrefix) {
			t.Errorf("R2 download stage remains: %s", entry.Name())
		}
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestPrepareRestoreRefusesWrongKeyTamperedAndTooNewArchivesWithoutStages(t *testing.T) {
	s, _, identity := newTestService(t)
	archive := restoreArchiveFixture(t, s, identity.Recipient(), "v1.2.3", time.Now().UTC())
	wrongIdentity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		archive  []byte
		identity string
	}{{"wrong identity", archive, wrongIdentity.String()}, {"tampered archive", flipArchiveByte(archive), identity.String()}} {
		t.Run(tc.name, func(t *testing.T) {
			id := uploadRestoreArchive(t, s, tc.archive, "bad-backup.tar.gz.age")
			_, err := (rpc{s}).PrepareRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.PrepareRestoreRequest{UploadId: id, IdentityFile: []byte(tc.identity)}))
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("prepare error = %v, want failed precondition", err)
			}
			assertNoRestoreStages(t, s.dataDir)
		})
	}
	_, latest, err := store.SchemaVersions(context.Background(), filepath.Join(s.dataDir, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.W.ExecContext(context.Background(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, latest+1); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "future.db")
	if err := s.st.SnapshotDatabase(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	var tooNew bytes.Buffer
	if err := CreateEncryptedArchive(context.Background(), s.dataDir, snapshot, s.masterKey, "v9.9.9", identity.Recipient().String(), time.Now(), &tooNew); err != nil {
		t.Fatal(err)
	}
	_, err = (rpc{s}).PrepareRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.PrepareRestoreRequest{
		UploadId: uploadRestoreArchive(t, s, tooNew.Bytes(), "future.tar.gz.age"), IdentityFile: []byte(identity.String()),
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("too-new archive error = %v", err)
	}
	assertNoRestoreStages(t, s.dataDir)
}

func flipArchiveByte(source []byte) []byte {
	result := append([]byte(nil), source...)
	result[len(result)/2] ^= 0x40
	return result
}

func TestConfirmRestoreGuardsAuditAndExpiredStages(t *testing.T) {
	s, _, identity := newTestService(t)
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	archive := restoreArchiveFixture(t, s, identity.Recipient(), "v1.2.3", now)
	prepared := prepareUploadedRestore(t, s, archive, "backup.tar.gz.age", []byte(identity.String()), "")
	confirm := func(ctx context.Context, typed string) error {
		_, err := (rpc{s}).ConfirmRestore(ctx, connect.NewRequest(&adminv1.ConfirmRestoreRequest{RestoreId: prepared.RestoreId, TypedPanelName: typed}))
		return err
	}
	s.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	if err := confirm(ownerContext("owner", store.RoleOwner), "Northstar"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("confirm without step-up = %v", err)
	}
	s.stepUp = func(context.Context) error { return nil }
	if err := confirm(ownerContext("helper", store.RoleHelper), "Northstar"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("confirm by non-owner = %v", err)
	}
	if err := confirm(ownerContext("token:owner", store.RoleOwner), "Northstar"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("confirm by token = %v", err)
	}
	if err := confirm(ownerContext("owner", store.RoleOwner), "Wrong name"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("confirm with wrong typed name = %v", err)
	}
	s.mu.Lock()
	busyErr := confirm(ownerContext("owner", store.RoleOwner), "Northstar")
	s.mu.Unlock()
	if connect.CodeOf(busyErr) != connect.CodeAborted {
		t.Fatalf("confirm during a backup = %v", busyErr)
	}
	s.restore.panelData = func(_ context.Context, req RestoreRequest, beforeStart func() error) error {
		if req.Source != "upload" || req.FileName != "backup.tar.gz.age" {
			t.Errorf("restore helper request = %+v", req)
		}
		if err := beforeStart(); err != nil {
			return err
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("a node rollout is active"))
	}
	if err := confirm(ownerContext("owner", store.RoleOwner), "Northstar"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("confirm during panel-update/rollout gate = %v", err)
	}
	assertNoRestoreStages(t, s.dataDir)
	rows, err := s.st.ListAudit(context.Background(), "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	started := false
	for _, row := range rows {
		if row.Action == "backup.restore_started" {
			started = true
			if strings.Contains(row.Params, identity.String()) {
				t.Errorf("restore-start audit contains the identity: %s", row.Params)
			}
		}
	}
	if !started {
		t.Fatalf("restore-start audit row missing: %+v", rows)
	}
	if strings.Contains(logs.String(), identity.String()) {
		t.Errorf("captured log contains the recovery identity: %s", logs.String())
	}

	prepared = prepareUploadedRestore(t, s, archive, "expires.tar.gz.age", []byte(identity.String()), "")
	s.restore.mu.Lock()
	record := s.restore.restores[prepared.RestoreId]
	s.restore.mu.Unlock()
	if record.stageDir == "" {
		t.Fatal("the expiring restore was not staged")
	}
	now = now.Add(restoreStageTTL + time.Second)
	s.cleanupExpiredStages(now)
	if _, err := os.Stat(record.stageDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired restore stage remains: %v", err)
	}
}

func TestRestoreOperationLockSurvivesPanelRestartAndBlocksSecondConfirm(t *testing.T) {
	s, _, identity := newTestService(t)
	archive := restoreArchiveFixture(t, s, identity.Recipient(), "v1.2.3", time.Now().UTC())
	prepared := prepareUploadedRestore(t, s, archive, "backup.tar.gz.age", []byte(identity.String()), "")
	s.restore.mu.Lock()
	record := s.restore.restores[prepared.RestoreId]
	s.restore.mu.Unlock()
	if record.stageDir == "" {
		t.Fatal("restore stage was not prepared")
	}
	t.Cleanup(func() { _ = os.RemoveAll(record.stageDir) })

	request := record.request
	request.RequestedUnix = time.Now().UTC().Unix()
	state := update.PanelOperationState{ID: strings.Repeat("1", 32), Kind: "restore",
		StartedUnix: request.RequestedUnix, HeartbeatUnix: request.RequestedUnix, RestoreRequest: &request}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.dataDir+".mistgate-operation.lock", encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	s.stepUp = func(context.Context) error { return nil }
	_, err = (rpc{s}).ConfirmRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.ConfirmRestoreRequest{
		RestoreId: prepared.RestoreId, TypedPanelName: prepared.Summary.PanelName,
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("second confirm while helper lock is active = %v", err)
	}

	restarted, err := New(Config{Store: s.st, Vault: s.vault, DataDir: s.dataDir, MasterKey: s.masterKey,
		RestoreHelperRunning: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	restarted.stepUp = func(context.Context) error { return nil }
	if !restarted.restore.pending.Load() || restarted.restore.pendingRestore.StageDir != request.StageDir {
		t.Fatalf("restarted service did not reconstruct pending restore: pending=%v request=%+v",
			restarted.restore.pending.Load(), restarted.restore.pendingRestore)
	}
	response := httptest.NewRecorder()
	upload := httptest.NewRequest(http.MethodPost, auth.BackupUploadProcedure, strings.NewReader("archive"))
	upload.Header.Set("Content-Type", "application/octet-stream")
	restarted.download(response, upload.WithContext(ownerContext("owner", store.RoleOwner)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "already running") {
		t.Fatalf("upload after panel restart during restore = %d %s", response.Code, response.Body.String())
	}
}

func TestRestoreSecretsNeverReachLogsOrAuditAndResultIsAvailableAfterRestart(t *testing.T) {
	s, _, _ := newTestService(t)
	const secret = "this passphrase must remain request-only 20261106"
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	recipient, err := age.NewScryptRecipient(secret)
	if err != nil {
		t.Fatal(err)
	}
	archive := restoreArchiveFixture(t, s, recipient, "v3.0.1", time.Now().UTC())
	prepared := prepareUploadedRestore(t, s, archive, "safe-name.tar.gz.age", nil, secret)
	s.restore.panelData = func(_ context.Context, _ RestoreRequest, beforeStart func() error) error {
		return beforeStart()
	}
	s.stepUp = func(context.Context) error { return nil }
	if _, err := (rpc{s}).ConfirmRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.ConfirmRestoreRequest{
		RestoreId: prepared.RestoreId, TypedPanelName: prepared.Summary.PanelName,
	})); err != nil {
		t.Fatal(err)
	}
	rows, err := s.st.ListAudit(context.Background(), "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Params, secret) || strings.Contains(row.Params, "AGE-SECRET-KEY-") {
			t.Errorf("audit row %s contains recovery material", row.Action)
		}
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "AGE-SECRET-KEY-") {
		t.Errorf("captured log contains recovery material: %s", logs.String())
	}
	marker := `{"outcome":"restored","source":"upload","file_name":"safe-name.tar.gz.age","made_by_version":"v3.0.1","completed_unix":1793952000}`
	if err := os.WriteFile(filepath.Join(s.dataDir, "panel-restore.result"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (rpc{s}).GetRestoreResult(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.GetRestoreResultRequest{}))
	if err != nil || result.Msg.Outcome != "restored" || result.Msg.Source != "upload" || result.Msg.FileName != "safe-name.tar.gz.age" || result.Msg.MadeByVersion != "v3.0.1" {
		t.Fatalf("restored result = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "panel-restore.result")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("result marker was not consumed: %v", err)
	}
	rows, err = s.st.ListAudit(context.Background(), "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	restored := 0
	for _, row := range rows {
		if row.Action == "backup.restored" {
			restored++
		}
	}
	if restored != 1 {
		t.Fatalf("restored audit rows = %d", restored)
	}
}

func TestRestoreAcceptedWithoutHelperStartIsReportedAndUnlocksBackup(t *testing.T) {
	s, _, identity := newTestService(t)
	archive := restoreArchiveFixture(t, s, identity.Recipient(), "v3.0.1", time.Now().UTC())
	prepared := prepareUploadedRestore(t, s, archive, "helper-never-started.tar.gz.age", []byte(identity.String()), "")
	s.stepUp = func(context.Context) error { return nil }
	s.restore.helperRunning = func() bool { return false }
	s.restore.panelData = func(_ context.Context, _ RestoreRequest, beforeStart func() error) error {
		return beforeStart()
	}
	if _, err := (rpc{s}).ConfirmRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.ConfirmRestoreRequest{
		RestoreId: prepared.RestoreId, TypedPanelName: prepared.Summary.PanelName,
	})); err != nil {
		t.Fatal(err)
	}
	s.restore.mu.Lock()
	stageDir := s.restore.pendingRestore.StageDir
	s.restore.mu.Unlock()
	if stageDir == "" || !s.restore.pending.Load() {
		t.Fatal("accepted restore was not retained as pending")
	}
	s.restore.mu.Lock()
	request := s.restore.pendingRestore
	s.restore.mu.Unlock()
	request.RequestedUnix = time.Now().Add(-restoreHelperStartGrace - time.Minute).Unix()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dataDir, "panel-restore.request"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(Config{Store: s.st, Vault: s.vault, DataDir: s.dataDir, MasterKey: s.masterKey,
		RestoreHelperRunning: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	startupCtx, cancelStartup := context.WithCancel(context.Background())
	startupDone := make(chan struct{})
	go func() {
		restarted.Run(startupCtx)
		close(startupDone)
	}()
	deadline := time.After(2 * time.Second)
	for restarted.restore.pending.Load() {
		select {
		case <-deadline:
			cancelStartup()
			<-startupDone
			t.Fatal("startup reconciliation did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancelStartup()
	<-startupDone
	if restarted.restore.pending.Load() {
		t.Fatal("a restore whose helper never started remained pending")
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, stageDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned restore stage remains: %v", err)
	}
	result, err := (rpc{restarted}).GetRestoreResult(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.GetRestoreResultRequest{}))
	if err != nil || result.Msg.Outcome != "failed" || result.Msg.FileName != "helper-never-started.tar.gz.age" {
		t.Fatalf("helper failure result = %+v, %v", result, err)
	}
}

func TestKeptDataListsAndDeletesOnlyMatchingRestoreDirectories(t *testing.T) {
	s, _, _ := newTestService(t)
	parent, dataBase := filepath.Dir(s.dataDir), filepath.Base(s.dataDir)
	validName := dataBase + ".before-restore-20261106-123045"
	validPath := filepath.Join(parent, validName)
	if err := os.Mkdir(validPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(validPath, "old.db"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(parent, dataBase+".before-update-20261106-123045")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	response, err := (rpc{s}).ListKeptData(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.ListKeptDataRequest{}))
	if err != nil || len(response.Msg.Directories) != 1 || response.Msg.Directories[0].Name != validName || response.Msg.Directories[0].SizeBytes != 5 {
		t.Fatalf("kept-data list = %+v, %v", response, err)
	}
	s.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up required"))
	}
	if _, err := (rpc{s}).DeleteKeptData(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.DeleteKeptDataRequest{Name: validName})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("delete without step-up = %v", err)
	}
	s.stepUp = func(context.Context) error { return nil }
	s.restore.deleteKeptData = func(_ context.Context, name string) error {
		if name != validName {
			return errors.New("unexpected kept name")
		}
		return os.RemoveAll(validPath)
	}
	if _, err := (rpc{s}).DeleteKeptData(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.DeleteKeptDataRequest{Name: dataBase + ".before-update-20261106-123045"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("delete decoy = %v", err)
	}
	if _, err := (rpc{s}).DeleteKeptData(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.DeleteKeptDataRequest{Name: validName})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(validPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kept restore directory was not deleted: %v", err)
	}
	if _, err := os.Stat(decoy); err != nil {
		t.Fatalf("unrelated directory was touched: %v", err)
	}
}

func TestRestoreUploadAndRPCsRefuseMissingStepUpNonOwnersTokensAndEdge(t *testing.T) {
	s, _, identity := newTestService(t)
	s.stepUp = func(context.Context) error {
		err := connect.NewError(connect.CodePermissionDenied, errors.New("confirm it is you first"))
		if detail, detailErr := connect.NewErrorDetail(&adminv1.StepUpRequired{Passkey: true, Totp: true}); detailErr == nil {
			err.AddDetail(detail)
		}
		return err
	}
	request := httptest.NewRequest(http.MethodPost, auth.BackupUploadProcedure, strings.NewReader("archive"))
	request.Header.Set("Content-Type", "application/octet-stream")
	response := httptest.NewRecorder()
	s.download(response, request.WithContext(ownerContext("owner", store.RoleOwner)))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "mistgate.admin.v1.StepUpRequired") {
		t.Fatalf("upload without step-up = %d %s", response.Code, response.Body.String())
	}
	s.stepUp = func(context.Context) error { return nil }
	for _, tc := range []struct{ id, role string }{{"helper", store.RoleHelper}, {"token:owner", store.RoleOwner}} {
		req := httptest.NewRequest(http.MethodPost, auth.BackupUploadProcedure, strings.NewReader("archive"))
		req.Header.Set("Content-Type", "application/octet-stream")
		rec := httptest.NewRecorder()
		s.download(rec, req.WithContext(ownerContext(tc.id, tc.role)))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"permission_denied"`) {
			t.Errorf("upload as %s = %d %s", tc.id, rec.Code, rec.Body.String())
		}
	}
	prepare := connect.NewRequest(&adminv1.PrepareRestoreRequest{UploadId: strings.Repeat("a", 32), IdentityFile: []byte(identity.String())})
	if _, err := (rpc{s}).PrepareRestore(ownerContext("token:owner", store.RoleOwner), prepare); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("token PrepareRestore = %v", err)
	}
	s.edge = true
	if _, err := (rpc{s}).PrepareRestore(ownerContext("owner", store.RoleOwner), prepare); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("edge PrepareRestore = %v", err)
	}
	if _, err := (rpc{s}).ListKeptData(ownerContext("owner", store.RoleOwner), connect.NewRequest(&adminv1.ListKeptDataRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("edge ListKeptData = %v", err)
	}
	upload := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, auth.BackupUploadProcedure, strings.NewReader("archive"))
	request.Header.Set("Content-Type", "application/octet-stream")
	s.download(upload, request.WithContext(ownerContext("owner", store.RoleOwner)))
	if upload.Code != http.StatusNotImplemented || !strings.Contains(upload.Body.String(), `"code":"unimplemented"`) {
		t.Fatalf("edge upload = %d %s", upload.Code, upload.Body.String())
	}
}

func TestRestoreUploadEnforcesStreamSizeAndClearsIdentityRequestBytes(t *testing.T) {
	s, _, identity := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	request := httptest.NewRequest(http.MethodPost, auth.BackupUploadProcedure, strings.NewReader(""))
	request.ContentLength = maxEncryptedBytes + 1
	request.Header.Set("Content-Type", "application/octet-stream")
	response := httptest.NewRecorder()
	s.download(response, request.WithContext(ownerContext("owner", store.RoleOwner)))
	if response.Code != http.StatusTooManyRequests && response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload = %d %s", response.Code, response.Body.String())
	}
	archive := restoreArchiveFixture(t, s, identity.Recipient(), "v1.2.3", time.Now().UTC())
	msg := &adminv1.PrepareRestoreRequest{UploadId: uploadRestoreArchive(t, s, archive, "clear.tar.gz.age"), IdentityFile: []byte(identity.String())}
	if _, err := (rpc{s}).PrepareRestore(ownerContext("owner", store.RoleOwner), connect.NewRequest(msg)); err != nil {
		t.Fatal(err)
	}
	if len(msg.IdentityFile) != 0 || msg.Passphrase != "" {
		t.Fatal("restore key material was not cleared from the request message")
	}
}

func TestCleanupExpiredUploadStage(t *testing.T) {
	s, _, _ := newTestService(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	uploadID := uploadRestoreArchive(t, s, []byte("encrypted archive"), "expiry.tar.gz.age")
	s.restore.mu.Lock()
	record := s.restore.uploads[uploadID]
	s.restore.mu.Unlock()
	if record.stageDir == "" {
		t.Fatal("upload stage was not retained")
	}
	now = now.Add(uploadStageTTL + time.Second)
	s.cleanupExpiredStages(now)
	if _, err := os.Stat(record.stageDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired upload stage remains: %v", err)
	}
	crashedExtract := filepath.Join(s.dataDir, extractStagePrefix+"crashed")
	if err := os.Mkdir(crashedExtract, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(crashedExtract, now.Add(-restoreStageTTL-time.Second), now.Add(-restoreStageTTL-time.Second)); err != nil {
		t.Fatal(err)
	}
	s.cleanupExpiredStages(now)
	if _, err := os.Stat(crashedExtract); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired extraction stage remains: %v", err)
	}
}

func TestRestorePendingBlocksAllBackupCreation(t *testing.T) {
	s, _, _ := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	s.restore.pending.Store(true)
	if err := s.startBackup(context.Background()); !errors.Is(err, errBackupBusy) {
		t.Fatalf("manual backup during restore = %v", err)
	}
	if _, _, err := s.createBackup(context.Background()); !errors.Is(err, errBackupBusy) {
		t.Fatalf("scheduled backup during restore = %v", err)
	}
	recorder := httptest.NewRecorder()
	s.download(recorder, downloadRequestFor("owner", store.RoleOwner, `{}`))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"backup_busy"`) {
		t.Fatalf("download during restore = %d %s", recorder.Code, recorder.Body.String())
	}
}
