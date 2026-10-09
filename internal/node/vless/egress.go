package vless

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	xuuid "github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/ratelimit"
	"github.com/mistgate/mistgate/internal/node/torrentguard"
	"github.com/mistgate/mistgate/internal/plugin"
)

type stoppable interface{ Stop() bool }
type afterFunc func(time.Duration, func()) stoppable

type credentialIndex struct {
	byID map[string]*credState
}

type parsedCredential struct {
	cred plugin.UserCred
	uuid xuuid.UUID
}

type credIdentity struct{ userID string }

type credState struct {
	id        string
	inboundID string
	uuid      xuuid.UUID
	owner     atomic.Pointer[credIdentity]

	validUntil atomic.Int64
	limit      atomic.Pointer[ratelimit.Limiter]
	up, down   atomic.Uint64
	removed    atomic.Bool

	now   func() time.Time
	after afterFunc

	mu          sync.Mutex
	links       map[*transport.Link]*linkHandle
	since       time.Time
	expiryTimer stoppable
}

func buildIndex(inboundID string, old *credentialIndex, creds []plugin.UserCred, now func() time.Time, after afterFunc) (*credentialIndex, error) {
	parsed := make([]parsedCredential, 0, len(creds))
	seenID := make(map[string]struct{}, len(creds))
	seenUUID := make(map[xuuid.UUID]string, len(creds))
	for _, c := range creds {
		if c.CredID == "" {
			return nil, errors.New("credential without id")
		}
		var data struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(c.Data, &data); err != nil {
			return nil, fmt.Errorf("credential %s: data: %w", c.CredID, err)
		}
		u, err := parseUUID(data.ID)
		if err != nil {
			return nil, fmt.Errorf("credential %s: data.id must be a UUID", c.CredID)
		}
		if _, dup := seenID[c.CredID]; dup {
			return nil, fmt.Errorf("duplicate credential id %s", c.CredID)
		}
		if other, dup := seenUUID[u]; dup {
			return nil, fmt.Errorf("credentials %s and %s share one UUID", other, c.CredID)
		}
		seenID[c.CredID] = struct{}{}
		seenUUID[u] = c.CredID
		c.Data = append([]byte(nil), c.Data...)
		parsed = append(parsed, parsedCredential{cred: c, uuid: u})
	}

	ix := &credentialIndex{byID: make(map[string]*credState, len(parsed))}
	for _, p := range parsed {
		var cs *credState
		if old != nil {
			if previous := old.byID[p.cred.CredID]; previous != nil && previous.uuid == p.uuid && !previous.removed.Load() {
				cs = previous
			}
		}
		if cs == nil {
			cs = &credState{id: p.cred.CredID, inboundID: inboundID, uuid: p.uuid, links: make(map[*transport.Link]*linkHandle), now: now, after: after}
		}
		cs.configure(p.cred)
		ix.byID[cs.id] = cs
	}
	return ix, nil
}

func parseUUID(text string) (xuuid.UUID, error) {
	if len(text) != 36 || text[8] != '-' || text[13] != '-' || text[18] != '-' || text[23] != '-' {
		return xuuid.UUID{}, errors.New("invalid UUID")
	}
	flat := strings.ReplaceAll(text, "-", "")
	b, err := hex.DecodeString(flat)
	if err != nil {
		return xuuid.UUID{}, err
	}
	return xuuid.ParseBytes(b)
}

func (cs *credState) configure(c plugin.UserCred) {
	cs.owner.Store(&credIdentity{userID: c.UserID})
	var until int64
	if !c.ValidUntil.IsZero() {
		until = c.ValidUntil.Unix()
	}
	cs.validUntil.Store(until)
	switch current := cs.limit.Load(); {
	case c.RateLimitBps == 0:
		cs.limit.Store(nil)
	case current == nil || current.BPS() != c.RateLimitBps:
		cs.limit.Store(ratelimit.New(c.RateLimitBps))
	}
	cs.mu.Lock()
	cs.armExpiryLocked()
	cs.mu.Unlock()
}

func (cs *credState) armExpiryLocked() {
	if cs.expiryTimer != nil {
		cs.expiryTimer.Stop()
		cs.expiryTimer = nil
	}
	until := cs.validUntil.Load()
	if until == 0 || len(cs.links) == 0 {
		return
	}
	d := time.Unix(until, 0).Sub(cs.now())
	if d <= 0 {
		for _, link := range cs.links {
			link.stop()
		}
		return
	}
	cs.expiryTimer = cs.after(d, cs.expire)
}

func (cs *credState) expire() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	until := cs.validUntil.Load()
	if until == 0 || cs.now().Unix() < until {
		cs.armExpiryLocked()
		return
	}
	cs.expiryTimer = nil
	for _, link := range cs.links {
		link.stop()
	}
}

