package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxStreamsPerExit       = 4096
	MaxStreamsPerEntry      = 8192
	MaxConnectionsPerExit   = 2 * MaxPoolSize
	AcceptQueueSize         = 1024 // a burst of people arrives faster than the listener hands them out
	PendingStreamsPerExit   = 256
	MaxPendingClients       = 4096
	MaxClientsPerEntry      = 8192
	ClientWaitForStream     = time.Second
	BridgeStreamIdleTimeout = 2 * time.Minute
	BridgeRequestIdle       = 30 * time.Second
	BridgePairTimeout       = 5 * time.Second
	BridgeProgressTimeout   = 30 * time.Second
	maxBridgeListeners      = 64
)

var (
	bridgePathPattern = regexp.MustCompile("^/[A-Za-z0-9_./-]{1,240}$")
	relayIDPattern    = regexp.MustCompile("^[A-Za-z0-9_.-]{1,64}$")
	nodeIDPattern     = regexp.MustCompile("^[A-Za-z0-9_.-]{1,128}$")
	pairIDPattern     = regexp.MustCompile("^[a-f0-9]{32}$")
)

type ServeConfig struct {
	EntryNodeID string
	Path        string
	ExitNodeIDs []string
	Roots       *x509.CertPool
	Now         func() time.Time
}

type RelayListener struct {
	RelayID    string
	ExitNodeID string
	Port       uint16
	Address    string // Only tests set an address; production listens on all local addresses.
}

type PeerState struct {
	PeerNodeID string
	Conns      uint32
	Since      time.Time
	Streams    uint64
	BytesIn    uint64
	BytesOut   uint64
	Error      string
	// Clients is how many people the bridge carries now: pairs a person's connection was handed to and that have not ended.
	// Streams only counts up, and the waiting pairs are not people.
	Clients uint32
}

type RelayCounters struct {
	Bytes     uint64 // client to the exit's server
	BytesBack uint64 // exit's server to the client
}

type entryConn struct {
	active int
}

type cachedProof struct {
	exitID   string
	certHash [32]byte
	sigHash  [32]byte
	notAfter time.Time
	last     time.Time
}

type entryPeer struct {
	id           string
	wait         *pendingQueue
	connections  map[[32]byte]entryConn
	pairs        map[pairKey]*bridgePair
	active       uint64
	clientActive uint64
	streams      uint64
	bytesIn      uint64
	bytesOut     uint64
	since        time.Time
	lastError    string
}

type pairRole string

const (
	pairDown pairRole = "down"
	pairUp   pairRole = "up"
)

type pairKey struct {
	connection [32]byte
	id         string
}

type bridgePair struct {
	peer      *entryPeer
	key       pairKey
	ctx       context.Context
	cancel    context.CancelFunc
	ready     chan struct{}
	attached  chan struct{}
	done      chan struct{}
	halves    map[pairRole]bool
	completed map[pairRole]bool
	paired    bool
	active    bool
	finished  bool
	err       string
	conn      net.Conn
	relayID   string
	pairTimer *time.Timer
	idleTimer *time.Timer
	progress  *time.Timer
	progressN uint64
}

type pendingQueue struct {
	mu    sync.Mutex
	items []*bridgePair
	wake  chan struct{}
}

func newPendingQueue() *pendingQueue { return &pendingQueue{wake: make(chan struct{}, 1)} }

func (q *pendingQueue) offer(pair *bridgePair) bool {
	q.mu.Lock()
	kept := q.items[:0]
	for _, item := range q.items {
		if !pairDone(item) {
			kept = append(kept, item)
		}
	}
	q.items = kept
	if len(q.items) >= PendingStreamsPerExit {
		q.mu.Unlock()
		return false
	}
	q.items = append(q.items, pair)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *pendingQueue) take(ctx context.Context, timeout time.Duration) (*bridgePair, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		q.mu.Lock()
		for len(q.items) > 0 {
			last := len(q.items) - 1
			pair := q.items[last]
			q.items[last] = nil
			q.items = q.items[:last]
			if !pairDone(pair) {
				q.mu.Unlock()
				return pair, nil
			}
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, context.DeadlineExceeded
		}
	}
}

func pairDone(pair *bridgePair) bool {
	if pair == nil {
		return true
	}
	select {
	case <-pair.done:
		return true
	default:
		return false
	}
}

type entryTiming struct {
	pairWait       time.Duration
	pendingWait    time.Duration
	pendingJitter  time.Duration
	progress       time.Duration
	streamIdle     time.Duration
	acceptRetryMin time.Duration
	acceptRetryMax time.Duration
}

