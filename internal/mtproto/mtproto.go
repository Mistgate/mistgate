// Package mtproto is the Telegram MTProto proxy protocol as a Mistgate node serves it: Fake TLS in front, obfuscated2
// inside, a fresh obfuscated2 connection to a Telegram DC behind. The node engine (internal/node/mtproto) owns the
// listener, the credentials and the decoy site; this package owns one connection from its first byte to its last.
//
// A client with an "ee" secret opens what looks like TLS 1.3 to the node's domain, with an HMAC of the hello under its
// key in the random field, so the server tells its clients from everyone else without a round trip. The server
// answers a ServerHello the client checks the same way, and both sides then wrap obfuscated2 in TLS application data
// records. The client's first 64 bytes inside give the AES-256-CTR keys of its leg, the transport tag and the DC it
// wants. The server dials that DC (directly, no middle proxy), sends a header with keys of its own and relays,
// decrypting one leg and encrypting the other; the plaintext between them is the MTProto transport, and its size is
// what the Observer counts.
//
// Whatever is not an authenticated client of a configured domain (another SNI, a failed HMAC, a replayed hello, a hello
// outside the clock window, plain HTTP, a browser) goes to Config.Fallback with every byte already read, so a prober
// talks to the real site the caller puts there. Plain obfuscated2 without Fake TLS is not served: on a node whose
// port 443 also shows a website it would be a stream of noise nothing else on the port explains.
//
// Sources and the reasons for the limits: design/adr/0010-mtproto.md.
package mtproto

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Tag is the MTProto transport inside obfuscated2 (header bytes 56..60). The server does not parse the transport: it
// writes the same tag to the DC and relays the bytes.
type Tag uint32

const (
	TagAbridged     Tag = 0xefefefef
	TagIntermediate Tag = 0xeeeeeeee
	TagPadded       Tag = 0xdddddddd
)

func (t Tag) String() string {
	switch t {
	case TagAbridged:
		return "abridged"
	case TagIntermediate:
		return "intermediate"
	case TagPadded:
		return "padded"
	}
	return "unknown"
}

// SessionInfo describes one relayed connection. DC is what the client asked for (negative: media).
type SessionInfo struct {
	ID           uint64
	CredentialID string
	DC           int16
	Tag          Tag
	Remote, SNI  string
	Since        time.Time
}

// Observer is told about sessions and their plaintext traffic. Calls come from the session's goroutine and must not
// block or call back into the Server. AddTraffic names the session (not only its credential), so traffic lands where
// the session was opened even if its credential's key changed meanwhile; a session's last AddTraffic comes before its
// Closed.
type Observer interface {
	Opened(SessionInfo)
	Closed(SessionInfo)
	AddTraffic(session SessionInfo, up, down uint64)
}

