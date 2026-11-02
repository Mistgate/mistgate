//go:build !js

package health

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	mp "github.com/mistgate/mistgate/internal/mtproto"
	mtclient "github.com/mistgate/mistgate/internal/mtproto/client"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	mtprotoDC          = int16(2)
	reqPQMulti         = uint32(0xbe7e8ef1)
	resPQ              = uint32(0x05162463)
	vectorConstructor  = uint32(0x1cb5c415)
	maxMTProtoResponse = 64 << 10
)

type mtprotoNodeSettings struct {
	Domains []string
}

// dialMTProto performs the complete client-eye check. MTProto is not a generic TCP tunnel, so the req_pq_multi request
// and its resPQ response are part of dialing; the returned tunnel carries those measurements for attempt to store.
func dialMTProto(ctx context.Context, t Target, o DialOptions) (Tunnel, error) {
	start := time.Now()
	handshakeTimeout := o.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = 8 * time.Second
	}
	probeTimeout := o.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = 10 * time.Second
	}
	if t.Spec.Listen.Port == 0 || strings.TrimSpace(t.Node.Address) == "" {
		return nil, errClientUnsupported
	}

	var settings mtprotoNodeSettings
	if err := json.Unmarshal(t.Spec.Settings, &settings); err != nil || len(settings.Domains) == 0 {
		return nil, errClientUnsupported
	}
	credential, err := mp.ParseCredential(t.CredID, t.Secret)
	if err != nil {
		return nil, &ProbeError{Code: "auth", Detail: "MTProto probe credential is invalid"}
	}

	connectCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	address := net.JoinHostPort(strings.Trim(strings.TrimSpace(t.Node.Address), "[]"), strconv.Itoa(int(t.Spec.Listen.Port)))
	conn, err := (&net.Dialer{}).DialContext(connectCtx, "tcp", address)
	if err != nil {
		return nil, mtprotoConnectError(ctx, err)
	}
	connectMS := elapsedMS(start)
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	keep := false
	defer func() {
		stopCancel()
		if !keep {
			_ = conn.Close()
		}
	}()

	handshakeDeadline := start.Add(handshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(handshakeDeadline) {
		handshakeDeadline = d
	}
	if err := conn.SetDeadline(handshakeDeadline); err != nil {
		return nil, &ProbeError{Code: "tls", Detail: "cannot set the MTProto handshake deadline"}
	}
	hello, err := mtclient.BuildClientHello(credential.Key, settings.Domains[0], uint32(time.Now().Unix()))
	if err != nil {
		return nil, &ProbeError{Code: "tls", Detail: "cannot build the Fake TLS ClientHello"}
	}
	client, err := mtclient.StartClient(conn, credential.Key, hello)
	if err != nil {
		return nil, mtprotoHelloError(ctx, err)
	}
	if err := client.SendHeader(mtclient.TagAbridged, mtprotoDC); err != nil {
		return nil, mtprotoDCError(ctx, "obfuscated2 handshake", err)
	}

	var nonce [16]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, &ProbeError{Code: "exit_unreachable", Detail: "cannot prepare the MTProto request"}
	}
	request := reqPQMessage(nonce)
	responseDeadline := time.Now().Add(probeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(responseDeadline) {
		responseDeadline = d
	}
	if err := conn.SetDeadline(responseDeadline); err != nil {
		return nil, &ProbeError{Code: "exit_unreachable", Detail: "cannot set the DC response deadline"}
	}
	responseStart := time.Now()
	if _, err := client.Write(abridgedFrame(request)); err != nil {
		return nil, mtprotoDCError(ctx, "req_pq_multi write", err)
	}
	response, err := readAbridged(client)
	if err != nil {
		return nil, mtprotoDCError(ctx, "req_pq_multi response", err)
	}
	if err := validateResPQ(response, nonce); err != nil {
		return nil, &ProbeError{Code: "exit_unreachable", Detail: "DC 2 did not return a valid resPQ response"}
	}

	result := Result{
		Status: adminv1.CheckStatus_CHECK_STATUS_OK, LatencyMS: elapsedMS(start), ConnectMS: connectMS,
		ResponseMS: elapsedMS(responseStart),
	}
	keep = true
	return &mtprotoTunnel{conn: conn, result: result}, nil
}

type mtprotoTunnel struct {
	conn   net.Conn
	result Result
}

func (t *mtprotoTunnel) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("MTProto health check does not provide a generic TCP tunnel")
}
func (t *mtprotoTunnel) Close() error        { return t.conn.Close() }
func (t *mtprotoTunnel) ProbeResult() Result { return t.result }

func elapsedMS(start time.Time) uint32 {
	return uint32(max(time.Since(start).Milliseconds(), 1))
}

