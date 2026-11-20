package webproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
)

type transportSession struct {
	relay        *Relay
	credentialID string
	token        string
	ordinal      uint64
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}

	mu             sync.Mutex
	closed         bool
	lastPoll       atomic.Int64
	streams        map[uint32]*webStream
	tombstones     map[uint32]struct{}
	tombstoneIDs   []uint32
	upSeq          uint64
	upInFlight     bool
	lastUpHash     [sha256.Size]byte
	outQueue       []*queuedFrame
	outSignal      chan struct{}
	pendingWindows map[uint32]uint32
	pendingCloses  map[uint32]struct{}
	ackedCursor    uint64
	lastBatch      []byte
	lastBatchFrom  uint64
	lastBatchTo    uint64
	lastBatchCost  int64
	hasLastBatch   bool
	activePoll     *downPoll
	children       sync.WaitGroup
}

type webStream struct {
	session *transportSession
	id      uint32
	ctx     context.Context
	cancel  context.CancelFunc
	local   net.Conn
	backend net.Conn
	done    chan struct{}

	started      bool
	closed       bool
	recvCredit   uint64
	sendCredit   uint64
	incoming     []*inboundChunk
	incomingWake chan struct{}
	windowWake   chan struct{}
	stopOnce     sync.Once
}

type inboundChunk struct {
	data   []byte
	offset int
	charge int64
}

type queuedFrame struct {
	encoded  []byte
	charge   int64
	streamID uint32
}

type downPoll struct {
	cursor   uint64
	takeover chan struct{}
}

type upActionKind uint8

const (
	upOpen upActionKind = iota
	upData
	upWindow
	upClose
	upIgnore
)

type upAction struct {
	kind   upActionKind
	frame  frame
	stream *webStream
}

type streamShadow struct {
	stream     *webStream
	new        bool
	alive      bool
	dead       bool
	recvCredit uint64
	sendCredit uint64
}

var errInvalidUp = errors.New("invalid uplink batch")

func (s *transportSession) run() {
	<-s.ctx.Done()

	s.mu.Lock()
	s.closed = true
	streams := make([]*webStream, 0, len(s.streams))
	for _, stream := range s.streams {
		s.markStreamClosedLocked(stream, false)
		streams = append(streams, stream)
	}
	var releaseDown int64
	for _, chunk := range s.outQueue {
		releaseDown += chunk.charge
	}
	s.outQueue = nil
	if s.hasLastBatch {
		releaseDown += s.lastBatchCost
		s.lastBatch = nil
		s.lastBatchCost = 0
		s.hasLastBatch = false
	}
	if s.activePoll != nil {
		close(s.activePoll.takeover)
		s.activePoll = nil
	}
	s.mu.Unlock()
	if releaseDown > 0 {
		s.relay.releasePending(s.credentialID, pendingDown, releaseDown)
	}
	s.relay.removeSession(s)
	for _, stream := range streams {
		stream.stop()
	}
	s.children.Wait()
	close(s.done)
}

func (s *transportSession) startStream(stream *webStream) {
	s.mu.Lock()
	if stream.started {
		s.mu.Unlock()
		return
	}
	stream.started = true
	if s.closed || stream.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		stream.stop()
		close(stream.done)
		return
	}
	s.children.Add(1)
	s.mu.Unlock()
	if !s.relay.spawn(func() {
		defer s.children.Done()
		stream.run()
	}) {
		s.children.Done()
		stream.stop()
		close(stream.done)
	}
}