// Dialer opens the connections to Telegram DCs; *net.Dialer is one.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type Config struct {
	Credentials []Credential
	// Domains are the SNI names a client may ask for, the domain in the "ee" secret of the links. A hello for another
	// name goes to Fallback without an HMAC. At least one is required.
	Domains  []string
	Dialer   Dialer // nil: net.Dialer
	Now      func() time.Time
	Random   io.Reader // nil: crypto/rand
	Observer Observer
	Log      *slog.Logger // operational warnings only; never secrets or traffic

	// HandshakeTimeout is the hard limit from the first byte to the DC connection; default 10 s. The DC addresses tried
	// share what is left of it, so a blackholed first address leaves time for the others.
	HandshakeTimeout time.Duration
	DialTimeout      time.Duration // at most this per DC address; default 10 s
	StatsInterval    time.Duration // how often an open session reports traffic and is checked for idleness; default 10 s
	// IdleTimeout closes a session that moved no byte either way for this long; default 10 min. Telegram clients ping
	// about once a minute, so a session this quiet is one nobody holds any more.
	IdleTimeout time.Duration
	// MaxSessionsPerCredential bounds the open sessions of one credential (one device); over it a new session is
	// refused. Default 64: a client keeps a main and a media connection per DC it uses and opens a few more for
	// uploads and downloads, about 10-15 per account at the busiest, and one app may hold several accounts behind one
	// proxy. 64 leaves room for that, and stops a leaked key from carrying more than a handful of people.
	MaxSessionsPerCredential int
	// ReplayWindow is how long an authenticated hello is remembered. A hello is accepted only when the client's clock
	// is within half of it from Now, so a hello old enough to have left the cache is refused by its time. Default 10 min.
	ReplayWindow time.Duration
	// ReplayCacheSize bounds the replay cache (about 60 bytes an entry); default 1<<17. Full of live entries, the
	// server refuses new hellos (they go to Fallback) until entries expire.
	ReplayCacheSize int
	// ReplayPerCredential bounds the entries one credential may hold in the replay cache, so one key holder cannot
	// spend the inbound's capacity; default ReplayCacheSize/64 (2048 hellos per window, over 3 a second for the default
	// window, where a client makes a few a minute). Over it that credential's new hellos go to Fallback until its
	// entries expire.
	ReplayPerCredential int
	// MaxHandshakes and MaxHandshakesPerAddr bound unauthenticated connections in progress; over a limit a connection
	// is closed at once. Defaults 1024 and 32 (per IPv4 address or IPv6 /64).
	MaxHandshakes, MaxHandshakesPerAddr int

	PreferIPv6 bool // try a DC's IPv6 addresses first
	// IgnoreTimeSkew accepts hellos whatever the client's clock says. Replays are then caught only while the cache
	// remembers them.
	IgnoreTimeSkew bool
	// DCAddress overrides the address of a DC (called with the DC as the client sent it); false falls through to
	// Telegram's published table.
	DCAddress func(dc int16) (string, bool)
	// Fallback takes every connection that is not an authenticated client, with the bytes read from it so far, and
	// owns it from then on (nil: the connection is closed). It runs on the Serve goroutine; by then the server holds
	// nothing of the connection (no handshake slot, no deadline), so it may serve it there. An authenticated session
	// never reaches it.
	Fallback func(ctx context.Context, conn net.Conn, initial []byte) error
}

var (
	ErrClosed = errors.New("mtproto: server is closed")
	ErrBusy   = errors.New("mtproto: too many handshakes")

	// errNotClient routes a connection to the fallback; errRevoked ends one whose credential went away mid-handshake.
	errNotClient    = errors.New("mtproto: not an authenticated client")
	errRevoked      = errors.New("mtproto: credential removed during the handshake")
	errSessionLimit = errors.New("mtproto: the credential has its maximum of open sessions")
)

type Server struct {
	cfg     Config
	domains map[string]struct{}
	creds   atomic.Pointer[credSet]
	guard   *guard
	replay  *replayCache
	obs     Observer

	scans   atomic.Uint64 // HMACs computed, for the tests
	spawned atomic.Uint64 // goroutines ServeListener started, for the tests

	warnFull, warnQuota atomic.Int64 // when (unix ns) the warning was last logged: once a minute at most

	mu       sync.Mutex
	closed   bool
	seq      uint64            // connections accepted; also the session id
	kicked   map[string]uint64 // credential id -> seq at its last CloseCredential
	pending  map[net.Conn]struct{}
	sessions map[uint64]*session
	perCred  map[string]int // registered sessions per credential
}

// credSet is immutable once published: handshakes scan list without a lock while SetCredentials swaps the pointer.
type credSet struct {
	list []Credential
	byID map[string][KeyLen]byte
}

type session struct {
	info     SessionInfo
	key      [KeyLen]byte
	client   net.Conn
	tc       *tlsConn
	fromCli  cipher.Stream
	toCli    cipher.Stream
	upstream net.Conn // set and read under Server.mu until the relay starts
	closed   bool     // under Server.mu
}