func (cs *credState) addLink(parent context.Context, link *transport.Link, inboundConn net.Conn) (*linkHandle, bool) {
	if cs.removed.Load() || cs.isExpired() {
		return nil, false
	}
	h := newLinkHandle(parent, link, inboundConn)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.removed.Load() || cs.isExpired() {
		h.stop()
		return nil, false
	}
	if len(cs.links) == 0 {
		cs.since = cs.now()
	}
	cs.links[link] = h
	cs.armExpiryLocked()
	return h, true
}

func (cs *credState) removeLink(link *transport.Link, h *linkHandle) {
	cs.mu.Lock()
	if cs.links[link] == h {
		delete(cs.links, link)
		if len(cs.links) == 0 {
			cs.since = time.Time{}
			if cs.expiryTimer != nil {
				cs.expiryTimer.Stop()
				cs.expiryTimer = nil
			}
		}
	}
	cs.mu.Unlock()
}

func (cs *credState) isExpired() bool {
	until := cs.validUntil.Load()
	return until != 0 && cs.now().Unix() >= until
}

func (cs *credState) retire() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.removed.Swap(true) {
		return false
	}
	if cs.expiryTimer != nil {
		cs.expiryTimer.Stop()
		cs.expiryTimer = nil
	}
	for _, link := range cs.links {
		link.stop()
	}
	return true
}

func (cs *credState) cancelLinks() int {
	cs.mu.Lock()
	links := make([]*linkHandle, 0, len(cs.links))
	for _, link := range cs.links {
		links = append(links, link)
	}
	cs.mu.Unlock()
	for _, link := range links {
		link.stop()
	}
	if len(links) > 0 {
		return 1
	}
	return 0
}

func (cs *credState) session() (time.Time, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.since, len(cs.links) != 0
}

