package webproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	headerSessionToken = "X-Session-Token"
	headerDownCursor   = "X-Down-Cursor"
	headerCarrierMode  = "X-Carrier-Mode"
	headerUpAck        = "X-Up-Ack"
	headerBootstrap    = "X-Bootstrap-Token"
	headerUpSequence   = "X-Up-Seq"
)

// Handler routes only canonical bridge and carrier requests into the relay.
// Every other request is passed to next as the same request with its body intact.
// This fallback is safe for requests carrying a genuine token only when next
// remains inside this process; it must not forward those requests to another host.
func (r *Relay) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r == nil || req == nil || req.URL == nil {
			next.ServeHTTP(w, req)
			return
		}
		path := req.URL.Path
		if req.Method == http.MethodGet && path == "/" && req.URL.EscapedPath() == "/" {
			if r.serveBridge(w, req) {
				return
			}
			next.ServeHTTP(w, req)
			return
		}
		if req.URL.EscapedPath() != path {
			next.ServeHTTP(w, req)
			return
		}
		if req.URL.RawQuery != "" || req.URL.ForceQuery {
			next.ServeHTTP(w, req)
			return
		}
		switch {
		case path == "/api/v1/session" && req.Method == http.MethodPost:
			token, ok := bearerToken(req.Header)
			if !ok {
				next.ServeHTTP(w, req)
				return
			}
			entry, ok := r.authenticateBootstrap(token)
			if !ok {
				next.ServeHTTP(w, req)
				return
			}
			w = r.authenticated(w, req)
			defer r.endHandler()
			defer clearWriteDeadline(w)
			r.serveSession(w, req, entry)
		case path == "/api/v1/up" && req.Method == http.MethodPost:
			session, ok := r.authenticateSession(req.Header)
			if !ok {
				next.ServeHTTP(w, req)
				return
			}
			w = r.authenticated(w, req)
			defer r.endHandler()
			defer clearWriteDeadline(w)
			r.serveUp(w, req, session)
		case path == "/api/v1/down" && req.Method == http.MethodPost:
			session, ok := r.authenticateSession(req.Header)
			if !ok {
				next.ServeHTTP(w, req)
				return
			}
			w = r.authenticated(w, req)
			defer r.endHandler()
			defer clearWriteDeadline(w)
			r.serveDown(w, req, session)
		case path == "/api/v1/session" && req.Method == http.MethodDelete:
			session, ok := r.authenticateSession(req.Header)
			if !ok {
				next.ServeHTTP(w, req)
				return
			}
			w = r.authenticated(w, req)
			defer r.endHandler()
			defer clearWriteDeadline(w)
			r.serveDelete(w, req, session)
		default:
			next.ServeHTTP(w, req)
		}
	})
}

