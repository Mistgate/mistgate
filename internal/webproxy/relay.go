package webproxy

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxSessionsPerCredential = 8
	MaxStreamsPerSession     = 256
	MaxSessionsPerRelay      = 512
	MaxPendingBytes          = 128 << 20
	MaxDownBatch             = 1 << 20
	InitialStreamWindow      = 4 << 20
	BackendChunkSize         = 64 << 10
	RelayIdleTimeout         = 2 * time.Minute
	DownLongPollTimeout      = 25 * time.Second

	maxUpBatchBytes       = 2 << 20
	maxTombstones         = 4096
	maxTokenEntries       = 1 << 16
	maxDownFramesPerBatch = 4096
	pendingFrameOverhead  = 256
)

const carrierModeHTTPS = "https"

var (
	errRelayClosed      = errors.New("webproxy relay is closed")
	errCredentialGone   = errors.New("webproxy credential is unavailable")
	errSessionCapacity  = errors.New("webproxy session capacity reached")
	errPendingCapacity  = errors.New("webproxy pending byte capacity reached")
	errInvalidBootstrap = errors.New("invalid bootstrap request")
)

type Backend func(context.Context, net.Conn, string) error

type BridgePage func(bootstrapToken string) []byte

type RelayTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type RelayClock interface {
	Now() time.Time
	NewTimer(time.Duration) RelayTimer
}

type RelayOptions struct {
	Host            string
	Backend         Backend
	BridgePage      BridgePage
	Random          io.Reader
	Clock           RelayClock
	OnAuthenticated func(*http.Request)
}

type Relay struct {
	mu               sync.Mutex
	setMu            sync.Mutex
	closed           bool
	host             string
	index            *CapabilityIndex
	credentials      map[string]struct{}
	bootstrap        map[string]*bootstrapState
	sessions         map[string]*transportSession
	byCredential     map[string]map[*transportSession]struct{}
	sessionOrdinal   uint64
	tokens           *TokenStore
	backend          Backend
	bridgePage       BridgePage
	onAuthenticated  func(*http.Request)
	clock            RelayClock
	stop             chan struct{}
	expiryWake       chan struct{}
	wg               sync.WaitGroup
	activeGoroutines atomic.Int64
	pendingBytes     atomic.Int64
	pendingChanged   chan struct{}
}

type bootstrapState struct {
	token         string
	credentialID  string
	expiresAt     time.Time
	authenticated bool
	processing    bool
	ready         chan struct{}
	body          []byte
	responseBody  []byte
	sessionToken  string
	session       *transportSession
	finished      bool
}

type realRelayClock struct{}

type realRelayTimer struct{ timer *time.Timer }

func (realRelayClock) Now() time.Time { return time.Now() }

func (realRelayClock) NewTimer(d time.Duration) RelayTimer {
	return realRelayTimer{timer: time.NewTimer(d)}
}

func (t realRelayTimer) C() <-chan time.Time { return t.timer.C }

func (t realRelayTimer) Stop() bool { return t.timer.Stop() }

