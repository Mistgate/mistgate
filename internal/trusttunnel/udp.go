package trusttunnel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"unicode/utf8"
)

const (
	MaxUDPAppNameBytes = 255
	// MaxUDPClientPayload is the payload limit of a client frame with an empty
	// app name; a name of L bytes lowers it to MaxUDPClientPayload - 2*L (the
	// official server drops a frame whose Length exceeds 65471 - L).
	MaxUDPClientPayload = 65434
	// MaxUDPServerPayload is the largest payload the encoder produces for the
	// client (65536 - 20 - 8, the official server's UDP read buffer).
	MaxUDPServerPayload   = 65508
	maxUDPClientFrameSize = 65471
	minUDPClientFrameSize = 37
	minUDPServerFrameSize = 36
	// udpAppNameLengthOffset is where the one-byte name length sits in a client frame body: after both addresses and ports.
	udpAppNameLengthOffset = 36
	// The decoder of server frames is as lenient as the official client's: it
	// takes a payload up to 65535 - 8, a little more than the server ever sends.
	maxUDPServerDecodePayload = 65527
	maxUDPServerFrameSize     = minUDPServerFrameSize + maxUDPServerDecodePayload
	maxUDPFrameWireSize       = 4 + maxUDPServerFrameSize
)

// UDPDirection selects which side's frame layout a UDPDecoder accepts.
type UDPDirection uint8

const (
	UDPClientToServer UDPDirection = iota + 1
	UDPServerToClient
)

// UDPFrame is one TrustTunnel _udp2 datagram. AppName is present only in
// client-to-server frames.
type UDPFrame struct {
	AppName string
	Source  netip.AddrPort
	Target  netip.AddrPort
	Payload []byte
}

var (
	ErrInvalidUDPFrame     = errors.New("invalid _udp2 frame")
	ErrUDPFrameTooShort    = errors.New("_udp2 frame is too short")
	ErrUDPFrameTooLarge    = errors.New("_udp2 frame exceeds the protocol limit")
	ErrInvalidUDPDirection = errors.New("invalid _udp2 frame direction")
)

// EncodeUDPClientFrame encodes a client-to-server frame, including its
// one-byte-length-prefixed application name.
func EncodeUDPClientFrame(frame UDPFrame) ([]byte, error) {
	if !utf8.ValidString(frame.AppName) {
		return nil, ErrInvalidUDPFrame
	}
	appNameLength := len(frame.AppName)
	if appNameLength > MaxUDPAppNameBytes {
		return nil, ErrUDPFrameTooLarge
	}
	maxPayload := MaxUDPClientPayload - 2*appNameLength
	if len(frame.Payload) > maxPayload {
		return nil, ErrUDPFrameTooLarge
	}
	bodyLength := minUDPClientFrameSize + appNameLength + len(frame.Payload)
	if bodyLength > maxUDPClientFrameSize-appNameLength {
		return nil, ErrUDPFrameTooLarge
	}
	return encodeUDPFrame(frame, true, bodyLength)
}

// EncodeUDPServerFrame encodes a server-to-client frame without an app name.
func EncodeUDPServerFrame(frame UDPFrame) ([]byte, error) {
	if len(frame.Payload) > MaxUDPServerPayload {
		return nil, ErrUDPFrameTooLarge
	}
	bodyLength := minUDPServerFrameSize + len(frame.Payload)
	return encodeUDPFrame(frame, false, bodyLength)
}

func encodeUDPFrame(frame UDPFrame, withAppName bool, bodyLength int) ([]byte, error) {
	source, err := paddedIP(frame.Source.Addr())
	if err != nil || !frame.Source.IsValid() {
		return nil, ErrInvalidUDPFrame
	}
	target, err := paddedIP(frame.Target.Addr())
	if err != nil || !frame.Target.IsValid() {
		return nil, ErrInvalidUDPFrame
	}

	wire := make([]byte, 4, 4+bodyLength)
	binary.BigEndian.PutUint32(wire, uint32(bodyLength))
	wire = append(wire, source[:]...)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], frame.Source.Port())
	wire = append(wire, port[:]...)
	wire = append(wire, target[:]...)
	binary.BigEndian.PutUint16(port[:], frame.Target.Port())
	wire = append(wire, port[:]...)
	if withAppName {
		// The application name follows the fixed header (addresses and ports), not the length.
		wire = append(wire, byte(len(frame.AppName)))
		wire = append(wire, frame.AppName...)
	}
	wire = append(wire, frame.Payload...)
	return wire, nil
}