func mtprotoConnectError(ctx context.Context, err error) error {
	if isTimeout(ctx, err) {
		return &ProbeError{Code: "timeout", Detail: "MTProto TCP connect timed out"}
	}
	code, _ := classify(err)
	if code != "refused" {
		code = "refused"
	}
	return &ProbeError{Code: code, Detail: "MTProto TCP connect failed: " + probeErrText(err)}
}

func mtprotoHelloError(ctx context.Context, err error) error {
	if isTimeout(ctx, err) {
		return &ProbeError{Code: "timeout", Detail: "Fake TLS handshake timed out"}
	}
	switch {
	case errors.Is(err, mtclient.ErrFakeTLSAuth):
		return &ProbeError{Code: "auth", Detail: "Fake TLS credential was rejected; the endpoint answered with its fallback"}
	case errors.Is(err, mtclient.ErrFakeTLSProtocol):
		return &ProbeError{Code: "tls", Detail: "invalid Fake TLS ServerHello"}
	}
	code, detail := classify(err)
	if code == "timeout" && !isTimeout(ctx, err) {
		code = "tls"
	}
	return &ProbeError{Code: code, Detail: "Fake TLS handshake: " + store.Clip(detail, 150)}
}

func mtprotoDCError(ctx context.Context, stage string, err error) error {
	if isTimeout(ctx, err) {
		return &ProbeError{Code: "timeout", Detail: "MTProto DC 2 " + stage + " timed out"}
	}
	return &ProbeError{Code: "exit_unreachable", Detail: "MTProto DC 2 " + stage + " failed: " + probeErrText(err)}
}

func isTimeout(ctx context.Context, err error) bool {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

func reqPQMessage(nonce [16]byte) []byte {
	var msg [40]byte
	msgID := uint64(time.Now().Unix())<<32 | uint64(binary.LittleEndian.Uint32(nonce[:4])&^3)
	binary.LittleEndian.PutUint64(msg[8:16], msgID)
	binary.LittleEndian.PutUint32(msg[20:24], reqPQMulti)
	copy(msg[24:], nonce[:])
	return msg[:]
}

func abridgedFrame(msg []byte) []byte {
	words := len(msg) / 4
	if len(msg)%4 != 0 {
		return nil
	}
	out := []byte{0xef}
	if words < 0x7f {
		out = append(out, byte(words))
	} else {
		out = append(out, 0x7f, byte(words), byte(words>>8), byte(words>>16))
	}
	return append(out, msg...)
}

func readAbridged(r io.Reader) ([]byte, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return nil, err
	}
	words := int(first[0])
	if first[0] == 0x7f {
		var length [3]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return nil, err
		}
		words = int(length[0]) | int(length[1])<<8 | int(length[2])<<16
	}
	if words == 0 || words > maxMTProtoResponse/4 {
		return nil, errors.New("invalid abridged response length")
	}
	msg := make([]byte, words*4)
	_, err := io.ReadFull(r, msg)
	return msg, err
}

func validateResPQ(msg []byte, nonce [16]byte) error {
	if len(msg) < 68 || !bytes.Equal(msg[:8], make([]byte, 8)) || binary.LittleEndian.Uint32(msg[20:24]) != resPQ {
		return errors.New("not resPQ")
	}
	if !bytes.Equal(msg[24:40], nonce[:]) {
		return errors.New("resPQ nonce mismatch")
	}
	offset, err := skipTLBytes(msg, 56)
	if err != nil || offset+8 > len(msg) || binary.LittleEndian.Uint32(msg[offset:offset+4]) != vectorConstructor {
		return errors.New("invalid resPQ public key vector")
	}
	count := int(binary.LittleEndian.Uint32(msg[offset+4 : offset+8]))
	if count == 0 || count > 64 || offset+8+count*8 != len(msg) {
		return errors.New("invalid resPQ public key fingerprints")
	}
	return nil
}

func skipTLBytes(msg []byte, offset int) (int, error) {
	if offset >= len(msg) {
		return 0, io.ErrUnexpectedEOF
	}
	first := int(msg[offset])
	header, length := 1, first
	switch first {
	case 254:
		if offset+4 > len(msg) {
			return 0, io.ErrUnexpectedEOF
		}
		header = 4
		length = int(msg[offset+1]) | int(msg[offset+2])<<8 | int(msg[offset+3])<<16
	case 255:
		return 0, errors.New("invalid TL bytes length")
	}
	end := offset + header + length
	padded := (end + 3) &^ 3
	if end > len(msg) || padded > len(msg) {
		return 0, io.ErrUnexpectedEOF
	}
	return padded, nil
}

var _ Tunnel = (*mtprotoTunnel)(nil)
var _ interface{ ProbeResult() Result } = (*mtprotoTunnel)(nil)