func (s *transportSession) applyUp(seq uint64, body []byte, frames []frame, remoteAddr string) (uint64, error) {
	bodyHash := sha256.Sum256(body)
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return 0, errRelayClosed
	}
	if seq == 0 {
		s.mu.Unlock()
		return 0, errInvalidUp
	}
	if seq == s.upSeq {
		if bodyHash == s.lastUpHash {
			ack := s.upSeq
			s.mu.Unlock()
			return ack, nil
		}
		s.mu.Unlock()
		return 0, errInvalidUp
	}
	if seq != s.upSeq+1 || s.upSeq == ^uint64(0) {
		s.mu.Unlock()
		return 0, errInvalidUp
	}

	shadows := make(map[uint32]*streamShadow, len(frames))
	deadAdded := make(map[uint32]struct{})
	shadowFor := func(id uint32) *streamShadow {
		if shadow, exists := shadows[id]; exists {
			return shadow
		}
		shadow := &streamShadow{}
		if stream := s.streams[id]; stream != nil {
			shadow.stream = stream
			shadow.alive = true
			shadow.recvCredit = stream.recvCredit
			shadow.sendCredit = stream.sendCredit
		} else if _, dead := s.tombstones[id]; dead {
			shadow.dead = true
		}
		shadows[id] = shadow
		return shadow
	}
	activeCount := len(s.streams)
	actions := make([]upAction, 0, len(frames))
	var pendingCharge int64
	closeShadow := func(id uint32, shadow *streamShadow) {
		if shadow != nil && shadow.alive {
			shadow.alive = false
			shadow.dead = true
			activeCount--
			deadAdded[id] = struct{}{}
		}
	}
	for _, frame := range frames {
		var shadow *streamShadow
		if frame.StreamID != 0 {
			shadow = shadowFor(frame.StreamID)
		}
		switch frame.Type {
		case frameOpen:
			if shadow != nil && shadow.alive {
				closeShadow(frame.StreamID, shadow)
				actions = append(actions, upAction{kind: upClose, frame: frame, stream: shadow.stream})
				continue
			}
			if shadow.dead {
				actions = append(actions, upAction{kind: upClose, frame: frame})
				continue
			}
			if activeCount >= maxStreamsPerSession {
				shadow.dead = true
				deadAdded[frame.StreamID] = struct{}{}
				actions = append(actions, upAction{kind: upClose, frame: frame})
				continue
			}
			shadow = &streamShadow{new: true, alive: true, recvCredit: initialStreamWindow, sendCredit: initialStreamWindow}
			shadows[frame.StreamID] = shadow
			activeCount++
			actions = append(actions, upAction{kind: upOpen, frame: frame})
		case frameData:
			if shadow != nil && shadow.alive {
				if uint64(len(frame.Payload)) > shadow.recvCredit {
					closeShadow(frame.StreamID, shadow)
					actions = append(actions, upAction{kind: upClose, frame: frame, stream: shadow.stream})
					continue
				}
				shadow.recvCredit -= uint64(len(frame.Payload))
				pendingCharge += dataFrameCharge(len(frame.Payload))
				actions = append(actions, upAction{kind: upData, frame: frame, stream: shadow.stream})
				continue
			}
			if !shadow.dead {
				shadow.dead = true
				deadAdded[frame.StreamID] = struct{}{}
				actions = append(actions, upAction{kind: upClose, frame: frame})
			} else {
				actions = append(actions, upAction{kind: upIgnore, frame: frame})
			}
		case frameWindow:
			credit, _ := parseWindowPayload(frame.Payload)
			if shadow != nil && shadow.alive {
				if shadow.sendCredit+uint64(credit) > initialStreamWindow {
					closeShadow(frame.StreamID, shadow)
					actions = append(actions, upAction{kind: upClose, frame: frame, stream: shadow.stream})
					continue
				}
				shadow.sendCredit += uint64(credit)
				actions = append(actions, upAction{kind: upWindow, frame: frame, stream: shadow.stream})
				continue
			}
			if !shadow.dead {
				shadow.dead = true
				deadAdded[frame.StreamID] = struct{}{}
				actions = append(actions, upAction{kind: upClose, frame: frame})
			} else {
				actions = append(actions, upAction{kind: upIgnore, frame: frame})
			}
		case frameClose:
			if shadow != nil && shadow.alive {
				closeShadow(frame.StreamID, shadow)
				actions = append(actions, upAction{kind: upClose, frame: frame, stream: shadow.stream})
				continue
			}
			if !shadow.dead {
				shadow.dead = true
				deadAdded[frame.StreamID] = struct{}{}
				actions = append(actions, upAction{kind: upClose, frame: frame})
			} else {
				actions = append(actions, upAction{kind: upIgnore, frame: frame})
			}
		case framePong:
			actions = append(actions, upAction{kind: upIgnore, frame: frame})
		default:
			s.mu.Unlock()
			return 0, errInvalidUp
		}
	}
	if !s.relay.reservePending(s.credentialID, pendingUp, pendingCharge) {
		s.mu.Unlock()
		return 0, errPendingCapacity
	}
	if s.ctx.Err() != nil {
		s.relay.releasePending(s.credentialID, pendingUp, pendingCharge)
		s.mu.Unlock()
		return 0, errRelayClosed
	}

	newStreams := make([]*webStream, 0)
	stopStreams := make([]*webStream, 0)
	for _, action := range actions {
		switch action.kind {
		case upOpen:
			local, backend := net.Pipe()
			streamCtx, cancel := context.WithCancel(s.ctx)
			stream := &webStream{
				session:      s,
				id:           action.frame.StreamID,
				ctx:          streamCtx,
				cancel:       cancel,
				local:        local,
				backend:      remoteAddressConn{Conn: backend, remote: tcpRemoteAddr(remoteAddr)},
				done:         make(chan struct{}),
				recvCredit:   initialStreamWindow,
				sendCredit:   initialStreamWindow,
				incomingWake: make(chan struct{}, 1),
				windowWake:   make(chan struct{}, 1),
			}
			s.streams[stream.id] = stream
			shadows[stream.id].stream = stream
			newStreams = append(newStreams, stream)
		case upData:
			stream := action.stream
			if stream == nil {
				stream = shadows[action.frame.StreamID].stream
			}
			if stream == nil || stream.closed {
				charge := dataFrameCharge(len(action.frame.Payload))
				s.relay.releasePending(s.credentialID, pendingUp, charge)
				continue
			}
			stream.incoming = append(stream.incoming, &inboundChunk{
				data:   append([]byte(nil), action.frame.Payload...),
				charge: dataFrameCharge(len(action.frame.Payload)),
			})
			signal(stream.incomingWake)
		case upWindow:
			if action.stream != nil && !action.stream.closed {
				signal(action.stream.windowWake)
			}
		case upClose:
			stream := action.stream
			if stream == nil {
				stream = s.streams[action.frame.StreamID]
			}
			if stream != nil {
				s.markStreamClosedLocked(stream, false)
				stopStreams = append(stopStreams, stream)
			}
			s.addTombstoneLocked(action.frame.StreamID)
			s.queueCloseLocked(action.frame.StreamID)
		case upIgnore:
		}
	}
	for id, shadow := range shadows {
		if shadow.alive && shadow.stream != nil {
			shadow.stream.recvCredit = shadow.recvCredit
			shadow.stream.sendCredit = shadow.sendCredit
		} else if shadow.new && !shadow.alive {
			delete(s.streams, id)
			s.addTombstoneLocked(id)
			if shadow.stream != nil {
				shadow.stream.closed = true
				s.closeUnstartedLocked(shadow.stream)
			}
		}
	}
	for id := range deadAdded {
		if shadow := shadows[id]; shadow == nil || !shadow.alive {
			s.addTombstoneLocked(id)
		}
	}
	s.upSeq = seq
	s.lastUpHash = bodyHash
	for _, stream := range newStreams {
		if s.streams[stream.id] != stream {
			stream.stop()
		}
	}
	s.mu.Unlock()

	for _, stream := range stopStreams {
		stream.stop()
	}
	for _, stream := range newStreams {
		if s.isActiveStream(stream) {
			s.startStream(stream)
		}
	}
	return seq, nil
}

