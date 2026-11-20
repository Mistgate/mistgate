package webproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type transportSession struct {
	relay        *Relay
	credentialID string
	token        string
	createdAt    time.Time
	ordinal      uint64
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}

	mu            sync.Mutex
	closed        bool
	lastPoll      time.Time
	streams       map[uint32]*webStream
	tombstones    map[uint32]struct{}
	tombstoneIDs  []uint32
	upSeq         uint64
	upInFlight    uint64
	lastUpBody    []byte
	outQueue      []*queuedFrame
	outSignal     chan struct{}
	ackedCursor   uint64
	lastBatch     []byte
	lastBatchFrom uint64
	lastBatchTo   uint64
	lastBatchCost int64
	hasLastBatch  bool
	activePoll    *downPoll
	children      sync.WaitGroup
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
	typeID   FrameType
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
	frame  Frame
	stream *webStream
}

type streamShadow struct {
	stream     *webStream
	new        bool
	alive      bool
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
	var release int64
	for _, chunk := range s.outQueue {
		release += chunk.charge
	}
	s.outQueue = nil
	if s.hasLastBatch {
		release += s.lastBatchCost
		s.lastBatch = nil
		s.lastBatchCost = 0
		s.hasLastBatch = false
	}
	if s.activePoll != nil {
		close(s.activePoll.takeover)
		s.activePoll = nil
	}
	s.mu.Unlock()
	if release > 0 {
		s.relay.releasePending(release)
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

func (s *transportSession) applyUp(seq uint64, body []byte, frames []Frame) (uint64, bool, []*webStream, error) {
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return 0, false, nil, errRelayClosed
	}
	if seq == 0 {
		s.mu.Unlock()
		return 0, false, nil, errInvalidUp
	}
	if seq == s.upSeq {
		if bytes.Equal(body, s.lastUpBody) {
			ack := s.upSeq
			s.mu.Unlock()
			return ack, true, nil, nil
		}
		s.mu.Unlock()
		return 0, false, nil, errInvalidUp
	}
	if seq != s.upSeq+1 || s.upSeq == ^uint64(0) {
		s.mu.Unlock()
		return 0, false, nil, errInvalidUp
	}

	shadows := make(map[uint32]*streamShadow, len(s.streams)+len(frames))
	for id, stream := range s.streams {
		shadows[id] = &streamShadow{stream: stream, alive: true, recvCredit: stream.recvCredit, sendCredit: stream.sendCredit}
	}
	dead := make(map[uint32]struct{}, len(s.tombstones))
	for id := range s.tombstones {
		dead[id] = struct{}{}
	}
	activeCount := len(s.streams)
	actions := make([]upAction, 0, len(frames))
	var pendingCharge int64
	closeCount := 0
	closeShadow := func(id uint32, shadow *streamShadow) {
		if shadow != nil && shadow.alive {
			shadow.alive = false
			activeCount--
			dead[id] = struct{}{}
			closeCount++
		}
	}
	for _, frame := range frames {
		var shadow *streamShadow
		if frame.StreamID != 0 {
			shadow = shadows[frame.StreamID]
		}
		switch frame.Type {
		case FrameOpen:
			if shadow != nil && shadow.alive {
				closeShadow(frame.StreamID, shadow)
				actions = append(actions, upAction{kind: upClose, frame: frame, stream: shadow.stream})
				continue
			}
			if _, exists := dead[frame.StreamID]; exists {
				closeCount++
				actions = append(actions, upAction{kind: upClose, frame: frame})
				continue
			}
			dead[frame.StreamID] = struct{}{}
			if activeCount >= MaxStreamsPerSession {
				closeCount++
				actions = append(actions, upAction{kind: upClose, frame: frame})
				continue
			}
			delete(dead, frame.StreamID)
			shadow = &streamShadow{new: true, alive: true, recvCredit: InitialStreamWindow, sendCredit: InitialStreamWindow}
			shadows[frame.StreamID] = shadow
			activeCount++
			actions = append(actions, upAction{kind: upOpen, frame: frame})
		case FrameData:
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
			if _, exists := dead[frame.StreamID]; !exists {
				dead[frame.StreamID] = struct{}{}
				closeCount++
				actions = append(actions, upAction{kind: upClose, frame: frame})
			} else {
				actions = append(actions, upAction{kind: upIgnore, frame: frame})
			}
		case FrameWindow:
			credit, _ := ParseWindowPayload(frame.Payload)
			if shadow != nil && shadow.alive {
				if shadow.sendCredit+uint64(credit) > InitialStreamWindow {
					closeShadow(frame.StreamID, shadow)
					actions = append(actions, upAction{kind: upClose, frame: frame, stream: shadow.stream})
					continue
				}
				shadow.sendCredit += uint64(credit)
				actions = append(actions, upAction{kind: upWindow, frame: frame, stream: shadow.stream})
				continue
			}
			if _, exists := dead[frame.StreamID]; !exists {
				dead[frame.StreamID] = struct{}{}
				closeCount++
				actions = append(actions, upAction{kind: upClose, frame: frame})
			} else {
				actions = append(actions, upAction{kind: upIgnore, frame: frame})
			}
		case FrameClose:
			if shadow != nil && shadow.alive {
				closeShadow(frame.StreamID, shadow)
				actions = append(actions, upAction{kind: upClose, frame: frame, stream: shadow.stream})
				continue
			}
			if _, exists := dead[frame.StreamID]; !exists {
				dead[frame.StreamID] = struct{}{}
				closeCount++
				actions = append(actions, upAction{kind: upClose, frame: frame})
			} else {
				actions = append(actions, upAction{kind: upIgnore, frame: frame})
			}
		case FramePong:
			actions = append(actions, upAction{kind: upIgnore, frame: frame})
		default:
			s.mu.Unlock()
			return 0, false, nil, errInvalidUp
		}
	}
	closeCharge := int64(closeCount * (FrameHeaderSize + pendingFrameOverhead))
	if !s.relay.reservePending(pendingCharge + closeCharge) {
		s.mu.Unlock()
		return 0, false, nil, errPendingCapacity
	}
	if s.ctx.Err() != nil {
		s.relay.releasePending(pendingCharge + closeCharge)
		s.mu.Unlock()
		return 0, false, nil, errRelayClosed
	}

	newStreams := make([]*webStream, 0)
	stopStreams := make([]*webStream, 0)
	reservedClose := closeCharge
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
				backend:      backend,
				done:         make(chan struct{}),
				recvCredit:   InitialStreamWindow,
				sendCredit:   InitialStreamWindow,
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
				s.relay.releasePending(charge)
				continue
			}
			stream.recvCredit -= uint64(len(action.frame.Payload))
			stream.incoming = append(stream.incoming, &inboundChunk{
				data:   append([]byte(nil), action.frame.Payload...),
				charge: dataFrameCharge(len(action.frame.Payload)),
			})
			signal(stream.incomingWake)
		case upWindow:
			if action.stream != nil && !action.stream.closed {
				credit, _ := ParseWindowPayload(action.frame.Payload)
				action.stream.sendCredit += uint64(credit)
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
			encoded, _ := AppendFrame(nil, Frame{Type: FrameClose, StreamID: action.frame.StreamID})
			charge := int64(FrameHeaderSize + pendingFrameOverhead)
			s.outQueue = append(s.outQueue, &queuedFrame{encoded: encoded, charge: charge, streamID: action.frame.StreamID, typeID: FrameClose})
			reservedClose -= charge
			signal(s.outSignal)
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
	if reservedClose > 0 {
		s.relay.releasePending(reservedClose)
	}
	s.upSeq = seq
	s.lastUpBody = append([]byte(nil), body...)
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
	return seq, false, newStreams, nil
}

func (s *transportSession) beginUp(seq uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return false, errRelayClosed
	}
	if seq == 0 {
		return false, errInvalidUp
	}
	if seq == s.upSeq {
		return false, nil
	}
	if s.upSeq == ^uint64(0) || seq != s.upSeq+1 {
		return false, errInvalidUp
	}
	if s.upInFlight != 0 {
		return false, errPendingCapacity
	}
	s.upInFlight = seq
	return true, nil
}

