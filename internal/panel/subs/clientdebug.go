package subs

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The logs a person's app sends for support: POST <base>/debug and POST <base>/debug/decline (the client API's routes, with sync and
// leave), and the debug object of sync. What is kept and how it is cleaned is internal/panel/clientdebug, which the VPS edition
// wires in (Config.ClientDebug); the Cloudflare edition has none, and its sync says debug.intake false.

const (
	// ClientDebugMaxWire is the most bytes of a delivery on the wire (gzip), ClientDebugMaxText the most after unpacking, and
	// ClientDebugReadWindow how long the body may take to arrive: the listener's own 30 seconds cut a megabyte on a slow line.
	ClientDebugMaxWire   = 1 << 20
	ClientDebugMaxText   = 4 << 20
	ClientDebugReadSpan  = 2 * time.Minute
	clientDebugRequestHd = "X-Mistgate-Debug-Request"
	clientDebugMediaType = "application/x-ndjson"
	// clientDebugIDMax is the longest request id worth looking up: ids are 26 characters.
	clientDebugIDMax = 64
)

// ClientDebugRequest is an open request of the owner as sync tells the person.
type ClientDebugRequest struct {
	ID, Level, Note string
	ExpiresUnix     int64
}

// ClientDebugIn is a delivery whose body (unpacked, at most ClientDebugMaxText+1 bytes) has not been read yet. RequestID is empty
// for a bundle the person sends on their own.
type ClientDebugIn struct {
	Device    store.AccessDevice
	RequestID string
	UserAgent string
	Body      io.Reader
	Now       time.Time
}

// ClientDebugResult is what a delivery stored.
type ClientDebugResult struct {
	ID               string
	Lines, DroppedKV int
}

// ClientDebugError is a refusal of the debug routes with the answer it is: the status, the code of the JSON body, a message that
// holds no value of the request, and for 429 and 503 how long to wait. Any other error is a 500.
type ClientDebugError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *ClientDebugError) Error() string { return e.Code }

// ClientDebug keeps the logs people send; internal/panel/clientdebug.Service implements it. Nil = the edition takes none.
type ClientDebug interface {
	// Open is the device's open request, for sync.
	Open(ctx context.Context, deviceID string, now time.Time) (ClientDebugRequest, bool, error)
	// Admit says, before the body is read, whether a delivery may come: a *ClientDebugError (409 no_request, 429, 503 debug_full)
	// when not. When the request is answered already (a repeat of a delivery the panel took) it returns that answer instead.
	Admit(ctx context.Context, dev store.AccessDevice, requestID string, now time.Time) (*ClientDebugResult, error)
	// Receive reads, checks, cleans and stores the delivery. A *ClientDebugError carries a refusal.
	Receive(ctx context.Context, in ClientDebugIn) (ClientDebugResult, error)
	// Decline is the person's refusal of an open request.
	Decline(ctx context.Context, dev store.AccessDevice, requestID string, now time.Time) (string, error)
}

func clientDebugRequestID(r *http.Request) string {
	id := strings.TrimSpace(r.Header.Get(clientDebugRequestHd))
	if len(id) > clientDebugIDMax {
		return "dbr_unknown" // not an id of ours: looked up like any other and found nowhere
	}
	return id
}