func (s *transportSession) beginUp(seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return errRelayClosed
	}
	if s.upInFlight {
		return errPendingCapacity
	}
	if seq == 0 {
		return errInvalidUp
	}
	if seq != s.upSeq && (s.upSeq == ^uint64(0) || seq != s.upSeq+1) {
		return errInvalidUp
	}
	s.upInFlight = true
	return nil
}

func (s *transportSession) finishUp() {
	s.mu.Lock()
	s.upInFlight = false
	s.mu.Unlock()
}

func (s *transportSession) isActiveStream(stream *webStream) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.streams[stream.id] == stream && !stream.closed
}

func (s *transportSession) markStreamClosedLocked(stream *webStream, sendClose bool) {
	if stream == nil || stream.closed {
		return
	}
	stream.closed = true
	s.closeUnstartedLocked(stream)
	if s.streams[stream.id] == stream {
		delete(s.streams, stream.id)
	}
	s.addTombstoneLocked(stream.id)
	for _, chunk := range stream.incoming {
		s.relay.releasePending(s.credentialID, pendingUp, chunk.charge)
	}
	stream.incoming = nil
	queued := s.outQueue[:0]
	for _, item := range s.outQueue {
		if item.streamID == stream.id {
			s.relay.releasePending(s.credentialID, pendingDown, item.charge)
			continue
		}
		queued = append(queued, item)
	}
	s.outQueue = queued
	delete(s.pendingWindows, stream.id)
	if sendClose {
		s.queueCloseLocked(stream.id)
	}
}