func (r *Relay) serveBridge(w http.ResponseWriter, req *http.Request) bool {
	const prefix = "bridge="
	raw := req.URL.RawQuery
	if !strings.HasPrefix(raw, prefix) || strings.Contains(raw, "&") {
		return false
	}
	capability := strings.TrimPrefix(raw, prefix)
	if len(capability) != capabilityTextSize {
		return false
	}
	var credentialID string
	var snapshot *capabilityIndex
	var ok bool
	for {
		index := r.index.Load()
		credentialID, ok = index.lookup(capability)
		if !ok {
			return false
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return false
		}
		if r.index.Load() != index {
			r.mu.Unlock()
			continue
		}
		if !r.beginHandlerLocked() {
			r.mu.Unlock()
			return false
		}
		snapshot = index
		r.mu.Unlock()
		break
	}
	defer r.endHandler()
	w = r.authenticated(w, req)
	defer clearWriteDeadline(w)

	token, err := r.issueBootstrap(credentialID, snapshot)
	if err != nil {
		writeUnavailable(w)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set(headerBootstrap, token)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(r.bridgePage(token))
	return true
}

func (r *Relay) authenticateBootstrap(token string) (*bootstrapState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.bootstrap[token]
	if !ok || r.closed || !r.clock.Now().Before(entry.expiresAt) {
		if ok {
			r.removeBootstrapLocked(token, entry)
		}
		return nil, false
	}
	if _, ok := r.credentials[entry.credentialID]; !ok {
		return nil, false
	}
	if !r.beginHandlerLocked() {
		return nil, false
	}
	if !entry.authenticated {
		entry.authenticated = true
		r.removeUnusedBootstrapLocked(entry)
	}
	return entry, true
}

func (r *Relay) authenticateSession(headers http.Header) (*transportSession, bool) {
	token, ok := bearerToken(headers)
	if !ok {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, false
	}
	session := r.sessions[token]
	if session == nil || session.ctx.Err() != nil {
		return nil, false
	}
	if !r.beginHandlerLocked() {
		return nil, false
	}
	return session, true
}

func (r *Relay) authenticated(w http.ResponseWriter, req *http.Request) http.ResponseWriter {
	if r.onAuthenticated != nil {
		r.onAuthenticated(req)
	}
	return &deadlineResponseWriter{ResponseWriter: w}
}

func (r *Relay) serveSession(w http.ResponseWriter, req *http.Request, entry *bootstrapState) {
	if hasCookie(req.Header) {
		writeLocalError(w, http.StatusNotFound)
		return
	}
	if !validBinaryContentType(req.Header) {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	body, err := readAuthenticatedBody(w, req.Body, 64)
	if err != nil {
		writeLocalError(w, http.StatusRequestEntityTooLarge)
		return
	}
	hello, rest, err := parseFrame(body)
	if err != nil || len(rest) != 0 || hello.Type != frameHello || hello.StreamID != 0 || parseHelloPayload(hello.Payload) != nil {
		r.forgetBootstrapEntry(entry)
		writeLocalError(w, http.StatusBadRequest)
		return
	}

	for {
		r.mu.Lock()
		current := r.bootstrap[entry.token]
		if current != entry || r.closed || !r.clock.Now().Before(entry.expiresAt) {
			r.mu.Unlock()
			writeLocalError(w, http.StatusNotFound)
			return
		}
		if entry.body != nil && !bytes.Equal(entry.body, body) {
			r.mu.Unlock()
			writeLocalError(w, http.StatusBadRequest)
			return
		}
		if entry.processing {
			ready := entry.ready
			r.mu.Unlock()
			select {
			case <-req.Context().Done():
				return
			case <-ready:
				continue
			}
		}
		if entry.session != nil {
			if entry.session.ctx.Err() != nil {
				r.mu.Unlock()
				writeLocalError(w, http.StatusNotFound)
				return
			}
			response := append([]byte(nil), entry.responseBody...)
			sessionToken := entry.sessionToken
			r.mu.Unlock()
			writeSessionResponse(w, sessionToken, response)
			return
		}
		if entry.body == nil {
			entry.body = append([]byte(nil), body...)
		}
		entry.processing = true
		entry.ready = make(chan struct{})
		r.mu.Unlock()

		session, sessionToken, createErr := r.createSession(entry.credentialID, entry.credentialSecret)
		response, _ := appendFrame(nil, frame{Type: frameWelcome})
		r.mu.Lock()
		if createErr == nil {
			currentSecrets := r.credentials[entry.credentialID]
			if r.bootstrap[entry.token] != entry || !sameSecrets(currentSecrets, entry.credentialSecret) || session.ctx.Err() != nil {
				r.detachSessionLocked(session)
				session.cancel()
				createErr = errCredentialGone
			} else {
				entry.session = session
				entry.sessionToken = sessionToken
				entry.responseBody = response
			}
		}
		entry.processing = false
		close(entry.ready)
		r.mu.Unlock()

		if errors.Is(createErr, errSessionCapacity) {
			writeUnavailable(w)
			return
		}
		if createErr != nil {
			writeLocalError(w, http.StatusNotFound)
			return
		}
		writeSessionResponse(w, sessionToken, response)
		return
	}
}

func (r *Relay) forgetBootstrapEntry(entry *bootstrapState) {
	r.mu.Lock()
	for token, current := range r.bootstrap {
		if current == entry {
			r.removeBootstrapLocked(token, current)
			break
		}
	}
	r.mu.Unlock()
	r.signalExpiry()
}

func writeSessionResponse(w http.ResponseWriter, token string, body []byte) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set(headerSessionToken, token)
	w.Header().Set(headerDownCursor, "0")
	w.Header().Set(headerCarrierMode, carrierModeHTTPS)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (r *Relay) serveUp(w http.ResponseWriter, req *http.Request, session *transportSession) {
	if hasCookie(req.Header) {
		writeLocalError(w, http.StatusNotFound)
		return
	}
	if !validBinaryContentType(req.Header) {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	sequenceText, ok := oneHeader(req.Header, headerUpSequence)
	if !ok {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	sequence, err := parseCanonicalUint(sequenceText)
	if err != nil || sequence == 0 {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	length := req.ContentLength
	if length < 0 {
		length = maxUpBatchBytes
	}
	if length > maxUpBatchBytes {
		writeLocalError(w, http.StatusRequestEntityTooLarge)
		return
	}
	if err := session.beginUp(sequence); errors.Is(err, errPendingCapacity) {
		writeUnavailable(w)
		return
	} else if errors.Is(err, errRelayClosed) {
		writeLocalError(w, http.StatusNotFound)
		return
	} else if err != nil {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	defer session.finishUp()
	if !r.reservePending(session.credentialID, pendingUp, length) {
		writeUnavailable(w)
		return
	}
	defer r.releasePending(session.credentialID, pendingUp, length)
	body, err := readAuthenticatedBody(w, req.Body, maxUpBatchBytes)
	if err != nil {
		writeLocalError(w, http.StatusRequestEntityTooLarge)
		return
	}
	frames, err := parseFrameBatch(body)
	if err != nil {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	ack, err := session.applyUp(sequence, body, frames, req.RemoteAddr)
	if errors.Is(err, errPendingCapacity) {
		writeUnavailable(w)
		return
	}
	if errors.Is(err, errRelayClosed) {
		writeLocalError(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(headerUpAck, strconv.FormatUint(ack, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (r *Relay) serveDown(w http.ResponseWriter, req *http.Request, session *transportSession) {
	if hasCookie(req.Header) {
		writeLocalError(w, http.StatusNotFound)
		return
	}
	if !validBinaryContentType(req.Header) {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	body, err := readAuthenticatedBody(w, req.Body, 0)
	if err != nil || len(body) != 0 {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	cursorText, ok := oneHeader(req.Header, headerDownCursor)
	if !ok {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	cursor, err := parseCanonicalUint(cursorText)
	if err != nil {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	batch, nextCursor, hasBatch, err := session.down(cursor, req.Context())
	if errors.Is(err, errRelayClosed) {
		writeLocalError(w, http.StatusNotFound)
		return
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	if err != nil {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(headerDownCursor, strconv.FormatUint(nextCursor, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	if !hasBatch {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(batch)
}

func (r *Relay) serveDelete(w http.ResponseWriter, req *http.Request, session *transportSession) {
	if hasCookie(req.Header) {
		writeLocalError(w, http.StatusNotFound)
		return
	}
	body, err := readAuthenticatedBody(w, req.Body, 0)
	if err != nil || len(body) != 0 {
		writeLocalError(w, http.StatusBadRequest)
		return
	}
	session.cancel()
	<-session.done
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
}

func writeLocalError(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
}

func bearerToken(headers http.Header) (string, bool) {
	value, ok := oneHeader(headers, "Authorization")
	if !ok || !strings.HasPrefix(value, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	if !canonicalBase64URL(token, tokenSize) {
		return "", false
	}
	return token, true
}

func oneHeader(headers http.Header, name string) (string, bool) {
	values := headers.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func validBinaryContentType(headers http.Header) bool {
	value, ok := oneHeader(headers, "Content-Type")
	return ok && value == "application/octet-stream"
}

func hasCookie(headers http.Header) bool { return len(headers.Values("Cookie")) != 0 }

func parseCanonicalUint(value string) (uint64, error) {
	if value == "" {
		return 0, fmt.Errorf("empty decimal value")
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != value {
		return 0, fmt.Errorf("noncanonical decimal value")
	}
	return n, nil
}

func readBounded(body io.Reader, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	readLimit := limit + 1
	if limit == 0 {
		readLimit = 1
	}
	data, err := io.ReadAll(io.LimitReader(body, readLimit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, io.ErrShortBuffer
	}
	return data, nil
}

const authenticatedRequestTimeout = 30 * time.Second

func readAuthenticatedBody(w http.ResponseWriter, body io.Reader, limit int64) ([]byte, error) {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(authenticatedRequestTimeout))
	defer controller.SetReadDeadline(time.Time{})
	return readBounded(body, limit)
}

type deadlineResponseWriter struct {
	http.ResponseWriter
	set bool
}

func (w *deadlineResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *deadlineResponseWriter) WriteHeader(status int) {
	w.setDeadline()
	w.ResponseWriter.WriteHeader(status)
}

func (w *deadlineResponseWriter) Write(body []byte) (int, error) {
	w.setDeadline()
	return w.ResponseWriter.Write(body)
}

func (w *deadlineResponseWriter) setDeadline() {
	if w.set {
		return
	}
	w.set = true
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(authenticatedRequestTimeout))
}

func clearWriteDeadline(w http.ResponseWriter) {
	if _, ok := w.(*deadlineResponseWriter); ok {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	}
}
