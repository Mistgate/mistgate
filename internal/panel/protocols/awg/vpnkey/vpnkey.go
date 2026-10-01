// Package vpnkey encodes and decodes the vpn:// connection key of AmneziaVPN.
//
// The client (amnezia-client 5.0.x, exportController.cpp / importController.cpp) writes
// "vpn://" + base64url-without-padding( qCompress(json, 8) ). Qt's qCompress is a 4-byte big-endian length of
// the UNCOMPRESSED data followed by a zlib stream (RFC 1950, with the header). The importer tries qUncompress
// first and, when that yields nothing, takes the bytes as plain JSON; Decode does the same.
package vpnkey

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Prefix is the scheme of the key.
const Prefix = "vpn://"

// compressLevel is the level the client uses (exportController.cpp).
const compressLevel = 8

// maxDecoded bounds what Decode inflates: a key is ~1.5 KB, the largest real one a few KB.
const maxDecoded = 1 << 20

// Encode marshals v as indented JSON (the client writes QJsonDocument::toJson(), indented; any valid JSON is
// accepted) and returns the key.
func Encode(v any) (string, error) {
	js, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return "", err
	}
	return EncodeJSON(js)
}

// EncodeJSON turns an already marshalled JSON document into a key.
func EncodeJSON(js []byte) (string, error) {
	blob, err := qCompress(js, compressLevel)
	if err != nil {
		return "", err
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(blob), nil
}

// Decode mirrors ImportController::extractConfigFromData: strip the prefix, base64url (padding tolerated),
// qUncompress; bytes that are not a qCompress blob are returned as they are (plain JSON).
func Decode(key string) ([]byte, error) {
	s := strings.TrimRight(strings.TrimPrefix(strings.TrimSpace(key), Prefix), "=")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if out, err := qUncompress(raw); err == nil {
		return out, nil
	}
	return raw, nil
}

var errBlob = errors.New("vpnkey: not a qCompress blob")

func qCompress(data []byte, level int) ([]byte, error) {
	var b bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	b.Write(hdr[:])
	zw, err := zlib.NewWriterLevel(&b, level)
	if err != nil {
		return nil, err
	}
	if _, err = zw.Write(data); err != nil {
		return nil, err
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func qUncompress(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, errBlob
	}
	want := binary.BigEndian.Uint32(data[:4])
	if want > maxDecoded {
		return nil, errBlob
	}
	zr, err := zlib.NewReader(bytes.NewReader(data[4:]))
	if err != nil {
		return nil, errBlob
	}
	out, err := io.ReadAll(io.LimitReader(zr, maxDecoded+1))
	if err != nil || uint32(len(out)) != want {
		return nil, errBlob
	}
	return out, nil
}