func paddedIP(address netip.Addr) ([16]byte, error) {
	var result [16]byte
	if !address.IsValid() {
		return result, ErrInvalidUDPFrame
	}
	if address.Is4In6() {
		address = address.Unmap()
	}
	if address.Is4() {
		ip := address.As4()
		copy(result[12:], ip[:])
	} else if address.Is6() && address.Zone() == "" {
		result = address.As16()
	} else {
		return result, ErrInvalidUDPFrame
	}
	// The wire has no type bit: twelve leading zero bytes mean IPv4, except the
	// exact ::1. An address the reader would misread (IPv4 0.0.0.1, or IPv6
	// "::" and "::a.b.c.d") cannot be sent.
	if decodedIP(result[:]) != address {
		return result, ErrInvalidUDPFrame
	}
	return result, nil
}

// decodedIP reads the 16-byte address field the way the official client does:
// twelve leading zero bytes mean IPv4, except for the exact ::1 (the official
// server has no such exception and reads ::1 as 0.0.0.1).
func decodedIP(encoded []byte) netip.Addr {
	firstTwelveZero := true
	for _, b := range encoded[:12] {
		if b != 0 {
			firstTwelveZero = false
			break
		}
	}
	isIPv6Loopback := firstTwelveZero &&
		encoded[12] == 0 && encoded[13] == 0 && encoded[14] == 0 && encoded[15] == 1
	if firstTwelveZero && !isIPv6Loopback {
		var address [4]byte
		copy(address[:], encoded[12:])
		return netip.AddrFrom4(address)
	}
	var address [16]byte
	copy(address[:], encoded)
	return netip.AddrFrom16(address)
}

// UDPDecoder incrementally reads length-prefixed _udp2 frames. Feed accepts
// arbitrary chunk boundaries, returns every complete valid frame, and reports
// the first dropped-frame error while continuing at the next frame boundary.
// A frame the official reader would drop is dropped here too, with the same
// number of bytes skipped, so both stay in step with the stream. Its retained
// buffer is bounded by the largest frame admitted by the protocol.
//
// Each returned Payload is a copy that the caller owns; the decoder reuses its
// internal buffer. A UDPDecoder is not safe for concurrent use.
type UDPDecoder struct {
	direction        UDPDirection
	buffer           []byte
	discardRemaining uint64
}

// NewUDPDecoder creates a streaming decoder for one frame direction.
func NewUDPDecoder(direction UDPDirection) *UDPDecoder {
	return &UDPDecoder{direction: direction}
}

