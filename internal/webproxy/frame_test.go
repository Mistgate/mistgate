package webproxy

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrameRoundTrips(t *testing.T) {
	var window [4]byte
	binary.BigEndian.PutUint32(window[:], 0x01020304)
	frames := []frame{
		{Type: frameOpen, StreamID: 1},
		{Type: frameData, StreamID: 0x010203, Payload: []byte{0x00, 0xff}},
		{Type: frameClose, StreamID: 1},
		{Type: frameWindow, StreamID: 2, Payload: window[:]},
		{Type: framePing, Payload: []byte("opaque ping")},
		{Type: framePong, Payload: []byte("opaque pong")},
		{Type: frameHello, Payload: appendHelloPayload(nil)},
		{Type: frameWelcome},
		{Type: frameBye, Payload: []byte("opaque reason")},
	}

	var wire []byte
	for _, want := range frames {
		encoded, err := appendFrame(nil, want)
		if err != nil {
			t.Fatalf("appendFrame(%d): %v", want.Type, err)
		}
		got, rest, err := parseFrame(encoded)
		if err != nil {
			t.Fatalf("parseFrame(%d): %v", want.Type, err)
		}
		if len(rest) != 0 || got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("round trip mismatch for type %d", want.Type)
		}
		wire = append(wire, encoded...)
	}

	for i, want := range frames {
		got, rest, err := parseFrame(wire)
		if err != nil {
			t.Fatalf("parseFrame batch item %d: %v", i, err)
		}
		if got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("batch round trip mismatch for item %d", i)
		}
		wire = rest
	}
	if len(wire) != 0 {
		t.Fatal("parser left trailing bytes")
	}
}

func TestFrameBoundarySizes(t *testing.T) {
	payload := bytes.Repeat([]byte{0xa5}, maxFramePayload)
	want := frame{Type: frameData, StreamID: maxStreamID, Payload: payload}
	wire, err := appendFrame(nil, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != frameHeaderSize+maxFramePayload {
		t.Fatalf("encoded size = %d", len(wire))
	}
	got, rest, err := parseFrame(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 || got.StreamID != maxStreamID || len(got.Payload) != maxFramePayload {
		t.Fatal("maximum-size frame did not round trip")
	}

	tooLarge := frame{Type: frameData, StreamID: 1, Payload: make([]byte, maxFramePayload+1)}
	if _, err := appendFrame(nil, tooLarge); err == nil {
		t.Fatal("appendFrame accepted an oversized payload")
	}
	oversizeHeader := testFrameHeader(frameData, 1, maxFramePayload+1)
	if _, _, err := parseFrame(oversizeHeader); err == nil {
		t.Fatal("parseFrame accepted an oversized advertised payload")
	}
}

func TestFramePayloadLayouts(t *testing.T) {
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], 7)
	credit, err := parseWindowPayload(payload[:])
	if err != nil || credit != 7 {
		t.Fatalf("WINDOW round trip = %d, %v", credit, err)
	}
	if _, err := parseWindowPayload([]byte{0, 0, 0, 0}); err == nil {
		t.Fatal("WINDOW accepted zero credit")
	}
	if err := parseHelloPayload(appendHelloPayload(nil)); err != nil {
		t.Fatal(err)
	}
	if err := parseHelloPayload([]byte{2}); err == nil {
		t.Fatal("HELLO accepted an unknown version")
	}
}

func FuzzParseFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add(testFrameHeader(frameHello, 0, 1))
	f.Add(append(testFrameHeader(frameHello, 0, 1), 1))
	f.Add(testFrameHeader(frameData, 1, ^uint32(0)))
	f.Add(testFrameHeader(frameType(0x7f), 0, 0))

	f.Fuzz(func(t *testing.T, data []byte) {
		frame, _, err := parseFrame(data)
		if err == nil && frame.validate() != nil {
			t.Fatal("parseFrame returned an invalid frame")
		}
	})
}

func testFrameHeader(t frameType, streamID, payloadLength uint32) []byte {
	header := make([]byte, frameHeaderSize)
	header[0] = byte(t)
	header[1] = byte(streamID >> 16)
	header[2] = byte(streamID >> 8)
	header[3] = byte(streamID)
	binary.BigEndian.PutUint32(header[4:], payloadLength)
	return header
}