func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	if cfg.Dialer == nil {
		cfg.Dialer = &net.Dialer{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	orDefault := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	orDefault(&cfg.HandshakeTimeout, 10*time.Second)
	orDefault(&cfg.DialTimeout, 10*time.Second)
	orDefault(&cfg.StatsInterval, 10*time.Second)
	orDefault(&cfg.IdleTimeout, 10*time.Minute)
	orDefault(&cfg.ReplayWindow, 10*time.Minute)
	if cfg.ReplayCacheSize <= 0 {
		cfg.ReplayCacheSize = 1 << 17
	}
	if cfg.ReplayPerCredential <= 0 {
		cfg.ReplayPerCredential = max(1, cfg.ReplayCacheSize/64)
	}
	if cfg.MaxSessionsPerCredential <= 0 {
		cfg.MaxSessionsPerCredential = 64
	}
	if cfg.MaxHandshakes <= 0 {
		cfg.MaxHandshakes = 1024
	}
	if cfg.MaxHandshakesPerAddr <= 0 {
		cfg.MaxHandshakesPerAddr = 32
	}
	s := &Server{
		cfg:      cfg,
		domains:  make(map[string]struct{}, len(cfg.Domains)),
		guard:    newGuard(cfg.MaxHandshakes, cfg.MaxHandshakesPerAddr),
		replay:   newReplayCache(cfg.ReplayWindow, cfg.ReplayCacheSize, cfg.ReplayPerCredential),
		obs:      cfg.Observer,
		kicked:   make(map[string]uint64),
		pending:  make(map[net.Conn]struct{}),
		sessions: make(map[uint64]*session),
		perCred:  make(map[string]int),
	}
	if s.obs == nil {
		s.obs = nopObserver{}
	}
	for _, d := range cfg.Domains {
		n := normalizeDomain(d)
		if n == "" {
			return nil, fmt.Errorf("mtproto: domain %q is not a host name", d)
		}
		s.domains[n] = struct{}{}
	}
	if len(s.domains) == 0 {
		return nil, errors.New("mtproto: no Fake TLS domain configured")
	}
	if err := s.SetCredentials(cfg.Credentials); err != nil {
		return nil, err
	}
	return s, nil
}

// SetCredentials replaces the accepted keys. Sessions of a credential that is gone, or whose key changed, are closed
// now, and a handshake that authenticated with such a key cannot open a session afterwards: registration re-checks the
// credential under the same lock.
func (s *Server) SetCredentials(creds []Credential) error {
	set := &credSet{list: slices.Clone(creds), byID: make(map[string][KeyLen]byte, len(creds))}
	keys := make(map[[KeyLen]byte]struct{}, len(creds))
	for _, c := range creds {
		if c.ID == "" {
			return errors.New("mtproto: credential without id")
		}
		if _, dup := set.byID[c.ID]; dup {
			return fmt.Errorf("mtproto: duplicate credential id %s", c.ID)
		}
		if _, dup := keys[c.Key]; dup {
			return fmt.Errorf("mtproto: credential %s has the key of another credential", c.ID)
		}
		set.byID[c.ID] = c.Key
		keys[c.Key] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds.Store(set)
	for _, sess := range s.sessions {
		if k, ok := set.byID[sess.info.CredentialID]; !ok || k != sess.key {
			s.closeLocked(sess)
		}
	}
	for id := range s.kicked {
		if _, ok := set.byID[id]; !ok {
			delete(s.kicked, id)
		}
	}
	return nil
}

// CloseCredential closes the sessions of a credential and stops its handshakes in progress from opening one. The
// credential stays accepted for new connections. It returns the number of sessions closed.
func (s *Server) CloseCredential(credID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.creds.Load().byID[credID]; ok {
		s.kicked[credID] = s.seq
	}
	n := 0
	for _, sess := range s.sessions {
		if sess.info.CredentialID == credID && !sess.closed {
			s.closeLocked(sess)
			n++
		}
	}
	return n
}

// CloseAll closes every connection, handshakes included, and refuses new ones. The server is done after it.
func (s *Server) CloseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for c := range s.pending {
		_ = c.Close()
	}
	clear(s.pending)
	for _, sess := range s.sessions {
		s.closeLocked(sess)
	}
}

func (s *Server) closeLocked(sess *session) {
	sess.closed = true
	_ = sess.client.Close()
	if sess.upstream != nil {
		_ = sess.upstream.Close()
	}
}

// Serve runs one accepted connection to its end. The returned error says why it ended; it never carries key material
// or bytes of the stream.
func (s *Server) Serve(ctx context.Context, conn net.Conn) error {
	a, err := s.admit(conn)
	if err != nil {
		return err
	}
	return s.serve(ctx, conn, a)
}

// ServeListener accepts connections from ln and serves each. A connection gets its handshake slot before a goroutine
// is started for it, so a flood over the limits costs an accept and a close each, not a goroutine. It returns when ln
// is closed and every connection it started has ended.
func (s *Server) ServeListener(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// A transient failure (out of file descriptors) must not end the listener.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		a, err := s.admit(conn)
		if err != nil {
			continue
		}
		s.spawned.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.serve(ctx, conn, a)
		}()
	}
}

