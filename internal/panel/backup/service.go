package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"filippo.io/age"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	adminPagePath       = "/settings/backups"
	secretRecordID      = "panel_backup_settings.secret_access_key"
	defaultIntervalHour = 24
	listResponseLimit   = 100
)

var (
	accountIDPattern             = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
	bucketPattern                = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	errBackupNotConfigured       = errors.New("backup_not_configured")
	errBackupInvalidSettings     = errors.New("backup_settings_invalid")
	errBackupStorageFailed       = errors.New("backup_storage_failed")
	errBackupStorageDeleteFailed = errors.New("backup_storage_delete_failed")
	errBackupArchiveFailed       = errors.New("backup_create_failed")
	errBackupBusy                = errors.New("backup_already_running")
)

type Config struct {
	Store     *store.Store
	Vault     *vault.Vault
	DataDir   string
	MasterKey []byte
	StepUp    func(context.Context) error
	Log       *slog.Logger
}

type Service struct {
	st        *store.Store
	vault     *vault.Vault
	dataDir   string
	masterKey []byte
	stepUp    func(context.Context) error
	log       *slog.Logger
	mu        sync.Mutex
	r2Factory func(context.Context, store.PanelBackupSettings) (s3API, error)
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Vault == nil || cfg.DataDir == "" || len(cfg.MasterKey) != vault.KeySize {
		return nil, errors.New("backup: store, vault, data directory and master key are required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	s := &Service{st: cfg.Store, vault: cfg.Vault, dataDir: cfg.DataDir, masterKey: append([]byte(nil), cfg.MasterKey...), stepUp: cfg.StepUp, log: cfg.Log}
	s.r2Factory = s.newR2Client
	return s, nil
}

func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewBackupServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)...)
}

func (s *Service) Run(ctx context.Context) {
	s.runDue(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDue(ctx)
		}
	}
}

func (s *Service) runDue(ctx context.Context) {
	settings, err := s.st.PanelBackupSettings(ctx)
	if err != nil {
		s.log.Error("read panel backup schedule", "code", "backup_settings_unavailable")
		return
	}
	if !settings.Enabled {
		return
	}
	interval := settings.IntervalHours
	if interval < 1 {
		interval = defaultIntervalHour
	}
	if !settings.LastSuccess.IsZero() && time.Since(settings.LastSuccess) < time.Duration(interval)*time.Hour {
		return
	}
	if _, warning, err := s.createBackup(ctx); err != nil {
		s.log.Error("scheduled panel backup failed", "code", errorCode(err))
	} else if warning != "" {
		s.log.Warn("panel backup completed with a warning", "code", warning)
	}
}

type rpc struct{ s *Service }

func (r rpc) GetBackupSettings(ctx context.Context, _ *connect.Request[adminv1.GetBackupSettingsRequest]) (*connect.Response[adminv1.GetBackupSettingsResponse], error) {
	if err := requireOwner(ctx); err != nil {
		return nil, err
	}
	settings, err := r.s.st.PanelBackupSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errBackupInvalidSettings)
	}
	return connect.NewResponse(&adminv1.GetBackupSettingsResponse{Settings: backupSettingsProto(settings)}), nil
}