// Feed appends data to the stream and returns all complete frames. When a
// malformed frame is dropped, Feed returns its error together with any later
// frames decoded from the same input.
func (decoder *UDPDecoder) Feed(input []byte) ([]UDPFrame, error) {
	if decoder == nil || (decoder.direction != UDPClientToServer && decoder.direction != UDPServerToClient) {
		return nil, ErrInvalidUDPDirection
	}

	var frames []UDPFrame
	var firstErr error
	recordError := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for {
		if decoder.discardRemaining > 0 {
			if len(input) == 0 {
				break
			}
			count := uint64(len(input))
			if count > decoder.discardRemaining {
				count = decoder.discardRemaining
			}
			input = input[int(count):]
			decoder.discardRemaining -= count
			continue
		}

		if len(decoder.buffer) < 4 {
			if len(input) == 0 {
				break
			}
			count := 4 - len(decoder.buffer)
			if count > len(input) {
				count = len(input)
			}
			decoder.buffer = append(decoder.buffer, input[:count]...)
			input = input[count:]
			if len(decoder.buffer) < 4 {
				break
			}
		}

		length := binary.BigEndian.Uint32(decoder.buffer[:4])
		minimum, maximum := udpFrameBounds(decoder.direction)
		if length < minimum {
			recordError(ErrUDPFrameTooShort)
			decoder.buffer = decoder.buffer[:0]
			decoder.discardRemaining = uint64(length)
			continue
		}
		if length > maximum {
			recordError(ErrUDPFrameTooLarge)
			decoder.buffer = decoder.buffer[:0]
			decoder.discardRemaining = uint64(length)
			continue
		}

		if decoder.direction == UDPClientToServer {
			if need := 4 + minUDPClientFrameSize - len(decoder.buffer); need > 0 {
				if need > len(input) {
					need = len(input)
				}
				decoder.buffer = append(decoder.buffer, input[:need]...)
				input = input[need:]
				if len(decoder.buffer) < 4+minUDPClientFrameSize {
					break
				}
			}
			appNameLength := uint32(decoder.buffer[4+udpAppNameLengthOffset])
			if length < minUDPClientFrameSize+appNameLength {
				recordError(ErrUDPFrameTooShort)
				bodyConsumed := uint64(len(decoder.buffer) - 4)
				decoder.buffer = decoder.buffer[:0]
				decoder.discardRemaining = uint64(length) - bodyConsumed
				continue
			}
			if length > maxUDPClientFrameSize-appNameLength {
				recordError(ErrUDPFrameTooLarge)
				bodyConsumed := uint64(len(decoder.buffer) - 4)
				decoder.buffer = decoder.buffer[:0]
				decoder.discardRemaining = uint64(length) - bodyConsumed
				continue
			}
		}

		totalLength := 4 + int(length)
		if len(decoder.buffer) < totalLength {
			if len(input) == 0 {
				break
			}
			count := totalLength - len(decoder.buffer)
			if count > len(input) {
				count = len(input)
			}
			if len(decoder.buffer)+count > maxUDPFrameWireSize {
				recordError(ErrUDPFrameTooLarge)
				bodyConsumed := len(decoder.buffer) - 4
				decoder.buffer = decoder.buffer[:0]
				decoder.discardRemaining = uint64(length) - uint64(bodyConsumed)
				continue
			}
			decoder.buffer = append(decoder.buffer, input[:count]...)
			input = input[count:]
			if len(decoder.buffer) < totalLength {
				break
			}
		}

		frame, err := decodeUDPFrame(decoder.direction, decoder.buffer[4:totalLength])
		decoder.buffer = decoder.buffer[:0]
		if err != nil {
			recordError(err)
		} else {
			frames = append(frames, frame)
		}
	}

	return frames, firstErr
}

// Reset forgets any partial frame and pending discard state.
func (decoder *UDPDecoder) Reset() {
	if decoder == nil {
		return
	}
	decoder.buffer = decoder.buffer[:0]
	decoder.discardRemaining = 0
}

func udpFrameBounds(direction UDPDirection) (uint32, uint32) {
	if direction == UDPServerToClient {
		return minUDPServerFrameSize, maxUDPServerFrameSize
	}
	return minUDPClientFrameSize, maxUDPClientFrameSize
}

func decodeUDPFrame(direction UDPDirection, body []byte) (UDPFrame, error) {
	frame := UDPFrame{}
	if direction != UDPClientToServer && direction != UDPServerToClient {
		return UDPFrame{}, ErrInvalidUDPDirection
	}
	if len(body) < minUDPServerFrameSize {
		return UDPFrame{}, ErrUDPFrameTooShort
	}
	frame.Source = netip.AddrPortFrom(decodedIP(body[0:16]), binary.BigEndian.Uint16(body[16:18]))
	frame.Target = netip.AddrPortFrom(decodedIP(body[18:34]), binary.BigEndian.Uint16(body[34:36]))
	offset := 36
	if direction == UDPClientToServer {
		if len(body) < minUDPClientFrameSize {
			return UDPFrame{}, ErrUDPFrameTooShort
		}
		appNameLength := int(body[udpAppNameLengthOffset])
		offset++
		if len(body) < minUDPClientFrameSize+appNameLength {
			return UDPFrame{}, ErrUDPFrameTooShort
		}
		if !utf8.Valid(body[offset : offset+appNameLength]) {
			return UDPFrame{}, fmt.Errorf("%w: invalid application name", ErrInvalidUDPFrame)
		}
		frame.AppName = string(body[offset : offset+appNameLength])
		offset += appNameLength
	}
	frame.Payload = append([]byte(nil), body[offset:]...)
	return frame, nil
}