func (s *transportSession) queueCloseLocked(streamID uint32) {
	if _, exists := s.pendingCloses[streamID]; exists || len(s.pendingCloses) >= maxStreamsPerSession {
		return
	}
	s.pendingCloses[streamID] = struct{}{}
	s.signalOutputLocked()
}

func (s *transportSession) queueWindowLocked(streamID, credit uint32) {
	if credit == 0 {
		return
	}
	if stream := s.streams[streamID]; stream == nil || stream.closed {
		return
	}
	if current := s.pendingWindows[streamID]; uint64(current)+uint64(credit) <= initialStreamWindow {
		s.pendingWindows[streamID] = current + credit
	} else {
		return
	}
	s.signalOutputLocked()
}

func (s *transportSession) signalOutputLocked() {
	close(s.outSignal)
	s.outSignal = make(chan struct{})
}

func (s *transportSession) closeUnstartedLocked(stream *webStream) {
	if stream.started {
		return
	}
	stream.started = true
	close(stream.done)
}

func (s *transportSession) addTombstoneLocked(id uint32) {
	if _, exists := s.tombstones[id]; exists {
		return
	}
	s.tombstones[id] = struct{}{}
	s.tombstoneIDs = append(s.tombstoneIDs, id)
	if len(s.tombstoneIDs) > maxTombstones {
		oldest := s.tombstoneIDs[0]
		s.tombstoneIDs = s.tombstoneIDs[1:]
		delete(s.tombstones, oldest)
	}
}

func (s *transportSession) closeAllStreams(sendClose bool) {
	s.mu.Lock()
	streams := make([]*webStream, 0, len(s.streams))
	for _, stream := range s.streams {
		s.markStreamClosedLocked(stream, sendClose)
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		stream.stop()
	}
}

func (s *transportSession) finishStream(stream *webStream, sendClose bool) {
	s.mu.Lock()
	wasActive := s.streams[stream.id] == stream && !stream.closed
	if wasActive {
		s.markStreamClosedLocked(stream, sendClose)
	}
	s.mu.Unlock()
	stream.stop()
}

func (s *transportSession) enqueueReserved(f frame, charge int64) bool {
	encoded, err := appendFrame(nil, f)
	if err != nil {
		s.relay.releasePending(s.credentialID, pendingDown, charge)
		return false
	}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		s.relay.releasePending(s.credentialID, pendingDown, charge)
		return false
	}
	if f.StreamID != 0 {
		stream := s.streams[f.StreamID]
		if stream == nil || stream.closed {
			s.mu.Unlock()
			s.relay.releasePending(s.credentialID, pendingDown, charge)
			return false
		}
	}
	s.outQueue = append(s.outQueue, &queuedFrame{encoded: encoded, charge: charge, streamID: f.StreamID})
	s.signalOutputLocked()
	s.mu.Unlock()
	return true
}

func (s *transportSession) down(cursor uint64, requestCtx context.Context) ([]byte, uint64, bool, error) {
	var releaseDown int64
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil, cursor, false, errRelayClosed
	}
	if s.hasLastBatch {
		if cursor != s.lastBatchFrom && cursor != s.lastBatchTo {
			s.mu.Unlock()
			return nil, cursor, false, errInvalidUp
		}
	} else if cursor != s.ackedCursor {
		s.mu.Unlock()
		return nil, cursor, false, errInvalidUp
	}
	poll := &downPoll{cursor: cursor, takeover: make(chan struct{})}
	if s.activePoll != nil {
		close(s.activePoll.takeover)
	}
	s.activePoll = poll
	if s.hasLastBatch && cursor == s.lastBatchFrom {
		body := append([]byte(nil), s.lastBatch...)
		to := s.lastBatchTo
		s.activePoll = nil
		s.mu.Unlock()
		return body, to, true, nil
	}
	if s.hasLastBatch && cursor == s.lastBatchTo {
		releaseDown += s.lastBatchCost
		s.hasLastBatch = false
		s.lastBatch = nil
		s.lastBatchCost = 0
		s.ackedCursor = cursor
		s.lastPoll.Store(s.relay.clock.Now().UnixNano())
	}
	if body, to, ok := s.takeBatchLocked(cursor); ok {
		s.activePoll = nil
		s.mu.Unlock()
		if releaseDown > 0 {
			s.relay.releasePending(s.credentialID, pendingDown, releaseDown)
		}
		return body, to, true, nil
	}
	if releaseDown == 0 {
		s.lastPoll.Store(s.relay.clock.Now().UnixNano())
	}
	wake := s.outSignal
	s.mu.Unlock()
	if releaseDown > 0 {
		s.relay.releasePending(s.credentialID, pendingDown, releaseDown)
	}

	timer := s.relay.clock.NewTimer(downLongPollTimeout)
	defer timer.Stop()
	for {
		select {
		case <-requestCtx.Done():
			s.clearPoll(poll)
			return nil, cursor, false, requestCtx.Err()
		case <-s.ctx.Done():
			s.clearPoll(poll)
			return nil, cursor, false, errRelayClosed
		case <-poll.takeover:
			s.mu.Lock()
			if s.activePoll != nil {
				s.signalOutputLocked()
			}
			s.mu.Unlock()
			return nil, cursor, false, nil
		case <-timer.C():
			s.clearPoll(poll)
			return nil, cursor, false, nil
		case <-wake:
			s.mu.Lock()
			if s.activePoll != poll {
				s.signalOutputLocked()
				s.mu.Unlock()
				return nil, cursor, false, nil
			}
			if body, to, ok := s.takeBatchLocked(cursor); ok {
				s.activePoll = nil
				s.mu.Unlock()
				return body, to, true, nil
			}
			wake = s.outSignal
			s.mu.Unlock()
		}
	}
}

