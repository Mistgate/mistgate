package webproxy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxSessionsPerCredential = 8
	maxStreamsPerSession     = 256
	maxSessionsPerRelay      = 512
	maxPendingBytes          = 128 << 20
	maxPendingUpBytes        = 64 << 20
	maxPendingDownBytes      = 64 << 20
	maxPendingPerCredential  = 16 << 20
	maxDownBatch             = 1 << 20
	initialStreamWindow      = 4 << 20
	backendChunkSize         = 64 << 10
	relayIdleTimeout         = 2 * time.Minute
	downLongPollTimeout      = 25 * time.Second
	bootstrapTokenTTL        = 2 * time.Minute
	maxUnusedBootstraps      = 8
	tokenSize                = 32
	tokenTextSize            = 43
	maxUpBatchBytes          = 2 << 20
	maxTombstones            = 4096
	maxTokenRetries          = 8
	maxDownFramesPerBatch    = 4096
	pendingFrameOverhead     = 256
)

const carrierModeHTTPS = "https"

var (
	errRelayClosed     = errors.New("webproxy relay is closed")
	errCredentialGone  = errors.New("webproxy credential is unavailable")
	errSessionCapacity = errors.New("webproxy session capacity reached")
	errPendingCapacity = errors.New("webproxy pending byte capacity reached")
	errTokenRandom     = errors.New("unable to generate token")
	errTokenCollision  = errors.New("unable to generate unique token")
)

type Backend func(context.Context, net.Conn, string) error

type BridgePage func(bootstrapToken string) []byte

type relayTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type relayClock interface {
	Now() time.Time
	NewTimer(time.Duration) relayTimer
}

type RelayOptions struct {
	Host            string
	Backend         Backend
	BridgePage      BridgePage
	Random          io.Reader
	Clock           relayClock
	OnAuthenticated func(*http.Request)
}

type Relay struct {
	mu                     sync.Mutex
	setMu                  sync.Mutex
	closed                 bool
	host                   string
	index                  atomic.Pointer[capabilityIndex]
	credentials            map[string]map[[plainSecretSize]byte]struct{}
	bootstrap              map[string]*bootstrapState
	unusedBootstrap        map[string][]*bootstrapState
	sessions               map[string]*transportSession
	byCredential           map[string]map[*transportSession]struct{}
	sessionOrdinal         uint64
	random                 io.Reader
	backend                Backend
	bridgePage             BridgePage
	onAuthenticated        func(*http.Request)
	clock                  relayClock
	stop                   chan struct{}
	expiryWake             chan struct{}
	wg                     sync.WaitGroup
	pendingBytes           atomic.Int64
	pendingMu              sync.Mutex
	pendingUpBytes         int64
	pendingDownBytes       int64
	pendingByCredential    map[string]int64
	pendingChanged         chan struct{}
	pendingUnderflow       atomic.Bool
	pendingUnderflowLogged atomic.Bool
}

type bootstrapState struct {
	token            string
	credentialID     string
	credentialSecret map[[plainSecretSize]byte]struct{}
	expiresAt        time.Time
	authenticated    bool
	processing       bool
	ready            chan struct{}
	body             []byte
	responseBody     []byte
	sessionToken     string
	session          *transportSession
}

type realRelayClock struct{}

type realRelayTimer struct{ timer *time.Timer }

func (realRelayClock) Now() time.Time { return time.Now() }

func (realRelayClock) NewTimer(d time.Duration) relayTimer {
	return realRelayTimer{timer: time.NewTimer(d)}
}

func (t realRelayTimer) C() <-chan time.Time { return t.timer.C }

func (t realRelayTimer) Stop() bool { return t.timer.Stop() }

