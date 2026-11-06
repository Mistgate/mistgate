package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"filippo.io/age"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	downloadPath       = "/backup/"
	downloadRoute      = auth.BackupDownloadProcedure
	maxDownloadBody    = 4 << 10
	minPassphraseRunes = 12
	scryptWorkFactor   = 18 // logN 18: approximately one second on a modern machine, matching age's reasonable default.
)

// DownloadHandler is mounted beside the Connect admin services under the same session and cross-origin protection.
func (s *Service) DownloadHandler() (string, http.Handler) {
	return downloadPath, http.HandlerFunc(s.download)
}

type downloadRequest struct {
	Passphrase string `json:"passphrase"`
}

type downloadError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Service) download(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeDownloadError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if r.URL.Path != downloadRoute {
		http.NotFound(w, r)
		return
	}
	if s.edge {
		writeDownloadError(w, http.StatusNotImplemented, "unavailable_in_edition", "backup downloads are available on the VPS edition")
		return
	}
	if err := requireOwner(r.Context()); err != nil {
		writeDownloadError(w, http.StatusForbidden, "owner_required", "owner access required")
		return
	}
	if err := s.requireOwnerStepUp(r.Context()); err != nil {
		// The RPCs' own answer, PERMISSION_DENIED with the StepUpRequired detail in Connect's JSON: the admin's step-up
		// guard reads it like any call's, asks for the passkey or the code, and repeats the download.
		var ce *connect.Error
		if !errors.As(err, &ce) {
			ce = connect.NewError(connect.CodePermissionDenied, err)
		}
		_ = connect.NewErrorWriter().Write(w, r, ce)
		return
	}
	passphrase, err := decodeDownloadRequest(w, r)
	if err != nil {
		writeDownloadError(w, http.StatusBadRequest, "invalid_request", "request body must be a JSON object with an optional passphrase")
		return
	}

	if !s.mu.TryLock() {
		writeDownloadError(w, http.StatusConflict, "backup_busy", "another backup is already running")
		return
	}
	defer s.mu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)

	ctx, cancel := context.WithTimeout(r.Context(), backupTimeout)
	defer cancel()
	settings, err := s.st.PanelBackupSettings(ctx)
	if err != nil {
		writeDownloadError(w, http.StatusInternalServerError, "backup_settings_unavailable", "backup settings could not be read")
		return
	}
	var recipient age.Recipient
	protection := "recipient"
	if strings.TrimSpace(settings.AgeRecipient) != "" {
		recipient, err = parseRecipient(settings.AgeRecipient)
		if err != nil {
			writeDownloadError(w, http.StatusInternalServerError, "backup_settings_invalid", "the configured recovery recipient is invalid")
			return
		}
	} else {
		protection = "passphrase"
		if utf8.RuneCountInString(passphrase) < minPassphraseRunes {
			writeDownloadError(w, http.StatusBadRequest, "passphrase_too_short", "a passphrase of at least 12 characters is required")
			return
		}
		scryptRecipient, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			writeDownloadError(w, http.StatusBadRequest, "invalid_passphrase", "the passphrase could not be used")
			return
		}
		scryptRecipient.SetWorkFactor(scryptWorkFactor)
		recipient = scryptRecipient
	}

	now := s.now().UTC()
	filename := downloadFilename(s.panelBrand(ctx), requestHost(r.Host), buildinfo.Version, now)
	response := &countingResponseWriter{ResponseWriter: w}
	completed := false
	defer func() {
		fields := map[string]any{"filename": filename, "size_bytes": response.size, "encryption": protection, "completed": completed}
		if !completed {
			fields["error"] = "backup_create_failed"
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		s.auditDownload(auditCtx, fields, completed)
	}()

	workDir, snapshot, err := s.stageSnapshot(ctx)
	if err != nil {
		writeDownloadError(w, http.StatusInternalServerError, "backup_create_failed", "the backup snapshot could not be created")
		return
	}
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			s.log.Warn("remove panel backup stage failed", "code", "backup_stage_cleanup_failed")
		}
	}()

	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", disposition)
	h.Set("X-Mistgate-Filename", filename)
	h.Set("Access-Control-Expose-Headers", "Content-Disposition, X-Mistgate-Filename")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(backupTimeout))

	if err := CreateEncryptedArchiveWithRecipient(ctx, s.dataDir, snapshot, s.masterKey, buildinfo.Version, recipient, now, response); err != nil {
		if response.size == 0 {
			h.Del("Content-Type")
			h.Del("Content-Disposition")
			h.Del("X-Mistgate-Filename")
			h.Del("Access-Control-Expose-Headers")
			writeDownloadError(w, http.StatusInternalServerError, "backup_create_failed", "the encrypted backup could not be created")
		} else if r.Context().Err() == nil {
			panic(http.ErrAbortHandler) // do not turn an incomplete archive into a successful-looking 200 response
		}
		return
	}
	completed = true
}