// admission is a connection's place in the server: its sequence number and its handshake slot.
type admission struct {
	seq  uint64
	addr netip.Prefix
}

// admit takes the connection's handshake slot, or closes it.
func (s *Server) admit(conn net.Conn) (admission, error) {
	seq, ok := s.track(conn)
	if !ok {
		_ = conn.Close()
		return admission{}, ErrClosed
	}
	addr := remoteKey(conn)
	if !s.guard.enter(addr) {
		s.untrack(conn)
		_ = conn.Close()
		return admission{}, ErrBusy
	}
	return admission{seq: seq, addr: addr}, nil
}

func (s *Server) serve(ctx context.Context, conn net.Conn, a admission) error {
	deadline := time.Now().Add(s.cfg.HandshakeTimeout)
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	sess, initial, err := s.handshake(conn, a.seq, a.addr)
	s.guard.leave(a.addr)

	if errors.Is(err, errNotClient) {
		s.untrack(conn)
		if !stop() {
			return ctx.Err()
		}
		_ = conn.SetDeadline(time.Time{})
		if s.cfg.Fallback == nil {
			_ = conn.Close()
			return err
		}
		return s.cfg.Fallback(ctx, conn, initial)
	}
	defer stop()
	if err != nil {
		s.untrack(conn)
		_ = conn.Close()
		return err
	}
	toDC, fromDC, err := s.connect(ctx, sess, deadline)
	if err != nil {
		s.finish(sess)
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	s.obs.Opened(sess.info)
	s.relay(sess, toDC, fromDC)
	return nil
}

func (s *Server) track(conn net.Conn) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, false
	}
	s.seq++
	s.pending[conn] = struct{}{}
	return s.seq, true
}

func (s *Server) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.pending, conn)
	s.mu.Unlock()
}

// handshake authenticates the client and reads its obfuscated2 header. errNotClient comes with every byte read so
// far; any other error means the connection is to be closed.
func (s *Server) handshake(conn net.Conn, seq uint64, addr netip.Prefix) (*session, []byte, error) {
	var hdr [recordHeaderLen]byte
	if n, err := io.ReadFull(conn, hdr[:]); err != nil {
		if n > 0 && errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, hdr[:n], errNotClient // a short probe that closed its side; the site answers it as any server would
		}
		return nil, nil, err
	}
	n := int(hdr[3])<<8 | int(hdr[4])
	if hdr[0] != recordHandshake || hdr[1] != 3 || hdr[2] != 1 || recordHeaderLen+n > maxHelloLen {
		return nil, hdr[:], errNotClient
	}
	rec := make([]byte, recordHeaderLen+n)
	copy(rec, hdr[:])
	if _, err := io.ReadFull(conn, rec[recordHeaderLen:]); err != nil {
		return nil, nil, err
	}
	hello, ok := parseClientHello(rec)
	if !ok || !hello.telegram() || !s.domain(hello.sni) {
		return nil, rec, errNotClient
	}
	now := s.cfg.Now()
	var replayKey [16]byte
	copy(replayKey[:], hello.random[:16])
	mark, ok := s.guard.spend(addr, now)
	if !ok {
		return nil, rec, errNotClient
	}
	if s.replay.has(replayKey, now) {
		return nil, rec, errNotClient
	}
	cred, ok := s.authenticate(rec, &hello, now)
	if !ok {
		return nil, rec, errNotClient
	}
	switch s.replay.add(replayKey, cred.ID, now) {
	case replaySeen:
		return nil, rec, errNotClient
	case replayFull:
		s.warn(&s.warnFull, now, "mtproto: replay cache is full, refusing clients until entries expire",
			"size", s.cfg.ReplayCacheSize, "window", s.cfg.ReplayWindow)
		return nil, rec, errNotClient
	case replayQuota:
		s.warn(&s.warnQuota, now, "mtproto: a credential made more handshakes than its share of the replay cache, refusing it until entries expire",
			"credential", cred.ID, "share", s.cfg.ReplayPerCredential, "window", s.cfg.ReplayWindow)
		return nil, rec, errNotClient
	}
	s.guard.refund(addr, mark)

	resp, err := serverHello(s.cfg.Random, &cred.Key, &hello)
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.Write(resp); err != nil {
		return nil, nil, err
	}
	tc := newTLSConn(conn)
	var h [headerLen]byte
	if _, err := io.ReadFull(tc, h[:]); err != nil {
		return nil, nil, err
	}
	fromCli, toCli, tag, dc, err := openHeader(&h, &cred.Key)
	if err != nil {
		return nil, nil, err
	}
	sess := &session{
		info: SessionInfo{
			ID: seq, CredentialID: cred.ID, DC: dc, Tag: tag,
			Remote: conn.RemoteAddr().String(), SNI: normalizeDomain(hello.sni), Since: now,
		},
		key: cred.Key, client: conn, tc: tc, fromCli: fromCli, toCli: toCli,
	}
	if err := s.register(sess, conn); err != nil {
		return nil, nil, err
	}
	return sess, nil, nil
}