func defaultEntryTiming() entryTiming {
	return entryTiming{
		pairWait:       BridgePairTimeout,
		pendingWait:    BridgeRequestIdle,
		pendingJitter:  BridgeRequestIdle / 5,
		progress:       BridgeProgressTimeout,
		streamIdle:     BridgeStreamIdleTimeout,
		acceptRetryMin: 5 * time.Millisecond,
		acceptRetryMax: time.Second,
	}
}

type listenerState struct {
	config RelayListener
	ln     net.Listener
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan net.Conn
	mu     sync.Mutex
	active map[net.Conn]struct{}
	wg     sync.WaitGroup
}

func (s *listenerState) add(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	s.active[conn] = struct{}{}
	return true
}

func (s *listenerState) remove(conn net.Conn) {
	s.mu.Lock()
	delete(s.active, conn)
	s.mu.Unlock()
}

func (s *listenerState) stop() {
	s.cancel()
	_ = s.ln.Close()
	s.mu.Lock()
	active := make([]net.Conn, 0, len(s.active))
	for conn := range s.active {
		active = append(active, conn)
	}
	s.mu.Unlock()
	for _, conn := range active {
		rejectConn(conn)
	}
	s.wg.Wait()
}

type Entry struct {
	mu            sync.RWMutex
	serve         ServeConfig
	peers         map[string]*entryPeer
	retired       map[string]PeerState
	relayCounters map[string]RelayCounters
	authCache     map[[32]byte]cachedProof
	closed        atomic.Bool
	totalStreams  uint64
	totalClients  uint64
	logged        map[string]time.Time
	log           *slog.Logger
	timing        entryTiming

	listenerSyncMu sync.Mutex
	listenerMu     sync.Mutex
	listeners      map[string]*listenerState

	// beforeAttach is a deterministic seam for the stream-attach race test.
	beforeAttach func(*bridgePair)
}

func NewEntry() *Entry {
	return &Entry{
		peers:         map[string]*entryPeer{},
		retired:       map[string]PeerState{},
		relayCounters: map[string]RelayCounters{},
		authCache:     map[[32]byte]cachedProof{},
		logged:        map[string]time.Time{},
		log:           slog.Default(),
		timing:        defaultEntryTiming(),
		listeners:     map[string]*listenerState{},
	}
}

func (e *Entry) SetLogger(log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	e.mu.Lock()
	e.log = log
	e.mu.Unlock()
}

func (e *Entry) SetServe(cfg ServeConfig) error {
	if cfg.Path == "" && cfg.EntryNodeID == "" && len(cfg.ExitNodeIDs) == 0 {
		e.mu.Lock()
		if e.closed.Load() {
			e.mu.Unlock()
			return errors.New("closed")
		}
		e.serve = ServeConfig{}
		clear(e.authCache)
		peers := make([]string, 0, len(e.peers))
		for id := range e.peers {
			peers = append(peers, id)
		}
		e.mu.Unlock()
		e.listenerMu.Lock()
		for _, listener := range e.listeners {
			peers = append(peers, listener.config.ExitNodeID)
		}
		e.listenerMu.Unlock()
		peers = uniqueStrings(peers)
		for _, id := range peers {
			e.dropPeer(id, true)
		}
		return nil
	}
	if !nodeIDPattern.MatchString(cfg.EntryNodeID) || !validBridgePath(cfg.Path) || cfg.Roots == nil || len(cfg.ExitNodeIDs) == 0 {
		return errors.New("bad_config")
	}
	ids := append([]string(nil), cfg.ExitNodeIDs...)
	sort.Strings(ids)
	allowed := ids[:0]
	for _, id := range ids {
		if !nodeIDPattern.MatchString(id) || (len(allowed) > 0 && allowed[len(allowed)-1] == id) {
			return errors.New("bad_config")
		}
		allowed = append(allowed, id)
	}
	if len(allowed) > maxBridgeListeners {
		return errors.New("bad_config")
	}
	cfg.ExitNodeIDs = append([]string(nil), allowed...)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	e.mu.Lock()
	if e.closed.Load() {
		e.mu.Unlock()
		return errors.New("closed")
	}
	old := e.serve
	identityChanged := old.EntryNodeID != cfg.EntryNodeID || old.Path != cfg.Path || old.Roots != cfg.Roots
	removed := make([]string, 0)
	for id := range e.peers {
		if identityChanged || !slicesContains(cfg.ExitNodeIDs, id) {
			removed = append(removed, id)
		}
	}
	if !sameServe(old, cfg) {
		for key, cached := range e.authCache {
			if identityChanged || !slicesContains(cfg.ExitNodeIDs, cached.exitID) {
				delete(e.authCache, key)
			}
		}
	}
	e.serve = cfg
	e.mu.Unlock()
	e.listenerMu.Lock()
	for _, listener := range e.listeners {
		if identityChanged || !slicesContains(cfg.ExitNodeIDs, listener.config.ExitNodeID) {
			removed = append(removed, listener.config.ExitNodeID)
		}
	}
	e.listenerMu.Unlock()
	removed = uniqueStrings(removed)
	for _, id := range removed {
		e.dropPeer(id, true)
	}
	return nil
}