// serveClientDebug answers the debug routes for a device whose key, budget, link and origin are checked already. The answers, in
// order: 403 debug_off (the edition takes no logs), then for a delivery 415 (not application/x-ndjson, not gzip), 413 (the
// announced size), the admission (409 no_request, user_inactive or app_disabled; 429 + Retry-After; 503 debug_full: all before the
// body is read), the claim of the device (429 + Retry-After 30 while another delivery of it is being read), a place among the few bodies
// read at once (503 retry + Retry-After 30), the person's hourly window of attempts (429), the reading of the whole wire body (413, 400),
// the slot of the process for unpacking and storing (503 retry + Retry-After 30 after a few seconds), the body's own 400, the store's. Nothing of the body, the key or the address is logged.
func (h *handler) serveClientDebug(w http.ResponseWriter, r *http.Request, dev store.AccessDevice, route string) {
	cd := h.cfg.ClientDebug
	if cd == nil {
		jsonError(w, http.StatusForbidden, "debug_off", "")
		return
	}
	ctx, now, id := r.Context(), h.cfg.Now(), clientDebugRequestID(r)
	if route == "debug/decline" {
		declined, err := cd.Decline(ctx, dev, id, now)
		if err != nil {
			h.clientDebugFailed(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			ID       string `json:"id"`
			Declined bool   `json:"declined"`
		}{declined, true})
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != clientDebugMediaType ||
		!strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		jsonError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "")
		return
	}
	if r.ContentLength > ClientDebugMaxWire {
		jsonError(w, http.StatusRequestEntityTooLarge, "too_large", "")
		return
	}
	done, err := cd.Admit(ctx, dev, id, now)
	if err != nil {
		h.clientDebugFailed(w, err)
		return
	}
	if done != nil {
		h.clientDebugDone(w, *done)
		return
	}
	// One delivery of a device at a time, a few bodies read at a time in the process, and a person brings at most a few to the reading of
	// the body in an hour, broken ones included (all refused before the body is touched). The body is read whole first, on a read
	// window of its own and without the slot, so that a slow sender holds nothing but its own device's claim and one of the few reader
	// places; the slot of the process, for unpacking, cleaning and storing (seconds of CPU), is taken once the bytes are here.
	if _, busy := h.debugBusy.LoadOrStore(dev.ID, struct{}{}); busy {
		tooManyWrites(w, clientDebugBackoff)
		return
	}
	defer h.debugBusy.Delete(dev.ID)
	select {
	case h.debugReaders <- struct{}{}:
	default:
		h.clientDebugRetryLater(w)
		return
	}
	reading := true
	releaseReader := func() {
		if reading {
			reading = false
			<-h.debugReaders
		}
	}
	defer releaseReader()
	if retry, ok := h.clientDebugAttempt(ctx, dev.UserID); !ok {
		tooManyWrites(w, retry)
		return
	}
	// The listener's write timeout is set when the headers are read and covers the answer too: a delivery that takes the whole read
	// window must still be answered, or the logs are kept and the person never hears.
	rc := http.NewResponseController(w) // not every writer can set a deadline
	until := time.Now().Add(ClientDebugReadSpan)
	_ = rc.SetReadDeadline(until)
	_ = rc.SetWriteDeadline(until.Add(clientDebugWriteGrace))
	wire, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ClientDebugMaxWire))
	releaseReader()
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			jsonError(w, http.StatusRequestEntityTooLarge, "too_large", "")
		} else {
			jsonError(w, http.StatusBadRequest, "bad_request", "body")
		}
		return
	}
	if !h.clientDebugSlot(ctx) {
		h.clientDebugRetryLater(w)
		return
	}
	defer func() { <-clientDebugSlotChan }()
	gz, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "gzip")
		return
	}
	res, err := cd.Receive(ctx, ClientDebugIn{Device: dev, RequestID: id, UserAgent: r.UserAgent(), Body: io.LimitReader(gz, ClientDebugMaxText+1), Now: now})
	if err != nil {
		h.clientDebugFailed(w, err)
		return
	}
	h.clientDebugDone(w, res)
}

// clientDebugRetryLater is 503 retry: the panel is busy with other deliveries; the body of this one was not read, or was read and
// not kept.
func (h *handler) clientDebugRetryLater(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(clientDebugBackoff/time.Second)))
	jsonError(w, http.StatusServiceUnavailable, "retry", "")
}

const (
	// clientDebugBackoff is what a refused delivery is told to wait.
	clientDebugBackoff = 30 * time.Second
	// clientDebugWriteGrace is how long past the read window the answer may take to be written.
	clientDebugWriteGrace = 30 * time.Second
	// clientDebugAttempts is how many deliveries a person may bring to the reading of the body in an hour.
	clientDebugAttempts     = 6
	clientDebugSlotDefault  = 8 * time.Second
	clientDebugAttemptsSpan = time.Hour
)

// clientDebugSlotChan is the one slot of the process for unpacking, cleaning and storing a delivery.
// ponytail: один слот на процесс; поднять, если приём станет узким местом
var clientDebugSlotChan = make(chan struct{}, 1)

// clientDebugSlot takes the slot of the process, waiting for it a few seconds.
func (h *handler) clientDebugSlot(ctx context.Context) bool {
	wait := h.cfg.ClientDebugSlotWait
	if wait <= 0 {
		wait = clientDebugSlotDefault
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case clientDebugSlotChan <- struct{}{}:
		return true
	case <-timer.C:
	case <-ctx.Done():
	}
	return false
}

// clientDebugAttempt counts one delivery that reached the reading of its body against the person's hourly window. A limiter that
// does not answer refuses ("later", not a free pass).
func (h *handler) clientDebugAttempt(ctx context.Context, userID string) (time.Duration, bool) {
	limit := h.cfg.ClientDebugAttemptsPerHour
	if limit < 0 {
		return 0, true
	}
	if limit == 0 {
		limit = clientDebugAttempts
	}
	d, err := h.cfg.Limiter.Record(ctx, securitylimit.Window{Name: "subscription-client-debug", Limit: limit, Span: clientDebugAttemptsSpan}, "u:"+userID)
	if err != nil {
		return clientDebugAttemptsSpan, false
	}
	return d.RetryAfter, d.Allowed
}

func (h *handler) clientDebugDone(w http.ResponseWriter, res ClientDebugResult) {
	writeJSON(w, http.StatusOK, struct {
		ID        string `json:"id"`
		Lines     int    `json:"lines"`
		DroppedKV int    `json:"dropped_kv"`
	}{res.ID, res.Lines, res.DroppedKV})
}

func (h *handler) clientDebugFailed(w http.ResponseWriter, err error) {
	var refusal *ClientDebugError
	if !errors.As(err, &refusal) {
		h.clientAPIFailed(w, "client_api_debug_failed", err)
		return
	}
	switch {
	case refusal.Status == http.StatusTooManyRequests:
		tooManyWrites(w, refusal.RetryAfter)
		return
	case refusal.RetryAfter > 0:
		w.Header().Set("Retry-After", strconv.Itoa(int((refusal.RetryAfter+time.Second-1)/time.Second)))
	}
	jsonError(w, refusal.Status, refusal.Code, refusal.Message)
}