// warn logs at most once a minute per kind: a key holder can make these happen at line rate.
func (s *Server) warn(last *atomic.Int64, now time.Time, msg string, args ...any) {
	t, prev := now.UnixNano(), last.Load()
	if prev != 0 && t-prev < int64(time.Minute) {
		return
	}
	if last.CompareAndSwap(prev, t) {
		s.cfg.Log.Warn(msg, args...)
	}
}

func (s *Server) domain(sni string) bool {
	_, ok := s.domains[normalizeDomain(sni)]
	return ok
}

// authenticate finds the credential whose HMAC the hello carries and checks the client's clock. rec's random is zeroed
// for the HMAC and restored before return, so the fallback still gets the bytes as sent.
func (s *Server) authenticate(rec []byte, hello *clientHello, now time.Time) (Credential, bool) {
	clear(rec[randomOff : randomOff+32])
	defer copy(rec[randomOff:], hello.random[:])
	for _, c := range s.creds.Load().list {
		s.scans.Add(1)
		sum := helloDigest(rec, &c.Key)
		unix, ok := matchDigest(&sum, &hello.random)
		if !ok {
			continue
		}
		if !s.cfg.IgnoreTimeSkew {
			skew := now.Sub(time.Unix(int64(unix), 0)).Abs()
			if skew > s.cfg.ReplayWindow/2 {
				return Credential{}, false
			}
		}
		return c, true
	}
	return Credential{}, false
}

// register makes an authenticated handshake a session, under the lock SetCredentials and CloseCredential take: the
// credential must still be there with the same key, not kicked after this connection was accepted, and below its
// session limit.
func (s *Server) register(sess *session, conn net.Conn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := sess.info.CredentialID
	if s.closed {
		return ErrClosed
	}
	if k, ok := s.creds.Load().byID[id]; !ok || k != sess.key {
		return errRevoked
	}
	if at, ok := s.kicked[id]; ok && sess.info.ID <= at {
		return errRevoked
	}
	if s.perCred[id] >= s.cfg.MaxSessionsPerCredential {
		return errSessionLimit
	}
	delete(s.pending, conn)
	s.sessions[sess.info.ID] = sess
	s.perCred[id]++
	return nil
}

