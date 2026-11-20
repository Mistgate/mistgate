package webproxy

import "encoding/binary"

const (
	frameHeaderSize = 8
	maxFramePayload = 1 << 20
	maxStreamID     = (1 << 24) - 1
)

type frameType uint8

const (
	frameOpen    frameType = 0x01
	frameData    frameType = 0x02
	frameClose   frameType = 0x03
	frameWindow  frameType = 0x04
	framePing    frameType = 0x05
	framePong    frameType = 0x06
	frameHello   frameType = 0x10
	frameWelcome frameType = 0x11
	frameBye     frameType = 0x1f
)

type frame struct {
	Type     frameType
	StreamID uint32
	Payload  []byte
}

type frameError string

func (err frameError) Error() string { return string(err) }

const (
	errFrameHeaderTooShort frameError = "truncated frame header"
	errFrameTooShort       frameError = "truncated frame"
	errUnknownFrameType    frameError = "unknown frame type"
	errInvalidStreamID     frameError = "invalid stream id"
	errPayloadTooLarge     frameError = "frame payload too large"
	errInvalidFramePayload frameError = "invalid frame payload"
)

// validate checks the frame type, stream id, payload bound, and defined payload layouts.
func (f frame) validate() error {
	if !knownFrameType(f.Type) {
		return errUnknownFrameType
	}
	if f.StreamID > maxStreamID {
		return errInvalidStreamID
	}
	if len(f.Payload) > maxFramePayload {
		return errPayloadTooLarge
	}
	if !frameUsesStream(f.Type) && f.StreamID != 0 || frameUsesStream(f.Type) && f.StreamID == 0 {
		return errInvalidStreamID
	}

	switch f.Type {
	case frameOpen, frameClose, frameWelcome:
		if len(f.Payload) != 0 {
			return errInvalidFramePayload
		}
	case frameData:
		if len(f.Payload) == 0 {
			return errInvalidFramePayload
		}
	case frameWindow:
		if _, err := parseWindowPayload(f.Payload); err != nil {
			return err
		}
	case frameHello:
		if err := parseHelloPayload(f.Payload); err != nil {
			return err
		}
	}
	return nil
}

// appendFrame appends one complete frame to dst.
func appendFrame(dst []byte, f frame) ([]byte, error) {
	if err := f.validate(); err != nil {
		return dst, err
	}

	length := uint32(len(f.Payload))
	dst = append(dst,
		byte(f.Type),
		byte(f.StreamID>>16),
		byte(f.StreamID>>8),
		byte(f.StreamID),
		byte(length>>24),
		byte(length>>16),
		byte(length>>8),
		byte(length),
	)
	dst = append(dst, f.Payload...)
	return dst, nil
}

// parseFrame decodes one frame from data and returns the unconsumed suffix.
// The returned payload aliases data.
func parseFrame(data []byte) (frame, []byte, error) {
	t, streamID, payloadLength, err := parseFrameHeader(data)
	if err != nil {
		return frame{}, nil, err
	}
	frameLength := frameHeaderSize + payloadLength
	if len(data) < frameLength {
		return frame{}, nil, errFrameTooShort
	}
	f := frame{Type: t, StreamID: streamID, Payload: data[frameHeaderSize:frameLength]}
	if err := f.validate(); err != nil {
		return frame{}, nil, err
	}
	return f, data[frameLength:], nil
}

func parseFrameHeader(header []byte) (frameType, uint32, int, error) {
	if len(header) < frameHeaderSize {
		return 0, 0, 0, errFrameHeaderTooShort
	}
	t := frameType(header[0])
	if !knownFrameType(t) {
		return 0, 0, 0, errUnknownFrameType
	}
	streamID := uint32(header[1])<<16 | uint32(header[2])<<8 | uint32(header[3])
	if !frameUsesStream(t) && streamID != 0 || frameUsesStream(t) && streamID == 0 {
		return 0, 0, 0, errInvalidStreamID
	}
	length := binary.BigEndian.Uint32(header[4:8])
	if length > maxFramePayload {
		return 0, 0, 0, errPayloadTooLarge
	}
	return t, streamID, int(length), nil
}

func knownFrameType(t frameType) bool {
	switch t {
	case frameOpen, frameData, frameClose, frameWindow, framePing, framePong, frameHello, frameWelcome, frameBye:
		return true
	default:
		return false
	}
}

func frameUsesStream(t frameType) bool {
	switch t {
	case frameOpen, frameData, frameClose, frameWindow:
		return true
	default:
		return false
	}
}

// parseWindowPayload decodes a nonzero big-endian WINDOW credit delta.
func parseWindowPayload(payload []byte) (uint32, error) {
	if len(payload) != 4 {
		return 0, errInvalidFramePayload
	}
	credit := binary.BigEndian.Uint32(payload)
	if credit == 0 {
		return 0, errInvalidFramePayload
	}
	return credit, nil
}

// parseHelloPayload accepts the version-one HELLO byte.
func parseHelloPayload(payload []byte) error {
	if len(payload) != 1 || payload[0] != 0x01 {
		return errInvalidFramePayload
	}
	return nil
}

func appendHelloPayload(dst []byte) []byte { return append(dst, 0x01) }
