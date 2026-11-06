//go:build !js

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"filippo.io/age"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/update"
)

const (
	maxRestoreKeyBytes      = 64 << 10
	maxRestoreStageBytes    = maxEncryptedBytes + 2*maxPlaintextBytes
	restoreStageTTL         = 30 * time.Minute
	uploadStageTTL          = 30 * time.Minute
	restoreHelperStartGrace = 3 * time.Minute
	restoreResultKey        = "backup.restore_result"
	restoreStagePrefix      = update.PanelRestoreStagePrefix
	uploadStagePrefix       = stagePrefix + "upload-"
)

var r2ArchiveKeyPattern = regexp.MustCompile(`^mistgate/backups/v1/[A-Za-z0-9/_-]+\.tar\.gz\.age$`)

type uploadRecord struct {
	stageDir string
	fileName string
	expires  time.Time
}

type restoreRecord struct {
	stageDir string
	request  update.PanelRestoreRequest
	summary  *adminv1.RestoreSummary
	expires  time.Time
}

type dataCounts struct {
	nodes, users, profiles int64
}

func newRestoreID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRestoreConnectError(w, r, connect.CodeInvalidArgument, "use POST")
		return
	}
	if r.URL.Path != auth.BackupUploadProcedure {
		http.NotFound(w, r)
		return
	}
	if err := edition.Check(s.edge, edition.Backups); err != nil {
		writeRestoreConnectError(w, r, connect.CodeUnimplemented, "backup restore is available on the VPS edition")
		return
	}
	if err := requireOwner(r.Context()); err != nil {
		writeRestoreConnectError(w, r, connect.CodePermissionDenied, "owner access required")
		return
	}
	if err := s.requireOwnerStepUp(r.Context()); err != nil {
		writeRestoreConnectErr(w, r, err)
		return
	}
	if s.panelOperationRunning() {
		writeRestoreConnectError(w, r, connect.CodeAborted, "a panel restore is already running")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/octet-stream" {
		writeRestoreConnectError(w, r, connect.CodeInvalidArgument, "send the archive as application/octet-stream")
		return
	}
	if r.ContentLength > maxEncryptedBytes {
		writeRestoreConnectError(w, r, connect.CodeResourceExhausted, "the encrypted archive exceeds the upload size limit")
		return
	}
	if !s.tryBeginArchiveWork(maxEncryptedBytes) {
		writeRestoreConnectError(w, r, connect.CodeAborted, "an archive transfer or restore preparation is already running")
		return
	}
	defer s.endArchiveWork()
	fileName := safeUploadFileName(r.Header.Get("X-Mistgate-Filename"))
	if fileName == "" {
		fileName = "backup.tar.gz.age"
	}
	stageDir, err := os.MkdirTemp(s.dataDir, uploadStagePrefix)
	if err != nil {
		writeRestoreConnectError(w, r, connect.CodeInternal, "could not create the private upload stage")
		return
	}
	if err := os.Chmod(stageDir, 0o700); err != nil {
		_ = os.RemoveAll(stageDir)
		writeRestoreConnectError(w, r, connect.CodeInternal, "could not secure the private upload stage")
		return
	}
	s.setStageActive(stageDir, true)
	defer s.setStageActive(stageDir, false)
	archivePath := filepath.Join(stageDir, "backup.tar.gz.age")
	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		writeRestoreConnectError(w, r, connect.CodeInternal, "could not create the private upload file")
		return
	}
	n, copyErr := io.Copy(f, http.MaxBytesReader(w, r.Body, maxEncryptedBytes))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n < 1 {
		_ = os.RemoveAll(stageDir)
		var maxErr *http.MaxBytesError
		if errors.As(copyErr, &maxErr) {
			writeRestoreConnectError(w, r, connect.CodeResourceExhausted, "the encrypted archive exceeds the upload size limit")
			return
		}
		writeRestoreConnectError(w, r, connect.CodeInvalidArgument, "the archive upload was incomplete or invalid")
		return
	}
	id, err := newRestoreID()
	if err != nil {
		_ = os.RemoveAll(stageDir)
		writeRestoreConnectError(w, r, connect.CodeInternal, "could not register the private upload")
		return
	}
	s.restore.mu.Lock()
	s.restore.uploads[id] = uploadRecord{stageDir: stageDir, fileName: fileName, expires: s.now().Add(uploadStageTTL)}
	s.restore.mu.Unlock()
	s.audit(r.Context(), "backup.uploaded", map[string]any{"file_name": fileName, "size_bytes": n})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		UploadID string `json:"upload_id"`
	}{UploadID: id})
}

