package webproxy

import (
	"encoding/binary"
	"io"
)

const (
	FrameHeaderSize = 8
	MaxFramePayload = 1 << 20
	MaxStreamID     = (1 << 24) - 1

	frameReadChunkSize = 16 << 10
)

type FrameType uint8

const (
	FrameOpen    FrameType = 0x01
	FrameData    FrameType = 0x02
	FrameClose   FrameType = 0x03
	FrameWindow  FrameType = 0x04
	FramePing    FrameType = 0x05
	FramePong    FrameType = 0x06
	FrameHello   FrameType = 0x10
	FrameWelcome FrameType = 0x11
	FrameBye     FrameType = 0x1f
)

type Frame struct {
	Type     FrameType
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
	errNilFrameReader      frameError = "nil frame reader"
)

// Validate checks the frame type, stream id, payload bound, and defined payload layouts.
func (f Frame) Validate() error {
	if !knownFrameType(f.Type) {
		return errUnknownFrameType
	}
	if f.StreamID > MaxStreamID {
		return errInvalidStreamID
	}
	if len(f.Payload) > MaxFramePayload {
		return errPayloadTooLarge
	}
	if !frameUsesStream(f.Type) && f.StreamID != 0 || frameUsesStream(f.Type) && f.StreamID == 0 {
		return errInvalidStreamID
	}

	switch f.Type {
	case FrameOpen, FrameClose, FrameWelcome:
		if len(f.Payload) != 0 {
			return errInvalidFramePayload
		}
	case FrameData:
		if len(f.Payload) == 0 {
			return errInvalidFramePayload
		}
	case FrameWindow:
		if _, err := ParseWindowPayload(f.Payload); err != nil {
			return err
		}
	case FrameHello:
		if err := ParseHelloPayload(f.Payload); err != nil {
			return err
		}
	}
	return nil
}

// AppendFrame appends one complete frame to dst.
func AppendFrame(dst []byte, f Frame) ([]byte, error) {
	if err := f.Validate(); err != nil {
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

// ParseFrame decodes one frame from data and returns the unconsumed suffix.
// The returned payload aliases data.
func ParseFrame(data []byte) (Frame, []byte, error) {
	t, streamID, payloadLength, err := parseFrameHeader(data)
	if err != nil {
		return Frame{}, nil, err
	}
	frameLength := FrameHeaderSize + payloadLength
	if len(data) < frameLength {
		return Frame{}, nil, errFrameTooShort
	}
	f := Frame{Type: t, StreamID: streamID, Payload: data[FrameHeaderSize:frameLength]}
	if err := f.Validate(); err != nil {
		return Frame{}, nil, err
	}
	return f, data[frameLength:], nil
}

// Decoder reads complete frames from a stream without allocating the advertised
// payload length before bytes arrive.
type Decoder struct {
	r io.Reader
}

func NewDecoder(r io.Reader) *Decoder { return &Decoder{r: r} }

// ReadFrame reads one frame. io.EOF is returned only when no header byte is available.
func (d *Decoder) ReadFrame() (Frame, error) {
	if d == nil || d.r == nil {
		return Frame{}, errNilFrameReader
	}

	var header [FrameHeaderSize]byte
	n, err := io.ReadFull(d.r, header[:])
	if err != nil {
		if n == 0 && err == io.EOF {
			return Frame{}, io.EOF
		}
		return Frame{}, errFrameHeaderTooShort
	}
	t, streamID, payloadLength, err := parseFrameHeader(header[:])
	if err != nil {
		return Frame{}, err
	}

	payload := make([]byte, 0)
	var chunk [frameReadChunkSize]byte
	remaining := payloadLength
	for remaining > 0 {
		want := remaining
		if want > len(chunk) {
			want = len(chunk)
		}
		read, readErr := io.ReadFull(d.r, chunk[:want])
		if read > 0 {
			payload = append(payload, chunk[:read]...)
			remaining -= read
		}
		if readErr != nil {
			return Frame{}, errFrameTooShort
		}
	}

	f := Frame{Type: t, StreamID: streamID, Payload: payload}
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

func parseFrameHeader(header []byte) (FrameType, uint32, int, error) {
	if len(header) < FrameHeaderSize {
		return 0, 0, 0, errFrameHeaderTooShort
	}
	t := FrameType(header[0])
	if !knownFrameType(t) {
		return 0, 0, 0, errUnknownFrameType
	}
	streamID := uint32(header[1])<<16 | uint32(header[2])<<8 | uint32(header[3])
	if !frameUsesStream(t) && streamID != 0 || frameUsesStream(t) && streamID == 0 {
		return 0, 0, 0, errInvalidStreamID
	}
	length := binary.BigEndian.Uint32(header[4:8])
	if length > MaxFramePayload {
		return 0, 0, 0, errPayloadTooLarge
	}
	return t, streamID, int(length), nil
}

func knownFrameType(t FrameType) bool {
	switch t {
	case FrameOpen, FrameData, FrameClose, FrameWindow, FramePing, FramePong, FrameHello, FrameWelcome, FrameBye:
		return true
	default:
		return false
	}
}

func frameUsesStream(t FrameType) bool {
	switch t {
	case FrameOpen, FrameData, FrameClose, FrameWindow:
		return true
	default:
		return false
	}
}

// ParseWindowPayload decodes a nonzero big-endian WINDOW credit delta.
func ParseWindowPayload(payload []byte) (uint32, error) {
	if len(payload) != 4 {
		return 0, errInvalidFramePayload
	}
	credit := binary.BigEndian.Uint32(payload)
	if credit == 0 {
		return 0, errInvalidFramePayload
	}
	return credit, nil
}

// AppendWindowPayload appends a nonzero big-endian WINDOW credit delta.
func AppendWindowPayload(dst []byte, credit uint32) ([]byte, error) {
	if credit == 0 {
		return dst, errInvalidFramePayload
	}
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], credit)
	return append(dst, payload[:]...), nil
}

// ParseHelloPayload accepts the version-one HELLO byte.
func ParseHelloPayload(payload []byte) error {
	if len(payload) != 1 || payload[0] != 0x01 {
		return errInvalidFramePayload
	}
	return nil
}

func AppendHelloPayload(dst []byte) []byte { return append(dst, 0x01) }

func ParseWelcomePayload(payload []byte) error {
	if len(payload) != 0 {
		return errInvalidFramePayload
	}
	return nil
}

func AppendWelcomePayload(dst []byte) []byte { return dst }

func ParseClosePayload(payload []byte) error {
	if len(payload) != 0 {
		return errInvalidFramePayload
	}
	return nil
}

func AppendClosePayload(dst []byte) []byte { return dst }