func NewRelay(options RelayOptions) (*Relay, error) {
	if options.Backend == nil || options.BridgePage == nil {
		return nil, errors.New("webproxy relay requires a backend and bridge page")
	}
	index, err := NewCapabilityIndex(options.Host)
	if err != nil {
		return nil, err
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Clock == nil {
		options.Clock = realRelayClock{}
	}
	tokens, err := NewTokenStore(maxTokenEntries, options.Random, options.Clock.Now)
	if err != nil {
		return nil, err
	}
	r := &Relay{
		host:            index.host,
		index:           index,
		credentials:     make(map[string]struct{}),
		bootstrap:       make(map[string]*bootstrapState),
		sessions:        make(map[string]*transportSession),
		byCredential:    make(map[string]map[*transportSession]struct{}),
		tokens:          tokens,
		backend:         options.Backend,
		bridgePage:      options.BridgePage,
		onAuthenticated: options.OnAuthenticated,
		clock:           options.Clock,
		stop:            make(chan struct{}),
		expiryWake:      make(chan struct{}, 1),
		pendingChanged:  make(chan struct{}, 1),
	}
	r.mu.Lock()
	r.spawnLocked(r.expiryLoop)
	r.mu.Unlock()
	return r, nil
}

// SetCredentials atomically replaces the capability snapshot and retires state
// owned by credentials that are no longer present.
func (r *Relay) SetCredentials(credentials []Credential) error {
	if r == nil {
		return errRelayClosed
	}
	r.setMu.Lock()
	defer r.setMu.Unlock()

	index, err := NewCapabilityIndex(r.host)
	if err != nil {
		return err
	}
	copyCredentials := make([]Credential, len(credentials))
	ids := make(map[string]struct{}, len(credentials))
	for i, credential := range credentials {
		copyCredentials[i] = Credential{ID: credential.ID, Secret: append([]byte(nil), credential.Secret...)}
		ids[credential.ID] = struct{}{}
	}
	if err := index.Rebuild(copyCredentials); err != nil {
		return err
	}

	var retiring []*transportSession
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errRelayClosed
	}
	r.index = index
	r.credentials = ids
	for token, entry := range r.bootstrap {
		if _, ok := ids[entry.credentialID]; !ok {
			delete(r.bootstrap, token)
			r.tokens.Delete(token)
		}
	}
	for credentialID, sessions := range r.byCredential {
		if _, ok := ids[credentialID]; ok {
			continue
		}
		for session := range sessions {
			r.detachSessionLocked(session)
			session.cancel()
			retiring = append(retiring, session)
		}
	}
	r.mu.Unlock()
	r.signalExpiry()
	for _, session := range retiring {
		<-session.done
	}
	return nil
}

// CloseCredential terminates all streams for a credential while leaving its
// authenticated transport sessions ready for later streams.
func (r *Relay) CloseCredential(credentialID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	sessions := make([]*transportSession, 0, len(r.byCredential[credentialID]))
	for session := range r.byCredential[credentialID] {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()

	var streams []*webStream
	for _, session := range sessions {
		streams = append(streams, session.closeAllStreams(true)...)
	}
	for _, stream := range streams {
		<-stream.done
	}
}

// Close stops admission, ends all sessions, and waits for every owned worker.
func (r *Relay) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.wg.Wait()
		return nil
	}
	r.closed = true
	close(r.stop)
	for _, session := range r.sessions {
		session.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}

// ActiveGoroutines reports relay-owned workers that have not returned yet.
func (r *Relay) ActiveGoroutines() int64 {
	if r == nil {
		return 0
	}
	return r.activeGoroutines.Load()
}

func (r *Relay) spawnLocked(fn func()) bool {
	if r.closed {
		return false
	}
	r.wg.Add(1)
	r.activeGoroutines.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.activeGoroutines.Add(-1)
		fn()
	}()
	return true
}

func (r *Relay) spawn(fn func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spawnLocked(fn)
}

func (r *Relay) beginHandlerLocked() bool {
	if r.closed {
		return false
	}
	r.wg.Add(1)
	r.activeGoroutines.Add(1)
	return true
}

func (r *Relay) endHandler() {
	r.activeGoroutines.Add(-1)
	r.wg.Done()
}

func (r *Relay) issueBootstrap(credentialID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", errRelayClosed
	}
	if _, ok := r.credentials[credentialID]; !ok {
		return "", errCredentialGone
	}
	token, err := r.tokens.Issue(TokenBootstrap, BootstrapTokenTTL)
	if err != nil {
		return "", err
	}
	r.bootstrap[token] = &bootstrapState{
		token:        token,
		credentialID: credentialID,
		expiresAt:    r.clock.Now().Add(BootstrapTokenTTL),
	}
	r.signalExpiryLocked()
	return token, nil
}

