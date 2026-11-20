package webproxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestFrameRoundTrips(t *testing.T) {
	window, err := AppendWindowPayload(nil, 0x01020304)
	if err != nil {
		t.Fatal(err)
	}
	frames := []Frame{
		{Type: FrameOpen, StreamID: 1},
		{Type: FrameData, StreamID: 0x010203, Payload: []byte{0x00, 0xff}},
		{Type: FrameClose, StreamID: 1, Payload: AppendClosePayload(nil)},
		{Type: FrameWindow, StreamID: 2, Payload: window},
		{Type: FramePing, Payload: []byte("opaque ping")},
		{Type: FramePong, Payload: []byte("opaque pong")},
		{Type: FrameHello, Payload: AppendHelloPayload(nil)},
		{Type: FrameWelcome, Payload: AppendWelcomePayload(nil)},
		{Type: FrameBye, Payload: []byte("opaque reason")},
	}

	var wire []byte
	for _, want := range frames {
		encoded, err := AppendFrame(nil, want)
		if err != nil {
			t.Fatalf("AppendFrame(%d): %v", want.Type, err)
		}
		got, rest, err := ParseFrame(encoded)
		if err != nil {
			t.Fatalf("ParseFrame(%d): %v", want.Type, err)
		}
		if len(rest) != 0 || got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("round trip mismatch for type %d", want.Type)
		}
		wire = append(wire, encoded...)
	}

	for i, want := range frames {
		got, rest, err := ParseFrame(wire)
		if err != nil {
			t.Fatalf("ParseFrame batch item %d: %v", i, err)
		}
		if got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("batch round trip mismatch for item %d", i)
		}
		wire = rest
	}
	if len(wire) != 0 {
		t.Fatal("decoder left trailing bytes")
	}

	reader := NewDecoder(bytes.NewReader(appendFrames(t, frames...)))
	for i, want := range frames {
		got, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame item %d: %v", i, err)
		}
		if got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("stream round trip mismatch for item %d", i)
		}
	}
	if _, err := reader.ReadFrame(); err != io.EOF {
		t.Fatalf("ReadFrame at end = %v, want EOF", err)
	}
}

func TestFrameBoundarySizes(t *testing.T) {
	payload := bytes.Repeat([]byte{0xa5}, MaxFramePayload)
	want := Frame{Type: FrameData, StreamID: MaxStreamID, Payload: payload}
	wire, err := AppendFrame(nil, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != FrameHeaderSize+MaxFramePayload {
		t.Fatalf("encoded size = %d", len(wire))
	}
	got, rest, err := ParseFrame(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 || got.StreamID != MaxStreamID || len(got.Payload) != MaxFramePayload {
		t.Fatal("maximum-size frame did not round trip")
	}

	tooLarge := Frame{Type: FrameData, StreamID: 1, Payload: make([]byte, MaxFramePayload+1)}
	if _, err := AppendFrame(nil, tooLarge); err == nil {
		t.Fatal("AppendFrame accepted an oversized payload")
	}
	oversizeHeader := testFrameHeader(FrameData, 1, MaxFramePayload+1)
	if _, _, err := ParseFrame(oversizeHeader); err == nil {
		t.Fatal("ParseFrame accepted an oversized advertised payload")
	}
	if _, err := NewDecoder(bytes.NewReader(oversizeHeader)).ReadFrame(); err == nil {
		t.Fatal("ReadFrame accepted an oversized advertised payload")
	}
}

func TestFramePayloadLayouts(t *testing.T) {
	payload, err := AppendWindowPayload(nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	credit, err := ParseWindowPayload(payload)
	if err != nil || credit != 7 {
		t.Fatalf("WINDOW round trip = %d, %v", credit, err)
	}
	if _, err := AppendWindowPayload(nil, 0); err == nil {
		t.Fatal("WINDOW accepted zero credit")
	}
	if _, err := ParseWindowPayload([]byte{0, 0, 0, 0}); err == nil {
		t.Fatal("WINDOW accepted zero credit")
	}
	if err := ParseHelloPayload(AppendHelloPayload(nil)); err != nil {
		t.Fatal(err)
	}
	if err := ParseHelloPayload([]byte{2}); err == nil {
		t.Fatal("HELLO accepted an unknown version")
	}
	if err := ParseWelcomePayload(AppendWelcomePayload(nil)); err != nil {
		t.Fatal(err)
	}
	if err := ParseClosePayload(AppendClosePayload(nil)); err != nil {
		t.Fatal(err)
	}
	if err := ParseClosePayload([]byte{1}); err == nil {
		t.Fatal("CLOSE accepted a payload")
	}
}

func TestReadFrameBoundsReadsForUnknownLengthInput(t *testing.T) {
	overlarge := testFrameHeader(FrameData, 1, MaxFramePayload+1)
	reader := &observedReader{data: append(overlarge, 1, 2, 3)}
	if _, err := NewDecoder(reader).ReadFrame(); err == nil {
		t.Fatal("ReadFrame accepted an oversized payload")
	}
	if reader.bytesRead != FrameHeaderSize {
		t.Fatalf("ReadFrame consumed %d bytes after an oversized header", reader.bytesRead)
	}

	truncated := testFrameHeader(FrameData, 1, MaxFramePayload)
	reader = &observedReader{data: append(truncated, 0x7f)}
	if _, err := NewDecoder(reader).ReadFrame(); err == nil {
		t.Fatal("ReadFrame accepted a truncated payload")
	}
	if reader.maxRequest > frameReadChunkSize {
		t.Fatalf("decoder requested %d bytes at once", reader.maxRequest)
	}
}

func TestReadFrameRejectsTruncatedHeader(t *testing.T) {
	if _, err := NewDecoder(bytes.NewReader([]byte{FrameHeaderSize})).ReadFrame(); err == nil {
		t.Fatal("ReadFrame accepted a truncated header")
	}
}

func FuzzDecodeFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add(testFrameHeader(FrameHello, 0, 1))
	f.Add(append(testFrameHeader(FrameHello, 0, 1), 1))
	f.Add(testFrameHeader(FrameData, 1, ^uint32(0)))
	f.Add(testFrameHeader(FrameType(0x7f), 0, 0))

	f.Fuzz(func(t *testing.T, data []byte) {
		frame, _, err := ParseFrame(data)
		if err == nil && frame.Validate() != nil {
			t.Fatal("ParseFrame returned an invalid frame")
		}

		frame, err = NewDecoder(bytes.NewReader(data)).ReadFrame()
		if err == nil {
			if len(frame.Payload) > MaxFramePayload || frame.Validate() != nil {
				t.Fatal("ReadFrame returned an invalid frame")
			}
		}
	})
}

func appendFrames(t *testing.T, frames ...Frame) []byte {
	t.Helper()
	var wire []byte
	for _, frame := range frames {
		var err error
		wire, err = AppendFrame(wire, frame)
		if err != nil {
			t.Fatal(err)
		}
	}
	return wire
}

func testFrameHeader(t FrameType, streamID, payloadLength uint32) []byte {
	header := make([]byte, FrameHeaderSize)
	header[0] = byte(t)
	header[1] = byte(streamID >> 16)
	header[2] = byte(streamID >> 8)
	header[3] = byte(streamID)
	binary.BigEndian.PutUint32(header[4:], payloadLength)
	return header
}

type observedReader struct {
	data       []byte
	bytesRead  int
	maxRequest int
}

func (r *observedReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	r.bytesRead += n
	return n, nil
}