func (r rpc) UpdateBackupSettings(ctx context.Context, req *connect.Request[adminv1.UpdateBackupSettingsRequest]) (*connect.Response[adminv1.UpdateBackupSettingsResponse], error) {
	if err := r.s.requireOwnerStepUp(ctx); err != nil {
		return nil, err
	}
	msg := req.Msg
	settings, err := r.s.st.PanelBackupSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errBackupInvalidSettings)
	}
	settings.AccountID = strings.ToLower(strings.TrimSpace(msg.AccountId))
	settings.Jurisdiction = strings.ToLower(strings.TrimSpace(msg.Jurisdiction))
	if settings.Jurisdiction == "" {
		settings.Jurisdiction = "default"
	}
	settings.Bucket = strings.TrimSpace(msg.Bucket)
	settings.AccessKeyID = strings.TrimSpace(msg.AccessKeyId)
	settings.AgeRecipient = strings.TrimSpace(msg.AgeRecipient)
	settings.Enabled = msg.Enabled
	settings.IntervalHours = int(msg.IntervalHours)
	settings.RetentionDays = int(msg.RetentionDays)
	if settings.IntervalHours == 0 {
		settings.IntervalHours = defaultIntervalHour
	}
	if msg.ClearSecret {
		settings.SecretAccessKey = nil
	}
	if msg.SecretAccessKey != "" {
		if msg.SecretAccessKey != strings.TrimSpace(msg.SecretAccessKey) || len(msg.SecretAccessKey) > 1024 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errBackupInvalidSettings)
		}
		settings.SecretAccessKey = r.s.vault.Seal([]byte(msg.SecretAccessKey), secretRecordID)
	}
	if err := validateSettings(settings, msg.Enabled); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errBackupInvalidSettings)
	}
	if err := r.s.st.SavePanelBackupSettings(ctx, settings, time.Now()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errBackupInvalidSettings)
	}
	r.s.audit(ctx, "backup_settings_update", map[string]any{
		"enabled": settings.Enabled, "interval_hours": settings.IntervalHours,
		"retention_days": settings.RetentionDays, "jurisdiction": settings.Jurisdiction,
		"has_secret": len(settings.SecretAccessKey) > 0,
	})
	return connect.NewResponse(&adminv1.UpdateBackupSettingsResponse{Settings: backupSettingsProto(settings)}), nil
}

func (r rpc) TestBackupStorage(ctx context.Context, _ *connect.Request[adminv1.TestBackupStorageRequest]) (*connect.Response[adminv1.TestBackupStorageResponse], error) {
	if err := r.s.requireOwnerStepUp(ctx); err != nil {
		return nil, err
	}
	settings, err := r.s.st.PanelBackupSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errBackupInvalidSettings)
	}
	api, err := r.s.r2Factory(ctx, settings)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(errorCode(err)))
	}
	if err := testStorage(ctx, api, settings.Bucket); err != nil {
		r.s.audit(ctx, "backup_storage_test", map[string]any{"ok": false})
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(errorCode(err)))
	}
	r.s.audit(ctx, "backup_storage_test", map[string]any{"ok": true})
	return connect.NewResponse(&adminv1.TestBackupStorageResponse{Ok: true}), nil
}

func (r rpc) CreateBackup(ctx context.Context, _ *connect.Request[adminv1.CreateBackupRequest]) (*connect.Response[adminv1.CreateBackupResponse], error) {
	if err := r.s.requireOwnerStepUp(ctx); err != nil {
		return nil, err
	}
	object, warning, err := r.s.createBackup(ctx)
	if err != nil {
		return nil, connect.NewError(connectCodeForBackupError(err), errors.New(errorCode(err)))
	}
	r.s.audit(ctx, "backup_create", map[string]any{"key": object.Key, "size_bytes": object.Size})
	return connect.NewResponse(&adminv1.CreateBackupResponse{Backup: backupProto(object), WarningCode: warning}), nil
}

func (r rpc) ListBackups(ctx context.Context, _ *connect.Request[adminv1.ListBackupsRequest]) (*connect.Response[adminv1.ListBackupsResponse], error) {
	if err := requireOwner(ctx); err != nil {
		return nil, err
	}
	settings, err := r.s.st.PanelBackupSettings(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errBackupInvalidSettings)
	}
	api, err := r.s.r2Factory(ctx, settings)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(errorCode(err)))
	}
	objects, err := recentBackups(ctx, api, settings.Bucket, listResponseLimit)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(errorCode(err)))
	}
	response := &adminv1.ListBackupsResponse{Backups: make([]*adminv1.Backup, 0, len(objects))}
	for _, object := range objects {
		response.Backups = append(response.Backups, backupProto(object))
	}
	return connect.NewResponse(response), nil
}

func (s *Service) requireOwnerStepUp(ctx context.Context) error {
	if err := requireOwner(ctx); err != nil {
		return err
	}
	if s.stepUp == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("backup step-up is unavailable"))
	}
	return s.stepUp(ctx)
}

func requireOwner(ctx context.Context) error {
	admin, ok := auth.AdminFrom(ctx)
	if !ok || admin.Role != store.RoleOwner || strings.HasPrefix(admin.ID, "token:") {
		return connect.NewError(connect.CodePermissionDenied, errors.New("owner access required"))
	}
	return nil
}