func (r *Relay) createSession(credentialID string) (*transportSession, string, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, "", errRelayClosed
	}
	if _, ok := r.credentials[credentialID]; !ok {
		r.mu.Unlock()
		return nil, "", errCredentialGone
	}

	var evicted *transportSession
	owned := r.byCredential[credentialID]
	if len(owned) >= MaxSessionsPerCredential {
		for session := range owned {
			if evicted == nil || session.ordinal < evicted.ordinal {
				evicted = session
			}
		}
		if evicted != nil {
			r.detachSessionLocked(evicted)
			evicted.cancel()
		}
	}
	if len(r.sessions) >= MaxSessionsPerRelay {
		r.mu.Unlock()
		if evicted != nil {
			<-evicted.done
		}
		return nil, "", errSessionCapacity
	}
	token, err := r.tokens.Issue(TokenSession, 100*365*24*time.Hour)
	if err != nil {
		r.mu.Unlock()
		if evicted != nil {
			<-evicted.done
		}
		return nil, "", errSessionCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.sessionOrdinal++
	session := &transportSession{
		relay:        r,
		credentialID: credentialID,
		token:        token,
		createdAt:    r.clock.Now(),
		ordinal:      r.sessionOrdinal,
		lastPoll:     r.clock.Now(),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		streams:      make(map[uint32]*webStream),
		tombstones:   make(map[uint32]struct{}),
		outSignal:    make(chan struct{}, 1),
	}
	r.sessions[token] = session
	if r.byCredential[credentialID] == nil {
		r.byCredential[credentialID] = make(map[*transportSession]struct{})
	}
	r.byCredential[credentialID][session] = struct{}{}
	if !r.spawnLocked(session.run) {
		r.detachSessionLocked(session)
		cancel()
		r.mu.Unlock()
		return nil, "", errRelayClosed
	}
	r.mu.Unlock()
	r.signalExpiry()
	if evicted != nil {
		<-evicted.done
	}
	return session, token, nil
}

func (r *Relay) detachSessionLocked(session *transportSession) {
	if session == nil {
		return
	}
	if current, ok := r.sessions[session.token]; !ok || current != session {
		return
	}
	delete(r.sessions, session.token)
	r.tokens.Delete(session.token)
	owned := r.byCredential[session.credentialID]
	delete(owned, session)
	if len(owned) == 0 {
		delete(r.byCredential, session.credentialID)
	}
	for _, entry := range r.bootstrap {
		if entry.session == session {
			entry.session = nil
			entry.finished = true
		}
	}
}

func (r *Relay) removeSession(session *transportSession) {
	r.mu.Lock()
	r.detachSessionLocked(session)
	r.mu.Unlock()
	r.signalExpiry()
}

func (r *Relay) reservePending(bytes int64) bool {
	if bytes <= 0 {
		return true
	}
	for {
		current := r.pendingBytes.Load()
		if bytes > MaxPendingBytes-current {
			return false
		}
		if r.pendingBytes.CompareAndSwap(current, current+bytes) {
			r.notifyPending()
			return true
		}
	}
}

func (r *Relay) releasePending(bytes int64) {
	if bytes <= 0 {
		return
	}
	remaining := r.pendingBytes.Add(-bytes)
	if remaining < 0 {
		panic("webproxy pending byte accounting underflow")
	}
	r.notifyPending()
}

func (r *Relay) notifyPending() {
	select {
	case r.pendingChanged <- struct{}{}:
	default:
	}
}

func (r *Relay) waitForPending(ctx context.Context, bytes int64) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		if r.reservePending(bytes) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-r.pendingChanged:
		}
	}
}

func (r *Relay) signalExpiry() {
	select {
	case r.expiryWake <- struct{}{}:
	default:
	}
}

func (r *Relay) signalExpiryLocked() { r.signalExpiry() }

func (r *Relay) expiryLoop() {
	for {
		r.mu.Lock()
		now := r.clock.Now()
		var earliest time.Time
		for token, entry := range r.bootstrap {
			if !now.Before(entry.expiresAt) {
				delete(r.bootstrap, token)
				r.tokens.Delete(token)
				continue
			}
			if earliest.IsZero() || entry.expiresAt.Before(earliest) {
				earliest = entry.expiresAt
			}
		}
		for _, session := range r.sessions {
			if session.ctx.Err() != nil {
				continue
			}
			session.mu.Lock()
			deadline := session.lastPoll.Add(RelayIdleTimeout)
			session.mu.Unlock()
			if !now.Before(deadline) {
				session.cancel()
				continue
			}
			if earliest.IsZero() || deadline.Before(earliest) {
				earliest = deadline
			}
		}
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return
		}
		if earliest.IsZero() {
			select {
			case <-r.stop:
				return
			case <-r.expiryWake:
			}
			continue
		}
		d := earliest.Sub(r.clock.Now())
		if d < 0 {
			d = 0
		}
		timer := r.clock.NewTimer(d)
		select {
		case <-r.stop:
			timer.Stop()
			return
		case <-r.expiryWake:
			timer.Stop()
		case <-timer.C():
			timer.Stop()
		}
	}
}
