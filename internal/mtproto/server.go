package mtproto

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Dialer is the subset of net.Dialer the server needs.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// SessionInfo describes an authenticated client session.
type SessionInfo struct {
	ID           uint64
	CredentialID string
	DC           int16
	Tag          Tag
	Remote       string
	SNI          string
	Since        time.Time
}

// Observer receives session and traffic events. Implementations must not
// block; the engine turns these into per-credential atomics.
type Observer interface {
	Opened(SessionInfo)
	Closed(SessionInfo)
	AddTraffic(credentialID string, up, down uint64)
}

type nopObserver struct{}

func (nopObserver) Opened(SessionInfo)                {}
func (nopObserver) Closed(SessionInfo)                {}
func (nopObserver) AddTraffic(string, uint64, uint64) {}

// Config is the server's immutable configuration.
type Config struct {
	Credentials []Credential
	Dialer      Dialer
	Now         func() time.Time
	Random      io.Reader
	Observer    Observer

	HandshakeTimeout  time.Duration
	DialTimeout       time.Duration
	StatsInterval     time.Duration
	ReplayWindow      time.Duration
	FakeCertLen       int
	NewSessionTickets int
	PreferIPv6        bool
	IgnoreTimeSkew    bool

	DCAddress func(dc int16) (string, bool)
	// Fallback receives a connection whose TLS ClientHello failed. initial
	// holds every byte already consumed. A nil Fallback closes the connection.
	Fallback func(ctx context.Context, conn net.Conn, initial []byte) error
}

// Server is safe for concurrent use.
type Server struct {
	cfg        Config
	dialer     Dialer
	now        func() time.Time
	rng        io.Reader
	observer   Observer
	replay     *replayCache
	certLen    int
	creds      atomic.Pointer[[]Credential]
	sessions   sync.Map // *session -> struct{}
	sessionSeq atomic.Uint64
}