func (s *Service) tryBeginArchiveWork(reserve int64) bool {
	s.restore.mu.Lock()
	if s.restore.archiveWork || s.panelOperationRunning() {
		s.restore.mu.Unlock()
		return false
	}
	s.restore.archiveWork = true
	s.restore.mu.Unlock()
	withinBudget, err := restoreStageBudgetAllows(s.dataDir, reserve, maxRestoreStageBytes)
	if err != nil || !withinBudget {
		s.endArchiveWork()
		return false
	}
	return true
}

func (s *Service) endArchiveWork() {
	s.restore.mu.Lock()
	s.restore.archiveWork = false
	s.restore.mu.Unlock()
}

func safeUploadFileName(value string) string {
	value = strings.ReplaceAll(value, `\`, "/")
	value = filepath.Base(value)
	value = strings.TrimSpace(value)
	if len(value) > 255 || value == "." || value == string(filepath.Separator) || !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func writeRestoreConnectError(w http.ResponseWriter, r *http.Request, code connect.Code, message string) {
	_ = connect.NewErrorWriter().Write(w, r, connect.NewError(code, errors.New(message)))
}

func writeRestoreConnectErr(w http.ResponseWriter, r *http.Request, err error) {
	var ce *connect.Error
	if errors.As(err, &ce) {
		_ = connect.NewErrorWriter().Write(w, r, ce)
		return
	}
	writeRestoreConnectError(w, r, connect.CodePermissionDenied, "confirm it is you first")
}

func (s *Service) setStageActive(path string, active bool) {
	s.restore.mu.Lock()
	defer s.restore.mu.Unlock()
	if s.restore.activeStages == nil {
		s.restore.activeStages = map[string]bool{}
	}
	if active {
		s.restore.activeStages[path] = true
	} else {
		delete(s.restore.activeStages, path)
	}
}

func (s *Service) trySetStageActive(path string) bool {
	s.restore.mu.Lock()
	defer s.restore.mu.Unlock()
	if s.panelOperationRunning() {
		return false
	}
	if s.restore.activeStages == nil {
		s.restore.activeStages = map[string]bool{}
	}
	s.restore.activeStages[path] = true
	return true
}

func (s *Service) cleanupExpiredStages(now time.Time) {
	cutoff := now.Add(-max(restoreStageTTL, uploadStageTTL))
	var remove []string
	s.restore.mu.Lock()
	for id, record := range s.restore.uploads {
		if !now.Before(record.expires) {
			remove = append(remove, record.stageDir)
			delete(s.restore.uploads, id)
		}
	}
	for id, record := range s.restore.restores {
		if !now.Before(record.expires) {
			remove = append(remove, record.stageDir)
			delete(s.restore.restores, id)
		}
	}
	active := make(map[string]bool, len(s.restore.activeStages))
	for path := range s.restore.activeStages {
		active[path] = true
	}
	activeStageCount := len(active)
	s.restore.mu.Unlock()
	for _, path := range remove {
		_ = os.RemoveAll(path)
	}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		extractionStage := strings.HasPrefix(name, extractStagePrefix)
		if !entry.IsDir() || (!strings.HasPrefix(name, restoreStagePrefix) && !strings.HasPrefix(name, uploadStagePrefix) && !extractionStage) {
			continue
		}
		path := filepath.Join(s.dataDir, name)
		if active[path] || (extractionStage && activeStageCount > 0) {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(path)
		}
	}
}

func restoreStageBudgetAllows(dataDir string, reserve, limit int64) (bool, error) {
	if reserve < 0 || limit < 0 || reserve > limit {
		return false, nil
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return false, err
	}
	var used int64
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, restoreStagePrefix) && !strings.HasPrefix(name, uploadStagePrefix) {
			continue
		}
		path := filepath.Join(dataDir, name)
		if err := filepath.WalkDir(path, func(current string, _ os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("restore stage contains a symlink")
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit-used {
				return errors.New("restore stage exceeds the aggregate size limit")
			}
			used += info.Size()
			return nil
		}); err != nil {
			return false, err
		}
	}
	return used <= limit-reserve, nil
}

func (s *Service) prepareRestore(ctx context.Context, req *connect.Request[adminv1.PrepareRestoreRequest]) (*connect.Response[adminv1.PrepareRestoreResponse], error) {
	if req != nil && req.Msg != nil {
		defer clearIdentities(req.Msg)
	}
	if err := requireOwner(ctx); err != nil {
		return nil, err
	}
	if err := edition.Check(s.edge, edition.Backups); err != nil {
		return nil, err
	}
	if err := s.requireOwnerStepUp(ctx); err != nil {
		return nil, err
	}
	if s.panelOperationRunning() {
		return nil, connect.NewError(connect.CodeAborted, errors.New("a panel restore is already running"))
	}
	s.cleanupExpiredStages(s.now())
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("restore request is missing"))
	}
	msg := req.Msg
	sourceCount := 0
	if msg.UploadId != "" {
		sourceCount++
	}
	if msg.R2ObjectKey != "" {
		sourceCount++
	}
	keyCount := 0
	if len(msg.IdentityFile) > 0 {
		keyCount++
	}
	if msg.Passphrase != "" {
		keyCount++
	}
	if sourceCount != 1 || keyCount != 1 || len(msg.IdentityFile) > maxRestoreKeyBytes {
		clear(msg.IdentityFile)
		msg.IdentityFile = nil
		msg.Passphrase = ""
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("choose one archive source and exactly one recovery key"))
	}
	reserve := maxPlaintextBytes
	if msg.R2ObjectKey != "" {
		reserve += maxEncryptedBytes
	}
	if !s.tryBeginArchiveWork(reserve) {
		return nil, connect.NewError(connect.CodeAborted, errors.New("an archive transfer or restore preparation is already running"))
	}
	defer s.endArchiveWork()
	identities, keyErr := restoreIdentities(msg)
	if keyErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the recovery key is invalid"))
	}
	var archivePath, fileName, source string
	var cleanupArchive func()
	if msg.UploadId != "" {
		record, ok := s.takeUpload(msg.UploadId)
		if !ok {
			if s.restore.pending.Load() {
				return nil, connect.NewError(connect.CodeAborted, errors.New("a panel restore is already running"))
			}
			return nil, connect.NewError(connect.CodeNotFound, errors.New("the uploaded archive has expired or does not exist"))
		}
		defer s.setStageActive(record.stageDir, false)
		defer os.RemoveAll(record.stageDir)
		archivePath, fileName, source = filepath.Join(record.stageDir, "backup.tar.gz.age"), record.fileName, "upload"
	} else {
		path, name, err := s.downloadR2Archive(ctx, msg.R2ObjectKey)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("the R2 backup could not be downloaded"))
		}
		defer s.setStageActive(filepath.Dir(path), false)
		archivePath, fileName, source, cleanupArchive = path, name, "r2", func() { _ = os.RemoveAll(filepath.Dir(path)) }
	}
	if cleanupArchive != nil {
		defer cleanupArchive()
	}
	stageID, err := newRestoreID()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not create the private restore stage"))
	}
	stageDir := filepath.Join(s.dataDir, restoreStagePrefix+stageID)
	if !s.trySetStageActive(stageDir) {
		return nil, connect.NewError(connect.CodeAborted, errors.New("a panel restore is already running"))
	}
	defer s.setStageActive(stageDir, false)
	info, err := restoreArchiveFile(ctx, archivePath, identities, stageDir)
	clearIdentities(msg)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the archive could not be decrypted or did not pass verification"))
	}
	summary, err := s.restoreSummary(ctx, stageDir, info)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the verified archive summary could not be read"))
	}
	restoreID, err := newRestoreID()
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register the private restore stage"))
	}
	metadata := update.PanelRestoreRequest{StageDir: filepath.Base(stageDir), Source: source, FileName: fileName, MadeByVersion: info.AppVersion}
	s.restore.mu.Lock()
	s.restore.restores[restoreID] = restoreRecord{stageDir: stageDir, request: metadata, summary: summary, expires: s.now().Add(restoreStageTTL)}
	s.restore.mu.Unlock()
	return connect.NewResponse(&adminv1.PrepareRestoreResponse{RestoreId: restoreID, Summary: summary}), nil
}

func restoreIdentities(msg *adminv1.PrepareRestoreRequest) ([]age.Identity, error) {
	if len(msg.IdentityFile) != 0 {
		identities, err := age.ParseIdentities(bytes.NewReader(msg.IdentityFile))
		clear(msg.IdentityFile)
		msg.IdentityFile = nil
		if err != nil || len(identities) == 0 {
			return nil, errors.New("invalid identity")
		}
		return identities, nil
	}
	passphrase := msg.Passphrase
	msg.Passphrase = ""
	identity, err := age.NewScryptIdentity(passphrase)
	passphraseBytes := []byte(passphrase)
	clear(passphraseBytes)
	if err != nil {
		return nil, errors.New("invalid passphrase")
	}
	return []age.Identity{identity}, nil
}

func clearIdentities(msg *adminv1.PrepareRestoreRequest) {
	if msg == nil {
		return
	}
	clear(msg.IdentityFile)
	msg.IdentityFile = nil
	msg.Passphrase = ""
}

func (s *Service) takeUpload(id string) (uploadRecord, bool) {
	if len(id) != 32 {
		return uploadRecord{}, false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return uploadRecord{}, false
	}
	s.restore.mu.Lock()
	defer s.restore.mu.Unlock()
	record, ok := s.restore.uploads[id]
	if !ok || s.panelOperationRunning() {
		return uploadRecord{}, false
	}
	delete(s.restore.uploads, id)
	if !s.now().Before(record.expires) {
		_ = os.RemoveAll(record.stageDir)
		return uploadRecord{}, false
	}
	if s.restore.activeStages == nil {
		s.restore.activeStages = map[string]bool{}
	}
	s.restore.activeStages[record.stageDir] = true
	return record, true
}

func restoreArchiveFile(ctx context.Context, path string, identities []age.Identity, destination string) (ArchiveInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return ArchiveInfo{}, err
	}
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxEncryptedBytes {
		_ = f.Close()
		return ArchiveInfo{}, errors.New("invalid encrypted archive")
	}
	archiveInfo, restoreErr := RestoreEncryptedArchiveWithInfo(ctx, f, identities, destination)
	closeErr := f.Close()
	return archiveInfo, errors.Join(restoreErr, closeErr)
}

func (s *Service) downloadR2Archive(ctx context.Context, key string) (string, string, error) {
	if !r2ArchiveKeyPattern.MatchString(key) || strings.Contains(key, "..") || strings.ContainsAny(key, "\\\r\n\x00") {
		return "", "", errors.New("invalid backup object key")
	}
	settings, err := s.st.PanelBackupSettings(ctx)
	if err != nil {
		return "", "", errBackupInvalidSettings
	}
	api, err := s.r2Factory(ctx, settings)
	if err != nil {
		return "", "", err
	}
	stageDir, err := os.MkdirTemp(s.dataDir, uploadStagePrefix)
	if err != nil {
		return "", "", err
	}
	if err := os.Chmod(stageDir, 0o700); err != nil {
		_ = os.RemoveAll(stageDir)
		return "", "", err
	}
	if !s.trySetStageActive(stageDir) {
		_ = os.RemoveAll(stageDir)
		return "", "", errBackupBusy
	}
	path := filepath.Join(stageDir, "backup.tar.gz.age")
	if _, err := downloadBackup(ctx, api, settings.Bucket, key, path); err != nil {
		s.setStageActive(stageDir, false)
		_ = os.RemoveAll(stageDir)
		return "", "", err
	}
	return path, filepath.Base(key), nil
}

func (s *Service) restoreSummary(ctx context.Context, stageDir string, info ArchiveInfo) (*adminv1.RestoreSummary, error) {
	archived, err := store.Open(ctx, filepath.Join(stageDir, databaseName))
	if err != nil {
		return nil, err
	}
	defer archived.Close()
	counts, err := queryDataCounts(ctx, archived.R)
	if err != nil {
		return nil, err
	}
	brand, err := instance.Load(ctx, archived)
	if err != nil {
		return nil, err
	}
	current, err := queryDataCounts(ctx, s.st.R)
	if err != nil {
		return nil, err
	}
	name := brand.BrandName()
	if strings.TrimSpace(name) == "" {
		name = "Mistgate"
	}
	return &adminv1.RestoreSummary{
		PanelName: name, MadeByVersion: info.AppVersion, CreatedUnix: info.CreatedAt.UTC().Unix(),
		BackupNodes: counts.nodes, BackupUsers: counts.users, BackupProfiles: counts.profiles,
		CurrentNodes: current.nodes, CurrentUsers: current.users, CurrentProfiles: current.profiles,
	}, nil
}

func queryDataCounts(ctx context.Context, db *sql.DB) (dataCounts, error) {
	var counts dataCounts
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM node`).Scan(&counts.nodes); err != nil {
		return dataCounts{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM user`).Scan(&counts.users); err != nil {
		return dataCounts{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM profile`).Scan(&counts.profiles); err != nil {
		return dataCounts{}, err
	}
	return counts, nil
}

func (r rpc) PrepareRestore(ctx context.Context, req *connect.Request[adminv1.PrepareRestoreRequest]) (*connect.Response[adminv1.PrepareRestoreResponse], error) {
	return r.s.prepareRestore(ctx, req)
}

func (r rpc) ConfirmRestore(ctx context.Context, req *connect.Request[adminv1.ConfirmRestoreRequest]) (*connect.Response[adminv1.ConfirmRestoreResponse], error) {
	if err := requireOwner(ctx); err != nil {
		return nil, err
	}
	if err := edition.Check(r.s.edge, edition.Backups); err != nil {
		return nil, err
	}
	if err := r.s.requireOwnerStepUp(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil || req.Msg.RestoreId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("restore id is required"))
	}
	r.s.cleanupExpiredStages(r.s.now())
	r.s.restore.mu.Lock()
	record, ok := r.s.restore.restores[req.Msg.RestoreId]
	r.s.restore.mu.Unlock()
	if !ok || !r.s.now().Before(record.expires) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("the prepared restore has expired or does not exist"))
	}
	if strings.TrimSpace(req.Msg.TypedPanelName) != record.summary.PanelName {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the typed panel name does not match"))
	}
	if r.s.panelOperationRunning() || !r.s.mu.TryLock() {
		return nil, connect.NewError(connect.CodeAborted, errors.New("a backup or restore is already running"))
	}
	defer r.s.mu.Unlock()
	r.s.restore.mu.Lock()
	current, stillPrepared := r.s.restore.restores[req.Msg.RestoreId]
	if !stillPrepared || current.stageDir != record.stageDir || !r.s.now().Before(current.expires) {
		r.s.restore.mu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("the prepared restore has expired or does not exist"))
	}
	if r.s.panelOperationRunning() || r.s.restore.archiveWork || len(r.s.restore.activeStages) > 0 {
		r.s.restore.mu.Unlock()
		return nil, connect.NewError(connect.CodeAborted, errors.New("an archive transfer or restore preparation is already running"))
	}
	delete(r.s.restore.restores, req.Msg.RestoreId)
	if r.s.restore.activeStages == nil {
		r.s.restore.activeStages = map[string]bool{}
	}
	r.s.restore.activeStages[record.stageDir] = true
	r.s.restore.pending.Store(true)
	r.s.restore.mu.Unlock()
	restoreScheduled := false
	defer func() {
		r.s.setStageActive(record.stageDir, false)
		if !restoreScheduled {
			_ = os.RemoveAll(record.stageDir)
		}
	}()
	beforeStart := func() error {
		return r.s.auditEvent(ctx, "backup.restore_started", map[string]any{
			"source": record.request.Source, "file_name": record.request.FileName, "made_by_version": record.request.MadeByVersion,
		})
	}
	if r.s.restore.panelData == nil {
		r.s.restore.pending.Store(false)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("panel restore helper is unavailable"))
	}
	acceptedRequest := record.request
	acceptedRequest.RequestedUnix = r.s.now().UTC().Unix()
	if err := r.s.restore.panelData(context.WithoutCancel(ctx), RestoreRequest{
		StageDir: acceptedRequest.StageDir, Source: acceptedRequest.Source, FileName: acceptedRequest.FileName,
		MadeByVersion: acceptedRequest.MadeByVersion, RequestedUnix: acceptedRequest.RequestedUnix,
	}, beforeStart); err != nil {
		r.s.restore.pending.Store(false)
		return nil, restoreStartError(err)
	}
	restoreScheduled = true
	r.s.restore.mu.Lock()
	r.s.restore.pendingRestore = acceptedRequest
	r.s.restore.mu.Unlock()
	return connect.NewResponse(&adminv1.ConfirmRestoreResponse{}), nil
}

