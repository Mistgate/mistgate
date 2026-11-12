//go:build !js

package clientdebug

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// Audit actions and the event code of the logs people send. The audit rows are written for the person ("user:<id>" acting for
// themselves, as an enrolment is) with the ids of the bundle, the person and the device; the event says that logs came, and neither
// carries a word of the logs.
const (
	AuditReceived = "client_debug_received"
	AuditDeclined = "client_debug_declined"
	EventReceived = "client_logs_received"
)

// Config configures Service. Store is required.
type Config struct {
	Store *store.Store
	// Secrets are the panel's secret names (the agent SNI and the secret path prefixes), cut out of every text.
	Secrets []string
	Log     *slog.Logger
}

// Service is subs.ClientDebug: what the app's debug routes and sync call.
type Service struct {
	cfg     Config
	cleaner *Cleaner
}

// A drift between the interface and the service would not compile.
var _ subs.ClientDebug = (*Service)(nil)

// New returns the service.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Service{cfg: cfg, cleaner: NewCleaner(cfg.Secrets)}
}

var (
	noRequest = &subs.ClientDebugError{Status: http.StatusConflict, Code: "no_request"}
	debugFull = &subs.ClientDebugError{Status: http.StatusServiceUnavailable, Code: "debug_full"}
	tryAgain  = &subs.ClientDebugError{Status: http.StatusServiceUnavailable, Code: "retry", RetryAfter: 5 * time.Second}
	tooLarge  = &subs.ClientDebugError{Status: http.StatusRequestEntityTooLarge, Code: "too_large"}
)

// refusal turns what the store or the parser says into the answer; anything it does not know stays an error (a 500).
func refusal(err error) error {
	var rate *store.ClientDebugRateError
	var bad *BadLine
	switch {
	case errors.Is(err, store.ErrClientDebugNoRequest):
		return noRequest
	case errors.As(err, &rate):
		return &subs.ClientDebugError{Status: http.StatusTooManyRequests, Code: "too_many_requests", RetryAfter: rate.After}
	case errors.Is(err, store.ErrClientDebugFull):
		return debugFull
	case errors.Is(err, store.ErrConflict):
		return tryAgain
	case errors.Is(err, ErrTooLarge):
		return tooLarge
	case errors.As(err, &bad):
		return &subs.ClientDebugError{Status: http.StatusBadRequest, Code: "bad_request", Message: bad.Error()}
	}
	return err
}

// Open is the device's open request, for sync.
func (s *Service) Open(ctx context.Context, deviceID string, now time.Time) (subs.ClientDebugRequest, bool, error) {
	row, open, err := s.cfg.Store.OpenClientDebug(ctx, deviceID, now)
	if err != nil || !open {
		return subs.ClientDebugRequest{}, false, err
	}
	return subs.ClientDebugRequest{ID: row.ID, Level: row.Level, Note: row.Note, ExpiresUnix: row.ExpiresAt.Unix()}, true, nil
}

// Admit is the check before the body is read. A delivery for a request: it must be this device's and open; if it was answered
// already, that answer is returned. A delivery with no request is the person's own: the device's ten-minute spacing and the panel's
// ceiling apply.
func (s *Service) Admit(ctx context.Context, dev store.AccessDevice, requestID string, now time.Time) (*subs.ClientDebugResult, error) {
	if requestID == "" {
		// a person who may not use the app sends nothing on their own; the owner's request is answered whatever the person's state
		status, appOff, err := s.userState(ctx, dev.UserID, now)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, noRequest
		case err != nil:
			return nil, err
		case status != access.StatusActive:
			return nil, &subs.ClientDebugError{Status: http.StatusConflict, Code: "user_inactive", Message: status}
		case appOff:
			return nil, &subs.ClientDebugError{Status: http.StatusConflict, Code: "app_disabled"}
		}
		if err := s.cfg.Store.ClientDebugAdmitUnasked(ctx, dev.ID, now); err != nil {
			return nil, refusal(err)
		}
		return nil, nil
	}
	row, err := s.cfg.Store.ClientDebug(ctx, requestID, now)
	if errors.Is(err, store.ErrNotFound) {
		return nil, noRequest
	}
	if err != nil {
		return nil, err
	}
	switch {
	case row.DeviceID != dev.ID:
		return nil, noRequest
	case row.State == "received":
		return &subs.ClientDebugResult{ID: row.ID, Lines: row.Records, DroppedKV: row.DroppedKV}, nil
	case row.State != "open":
		return nil, noRequest
	}
	return nil, nil
}