// connect dials the client's DC and sends the header of the DC leg. Each address gets an equal share of what is left of
// the handshake deadline (at most DialTimeout), so one that drops packets cannot use up the time of the others. A
// session closed meanwhile (its credential removed or kicked during the dial) is not attached.
func (s *Server) connect(ctx context.Context, sess *session, deadline time.Time) (toDC, fromDC cipher.Stream, err error) {
	targets := s.dcTargets(sess.info.DC)
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("mtproto: DC %d is not known", sess.info.DC)
	}
	var up net.Conn
	err = os.ErrDeadlineExceeded
	for i, addr := range targets {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		dctx, cancel := context.WithTimeout(ctx, min(left/time.Duration(len(targets)-i), s.cfg.DialTimeout))
		up, err = s.cfg.Dialer.DialContext(dctx, "tcp", addr)
		cancel()
		if err == nil {
			break
		}
	}
	if up == nil {
		return nil, nil, fmt.Errorf("mtproto: DC %d: %w", sess.info.DC, err)
	}
	s.mu.Lock()
	if sess.closed {
		s.mu.Unlock()
		_ = up.Close()
		return nil, nil, errRevoked
	}
	sess.upstream = up
	s.mu.Unlock()
	hdr, toDC, fromDC, err := newHeader(s.cfg.Random, sess.info.Tag, sess.info.DC)
	if err != nil {
		return nil, nil, err
	}
	_ = up.SetWriteDeadline(deadline)
	if _, err := up.Write(hdr[:]); err != nil {
		return nil, nil, fmt.Errorf("mtproto: DC %d: %w", sess.info.DC, err)
	}
	_ = up.SetWriteDeadline(time.Time{})
	return toDC, fromDC, nil
}

// relay copies both ways until both directions have ended. A direction that ends cleanly half-closes the other side's
// write and the reverse copy goes on, as TCP allows; an error on either closes both, and so does IdleTimeout without
// a byte either way (checked every StatsInterval, from the same counters as the traffic).
func (s *Server) relay(sess *session, toDC, fromDC cipher.Stream) {
	var up, down atomic.Uint64
	ended := make(chan error, 2)
	go func() {
		err := pipe(sess.upstream, sess.tc, sess.fromCli, toDC, &up)
		if err == nil {
			err = closeWrite(sess.upstream)
		}
		ended <- err
	}()
	go func() {
		err := pipe(sess.tc, sess.upstream, fromDC, sess.toCli, &down)
		if err == nil {
			err = closeWrite(sess.client)
		}
		ended <- err
	}()
	var sentUp, sentDown uint64
	flush := func() {
		u, d := up.Load(), down.Load()
		if u != sentUp || d != sentDown {
			s.obs.AddTraffic(sess.info, u-sentUp, d-sentDown)
			sentUp, sentDown = u, d
		}
	}
	closeBoth := func() {
		s.mu.Lock()
		s.closeLocked(sess)
		s.mu.Unlock()
	}
	tick := time.NewTicker(s.cfg.StatsInterval)
	defer tick.Stop()
	lastMoved, lastTotal := time.Now(), uint64(0)
	for open := 2; open > 0; {
		select {
		case err := <-ended:
			open--
			if err != nil {
				closeBoth()
			}
		case <-tick.C:
			flush()
			if total := sentUp + sentDown; total != lastTotal {
				lastMoved, lastTotal = time.Now(), total
			} else if time.Since(lastMoved) >= s.cfg.IdleTimeout {
				closeBoth()
			}
		}
	}
	s.finish(sess)
	flush()
	s.obs.Closed(sess.info)
}

// finish closes a registered session and forgets it.
func (s *Server) finish(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked(sess)
	if _, ok := s.sessions[sess.info.ID]; !ok {
		return
	}
	delete(s.sessions, sess.info.ID)
	if s.perCred[sess.info.CredentialID]--; s.perCred[sess.info.CredentialID] <= 0 {
		delete(s.perCred, sess.info.CredentialID)
	}
}

// pipe copies src to dst, decrypting with open and encrypting with seal, and counts the plaintext. nil at a clean EOF.
func pipe(dst io.Writer, src io.Reader, open, seal cipher.Stream, n *atomic.Uint64) error {
	buf := make([]byte, maxWriteLen)
	for {
		k, err := src.Read(buf)
		if k > 0 {
			open.XORKeyStream(buf[:k], buf[:k])
			n.Add(uint64(k))
			seal.XORKeyStream(buf[:k], buf[:k])
			if _, werr := dst.Write(buf[:k]); werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

var errNoHalfClose = errors.New("mtproto: connection cannot half-close")

func closeWrite(c net.Conn) error {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errNoHalfClose
}

type nopObserver struct{}

func (nopObserver) Opened(SessionInfo)                     {}
func (nopObserver) Closed(SessionInfo)                     {}
func (nopObserver) AddTraffic(SessionInfo, uint64, uint64) {}