func (s *transportSession) takeBatchLocked(cursor uint64) ([]byte, uint64, bool) {
	if len(s.outQueue) == 0 && len(s.pendingWindows) == 0 && len(s.pendingCloses) == 0 || cursor == ^uint64(0) {
		return nil, cursor, false
	}
	var batch bytes.Buffer
	var charge int64
	count := 0
	consumed := 0
	for consumed < len(s.outQueue) && count < maxDownFramesPerBatch {
		item := s.outQueue[consumed]
		if batch.Len()+len(item.encoded) > maxDownBatch {
			break
		}
		_, _ = batch.Write(item.encoded)
		charge += item.charge
		consumed++
		count++
	}
	var windows []uint32
	for streamID, credit := range s.pendingWindows {
		if count >= maxDownFramesPerBatch {
			break
		}
		var payload [4]byte
		binary.BigEndian.PutUint32(payload[:], credit)
		encoded, err := appendFrame(nil, frame{Type: frameWindow, StreamID: streamID, Payload: payload[:]})
		if err != nil || batch.Len()+len(encoded) > maxDownBatch {
			break
		}
		_, _ = batch.Write(encoded)
		windows = append(windows, streamID)
		count++
	}
	var closes []uint32
	for streamID := range s.pendingCloses {
		if count >= maxDownFramesPerBatch {
			break
		}
		encoded, err := appendFrame(nil, frame{Type: frameClose, StreamID: streamID})
		if err != nil || batch.Len()+len(encoded) > maxDownBatch {
			break
		}
		_, _ = batch.Write(encoded)
		closes = append(closes, streamID)
		count++
	}
	if count == 0 {
		return nil, cursor, false
	}
	s.outQueue = append([]*queuedFrame(nil), s.outQueue[consumed:]...)
	for _, streamID := range windows {
		delete(s.pendingWindows, streamID)
	}
	for _, streamID := range closes {
		delete(s.pendingCloses, streamID)
	}
	s.lastBatch = append([]byte(nil), batch.Bytes()...)
	s.lastBatchFrom = cursor
	s.lastBatchTo = cursor + 1
	s.lastBatchCost = charge
	s.hasLastBatch = true
	return append([]byte(nil), s.lastBatch...), s.lastBatchTo, true
}

func (s *transportSession) clearPoll(poll *downPoll) {
	s.mu.Lock()
	if s.activePoll == poll {
		s.activePoll = nil
	}
	s.mu.Unlock()
}

func (st *webStream) run() {
	defer close(st.done)
	if st.ctx.Err() != nil {
		return
	}
	var workers sync.WaitGroup
	launch := func(fn func()) bool {
		workers.Add(1)
		if !st.session.relay.spawn(func() {
			defer workers.Done()
			defer func() {
				if recover() != nil {
					st.session.finishStream(st, true)
				}
			}()
			fn()
		}) {
			workers.Done()
			return false
		}
		return true
	}
	if !launch(st.incomingLoop) {
		st.session.finishStream(st, true)
		return
	}
	if !launch(st.outgoingLoop) {
		st.session.finishStream(st, true)
		workers.Wait()
		return
	}
	st.backendLoop()
	workers.Wait()
}