func (s *Service) newR2Client(ctx context.Context, settings store.PanelBackupSettings) (s3API, error) {
	return newS3API(ctx, settings, func(ciphertext []byte) ([]byte, error) {
		return s.vault.Open(ciphertext, secretRecordID)
	})
}

func validateSettings(settings store.PanelBackupSettings, enabled bool) error {
	if settings.Jurisdiction == "" {
		settings.Jurisdiction = "default"
	}
	if settings.Jurisdiction != "default" && settings.Jurisdiction != "eu" && settings.Jurisdiction != "us" && settings.Jurisdiction != "fedramp" {
		return errBackupInvalidSettings
	}
	if _, err := s3Endpoint(settings.AccountID, settings.Jurisdiction); err != nil && settings.AccountID != "" {
		return errBackupInvalidSettings
	}
	if settings.AccountID != "" && !accountIDPattern.MatchString(settings.AccountID) {
		return errBackupInvalidSettings
	}
	if settings.Bucket != "" && (!bucketPattern.MatchString(settings.Bucket) || strings.Contains(settings.Bucket, "..") || strings.Contains(settings.Bucket, ".-") || strings.Contains(settings.Bucket, "-.")) {
		return errBackupInvalidSettings
	}
	if len(settings.AccessKeyID) > 128 || strings.ContainsAny(settings.AccessKeyID, "\r\n\x00") {
		return errBackupInvalidSettings
	}
	if settings.IntervalHours < 1 || settings.IntervalHours > 168 {
		return errBackupInvalidSettings
	}
	if settings.RetentionDays != 0 && (settings.RetentionDays < 7 || settings.RetentionDays > 3650) {
		return errBackupInvalidSettings
	}
	if settings.AgeRecipient != "" {
		if _, err := parseRecipient(settings.AgeRecipient); err != nil {
			return errBackupInvalidSettings
		}
	}
	if enabled && (settings.AccountID == "" || settings.Bucket == "" || settings.AccessKeyID == "" || len(settings.SecretAccessKey) == 0 || settings.AgeRecipient == "") {
		return errBackupInvalidSettings
	}
	return nil
}

func (s *Service) createBackup(ctx context.Context) (backupObject, string, error) {
	if !s.mu.TryLock() {
		return backupObject{}, "", errBackupBusy
	}
	defer s.mu.Unlock()
	settings, err := s.st.PanelBackupSettings(ctx)
	if err != nil {
		return backupObject{}, "", errBackupInvalidSettings
	}
	if err := validateSettings(settings, true); err != nil {
		return backupObject{}, "", errBackupNotConfigured
	}
	api, err := s.r2Factory(ctx, settings)
	if err != nil {
		s.recordRun(ctx, false, errorCode(err))
		return backupObject{}, "", err
	}
	if _, err := api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(settings.Bucket), Prefix: aws.String(r2BackupPrefix), MaxKeys: aws.Int32(1)}); err != nil {
		s.recordRun(ctx, false, errorCode(errBackupStorageFailed))
		return backupObject{}, "", errBackupStorageFailed
	}
	workDir, err := os.MkdirTemp("", "mistgate-backup-")
	if err != nil {
		s.recordRun(ctx, false, errorCode(errBackupArchiveFailed))
		return backupObject{}, "", errBackupArchiveFailed
	}
	defer os.RemoveAll(workDir)
	if err := os.Chmod(workDir, 0o700); err != nil {
		s.recordRun(ctx, false, errorCode(errBackupArchiveFailed))
		return backupObject{}, "", errBackupArchiveFailed
	}
	snapshot := filepath.Join(workDir, "snapshot.db")
	if err := s.st.SnapshotDatabase(ctx, snapshot); err != nil {
		s.recordRun(ctx, false, errorCode(errBackupArchiveFailed))
		return backupObject{}, "", errBackupArchiveFailed
	}
	archivePath := filepath.Join(workDir, "mistgate-backup.tar.gz.age")
	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		s.recordRun(ctx, false, errorCode(errBackupArchiveFailed))
		return backupObject{}, "", errBackupArchiveFailed
	}
	now := time.Now().UTC()
	createErr := CreateEncryptedArchive(ctx, s.dataDir, snapshot, s.masterKey, buildinfo.Version, settings.AgeRecipient, now, workDir, archive)
	closeErr := archive.Close()
	if createErr != nil || closeErr != nil {
		s.recordRun(ctx, false, errorCode(errBackupArchiveFailed))
		return backupObject{}, "", errBackupArchiveFailed
	}
	key, err := backupObjectKey(now)
	if err != nil {
		s.recordRun(ctx, false, errorCode(errBackupArchiveFailed))
		return backupObject{}, "", errBackupArchiveFailed
	}
	size, err := uploadBackup(ctx, api, settings.Bucket, key, archivePath)
	if err != nil {
		s.recordRun(ctx, false, errorCode(err))
		return backupObject{}, "", err
	}
	object := backupObject{Key: key, Size: size, LastModified: now}
	warning := ""
	if err := pruneBackups(ctx, api, settings.Bucket, key, settings.RetentionDays, now); err != nil {
		warning = "retention_failed"
		s.log.Warn("panel backup retention failed", "code", warning)
	}
	if err := s.recordRun(ctx, true, warning); err != nil {
		s.log.Warn("save panel backup status failed", "code", "backup_status_unavailable")
		if warning == "" {
			warning = "backup_status_unavailable"
		}
	}
	return object, warning, nil
}