func (s *transportSession) finishUp(seq uint64) {
	s.mu.Lock()
	if s.upInFlight == seq {
		s.upInFlight = 0
	}
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
		s.relay.releasePending(chunk.charge)
	}
	stream.incoming = nil
	queued := s.outQueue[:0]
	for _, item := range s.outQueue {
		if item.streamID == stream.id && item.typeID != FrameClose {
			s.relay.releasePending(item.charge)
			continue
		}
		queued = append(queued, item)
	}
	s.outQueue = queued
	if sendClose {
		encoded, _ := AppendFrame(nil, Frame{Type: FrameClose, StreamID: stream.id})
		charge := int64(FrameHeaderSize + pendingFrameOverhead)
		if s.relay.reservePending(charge) {
			s.outQueue = append(s.outQueue, &queuedFrame{encoded: encoded, charge: charge, streamID: stream.id, typeID: FrameClose})
			signal(s.outSignal)
		}
	}
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

func (s *transportSession) closeAllStreams(sendClose bool) []*webStream {
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
	return streams
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

func (s *transportSession) enqueueReserved(f Frame, charge int64) bool {
	encoded, err := AppendFrame(nil, f)
	if err != nil {
		s.relay.releasePending(charge)
		return false
	}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		s.relay.releasePending(charge)
		return false
	}
	if f.StreamID != 0 {
		stream := s.streams[f.StreamID]
		if stream == nil || stream.closed {
			s.mu.Unlock()
			s.relay.releasePending(charge)
			return false
		}
	}
	s.outQueue = append(s.outQueue, &queuedFrame{encoded: encoded, charge: charge, streamID: f.StreamID, typeID: f.Type})
	signal(s.outSignal)
	s.mu.Unlock()
	return true
}

func (s *transportSession) closeHTTP() {
	s.cancel()
	<-s.done
}