func restoreStartError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("the panel restore helper could not be started"))
}

// These two reads are owner-only without fresh step-up so the Backups page can load them every time it opens.
func (r rpc) ListKeptData(ctx context.Context, _ *connect.Request[adminv1.ListKeptDataRequest]) (*connect.Response[adminv1.ListKeptDataResponse], error) {
	if err := requireOwner(ctx); err != nil {
		return nil, err
	}
	if err := edition.Check(r.s.edge, edition.Backups); err != nil {
		return nil, err
	}
	items, err := listKeptData(filepath.Dir(r.s.dataDir), filepath.Base(r.s.dataDir))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("kept restore data could not be listed"))
	}
	return connect.NewResponse(&adminv1.ListKeptDataResponse{Directories: items}), nil
}

func (r rpc) DeleteKeptData(ctx context.Context, req *connect.Request[adminv1.DeleteKeptDataRequest]) (*connect.Response[adminv1.DeleteKeptDataResponse], error) {
	if err := requireOwner(ctx); err != nil {
		return nil, err
	}
	if err := edition.Check(r.s.edge, edition.Backups); err != nil {
		return nil, err
	}
	if err := r.s.requireOwnerStepUp(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil || req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("kept directory name is required"))
	}
	if r.s.panelOperationRunning() {
		return nil, connect.NewError(connect.CodeAborted, errors.New("a panel update or restore is already running"))
	}
	keptPath, ok := keptDataPath(filepath.Dir(r.s.dataDir), filepath.Base(r.s.dataDir), req.Msg.Name)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("only a .before-restore directory can be deleted"))
	}
	info, err := os.Lstat(keptPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("kept restore directory does not exist"))
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("kept restore path is not a real directory"))
	}
	if r.s.restore.deleteKeptData == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("kept-data deletion helper is unavailable"))
	}
	if !r.s.mu.TryLock() {
		return nil, connect.NewError(connect.CodeAborted, errors.New("a backup or restore is already running"))
	}
	defer r.s.mu.Unlock()
	r.s.restore.mu.Lock()
	workRunning := r.s.restore.archiveWork || len(r.s.restore.activeStages) > 0
	r.s.restore.mu.Unlock()
	if workRunning {
		return nil, connect.NewError(connect.CodeAborted, errors.New("an archive transfer or restore preparation is already running"))
	}
	if err := r.s.restore.deleteKeptData(ctx, req.Msg.Name); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("kept restore directory could not be deleted"))
	}
	r.s.audit(ctx, "backup.kept_data_deleted", map[string]any{"name": req.Msg.Name})
	return connect.NewResponse(&adminv1.DeleteKeptDataResponse{}), nil
}