func (s *Service) recordRun(ctx context.Context, success bool, code string) error {
	statusCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.st.RecordPanelBackupRun(statusCtx, time.Now().UTC(), success, code)
}

func (s *Service) audit(ctx context.Context, action string, params any) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return
	}
	ip := auth.ClientIPFrom(ctx)
	ipText := ""
	if ip.IsValid() {
		ipText = ip.String()
	}
	if err := s.st.Audit(ctx, time.Now(), store.AuditEntry{
		Actor: auth.ActorLabel(ctx), Action: action, Params: string(encoded),
		Result: "ok", IP: ipText,
	}); err != nil {
		s.log.Warn("write backup audit event failed", "code", "audit_unavailable")
	}
}

func backupSettingsProto(value store.PanelBackupSettings) *adminv1.BackupSettings {
	result := &adminv1.BackupSettings{
		AccountId: value.AccountID, Jurisdiction: value.Jurisdiction, Bucket: value.Bucket,
		AccessKeyId: value.AccessKeyID, HasSecret: len(value.SecretAccessKey) > 0,
		AgeRecipient: value.AgeRecipient, Enabled: value.Enabled,
		IntervalHours: int32(value.IntervalHours), RetentionDays: int32(value.RetentionDays),
		LastErrorCode: value.LastErrorCode,
	}
	if !value.LastSuccess.IsZero() {
		result.LastSuccessUnix = value.LastSuccess.UTC().Unix()
	}
	return result
}

func backupProto(value backupObject) *adminv1.Backup {
	result := &adminv1.Backup{Key: value.Key, SizeBytes: value.Size}
	if !value.LastModified.IsZero() {
		result.CreatedUnix = value.LastModified.UTC().Unix()
	}
	return result
}

func connectCodeForBackupError(err error) connect.Code {
	switch {
	case errors.Is(err, errBackupBusy):
		return connect.CodeAborted
	case errors.Is(err, errBackupNotConfigured), errors.Is(err, errBackupInvalidSettings):
		return connect.CodeFailedPrecondition
	case errors.Is(err, errBackupStorageFailed), errors.Is(err, errBackupStorageDeleteFailed):
		return connect.CodeUnavailable
	default:
		return connect.CodeInternal
	}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, errBackupBusy):
		return "backup_already_running"
	case errors.Is(err, errBackupNotConfigured):
		return "backup_not_configured"
	case errors.Is(err, errBackupInvalidSettings):
		return "backup_settings_invalid"
	case errors.Is(err, errBackupStorageDeleteFailed):
		return "backup_storage_delete_failed"
	case errors.Is(err, errBackupStorageFailed):
		return "backup_storage_failed"
	default:
		return "backup_create_failed"
	}
}

// generateRecoveryKey creates a hybrid age identity using the library's native format.
func generateRecoveryKey(dst io.Writer) (string, error) {
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(dst, identity.String()+"\n"); err != nil {
		return "", err
	}
	return identity.Recipient().String(), nil
}