func decodeDownloadRequest(w http.ResponseWriter, r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	if r.ContentLength == 0 {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return "", errors.New("backup: invalid download content type")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDownloadBody))
	decoder.DisallowUnknownFields()
	var encoded json.RawMessage
	if err := decoder.Decode(&encoded); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}
	if len(encoded) == 0 || encoded[0] != '{' {
		return "", errors.New("backup: download request must be an object")
	}
	var request downloadRequest
	requestDecoder := json.NewDecoder(bytes.NewReader(encoded))
	requestDecoder.DisallowUnknownFields()
	if err := requestDecoder.Decode(&request); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("backup: trailing download request data")
	}
	return request.Passphrase, nil
}

func (s *Service) panelBrand(ctx context.Context) string {
	settings, err := instance.Load(ctx, s.st)
	if err != nil {
		return ""
	}
	return settings.BrandName()
}

func requestHost(hostport string) string {
	host := hostport
	if name, _, err := net.SplitHostPort(hostport); err == nil {
		host = name
	}
	return strings.TrimSuffix(strings.Trim(host, "[]"), ".")
}

func downloadFilename(brand, host, version string, created time.Time) string {
	panel := sanitizeFilenamePart(brand, false)
	if panel == "" {
		panel = sanitizeFilenamePart(host, false)
	}
	if panel == "" {
		panel = "panel"
	}
	version = sanitizeFilenamePart(version, true)
	if version == "" {
		version = "dev"
	}
	return "mistgate-backup-" + panel + "-" + version + "-" + created.UTC().Format("2006-01-02-1504") + ".tar.gz.age"
}

func sanitizeFilenamePart(value string, allowDot bool) string {
	var result strings.Builder
	separator := false
	last := rune(0)
	for _, char := range strings.ToLower(value) {
		allowed := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' || allowDot && char == '.'
		if !allowed {
			separator = true
			continue
		}
		if separator && result.Len() > 0 && last != '-' {
			result.WriteByte('-')
			last = '-'
		}
		separator = false
		if char == '-' && last == '-' {
			continue
		}
		result.WriteRune(char)
		last = char
	}
	return strings.Trim(result.String(), "-.")
}

type countingResponseWriter struct {
	http.ResponseWriter
	size int64
}

func (w *countingResponseWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.size += int64(n)
	return n, err
}

func writeDownloadError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(downloadError{Code: code, Message: message})
}

func (s *Service) auditDownload(ctx context.Context, fields map[string]any, completed bool) {
	params, err := json.Marshal(fields)
	if err != nil {
		return
	}
	result := "failed"
	if completed {
		result = "ok"
	}
	ip := auth.ClientIPFrom(ctx)
	ipText := ""
	if ip.IsValid() {
		ipText = ip.String()
	}
	if err := s.st.Audit(ctx, time.Now(), store.AuditEntry{
		Actor: auth.ActorLabel(ctx), Action: "backup.download", Params: string(params), Result: result, IP: ipText,
	}); err != nil {
		s.log.Warn("write backup download audit event failed", "code", "audit_unavailable")
	}
}