var keptDataNamePattern = regexp.MustCompile(`^.+\.before-restore-[0-9]{8}-[0-9]{6}$`)

func keptDataPath(parent, dataBase, name string) (string, bool) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") || !strings.HasPrefix(name, dataBase+".before-restore-") || !keptDataNamePattern.MatchString(name) {
		return "", false
	}
	return filepath.Join(parent, name), true
}

func listKeptData(parent, dataBase string) ([]*adminv1.KeptData, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	var result []*adminv1.KeptData
	for _, entry := range entries {
		name := entry.Name()
		path, ok := keptDataPath(parent, dataBase, name)
		if !ok || !entry.IsDir() {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		var size int64
		modified := info.ModTime()
		err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			item, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if item.Mode().IsRegular() {
				size += item.Size()
			}
			if item.ModTime().After(modified) {
				modified = item.ModTime()
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		result = append(result, &adminv1.KeptData{Name: name, SizeBytes: size, ModifiedUnix: modified.Unix()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name > result[j].Name })
	return result, nil
}

func (r rpc) GetRestoreResult(ctx context.Context, _ *connect.Request[adminv1.GetRestoreResultRequest]) (*connect.Response[adminv1.GetRestoreResultResponse], error) {
	if err := requireOwner(ctx); err != nil {
		return nil, err
	}
	if err := edition.Check(r.s.edge, edition.Backups); err != nil {
		return nil, err
	}
	r.s.consumeRestoreResult(ctx)
	encoded, err := r.s.st.Setting(ctx, restoreResultKey)
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewResponse(&adminv1.GetRestoreResultResponse{}), nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("restore result could not be read"))
	}
	var result update.PanelRestoreResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("restore result is invalid"))
	}
	return connect.NewResponse(&adminv1.GetRestoreResultResponse{Outcome: result.Outcome, Source: result.Source,
		FileName: result.FileName, MadeByVersion: result.MadeByVersion, CompletedUnix: result.CompletedUnix}), nil
}