func sortedCredentialIDs(ix *credentialIndex) []string {
	ids := make([]string, 0, len(ix.byID))
	for id := range ix.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type linkHandle struct {
	ctx         context.Context
	cancel      context.CancelFunc
	link        *transport.Link
	inboundConn net.Conn
	activity    signal.ActivityUpdater

	mu     sync.Mutex
	tcp    net.Conn
	udp    engine.EgressUDP
	closed sync.Once
}

func newLinkHandle(parent context.Context, link *transport.Link, inboundConn net.Conn) *linkHandle {
	ctx, cancel := context.WithCancel(parent)
	return &linkHandle{ctx: ctx, cancel: cancel, link: link, inboundConn: inboundConn}
}

func (h *linkHandle) setTCP(conn net.Conn) bool {
	h.mu.Lock()
	if h.ctx.Err() != nil {
		h.mu.Unlock()
		_ = conn.Close()
		return false
	}
	h.tcp = conn
	h.mu.Unlock()
	return true
}

func (h *linkHandle) setUDP(conn engine.EgressUDP) bool {
	h.mu.Lock()
	if h.ctx.Err() != nil {
		h.mu.Unlock()
		_ = conn.Close()
		return false
	}
	h.udp = conn
	h.mu.Unlock()
	return true
}

func (h *linkHandle) stop() {
	h.close(true)
}

func (h *linkHandle) finish() {
	h.close(false)
}

func (h *linkHandle) close(closeInbound bool) {
	h.closed.Do(func() {
		h.cancel()
		h.mu.Lock()
		tcp, udp := h.tcp, h.udp
		h.mu.Unlock()
		if tcp != nil {
			_ = tcp.SetDeadline(time.Now())
			go func() { _ = tcp.Close() }()
		}
		if udp != nil {
			go func() { _ = udp.Close() }()
		}
		if closeInbound && h.inboundConn != nil {
			_ = h.inboundConn.SetDeadline(time.Now())
			go func() { _ = h.inboundConn.Close() }()
		}
		interruptOnly(h.link.Reader)
		interruptOnly(h.link.Writer)
	})
}

// interruptOnly interrupts pipes. It never falls back to Close as common.Interrupt does: on an XHTTP link the writer is
// xray's BufferedWriter, whose Close waits for the lock a write blocked on a client that stopped reading holds, so a kick
// would hang the engine.
func interruptOnly(v any) {
	if i, ok := v.(common.Interruptible); ok {
		i.Interrupt()
	}
}

type countWriter struct {
	buf.Writer
	cred *credState
	up   bool
	ctx  context.Context
}

func (w countWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	n := uint64(mb.Len())
	if limit := w.cred.limit.Load(); limit != nil {
		ok := false
		if w.up {
			ok = limit.WaitUp(w.ctx, n)
		} else {
			ok = limit.WaitDown(w.ctx, n)
		}
		if !ok {
			return w.ctx.Err()
		}
	}
	if err := w.Writer.WriteMultiBuffer(mb); err != nil {
		return err
	}
	if w.up {
		w.cred.up.Add(n)
	} else {
		w.cred.down.Add(n)
	}
	return nil
}

// egressHandler is installed as the only xray outbound for one inbound instance.
type egressHandler struct{ in *inbound }

var _ outbound.Handler = (*egressHandler)(nil)

func (h *egressHandler) Tag() string                          { return "mistgate-vless-egress" }
func (h *egressHandler) Start() error                         { return nil }
func (h *egressHandler) Close() error                         { return nil }
func (h *egressHandler) SenderSettings() *serial.TypedMessage { return nil }
func (h *egressHandler) ProxySettings() *serial.TypedMessage  { return nil }

func (h *egressHandler) Dispatch(ctx context.Context, link *transport.Link) {
	if h.in.dispatch(ctx, link) != nil {
		common.Interrupt(link.Reader)
		common.Interrupt(link.Writer)
	}
}

func (in *inbound) dispatch(ctx context.Context, link *transport.Link) error {
	if in.ctx == nil || in.ctx.Err() != nil {
		return context.Canceled
	}
	obs := session.OutboundsFromContext(ctx)
	if len(obs) == 0 {
		return errors.New("missing destination")
	}
	dest := obs[len(obs)-1].Target
	inboundSession := session.InboundFromContext(ctx)
	if inboundSession == nil || inboundSession.User == nil || inboundSession.User.Email == "" {
		return errors.New("missing credential")
	}
	ix := in.idx.Load()
	if ix == nil {
		return errors.New("credential index unavailable")
	}
	cs := ix.byID[inboundSession.User.Email]
	if cs == nil || cs.removed.Load() || cs.isExpired() {
		return errors.New("credential unavailable")
	}
	handle, ok := cs.addLink(ctx, link, inboundSession.Conn)
	if !ok {
		return errors.New("credential unavailable")
	}
	stopOnInbound := context.AfterFunc(in.ctx, handle.stop)
	if in.ctx.Err() != nil {
		handle.stop()
	}
	// An idle link ends like a natural one (finish): closing the client's connection would also end its mux siblings.
	idleTimer := signal.CancelAfterInactivity(handle.ctx, handle.finish, in.e.idleTimeout)
	handle.activity = idleTimer
	defer func() {
		handle.finish()
		idleTimer.SetTimeout(0)
		cs.removeLink(link, handle)
	}()
	defer stopOnInbound()
	if handle.ctx.Err() != nil {
		return context.Canceled
	}

	switch dest.Network {
	case xnet.Network_TCP:
		return in.dispatchTCP(handle, cs, dest.NetAddr())
	case xnet.Network_UDP:
		return in.dispatchUDP(handle, cs, dest)
	default:
		return fmt.Errorf("network %s is not supported", dest.Network)
	}
}

func (in *inbound) dispatchTCP(handle *linkHandle, cs *credState, addr string) error {
	conn, err := in.out.TCP(addr)
	if err != nil {
		return err
	}
	if in.e.torrentEnabled.Load() {
		owner := cs.owner.Load()
		userID := ""
		if owner != nil {
			userID = owner.userID
		}
		conn = torrentguard.WrapTCP(conn, func() {
			in.reportTorrent(torrentguard.ProtocolBitTorrentTCP, torrentguard.EvidenceTCPHandshake, "tcp", userID, torrentguard.Port(addr))
		})
	}
	if !handle.setTCP(conn) {
		return context.Canceled
	}
	upDone := make(chan error, 1)
	downDone := make(chan error, 1)
	go func() {
		err := buf.Copy(handle.link.Reader, countWriter{Writer: buf.NewWriter(conn), cred: cs, up: true, ctx: handle.ctx}, buf.UpdateActivity(handle.activity))
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		upDone <- err
	}()
	go func() {
		downDone <- buf.Copy(buf.NewReader(conn), countWriter{Writer: handle.link.Writer, cred: cs, ctx: handle.ctx}, buf.UpdateActivity(handle.activity))
	}()
	select {
	case <-handle.ctx.Done():
		return handle.ctx.Err()
	case downErr := <-downDone:
		if downErr != nil {
			return downErr
		}
		// The destination is done: the client sees the end now, and the rest of its upload gets 1 s without traffic
		// (xray's UplinkOnly) instead of the full idle timeout.
		common.Close(handle.link.Writer)
		if t, ok := handle.activity.(*signal.ActivityTimer); ok {
			t.SetTimeout(halfCloseIdle)
		}
		select {
		case upErr := <-upDone:
			return upErr
		case <-handle.ctx.Done():
			return nil
		}
	case upErr := <-upDone:
		if upErr != nil {
			return upErr
		}
		// The client half-closed: the destination may still answer for as long as it keeps sending (xray's 1 s DownlinkOnly
		// would cut a slow answer short). Xray-based clients never half-close, so an upload end is mostly a client that has
		// gone: the answer gets halfClosedAnswerIdle without traffic, not the full idle timeout.
		if t, ok := handle.activity.(*signal.ActivityTimer); ok {
			t.SetTimeout(halfClosedAnswerIdle)
		}
		select {
		case downErr := <-downDone:
			if downErr == nil {
				common.Close(handle.link.Writer)
			}
			return downErr
		case <-handle.ctx.Done():
			return handle.ctx.Err()
		}
	}
}

// How long one direction may stay silent once the other has ended: the upload after the destination closed, the answer
// after the client half-closed. The idle timer checks once per interval, so a link may live up to twice as long.
const (
	halfCloseIdle        = time.Second
	halfClosedAnswerIdle = 30 * time.Second
)

func (in *inbound) dispatchUDP(handle *linkHandle, cs *credState, initial xnet.Destination) error {
	conn, err := in.out.UDP(initial.NetAddr())
	if err != nil {
		return err
	}
	if !handle.setUDP(conn) {
		return context.Canceled
	}
	var guarded torrentguard.UDPConn = conn
	if in.e.torrentEnabled.Load() {
		owner := cs.owner.Load()
		userID := ""
		if owner != nil {
			userID = owner.userID
		}
		guarded = torrentguard.WrapUDP(conn, func(p torrentguard.Protocol, ev torrentguard.Evidence, addr string) {
			in.reportTorrent(p, ev, "udp", userID, torrentguard.Port(addr))
		})
	}
	done := make(chan error, 2)
	go func() { done <- in.udpUpload(handle.ctx, handle.link, cs, guarded, initial, handle.activity) }()
	go func() { done <- in.udpDownload(handle.ctx, handle.link, cs, guarded, initial, handle.activity) }()
	first := <-done
	if handle.ctx.Err() != nil {
		return handle.ctx.Err()
	}
	common.Close(handle.link.Writer)
	handle.finish()
	second := <-done
	if first != nil && !errors.Is(first, context.Canceled) {
		return first
	}
	if second != nil && !errors.Is(second, context.Canceled) {
		return second
	}
	return nil
}

func (in *inbound) udpUpload(ctx context.Context, link *transport.Link, cs *credState, conn torrentguard.UDPConn, initial xnet.Destination, activity signal.ActivityUpdater) error {
	for {
		mb, err := link.Reader.ReadMultiBuffer()
		for _, packet := range mb {
			target := initial
			if packet.UDP != nil {
				target = *packet.UDP
			}
			if limit := cs.limit.Load(); limit != nil && !limit.WaitUp(ctx, uint64(packet.Len())) {
				buf.ReleaseMulti(mb)
				return ctx.Err()
			}
			n, writeErr := conn.WriteTo(packet.Bytes(), target.NetAddr())
			if writeErr != nil {
				buf.ReleaseMulti(mb)
				return writeErr
			}
			cs.up.Add(uint64(n))
			activity.Update()
		}
		buf.ReleaseMulti(mb)
		if err != nil {
			return err
		}
	}
}

func (in *inbound) udpDownload(ctx context.Context, link *transport.Link, cs *credState, conn torrentguard.UDPConn, initial xnet.Destination, activity signal.ActivityUpdater) error {
	packet := make([]byte, 65535)
	for {
		n, source, err := conn.ReadFrom(packet)
		if err != nil {
			return err
		}
		if limit := cs.limit.Load(); limit != nil && !limit.WaitDown(ctx, uint64(n)) {
			return ctx.Err()
		}
		response := buf.FromBytes(append([]byte(nil), packet[:n]...))
		dest, parseErr := xnet.ParseDestination("udp:" + source)
		if parseErr != nil {
			dest = initial
		}
		response.UDP = &dest
		if err := link.Writer.WriteMultiBuffer(buf.MultiBuffer{response}); err != nil {
			response.Release()
			return err
		}
		cs.down.Add(uint64(n))
		activity.Update()
	}
}

func (in *inbound) reportTorrent(protocol torrentguard.Protocol, evidence torrentguard.Evidence, transport, userID, dstPort string) {
	if in.e.env.Event == nil {
		return
	}
	params := map[string]string{"protocol": transport, "torrent_protocol": string(protocol), "evidence": string(evidence)}
	if dstPort != "" {
		params["dst_port"] = dstPort
	}
	if userID != "" {
		params["user_id"] = userID
	}
	in.e.env.Event(engine.Event{Code: "torrent_attempt", InboundID: in.spec.ID, Warning: true, Params: params})
}
