package awgprep

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestEmbeddedAmneziaPPAKeyFingerprint(t *testing.T) {
	got, err := openPGPV4Fingerprint(amneziaPPAKey)
	if err != nil {
		t.Fatal(err)
	}
	const want = "75C9DD72C799870E310542E24166F2C257290828"
	if got != want {
		t.Errorf("Amnezia PPA key fingerprint = %s, want %s", got, want)
	}
}

func openPGPV4Fingerprint(armored []byte) (string, error) {
	const (
		begin = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
		end   = "-----END PGP PUBLIC KEY BLOCK-----"
	)
	var encoded strings.Builder
	inHeaders, inBody := false, false
	for _, line := range strings.Split(string(armored), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == begin:
			inHeaders = true
		case inHeaders && line == "":
			inHeaders, inBody = false, true
		case inBody && line == end:
			inBody = false
		case inBody && strings.HasPrefix(line, "="):
			// The optional armor checksum is not part of the packet data.
		case inBody:
			encoded.WriteString(strings.TrimSpace(line))
		}
		if line == end {
			break
		}
	}
	if encoded.Len() == 0 {
		return "", fmt.Errorf("no armored OpenPGP public key")
	}
	packets, err := base64.StdEncoding.DecodeString(encoded.String())
	if err != nil {
		return "", fmt.Errorf("decode OpenPGP armor: %w", err)
	}
	for offset := 0; offset < len(packets); {
		tag, body, next, err := readOpenPGPPacket(packets, offset)
		if err != nil {
			return "", err
		}
		if tag == 6 {
			if len(body) == 0 || body[0] != 4 {
				return "", fmt.Errorf("public key packet is not OpenPGP v4")
			}
			if len(body) > 0xffff {
				return "", fmt.Errorf("OpenPGP v4 public key packet is too large")
			}
			preimage := make([]byte, 3+len(body))
			preimage[0] = 0x99
			preimage[1] = byte(len(body) >> 8)
			preimage[2] = byte(len(body))
			copy(preimage[3:], body)
			sum := sha1.Sum(preimage)
			return strings.ToUpper(hex.EncodeToString(sum[:])), nil
		}
		offset = next
	}
	return "", fmt.Errorf("no OpenPGP public-key packet")
}

func readOpenPGPPacket(data []byte, offset int) (tag int, body []byte, next int, err error) {
	if offset >= len(data) || data[offset]&0x80 == 0 {
		return 0, nil, 0, fmt.Errorf("invalid OpenPGP packet header")
	}
	header := data[offset]
	offset++
	if header&0x40 == 0 {
		tag = int(header>>2) & 0x0f
		switch header & 0x03 {
		case 0:
			if offset >= len(data) {
				return 0, nil, 0, fmt.Errorf("truncated OpenPGP packet length")
			}
			length := int(data[offset])
			offset++
			return packetBody(data, tag, offset, length)
		case 1:
			if len(data)-offset < 2 {
				return 0, nil, 0, fmt.Errorf("truncated OpenPGP packet length")
			}
			length := int(data[offset])<<8 | int(data[offset+1])
			offset += 2
			return packetBody(data, tag, offset, length)
		case 2:
			if len(data)-offset < 4 {
				return 0, nil, 0, fmt.Errorf("truncated OpenPGP packet length")
			}
			length := int(data[offset])<<24 | int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3])
			offset += 4
			return packetBody(data, tag, offset, length)
		case 3:
			return tag, data[offset:], len(data), nil
		}
	}
	tag = int(header & 0x3f)
	var assembled []byte
	for {
		length, partial, bodyOffset, err := readOpenPGPLength(data, offset)
		if err != nil {
			return 0, nil, 0, err
		}
		if length > len(data)-bodyOffset {
			return 0, nil, 0, fmt.Errorf("truncated OpenPGP packet body")
		}
		assembled = append(assembled, data[bodyOffset:bodyOffset+length]...)
		offset = bodyOffset + length
		if !partial {
			return tag, assembled, offset, nil
		}
	}
}

func packetBody(data []byte, tag, offset, length int) (int, []byte, int, error) {
	if length > len(data)-offset {
		return 0, nil, 0, fmt.Errorf("truncated OpenPGP packet body")
	}
	return tag, data[offset : offset+length], offset + length, nil
}

func readOpenPGPLength(data []byte, offset int) (length int, partial bool, bodyOffset int, err error) {
	if offset >= len(data) {
		return 0, false, 0, fmt.Errorf("truncated OpenPGP packet length")
	}
	first := data[offset]
	offset++
	switch {
	case first < 192:
		return int(first), false, offset, nil
	case first < 224:
		if offset >= len(data) {
			return 0, false, 0, fmt.Errorf("truncated OpenPGP packet length")
		}
		return (int(first)-192)<<8 + int(data[offset]) + 192, false, offset + 1, nil
	case first < 255:
		return 1 << (first & 0x1f), true, offset, nil
	default:
		if len(data)-offset < 4 {
			return 0, false, 0, fmt.Errorf("truncated OpenPGP packet length")
		}
		return int(data[offset])<<24 | int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3]), false, offset + 4, nil
	}
}