func sameServe(a, b ServeConfig) bool {
	if a.EntryNodeID != b.EntryNodeID || a.Path != b.Path || a.Roots != b.Roots || len(a.ExitNodeIDs) != len(b.ExitNodeIDs) {
		return false
	}
	for i := range a.ExitNodeIDs {
		if a.ExitNodeIDs[i] != b.ExitNodeIDs[i] {
			return false
		}
	}
	return true
}

func validBridgePath(path string) bool {
	return bridgePathPattern.MatchString(path) && !strings.Contains(path, "..") && !strings.ContainsAny(path, "?#%")
}

// Handler adds the private bridge route to a site without changing ordinary site responses.
func (e *Entry) Handler(site http.Handler) http.Handler {
	if site == nil {
		site = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := e.serveSnapshot()
		role := pairRole(r.Header.Get(StreamHeader))
		pairID := r.Header.Get(PairIDHeader)
		if cfg.Path == "" || r.URL.RawQuery != "" || r.URL.Fragment != "" ||
			!sameSecretPath(r.URL.Path, cfg.Path) || r.Method != http.MethodPost || r.Header.Get(ProtocolHeader) != protocolVersion ||
			(role != pairDown && role != pairUp) || !pairIDPattern.MatchString(pairID) ||
			(role == pairUp && (r.ContentLength > 0 || len(r.TransferEncoding) > 0)) {
			site.ServeHTTP(w, r)
			return
		}
		exporter, err := exporterFor(r.TLS)
		if err != nil {
			site.ServeHTTP(w, r)
			return
		}
		proof, err := proofFromHeaders(r.Header.Get(CertificateHeader), r.Header.Get(SignatureHeader))
		if err != nil {
			site.ServeHTTP(w, r)
			return
		}
		allowed := make(map[string]struct{}, len(cfg.ExitNodeIDs))
		for _, id := range cfg.ExitNodeIDs {
			allowed[id] = struct{}{}
		}
		now := cfg.Now()
		exitID, err := e.verifyRequest(cfg.EntryNodeID, exporter, proof, cfg.Roots, allowed, now)
		if err != nil {
			site.ServeHTTP(w, r)
			return
		}
		markBridgeConnection(r.Context())
		peer := e.peer(exitID)
		if peer == nil {
			e.writeBridgeError(w, exitID, "closed")
			return
		}
		if err := clearSiteDeadlines(w); err != nil {
			e.setPeerError(exitID, "timeout_unsupported")
			e.writeBridgeError(w, exitID, "timeout_unsupported")
			return
		}
		key := sha256.Sum256(exporter)
		if code := e.acquireHalf(peer, key, now); code != "" {
			e.setPeerError(exitID, code)
			e.writeBridgeError(w, exitID, code)
			return
		}
		pair, paired, code := e.joinPair(peer, pairKey{connection: key, id: pairID}, role)
		if code != "" {
			e.releaseHalf(peer, key)
			e.setPeerError(exitID, code)
			e.writeBridgeError(w, exitID, code)
			return
		}
		defer e.releaseHalf(peer, key)
		if paired && !e.offerPair(pair) {
			e.setPeerError(exitID, "queue_full")
			e.finishPair(pair, "queue_full", true)
		}
		e.servePairHalf(w, r, pair, role)
	})
}

func clearSiteDeadlines(w http.ResponseWriter) error {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	return rc.SetWriteDeadline(time.Time{})
}

func (e *Entry) serveSnapshot() ServeConfig {
	e.mu.RLock()
	cfg := e.serve
	cfg.ExitNodeIDs = append([]string(nil), cfg.ExitNodeIDs...)
	e.mu.RUnlock()
	return cfg
}