func (s *Service) consumeRestoreResult(ctx context.Context) {
	result, err := update.ReadPanelRestoreResult(s.dataDir)
	if err != nil {
		return
	}
	action := "backup.restore_failed"
	if result.Outcome == "restored" {
		action = "backup.restored"
	}
	params := map[string]any{"source": result.Source, "file_name": result.FileName, "made_by_version": result.MadeByVersion}
	if err := s.auditEvent(ctx, action, params); err != nil {
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return
	}
	if err := s.st.SetSettings(ctx, map[string]string{restoreResultKey: string(encoded)}); err != nil {
		return
	}
	_ = os.Remove(filepath.Join(s.dataDir, update.PanelRestoreResultFileName))
	s.restore.mu.Lock()
	if s.restore.pendingRestore.StageDir != "" {
		delete(s.restore.activeStages, filepath.Join(s.dataDir, s.restore.pendingRestore.StageDir))
	}
	s.restore.pendingRestore = update.PanelRestoreRequest{}
	s.restore.mu.Unlock()
	s.restore.pending.Store(false)
}

// reconcilePendingRestore records a durable failure if the updater has stopped reporting a scheduled helper but no
// helper result arrived. This prevents an accepted request whose systemd unit never started from locking backups.
func (s *Service) reconcilePendingRestore(ctx context.Context) {
	s.restore.mu.Lock()
	request := s.restore.pendingRestore
	s.restore.mu.Unlock()
	if request.StageDir == "" {
		s.reconstructPendingRestore()
		s.restore.mu.Lock()
		request = s.restore.pendingRestore
		s.restore.mu.Unlock()
	}
	if request.StageDir == "" || !s.restore.pending.Load() {
		return
	}
	if _, err := update.ReadPanelRestoreResult(s.dataDir); err == nil {
		s.consumeRestoreResult(ctx)
		return
	}
	state, active, exists, stateErr := update.ReadPanelOperationState(s.dataDir, s.now())
	if stateErr != nil || active {
		return
	}
	requestedAt := time.Time{}
	if request.RequestedUnix > 0 {
		requestedAt = time.Unix(request.RequestedUnix, 0)
	}
	if state.Kind == "restore" && state.RestoreRequest != nil {
		request = *state.RestoreRequest
		if request.RequestedUnix == 0 {
			request.RequestedUnix = state.StartedUnix
		}
		requestedAt = time.Unix(request.RequestedUnix, 0)
	}
	requestOnDisk, requestErr := update.ReadPanelRestoreRequest(s.dataDir)
	if requestErr == nil {
		request = requestOnDisk
		requestedAt = time.Unix(request.RequestedUnix, 0)
	} else if requestedAt.IsZero() {
		if info, err := os.Lstat(filepath.Join(s.dataDir, "panel-restore.request")); err == nil && info.Mode().IsRegular() {
			requestedAt = info.ModTime()
		}
	}
	if !requestedAt.IsZero() && s.now().Sub(requestedAt) < restoreHelperStartGrace {
		return
	}
	if s.restore.helperRunning != nil && s.restore.helperRunning() && requestErr != nil && !exists {
		return
	}
	result := update.PanelRestoreResult{
		Outcome: "failed", Source: request.Source, FileName: request.FileName,
		MadeByVersion: request.MadeByVersion, CompletedUnix: s.now().UTC().Unix(),
	}
	if err := update.RemovePanelRestoreRequest(s.dataDir); err != nil {
		s.log.Warn("remove stale panel restore request", "code", "restore_request_cleanup_failed")
	}
	if err := update.WritePanelRestoreResult(s.dataDir, result); err != nil {
		s.log.Error("record panel restore helper failure", "code", "restore_result_write_failed")
		return
	}
	if stagePath, ok := restoreStagePath(s.dataDir, request.StageDir); ok {
		_ = os.RemoveAll(stagePath)
	}
	s.consumeRestoreResult(ctx)
}

func restoreStagePath(dataDir, name string) (string, bool) {
	if filepath.Base(name) != name || len(name) != len(restoreStagePrefix)+32 || !strings.HasPrefix(name, restoreStagePrefix) {
		return "", false
	}
	for _, r := range strings.TrimPrefix(name, restoreStagePrefix) {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", false
		}
	}
	return filepath.Join(dataDir, name), true
}

func (s *Service) auditEvent(ctx context.Context, action string, params any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ip := auth.ClientIPFrom(ctx)
	ipText := ""
	if ip.IsValid() {
		ipText = ip.String()
	}
	return s.st.Audit(ctx, time.Now(), store.AuditEntry{Actor: auth.ActorLabel(ctx), Action: action, Params: string(encoded), Result: "ok", IP: ipText})
}