func normalizeCredentials(in []Credential) ([]Credential, error) {
	seen := make(map[string]struct{}, len(in))
	creds := make([]Credential, len(in))
	copy(creds, in)
	for _, c := range creds {
		if c.ID == "" {
			return nil, errors.New("mtproto: credential with empty id")
		}
		if _, ok := seen[c.ID]; ok {
			return nil, fmt.Errorf("mtproto: duplicate credential id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
	}
	return creds, nil
}

func New(cfg Config) (*Server, error) {
	creds, err := normalizeCredentials(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	cfg.Credentials = creds
	if cfg.Dialer == nil {
		cfg.Dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	if cfg.Observer == nil {
		cfg.Observer = nopObserver{}
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.StatsInterval <= 0 {
		cfg.StatsInterval = time.Second
	}
	if cfg.ReplayWindow <= 0 {
		cfg.ReplayWindow = 10 * time.Minute
	}
	certLen := cfg.FakeCertLen
	if certLen <= 0 {
		var b [2]byte
		if _, err := io.ReadFull(cfg.Random, b[:]); err != nil {
			return nil, fmt.Errorf("mtproto: fake certificate length: %w", err)
		}
		certLen = 1024 + int(binary.BigEndian.Uint16(b[:]))%3073
	}
	if certLen < 64 {
		certLen = 64
	}
	if certLen > tlsMaxCiphertext {
		certLen = tlsMaxCiphertext
	}
	s := &Server{
		cfg:      cfg,
		dialer:   cfg.Dialer,
		now:      cfg.Now,
		rng:      cfg.Random,
		observer: cfg.Observer,
		replay:   newReplayCache(cfg.ReplayWindow, cfg.Now),
		certLen:  certLen,
	}
	s.creds.Store(&creds)
	return s, nil
}

// SetCredentials replaces the credential set atomically. Sessions that already
// authenticated keep their key and continue; new handshakes see the new set.
func (s *Server) SetCredentials(creds []Credential) error {
	next, err := normalizeCredentials(creds)
	if err != nil {
		return err
	}
	s.creds.Store(&next)
	return nil
}

func (s *Server) credentials() []Credential {
	if p := s.creds.Load(); p != nil {
		return *p
	}
	return s.cfg.Credentials
}

type session struct {
	conn   net.Conn
	credID string
	once   sync.Once
}

func (s *session) close() {
	s.once.Do(func() { _ = s.conn.Close() })
}

// CloseCredential closes every open session of one credential and returns how
// many connections it closed.
func (s *Server) CloseCredential(credID string) int {
	n := 0
	s.sessions.Range(func(k, _ any) bool {
		if sess := k.(*session); sess.credID == credID {
			sess.close()
			n++
		}
		return true
	})
	return n
}

// CloseAll closes every open session.
func (s *Server) CloseAll() {
	s.sessions.Range(func(k, _ any) bool {
		k.(*session).close()
		return true
	})
}

// Serve authenticates one client connection and relays it until either side
// closes. It takes ownership of conn.
func (s *Server) Serve(ctx context.Context, conn net.Conn) error {
	remote := ""
	if addr := conn.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	defer conn.Close()
	_ = conn.SetDeadline(s.now().Add(s.cfg.HandshakeTimeout))
	creds := s.credentials()

	raw, err := readClientHelloRecord(conn)
	if err != nil {
		return s.fail(ctx, conn, raw, err)
	}
	hello, err := parseClientHello(raw)
	if err != nil {
		return s.fail(ctx, conn, raw, err)
	}
	cred, err := validateClientHello(hello, creds, s.now(), s.replay, s.cfg.IgnoreTimeSkew)
	if err != nil {
		return s.fail(ctx, conn, raw, err)
	}
	response, err := buildServerHello(cred.Key[:], hello, s.certLen, s.cfg.NewSessionTickets, s.rng)
	if err != nil {
		return s.fail(ctx, conn, raw, err)
	}
	if err := writeFull(conn, response); err != nil {
		return fmt.Errorf("mtproto: write server hello: %w", err)
	}

	tlsReader := &tlsRecordReader{r: conn}
	var frame [64]byte
	if _, err := io.ReadFull(tlsReader, frame[:]); err != nil {
		return fmt.Errorf("mtproto: read obfuscated handshake: %w", err)
	}
	sess, err := parseClientHandshake(frame, creds)
	if err != nil {
		return err
	}
	if sess.Credential.Key != cred.Key || sess.Credential.ID != cred.ID {
		return errAuth
	}
	handle := &session{conn: conn, credID: cred.ID}
	s.sessions.Store(handle, struct{}{})
	defer s.sessions.Delete(handle)
	defer handle.close()
	address, ok := s.dcAddress(sess.DC)
	if !ok {
		return fmt.Errorf("mtproto: no address for DC %d", sess.DC)
	}

	dialCtx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout)
	upstream, err := s.dialer.DialContext(dialCtx, "tcp", address)
	cancel()
	if err != nil {
		return fmt.Errorf("mtproto: dial DC %d at %s: %w", sess.DC, address, err)
	}
	defer upstream.Close()
	_ = upstream.SetDeadline(s.now().Add(s.cfg.HandshakeTimeout))

	nonce, upSend, upRecv, err := newUpstreamHandshake(sess.Tag, sess.DC, s.rng)
	if err != nil {
		return err
	}
	if err := writeFull(upstream, nonce[:]); err != nil {
		return fmt.Errorf("mtproto: write upstream handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Time{})

	info := SessionInfo{
		ID:           s.sessionSeq.Add(1),
		CredentialID: cred.ID,
		DC:           sess.DC,
		Tag:          sess.Tag,
		Remote:       remote,
		SNI:          hello.sni,
		Since:        s.now(),
	}
	s.observer.Opened(info)
	defer s.observer.Closed(info)
	return s.relay(ctx, conn, upstream, tlsReader, sess, upSend, upRecv, info)
}

func (s *Server) fail(ctx context.Context, conn net.Conn, initial []byte, cause error) error {
	if s.cfg.Fallback == nil {
		return cause
	}
	if err := s.cfg.Fallback(ctx, conn, initial); err != nil {
		return fmt.Errorf("mtproto: fallback after %v: %w", cause, err)
	}
	return nil
}

func (s *Server) dcAddress(dc int16) (string, bool) {
	if s.cfg.DCAddress != nil {
		return s.cfg.DCAddress(dc)
	}
	return DefaultDCAddress(dc, s.cfg.PreferIPv6)
}

type cipherReader struct {
	r io.Reader
	s cryptoStream
}

type cryptoStream interface {
	XORKeyStream(dst, src []byte)
}

func (r *cipherReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.s.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}

type cipherWriter struct {
	w   io.Writer
	s   cryptoStream
	buf []byte
}

func (w *cipherWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if cap(w.buf) < len(p) {
		w.buf = make([]byte, len(p))
	}
	b := w.buf[:len(p)]
	w.s.XORKeyStream(b, p)
	if err := writeFull(w.w, b); err != nil {
		return 0, err
	}
	return len(p), nil
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.n.Add(int64(n))
	}
	return n, err
}

type trafficMeter struct {
	up       atomic.Int64
	down     atomic.Int64
	observer Observer
	credID   string
	stop     chan struct{}
	done     chan struct{}
}

func newTrafficMeter(o Observer, credID string) *trafficMeter {
	return &trafficMeter{observer: o, credID: credID, stop: make(chan struct{}), done: make(chan struct{})}
}

func (m *trafficMeter) start(interval time.Duration) {
	go func() {
		defer close(m.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				m.flush()
			case <-m.stop:
				m.flush()
				return
			}
		}
	}()
}

func (m *trafficMeter) flush() {
	up := m.up.Swap(0)
	down := m.down.Swap(0)
	if up != 0 || down != 0 {
		m.observer.AddTraffic(m.credID, uint64(up), uint64(down))
	}
}

func (m *trafficMeter) close() {
	close(m.stop)
	<-m.done
}

func (s *Server) relay(ctx context.Context, client, upstream net.Conn, tlsReader *tlsRecordReader, sess *clientSession, upSend, upRecv cryptoStream, info SessionInfo) error {
	meter := newTrafficMeter(s.observer, info.CredentialID)
	meter.start(s.cfg.StatsInterval)
	defer meter.close()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
			_ = upstream.Close()
		case <-done:
		}
	}()
	defer close(done)

	clientReader := &countingReader{r: &cipherReader{r: tlsReader, s: sess.Dec}, n: &meter.up}
	clientWriter := &cipherWriter{w: &tlsRecordWriter{w: client}, s: sess.Enc}
	upstreamReader := &countingReader{r: &cipherReader{r: upstream, s: upRecv}, n: &meter.down}
	upstreamWriter := &cipherWriter{w: upstream, s: upSend}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := io.CopyBuffer(upstreamWriter, clientReader, make([]byte, 32*1024))
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := io.CopyBuffer(clientWriter, upstreamReader, make([]byte, 32*1024))
		errs <- err
	}()

	first := <-errs
	_ = client.Close()
	_ = upstream.Close()
	second := <-errs
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return relayError(first, second)
}

func relayError(errs ...error) error {
	for _, err := range errs {
		if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		return err
	}
	return nil
}