func (e *Entry) verifyRequest(entryID string, exporter []byte, proof Proof, roots *x509.CertPool, allowed map[string]struct{}, now time.Time) (string, error) {
	key := sha256.Sum256(exporter)
	certHash := sha256.Sum256(proof.CertificateDER)
	sigHash := sha256.Sum256(proof.Signature)
	e.mu.Lock()
	for k, cached := range e.authCache {
		if now.Sub(cached.last) > BridgeRequestIdle || !now.Before(cached.notAfter) {
			delete(e.authCache, k)
		}
	}
	if cached, ok := e.authCache[key]; ok {
		_, allowedOK := allowed[cached.exitID]
		certOK := subtle.ConstantTimeCompare(certHash[:], cached.certHash[:]) == 1
		sigOK := subtle.ConstantTimeCompare(sigHash[:], cached.sigHash[:]) == 1
		if allowedOK && certOK && sigOK && now.Before(cached.notAfter) {
			cached.last = now
			e.authCache[key] = cached
			e.mu.Unlock()
			return cached.exitID, nil
		}
		delete(e.authCache, key)
		e.mu.Unlock()
		return "", errBadProof
	}
	e.mu.Unlock()

	exitID, err := VerifyProof(entryID, exporter, proof, roots, allowed, now)
	if err != nil {
		return "", err
	}
	leaf, err := x509.ParseCertificate(proof.CertificateDER)
	if err != nil {
		return "", errBadProof
	}
	e.mu.Lock()
	if len(e.authCache) >= maxBridgeListeners*MaxConnectionsPerExit {
		for k, cached := range e.authCache {
			if now.Sub(cached.last) > BridgeRequestIdle || !now.Before(cached.notAfter) {
				delete(e.authCache, k)
			}
		}
	}
	if len(e.authCache) < maxBridgeListeners*MaxConnectionsPerExit {
		e.authCache[key] = cachedProof{exitID: exitID, certHash: certHash, sigHash: sigHash, notAfter: leaf.NotAfter, last: now}
	}
	e.mu.Unlock()
	return exitID, nil
}

func (e *Entry) peer(id string) *entryPeer {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return nil
	}
	if p := e.peers[id]; p != nil {
		return p
	}
	retired := e.retired[id]
	delete(e.retired, id)
	p := &entryPeer{id: id, wait: newPendingQueue(), connections: map[[32]byte]entryConn{}, pairs: map[pairKey]*bridgePair{},
		streams: retired.Streams, bytesIn: retired.BytesIn, bytesOut: retired.BytesOut, since: retired.Since, lastError: retired.Error}
	e.peers[id] = p
	return p
}

func (e *Entry) acquireHalf(p *entryPeer, key [32]byte, now time.Time) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return "closed"
	}
	c, ok := p.connections[key]
	if !ok && len(p.connections) >= MaxConnectionsPerExit {
		return "connection_limit"
	}
	if !ok && p.since.IsZero() {
		p.since = now
	}
	c.active++
	p.connections[key] = c
	return ""
}

func (e *Entry) releaseHalf(p *entryPeer, key [32]byte) {
	e.mu.Lock()
	c, ok := p.connections[key]
	if ok {
		c.active--
		if c.active <= 0 {
			delete(p.connections, key)
		} else {
			p.connections[key] = c
		}
	}
	e.mu.Unlock()
}

func (e *Entry) joinPair(peer *entryPeer, key pairKey, role pairRole) (*bridgePair, bool, string) {
	e.mu.Lock()
	if e.closed.Load() {
		e.mu.Unlock()
		return nil, false, "closed"
	}
	pair := peer.pairs[key]
	if pair == nil {
		ctx, cancel := context.WithCancel(context.Background())
		pair = &bridgePair{
			peer: peer, key: key, ctx: ctx, cancel: cancel, ready: make(chan struct{}),
			attached: make(chan struct{}), done: make(chan struct{}), halves: map[pairRole]bool{},
			completed: map[pairRole]bool{},
		}
		peer.pairs[key] = pair
		pair.pairTimer = time.AfterFunc(e.timing.pairWait, func() { e.finishPair(pair, "pair_timeout", true) })
	}
	if pair.finished || pair.halves[role] {
		e.mu.Unlock()
		return pair, false, "duplicate_pair"
	}
	pair.halves[role] = true
	paired := pair.halves[pairDown] && pair.halves[pairUp] && !pair.paired
	if paired && (peer.active >= MaxStreamsPerExit || e.totalStreams >= MaxStreamsPerEntry) {
		e.mu.Unlock()
		e.finishPair(pair, "stream_limit", true)
		return pair, false, "stream_limit"
	}
	if paired {
		pair.paired = true
		pair.active = true
		peer.active++
		e.totalStreams++
		if pair.pairTimer != nil {
			pair.pairTimer.Stop()
			pair.pairTimer = nil
		}
		close(pair.ready)
	}
	e.mu.Unlock()
	return pair, paired, ""
}