func NewRelay(options RelayOptions) (*Relay, error) {
	if options.Backend == nil || options.BridgePage == nil {
		return nil, errors.New("webproxy relay requires a backend and bridge page")
	}
	index, err := buildCapabilityIndex(options.Host, nil)
	if err != nil {
		return nil, err
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Clock == nil {
		options.Clock = realRelayClock{}
	}
	r := &Relay{
		host:                index.host,
		credentials:         make(map[string]map[[plainSecretSize]byte]struct{}),
		bootstrap:           make(map[string]*bootstrapState),
		unusedBootstrap:     make(map[string][]*bootstrapState),
		sessions:            make(map[string]*transportSession),
		byCredential:        make(map[string]map[*transportSession]struct{}),
		random:              options.Random,
		backend:             options.Backend,
		bridgePage:          options.BridgePage,
		onAuthenticated:     options.OnAuthenticated,
		clock:               options.Clock,
		stop:                make(chan struct{}),
		expiryWake:          make(chan struct{}, 1),
		pendingByCredential: make(map[string]int64),
		pendingChanged:      make(chan struct{}),
	}
	r.index.Store(index)
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

	copyCredentials := make([]Credential, len(credentials))
	identities := make(map[string]map[[plainSecretSize]byte]struct{}, len(credentials))
	for i, credential := range credentials {
		copyCredentials[i] = Credential{ID: credential.ID, Secret: append([]byte(nil), credential.Secret...)}
		plain, ok := plainSecret(credential.Secret)
		if !ok {
			return errInvalidSecret
		}
		if identities[credential.ID] == nil {
			identities[credential.ID] = make(map[[plainSecretSize]byte]struct{})
		}
		identities[credential.ID][plain] = struct{}{}
	}
	index, err := buildCapabilityIndex(r.host, copyCredentials)
	if err != nil {
		return err
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errRelayClosed
	}
	if len(r.credentials) == len(identities) {
		unchanged := true
		for id, oldSecrets := range r.credentials {
			if newSecrets, exists := identities[id]; !exists || !sameSecrets(oldSecrets, newSecrets) {
				unchanged = false
				break
			}
		}
		if unchanged {
			r.mu.Unlock()
			return nil
		}
	}
	changed := make(map[string]struct{})
	for id, oldSecrets := range r.credentials {
		newSecrets, exists := identities[id]
		if exists && !sameSecrets(oldSecrets, newSecrets) {
			changed[id] = struct{}{}
		}
	}
	r.index.Store(index)
	r.credentials = identities
	for token, entry := range r.bootstrap {
		_, exists := identities[entry.credentialID]
		_, rotated := changed[entry.credentialID]
		if !exists || rotated {
			r.removeBootstrapLocked(token, entry)
		}
	}
	for credentialID, sessions := range r.byCredential {
		_, exists := identities[credentialID]
		_, rotated := changed[credentialID]
		if exists && !rotated {
			continue
		}
		for session := range sessions {
			r.detachSessionLocked(session)
			session.cancel()
		}
	}
	r.mu.Unlock()
	r.signalExpiry()
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

	for _, session := range sessions {
		session.closeAllStreams(true)
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

func (r *Relay) spawnLocked(fn func()) bool {
	if r.closed {
		return false
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
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
	return true
}

func (r *Relay) endHandler() {
	r.wg.Done()
}

func (r *Relay) issueBootstrap(credentialID string, snapshot *capabilityIndex) (string, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", errRelayClosed
	}
	if r.index.Load() != snapshot {
		r.mu.Unlock()
		return "", errCredentialGone
	}
	if _, ok := r.credentials[credentialID]; !ok {
		r.mu.Unlock()
		return "", errCredentialGone
	}
	token, err := r.newTokenLocked()
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	unused := r.unusedBootstrap[credentialID]
	for len(unused) > 0 && !r.clock.Now().Before(unused[0].expiresAt) {
		oldest := unused[0]
		r.removeBootstrapLocked(oldest.token, oldest)
		unused = r.unusedBootstrap[credentialID]
	}
	if len(unused) >= maxUnusedBootstraps {
		oldest := unused[0]
		r.removeBootstrapLocked(oldest.token, oldest)
	}
	entry := &bootstrapState{
		token:            token,
		credentialID:     credentialID,
		credentialSecret: r.credentials[credentialID],
		expiresAt:        r.clock.Now().Add(bootstrapTokenTTL),
	}
	r.bootstrap[token] = entry
	r.unusedBootstrap[credentialID] = append(r.unusedBootstrap[credentialID], entry)
	r.mu.Unlock()
	r.signalExpiry()
	return token, nil
}

func randomToken(random io.Reader) (string, error) {
	var raw [tokenSize]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", errTokenRandom
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (r *Relay) newTokenLocked() (string, error) {
	for attempt := 0; attempt < maxTokenRetries; attempt++ {
		token, err := randomToken(r.random)
		if err != nil {
			return "", err
		}
		if _, exists := r.bootstrap[token]; exists {
			continue
		}
		if _, exists := r.sessions[token]; exists {
			continue
		}
		return token, nil
	}
	return "", errTokenCollision
}

func sameSecrets(left, right map[[plainSecretSize]byte]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for secret := range left {
		if _, exists := right[secret]; !exists {
			return false
		}
	}
	return true
}

func (r *Relay) removeBootstrapLocked(token string, entry *bootstrapState) {
	if r.bootstrap[token] == entry {
		delete(r.bootstrap, token)
	}
	r.removeUnusedBootstrapLocked(entry)
}

func (r *Relay) removeUnusedBootstrapLocked(entry *bootstrapState) {
	if entry == nil || entry.authenticated {
		return
	}
	entries := r.unusedBootstrap[entry.credentialID]
	for i, current := range entries {
		if current == entry {
			copy(entries[i:], entries[i+1:])
			entries[len(entries)-1] = nil
			entries = entries[:len(entries)-1]
			if len(entries) == 0 {
				delete(r.unusedBootstrap, entry.credentialID)
			} else {
				r.unusedBootstrap[entry.credentialID] = entries
			}
			return
		}
	}
}

func (r *Relay) createSession(credentialID string, credentialSecret map[[plainSecretSize]byte]struct{}) (*transportSession, string, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, "", errRelayClosed
	}
	currentSecrets, ok := r.credentials[credentialID]
	if !ok || !sameSecrets(currentSecrets, credentialSecret) {
		r.mu.Unlock()
		return nil, "", errCredentialGone
	}

	var evicted *transportSession
	owned := r.byCredential[credentialID]
	if len(owned) >= maxSessionsPerCredential {
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
	if len(r.sessions) >= maxSessionsPerRelay {
		r.mu.Unlock()
		return nil, "", errSessionCapacity
	}
	token, err := r.newTokenLocked()
	if err != nil {
		r.mu.Unlock()
		return nil, "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.sessionOrdinal++
	session := &transportSession{
		relay:          r,
		credentialID:   credentialID,
		token:          token,
		ordinal:        r.sessionOrdinal,
		lastPoll:       atomic.Int64{},
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
		streams:        make(map[uint32]*webStream),
		tombstones:     make(map[uint32]struct{}),
		outSignal:      make(chan struct{}),
		pendingWindows: make(map[uint32]uint32),
		pendingCloses:  make(map[uint32]struct{}),
	}
	session.lastPoll.Store(r.clock.Now().UnixNano())
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
	owned := r.byCredential[session.credentialID]
	delete(owned, session)
	if len(owned) == 0 {
		delete(r.byCredential, session.credentialID)
	}
	for token, entry := range r.bootstrap {
		if entry.session == session {
			r.removeBootstrapLocked(token, entry)
		}
	}
}

func (r *Relay) removeSession(session *transportSession) {
	r.mu.Lock()
	r.detachSessionLocked(session)
	r.mu.Unlock()
	r.signalExpiry()
}

type pendingDirection uint8

const (
	pendingUp pendingDirection = iota
	pendingDown
)

func (r *Relay) reservePending(credentialID string, direction pendingDirection, bytes int64) bool {
	if bytes <= 0 {
		return true
	}
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	if !r.canReservePendingLocked(credentialID, direction, bytes) {
		return false
	}
	r.addPendingLocked(credentialID, direction, bytes)
	return true
}

func (r *Relay) releasePending(credentialID string, direction pendingDirection, bytes int64) {
	if bytes <= 0 {
		return
	}
	r.pendingMu.Lock()
	underflow := false
	used := r.pendingByCredential[credentialID]
	if bytes > used {
		bytes = used
		underflow = true
	}
	var directionUsed *int64
	if direction == pendingUp {
		directionUsed = &r.pendingUpBytes
	} else {
		directionUsed = &r.pendingDownBytes
	}
	if bytes > *directionUsed {
		bytes = *directionUsed
		underflow = true
	}
	*directionUsed -= bytes
	if used-bytes == 0 {
		delete(r.pendingByCredential, credentialID)
	} else {
		r.pendingByCredential[credentialID] = used - bytes
	}
	r.pendingBytes.Add(-bytes)
	r.notifyPendingLocked()
	r.pendingMu.Unlock()
	if underflow && r.pendingUnderflow.CompareAndSwap(false, true) {
		r.signalExpiry()
	}
}

func (r *Relay) waitForPending(ctx context.Context, credentialID string, direction pendingDirection, bytes int64) bool {
	for {
		r.pendingMu.Lock()
		if ctx.Err() != nil {
			r.pendingMu.Unlock()
			return false
		}
		if r.canReservePendingLocked(credentialID, direction, bytes) {
			r.addPendingLocked(credentialID, direction, bytes)
			r.pendingMu.Unlock()
			return true
		}
		changed := r.pendingChanged
		r.pendingMu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-changed:
		}
	}
}

func (r *Relay) canReservePendingLocked(credentialID string, direction pendingDirection, bytes int64) bool {
	if bytes > maxPendingBytes-r.pendingBytes.Load() || bytes > maxPendingPerCredential-r.pendingByCredential[credentialID] {
		return false
	}
	if direction == pendingUp {
		return bytes <= maxPendingUpBytes-r.pendingUpBytes
	}
	return bytes <= maxPendingDownBytes-r.pendingDownBytes
}

func (r *Relay) addPendingLocked(credentialID string, direction pendingDirection, bytes int64) {
	if direction == pendingUp {
		r.pendingUpBytes += bytes
	} else {
		r.pendingDownBytes += bytes
	}
	r.pendingByCredential[credentialID] += bytes
	r.pendingBytes.Add(bytes)
}

func (r *Relay) notifyPendingLocked() {
	close(r.pendingChanged)
	r.pendingChanged = make(chan struct{})
}

func (r *Relay) signalExpiry() {
	select {
	case r.expiryWake <- struct{}{}:
	default:
	}
}

func (r *Relay) expiryLoop() {
	for {
		r.mu.Lock()
		now := r.clock.Now()
		var earliest time.Time
		for token, entry := range r.bootstrap {
			if !now.Before(entry.expiresAt) {
				r.removeBootstrapLocked(token, entry)
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
			deadline := time.Unix(0, session.lastPoll.Load()).Add(relayIdleTimeout)
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
		if r.pendingUnderflow.Load() && r.pendingUnderflowLogged.CompareAndSwap(false, true) {
			log.Printf("webproxy: pending-byte accounting underflow; clamped to zero")
		}
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