func (st *webStream) backendLoop() {
	defer func() {
		if recover() != nil {
			st.session.finishStream(st, true)
		}
	}()
	_ = st.session.relay.backend(st.ctx, st.backend, st.session.credentialID)
	st.session.finishStream(st, true)
}

func (st *webStream) incomingLoop() {
	for {
		st.session.mu.Lock()
		if st.closed || st.ctx.Err() != nil {
			st.session.mu.Unlock()
			return
		}
		var chunk *inboundChunk
		if len(st.incoming) > 0 {
			chunk = st.incoming[0]
			st.incoming[0] = nil
			st.incoming = st.incoming[1:]
		}
		st.session.mu.Unlock()
		if chunk == nil {
			select {
			case <-st.ctx.Done():
				return
			case <-st.incomingWake:
				continue
			}
		}
		for chunk.offset < len(chunk.data) {
			written, err := st.local.Write(chunk.data[chunk.offset:])
			if written > 0 {
				chunk.offset += written
				chunk.charge -= int64(written)
				st.session.relay.releasePending(st.session.credentialID, pendingUp, int64(written))
				st.session.mu.Lock()
				if !st.closed {
					st.recvCredit += uint64(written)
					if st.recvCredit > initialStreamWindow {
						st.recvCredit = initialStreamWindow
					}
				}
				st.session.queueWindowLocked(st.id, uint32(written))
				st.session.mu.Unlock()
			}
			if err != nil || written == 0 {
				st.session.relay.releasePending(st.session.credentialID, pendingUp, chunk.charge)
				st.session.finishStream(st, true)
				return
			}
		}
		st.session.relay.releasePending(st.session.credentialID, pendingUp, chunk.charge)
	}
}

func (st *webStream) outgoingLoop() {
	buffer := make([]byte, backendChunkSize)
	for {
		st.session.mu.Lock()
		if st.closed || st.ctx.Err() != nil {
			st.session.mu.Unlock()
			return
		}
		if st.sendCredit == 0 {
			st.session.mu.Unlock()
			select {
			case <-st.ctx.Done():
				return
			case <-st.windowWake:
				continue
			}
		}
		want := uint64(backendChunkSize)
		if st.sendCredit < want {
			want = st.sendCredit
		}
		st.sendCredit -= want
		st.session.mu.Unlock()

		read, err := st.local.Read(buffer[:int(want)])
		if read > 0 {
			st.restoreCredit(want - uint64(read))
			frame := frame{Type: frameData, StreamID: st.id, Payload: buffer[:read]}
			charge := dataFrameCharge(read)
			if st.session.relay.waitForPending(st.ctx, st.session.credentialID, pendingDown, charge) {
				st.session.enqueueReserved(frame, charge)
			} else {
				return
			}
		} else {
			st.restoreCredit(want)
		}
		if err != nil || read == 0 {
			st.session.finishStream(st, true)
			return
		}
	}
}

func (st *webStream) restoreCredit(amount uint64) {
	if amount == 0 {
		return
	}
	st.session.mu.Lock()
	if !st.closed {
		st.sendCredit += amount
		if st.sendCredit > initialStreamWindow {
			st.sendCredit = initialStreamWindow
		}
	}
	st.session.mu.Unlock()
}

func (st *webStream) stop() {
	st.stopOnce.Do(func() {
		st.cancel()
		_ = st.local.Close()
		_ = st.backend.Close()
	})
}

func dataFrameCharge(payloadBytes int) int64 {
	return int64(payloadBytes + frameHeaderSize + pendingFrameOverhead)
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func parseFrameBatch(body []byte) ([]frame, error) {
	if len(body) == 0 || len(body) > maxUpBatchBytes {
		return nil, errInvalidUp
	}
	rest := body
	frames := make([]frame, 0, 16)
	for len(rest) > 0 {
		frame, next, err := parseFrame(rest)
		if err != nil {
			return nil, errInvalidUp
		}
		frames = append(frames, frame)
		if len(frames) > maxDownFramesPerBatch {
			return nil, errInvalidUp
		}
		rest = next
	}
	return frames, nil
}

type remoteAddressConn struct {
	net.Conn
	remote *net.TCPAddr
}

func (c remoteAddressConn) RemoteAddr() net.Addr { return c.remote }

func tcpRemoteAddr(value string) *net.TCPAddr {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return &net.TCPAddr{}
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return &net.TCPAddr{}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return &net.TCPAddr{}
	}
	return &net.TCPAddr{IP: net.IP(addr.AsSlice()), Port: port, Zone: addr.Zone()}
}