func (e *Entry) offerPair(pair *bridgePair) bool {
	e.mu.Lock()
	if pair.finished || !pair.paired {
		e.mu.Unlock()
		return false
	}
	wait := e.timing.pendingWait
	if e.timing.pendingJitter > 0 {
		spread := e.timing.pendingJitter
		wait += time.Duration(mrand.Int64N(int64(2*spread)+1)) - spread
	}
	if wait <= 0 {
		wait = time.Millisecond
	}
	pair.idleTimer = time.AfterFunc(wait, func() { e.finishPairWhen(pair, "request_idle", true, true) })
	e.mu.Unlock()
	return pair.peer.wait.offer(pair)
}

func (e *Entry) servePairHalf(w http.ResponseWriter, r *http.Request, pair *bridgePair, role pairRole) {
	select {
	case <-pair.ready:
	case <-pair.done:
		e.writePairError(w, pair)
		return
	case <-r.Context().Done():
		e.finishPair(pair, "stream", true)
		return
	}
	select {
	case <-pair.attached:
	case <-pair.done:
		e.writePairError(w, pair)
		return
	case <-r.Context().Done():
		e.finishPair(pair, "stream", true)
		return
	}
	e.mu.RLock()
	relayID := pair.relayID
	conn := pair.conn
	finished := pair.finished
	e.mu.RUnlock()
	if finished || conn == nil {
		e.writePairError(w, pair)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		e.finishPair(pair, "stream", true)
		return
	}
	w.Header().Set(ProtocolHeader, protocolVersion)
	w.Header().Set(StreamHeader, string(role))
	w.Header().Set(RelayIDHeader, relayID)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var err error
	switch role {
	case pairDown:
		err = e.copyDown(pair, conn, r.Body)
	case pairUp:
		err = e.copyUp(pair, conn, w, flusher)
	}
	if err != nil {
		// A session that ends badly (the person resets, the exit's server resets, the stream is cut) is not a failure of the bridge:
		// it is no error of the peer, which would otherwise flap with every reset of a phone.
		e.finishPair(pair, "stream", true)
		panic(http.ErrAbortHandler)
	}
	e.finishHalf(pair, role)
}

func (p *bridgePair) peerID() string {
	if p == nil || p.peer == nil {
		return ""
	}
	return p.peer.id
}

func (e *Entry) finishHalf(pair *bridgePair, role pairRole) {
	e.mu.Lock()
	if pair.finished {
		e.mu.Unlock()
		return
	}
	pair.completed[role] = true
	done := pair.completed[pairDown] && pair.completed[pairUp]
	e.mu.Unlock()
	if done {
		e.finishPair(pair, "", false)
	}
}

func (e *Entry) copyDown(pair *bridgePair, dst net.Conn, src io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			e.startProgress(pair)
			if err := writeFull(dst, buf[:n]); err != nil {
				return err
			}
			e.resetProgress(pair)
			e.addBytes(pair, uint64(n), 0)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return closeWrite(dst)
			}
			return readErr
		}
	}
}