// userState is the person's status as sync tells it (access.subView): disabled, expired, limited or active at this instant, with the quota
// period advanced, not the stored column, which the sweep updates later. One read of the person, the one sync makes.
func (s *Service) userState(ctx context.Context, userID string, now time.Time) (status string, appOff bool, err error) {
	u, err := s.cfg.Store.Access().User(ctx, userID)
	if err != nil {
		return "", false, err
	}
	if _, reset := access.AdvancePeriod(u.QuotaReset, u.PeriodStart, now); reset {
		u.UsedBytes = 0
	}
	return access.ComputeStatus(u.Disabled, u.ExpiresAt, u.QuotaBytes, u.UsedBytes, now), u.AppMistgateOff, nil
}

// Receive reads and cleans the body and stores it as the answer to the request, or as the person's own bundle.
func (s *Service) Receive(ctx context.Context, in subs.ClientDebugIn) (subs.ClientDebugResult, error) {
	bundle, err := Parse(in.Body, in.Now, s.cleaner)
	if err != nil {
		return subs.ClientDebugResult{}, refusal(err)
	}
	intake := store.ClientDebugIntake{RequestID: in.RequestID, UserID: in.Device.UserID, DeviceID: in.Device.ID, App: s.cleaner.Secrets(appToken(in.UserAgent)),
		Lines: bundle.Lines, Bytes: bundle.Bytes, DroppedKV: bundle.DroppedKV, FirstMs: bundle.FirstMs, LastMs: bundle.LastMs, Now: in.Now}
	if in.RequestID == "" {
		intake.NewID = store.NewClientDebugID()
	}
	row, replay, err := s.cfg.Store.ReceiveClientDebug(ctx, intake)
	if err != nil {
		return subs.ClientDebugResult{}, refusal(err)
	}
	if !replay {
		s.received(ctx, row, in.Now)
	}
	return subs.ClientDebugResult{ID: row.ID, Lines: row.Records, DroppedKV: row.DroppedKV}, nil
}

// Decline is the person's refusal. The owner sees it as the request's state at once, and the audit log says who refused.
func (s *Service) Decline(ctx context.Context, dev store.AccessDevice, requestID string, now time.Time) (string, error) {
	row, err := s.cfg.Store.DeclineClientDebug(ctx, dev.ID, requestID, now)
	if err != nil {
		return "", refusal(err)
	}
	s.audit(ctx, now, AuditDeclined, row, nil)
	return row.ID, nil
}

// received records that a bundle came: an audit row and an event, neither with any of its content.
func (s *Service) received(ctx context.Context, row store.ClientDebugRow, now time.Time) {
	lines := strconv.Itoa(row.Records)
	s.audit(ctx, now, AuditReceived, row, map[string]string{"lines": lines})
	ev := store.EventRow{Time: now, Severity: 1, Code: EventReceived, Source: "panel", UserID: row.UserID, DeviceID: row.DeviceID,
		Params: map[string]string{"id": row.ID, "lines": lines, "unasked": strconv.FormatBool(row.Unasked)}}
	if err := s.cfg.Store.InsertEvent(ctx, ev); err != nil {
		s.cfg.Log.Warn("client debug: event write failed", "code", "client_debug_event_failed", "err", err)
	}
}

func (s *Service) audit(ctx context.Context, now time.Time, action string, row store.ClientDebugRow, extra map[string]string) {
	params := map[string]string{"user": row.UserID, "device": row.DeviceID, "id": row.ID}
	if row.Unasked {
		params["unasked"] = "true"
	}
	for k, v := range extra {
		params[k] = v
	}
	encoded, _ := json.Marshal(params)
	entry := store.AuditEntry{Actor: "user:" + row.UserID, Action: action, Params: string(encoded), Result: "ok"}
	if err := s.cfg.Store.Audit(ctx, now, entry); err != nil {
		s.cfg.Log.Warn("client debug: audit write failed", "code", "client_debug_audit_failed", "err", err)
	}
}

// appToken is the first token of the app's User-Agent ("Mistgate/0.3.1"), cut to the characters of a product token.
func appToken(userAgent string) string {
	token, _, _ := strings.Cut(userAgent, " ")
	out := make([]byte, 0, 48)
	for i := 0; i < len(token) && len(out) < 48; i++ {
		c := token[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '/' || c == '-' || c == '_' || c == '+' {
			out = append(out, c)
		}
	}
	return string(out)
}