func (s *transportSession) down(cursor uint64, requestCtx context.Context) ([]byte, uint64, bool, error) {
	var release int64
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
	s.lastPoll = s.relay.clock.Now()
	s.relay.signalExpiry()
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
		release += s.lastBatchCost
		s.hasLastBatch = false
		s.lastBatch = nil
		s.lastBatchCost = 0
		s.ackedCursor = cursor
	}
	if body, to, ok := s.takeBatchLocked(cursor); ok {
		s.activePoll = nil
		s.mu.Unlock()
		if release > 0 {
			s.relay.releasePending(release)
		}
		return body, to, true, nil
	}
	s.mu.Unlock()
	if release > 0 {
		s.relay.releasePending(release)
	}
	s.relay.signalExpiry()

	timer := s.relay.clock.NewTimer(DownLongPollTimeout)
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
			return nil, cursor, false, nil
		case <-timer.C():
			s.clearPoll(poll)
			return nil, cursor, false, nil
		case <-s.outSignal:
			s.mu.Lock()
			if s.activePoll != poll {
				s.mu.Unlock()
				return nil, cursor, false, nil
			}
			if body, to, ok := s.takeBatchLocked(cursor); ok {
				s.activePoll = nil
				s.mu.Unlock()
				return body, to, true, nil
			}
			s.mu.Unlock()
		}
	}
}

func (s *transportSession) takeBatchLocked(cursor uint64) ([]byte, uint64, bool) {
	if len(s.outQueue) == 0 || cursor == ^uint64(0) {
		return nil, cursor, false
	}
	var batch bytes.Buffer
	var charge int64
	count := 0
	consumed := 0
	for consumed < len(s.outQueue) && count < maxDownFramesPerBatch {
		item := s.outQueue[consumed]
		if batch.Len()+len(item.encoded) > MaxDownBatch {
			break
		}
		_, _ = batch.Write(item.encoded)
		charge += item.charge
		consumed++
		count++
	}
	if consumed == 0 {
		return nil, cursor, false
	}
	s.outQueue = append([]*queuedFrame(nil), s.outQueue[consumed:]...)
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
	if !launch(st.backendLoop) || !launch(st.incomingLoop) || !launch(st.outgoingLoop) {
		st.session.finishStream(st, true)
	}
	workers.Wait()
	st.session.finishStream(st, true)
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
				st.session.relay.releasePending(int64(written))
				st.session.mu.Lock()
				if !st.closed {
					st.recvCredit += uint64(written)
					if st.recvCredit > InitialStreamWindow {
						st.recvCredit = InitialStreamWindow
					}
				}
				st.session.mu.Unlock()
				var payload [4]byte
				binary.BigEndian.PutUint32(payload[:], uint32(written))
				frame := Frame{Type: FrameWindow, StreamID: st.id, Payload: payload[:]}
				charge := framePendingCharge(len(frame.Payload))
				if st.session.relay.waitForPending(st.ctx, charge) {
					st.session.enqueueReserved(frame, charge)
				} else {
					st.session.relay.releasePending(chunk.charge)
					return
				}
			}
			if err != nil || written == 0 {
				st.session.relay.releasePending(chunk.charge)
				st.session.finishStream(st, true)
				return
			}
		}
		st.session.relay.releasePending(chunk.charge)
	}
}

func (st *webStream) outgoingLoop() {
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
		want := uint64(BackendChunkSize)
		if st.sendCredit < want {
			want = st.sendCredit
		}
		st.sendCredit -= want
		st.session.mu.Unlock()

		reserved := framePendingCharge(int(want))
		if !st.session.relay.waitForPending(st.ctx, reserved) {
			st.restoreCredit(want)
			return
		}
		buffer := make([]byte, int(want))
		read, err := st.local.Read(buffer)
		if read < int(want) {
			st.session.relay.releasePending(int64(int(want) - read))
		}
		if read > 0 {
			st.restoreCredit(want - uint64(read))
			frame := Frame{Type: FrameData, StreamID: st.id, Payload: buffer[:read]}
			st.session.enqueueReserved(frame, framePendingCharge(read))
		} else {
			st.session.relay.releasePending(FrameHeaderSize + pendingFrameOverhead)
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
		if st.sendCredit > InitialStreamWindow {
			st.sendCredit = InitialStreamWindow
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
	return int64(payloadBytes + FrameHeaderSize + pendingFrameOverhead)
}

func framePendingCharge(payloadBytes int) int64 { return dataFrameCharge(payloadBytes) }

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func readFrameBatch(body []byte) ([]Frame, error) {
	if len(body) == 0 || len(body) > maxUpBatchBytes {
		return nil, errInvalidUp
	}
	decoder := NewDecoder(bytes.NewReader(body))
	frames := make([]Frame, 0, 16)
	for {
		frame, err := decoder.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errInvalidUp
		}
		frames = append(frames, frame)
		if len(frames) > maxDownFramesPerBatch {
			return nil, errInvalidUp
		}
	}
	return frames, nil
}