func (e *Entry) copyUp(pair *bridgePair, src net.Conn, w http.ResponseWriter, flusher http.Flusher) error {
	buf := make([]byte, 32<<10)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			written, err := w.Write(buf[:n])
			flusher.Flush()
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
			e.addBytes(pair, 0, uint64(n))
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func (e *Entry) startProgress(pair *bridgePair) {
	e.mu.Lock()
	if pair.finished {
		e.mu.Unlock()
		return
	}
	pair.progressN++
	generation := pair.progressN
	if pair.progress != nil {
		pair.progress.Stop()
	}
	pair.progress = time.AfterFunc(e.timing.progress, func() { e.progressExpired(pair, generation) })
	e.mu.Unlock()
}

// resetProgress ends the watch of a write that completed: the timer exists only while a write to the person is blocked.
func (e *Entry) resetProgress(pair *bridgePair) {
	e.mu.Lock()
	pair.progressN++
	if pair.progress != nil {
		pair.progress.Stop()
		pair.progress = nil
	}
	e.mu.Unlock()
}

func (e *Entry) progressExpired(pair *bridgePair, generation uint64) {
	e.mu.RLock()
	expired := !pair.finished && pair.progressN == generation
	e.mu.RUnlock()
	if expired { // a person who does not read: not a failure of the bridge
		e.finishPair(pair, "progress_timeout", true)
	}
}

func (e *Entry) addBytes(pair *bridgePair, toPeople, toExit uint64) {
	e.mu.Lock()
	pair.peer.bytesIn += toPeople
	pair.peer.bytesOut += toExit
	counter := e.relayCounters[pair.relayID]
	counter.Bytes += toExit
	counter.BytesBack += toPeople
	e.relayCounters[pair.relayID] = counter
	e.mu.Unlock()
}

func (e *Entry) finishPair(pair *bridgePair, code string, reset bool) {
	e.finishPairWhen(pair, code, reset, false)
}

// finishPairWhen ends a pair; with unlessTaken a pair that a person has taken in the meantime is left alone (the idle time of
// a waiting request must not end a session).
func (e *Entry) finishPairWhen(pair *bridgePair, code string, reset, unlessTaken bool) {
	if pair == nil {
		return
	}
	e.mu.Lock()
	if pair.finished || (unlessTaken && pair.conn != nil) {
		e.mu.Unlock()
		return
	}
	pair.finished = true
	pair.err = code
	if pair.pairTimer != nil {
		pair.pairTimer.Stop()
		pair.pairTimer = nil
	}
	if pair.idleTimer != nil {
		pair.idleTimer.Stop()
		pair.idleTimer = nil
	}
	if pair.progress != nil {
		pair.progress.Stop()
		pair.progress = nil
	}
	delete(pair.peer.pairs, pair.key)
	if pair.active {
		pair.active = false
		if pair.peer.active > 0 {
			pair.peer.active--
		}
		if e.totalStreams > 0 {
			e.totalStreams--
		}
	}
	conn := pair.conn
	pair.conn = nil
	close(pair.done)
	e.mu.Unlock()
	pair.cancel()
	if conn != nil {
		if reset {
			rejectConn(conn)
		} else {
			_ = conn.Close()
		}
	}
}

func (e *Entry) writePairError(w http.ResponseWriter, pair *bridgePair) {
	e.mu.RLock()
	code := pair.err
	e.mu.RUnlock()
	if code == "" {
		code = "closed"
	}
	e.writeBridgeError(w, pair.peerID(), code)
}

func (e *Entry) writeBridgeError(w http.ResponseWriter, exitID, code string) {
	if code != "request_idle" {
		e.setPeerError(exitID, code)
	}
	w.Header().Set(ErrorHeader, code)
	status := http.StatusServiceUnavailable
	if code == "request_idle" {
		status = http.StatusGatewayTimeout
	} else if code == "pair_timeout" {
		status = http.StatusRequestTimeout
	}
	w.WriteHeader(status)
}

func (e *Entry) setPeerError(exitID, code string) {
	if exitID == "" {
		return
	}
	e.mu.Lock()
	if p := e.peers[exitID]; p != nil {
		p.lastError = code
	}
	if code == "request_idle" || code == "" {
		e.mu.Unlock()
		return
	}
	now := time.Now()
	if cfgNow := e.serve.Now; cfgNow != nil {
		now = cfgNow()
	}
	last := e.logged[code]
	shouldLog := last.IsZero() || now.Sub(last) >= time.Minute
	if shouldLog {
		e.logged[code] = now
	}
	logger := e.log
	e.mu.Unlock()
	if shouldLog && logger != nil {
		logger.Warn("reverse relay bridge refused a stream", "code", code)
	}
}

func (e *Entry) dropPeer(exitID string, remove bool) {
	e.mu.Lock()
	peer := e.peers[exitID]
	var pairs []*bridgePair
	if peer != nil {
		for _, pair := range peer.pairs {
			pairs = append(pairs, pair)
		}
		if remove {
			e.retired[exitID] = PeerState{PeerNodeID: exitID, Streams: peer.streams, BytesIn: peer.bytesIn,
				BytesOut: peer.bytesOut, Since: peer.since, Error: peer.lastError}
			delete(e.peers, exitID)
		}
	}
	for key, cached := range e.authCache {
		if cached.exitID == exitID {
			delete(e.authCache, key)
		}
	}
	e.mu.Unlock()
	for _, pair := range pairs {
		e.finishPair(pair, "closed", true)
	}
	e.listenerSyncMu.Lock()
	e.listenerMu.Lock()
	var stopped []*listenerState
	for id, listener := range e.listeners {
		if listener.config.ExitNodeID == exitID {
			delete(e.listeners, id)
			stopped = append(stopped, listener)
		}
	}
	e.listenerMu.Unlock()
	e.listenerSyncMu.Unlock()
	for _, listener := range stopped {
		listener.stop()
	}
}

func (e *Entry) acquireClient(peer *entryPeer) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() || peer.clientActive >= MaxPendingClients || e.totalClients >= MaxClientsPerEntry {
		return false
	}
	peer.clientActive++
	e.totalClients++
	return true
}

func (e *Entry) releaseClient(peer *entryPeer) {
	e.mu.Lock()
	if peer.clientActive > 0 {
		peer.clientActive--
	}
	if e.totalClients > 0 {
		e.totalClients--
	}
	e.mu.Unlock()
}

func (e *Entry) SyncListeners(relays []RelayListener) map[string]error {
	e.listenerSyncMu.Lock()
	defer e.listenerSyncMu.Unlock()
	cfg := e.serveSnapshot()
	allowed := make(map[string]struct{}, len(cfg.ExitNodeIDs))
	for _, id := range cfg.ExitNodeIDs {
		allowed[id] = struct{}{}
	}
	desired := make(map[string]RelayListener, len(relays))
	errs := make(map[string]error)
	for _, l := range relays {
		if !relayIDPattern.MatchString(l.RelayID) || !nodeIDPattern.MatchString(l.ExitNodeID) || l.Port == 0 {
			errs[l.RelayID] = errors.New("bad_config")
			continue
		}
		if _, ok := allowed[l.ExitNodeID]; !ok {
			errs[l.RelayID] = errors.New("exit_not_allowed")
			continue
		}
		if _, ok := desired[l.RelayID]; ok {
			errs[l.RelayID] = errors.New("duplicate_relay")
			continue
		}
		desired[l.RelayID] = l
	}
	if len(desired) > maxBridgeListeners {
		for id := range desired {
			errs[id] = errors.New("listener_limit")
		}
		desired = map[string]RelayListener{}
	}
	for id, l := range desired {
		for otherID, other := range desired {
			if id != otherID && l.Port == other.Port && l.Address == other.Address {
				errs[id] = errors.New("port_conflict")
			}
		}
	}
	for id := range errs {
		delete(desired, id)
	}

	e.listenerMu.Lock()
	if e.closed.Load() {
		e.listenerMu.Unlock()
		for id := range desired {
			errs[id] = errors.New("closed")
		}
		return errs
	}
	var stopped []*listenerState
	for id, old := range e.listeners {
		want, ok := desired[id]
		if ok && old.config == want {
			delete(desired, id)
			continue
		}
		delete(e.listeners, id)
		stopped = append(stopped, old)
	}
	e.listenerMu.Unlock()
	for _, old := range stopped {
		old.stop()
	}

	for id, l := range desired {
		addr := l.Address
		if addr == "" {
			addr = net.JoinHostPort("", strconv.Itoa(int(l.Port)))
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errs[id] = errors.New("listen_failed")
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		state := &listenerState{config: l, ln: ln, ctx: ctx, cancel: cancel, queue: make(chan net.Conn, AcceptQueueSize), active: map[net.Conn]struct{}{}}
		e.listenerMu.Lock()
		if e.closed.Load() {
			e.listenerMu.Unlock()
			cancel()
			_ = ln.Close()
			errs[id] = errors.New("closed")
			continue
		}
		e.listeners[id] = state
		e.listenerMu.Unlock()
		state.wg.Add(2)
		go e.acceptLoop(state)
		go e.listenerWorker(state)
	}
	return errs
}

func (e *Entry) acceptLoop(state *listenerState) {
	defer state.wg.Done()
	delay := e.timing.acceptRetryMin
	for {
		conn, err := state.ln.Accept()
		if err != nil {
			if state.ctx.Err() != nil {
				return
			}
			timer := time.NewTimer(delay)
			select {
			case <-state.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay < e.timing.acceptRetryMax {
				delay = min(delay*2, e.timing.acceptRetryMax)
			}
			continue
		}
		delay = e.timing.acceptRetryMin
		select {
		case state.queue <- conn:
		default:
			rejectConn(conn)
		}
	}
}

func (e *Entry) listenerWorker(state *listenerState) {
	defer state.wg.Done()
	defer func() {
		for {
			select {
			case conn := <-state.queue:
				rejectConn(conn)
			default:
				return
			}
		}
	}()
	peer := e.peer(state.config.ExitNodeID)
	if peer == nil {
		return
	}
	for {
		select {
		case <-state.ctx.Done():
			return
		case conn := <-state.queue:
			if !e.acquireClient(peer) {
				rejectConn(conn)
				continue
			}
			if !state.add(conn) {
				e.releaseClient(peer)
				rejectConn(conn)
				continue
			}
			state.wg.Add(1)
			go func(client net.Conn) {
				defer state.wg.Done()
				defer state.remove(client)
				defer e.releaseClient(peer)
				if !e.attachClient(state.ctx, peer, state.config, client) {
					rejectConn(client)
				}
			}(conn)
		}
	}
}

func rejectConn(conn net.Conn) {
	if idle, ok := conn.(*idleConn); ok {
		if tcp, ok := idle.Conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = idle.Close()
		return
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}

func (e *Entry) attachClient(ctx context.Context, peer *entryPeer, listener RelayListener, conn net.Conn) bool {
	deadline := time.Now().Add(ClientWaitForStream)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			e.setPeerError(listener.ExitNodeID, "stream_unavailable")
			return false
		}
		pair, err := peer.wait.take(ctx, remaining)
		if err != nil {
			if ctx.Err() == nil {
				e.setPeerError(listener.ExitNodeID, "stream_unavailable")
			}
			return false
		}
		if e.beforeAttach != nil {
			e.beforeAttach(pair)
		}
		e.mu.Lock()
		attached := !pair.finished && pair.paired && pair.conn == nil
		if attached {
			pair.conn = newIdleConn(conn, e.timing.streamIdle)
			pair.relayID = listener.RelayID
			peer.streams++
			peer.lastError = ""
			if pair.idleTimer != nil {
				pair.idleTimer.Stop()
				pair.idleTimer = nil
			}
			close(pair.attached)
		}
		e.mu.Unlock()
		if !attached {
			continue
		}
		select {
		case <-pair.done:
			return true
		case <-ctx.Done():
			e.finishPair(pair, "closed", true)
			return false
		}
	}
}

func (e *Entry) StopListeners() {
	e.listenerSyncMu.Lock()
	e.listenerMu.Lock()
	old := e.listeners
	e.listeners = map[string]*listenerState{}
	e.listenerMu.Unlock()
	e.listenerSyncMu.Unlock()
	for _, state := range old {
		state.stop()
	}
}

func (e *Entry) Close() {
	if !e.closed.CompareAndSwap(false, true) {
		return
	}
	e.mu.Lock()
	e.serve = ServeConfig{}
	var pairs []*bridgePair
	for _, peer := range e.peers {
		for _, pair := range peer.pairs {
			pairs = append(pairs, pair)
		}
	}
	clear(e.authCache)
	e.mu.Unlock()
	for _, pair := range pairs {
		e.finishPair(pair, "closed", true)
	}
	e.StopListeners()
}

func (e *Entry) States() []PeerState {
	e.mu.RLock()
	out := make([]PeerState, 0, len(e.peers)+len(e.retired))
	for _, state := range e.retired {
		out = append(out, state)
	}
	for id, peer := range e.peers {
		clients := uint32(0)
		for _, pair := range peer.pairs {
			if pair.conn != nil && !pair.finished {
				clients++
			}
		}
		out = append(out, PeerState{PeerNodeID: id, Conns: uint32(len(peer.connections)), Since: peer.since,
			Streams: peer.streams, BytesIn: peer.bytesIn, BytesOut: peer.bytesOut, Error: peer.lastError, Clients: clients})
	}
	e.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].PeerNodeID < out[j].PeerNodeID })
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (e *Entry) RelayCounters() map[string]RelayCounters {
	e.mu.RLock()
	out := make(map[string]RelayCounters, len(e.relayCounters))
	for id, counter := range e.relayCounters {
		out[id] = counter
	}
	e.mu.RUnlock()
	return out
}

func closeWrite(conn net.Conn) error {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return conn.Close()
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if n < 0 || n > len(b) {
			return io.ErrShortWrite
		}
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

type idleConn struct {
	net.Conn
	idle  time.Duration
	mu    sync.Mutex
	timer *time.Timer
	close sync.Once
}

func newIdleConn(conn net.Conn, idle time.Duration) *idleConn {
	c := &idleConn{Conn: conn, idle: idle}
	c.timer = time.AfterFunc(idle, func() { _ = c.Close() })
	return c
}

func (c *idleConn) touch() {
	c.mu.Lock()
	if c.timer != nil {
		c.timer.Reset(c.idle)
	}
	c.mu.Unlock()
}

func (c *idleConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *idleConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *idleConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

func (c *idleConn) Close() error {
	var err error
	c.close.Do(func() {
		c.mu.Lock()
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
		c.mu.Unlock()
		err = c.Conn.Close()
	})
	return err
}

func slicesContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func jitterDuration(base, spread time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	if spread <= 0 {
		return base
	}
	return base - spread + time.Duration(mrand.Int64N(int64(2*spread)+1))
}

func newPairID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func responseCode(status int, header http.Header) string {
	if code := header.Get(ErrorHeader); code != "" {
		return code
	}
	if status == http.StatusOK {
		return ""
	}
	return "stream"
}
