//go:build !js

package vlesshelper

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	xraylog "github.com/xtls/xray-core/common/log"
	xraycore "github.com/xtls/xray-core/core"

	"github.com/mistgate/mistgate/internal/panel/health/vlesswire"
)

const maxRequestBytes = 64 << 10

// Run reads one request, starts a loopback SOCKS listener backed by one Xray instance, and serves until the parent
// closes stdin or the bounded helper lifetime expires.
func Run(input io.Reader, output, stderr io.Writer) error {
	reader := bufio.NewReaderSize(input, 4096)
	line, err := readRequestLine(reader)
	if err != nil {
		writeLine(stderr, "[Error] invalid VLESS helper request")
		return errors.New("invalid VLESS helper request")
	}
	var request vlesswire.Request
	if err := json.Unmarshal(line, &request); err != nil || validateRequest(request) != nil {
		writeLine(stderr, "[Error] invalid VLESS helper request")
		return errors.New("invalid VLESS helper request")
	}
	xraylog.RegisterHandler(&stderrLogger{writer: stderr, secrets: []string{request.Client.UUID, request.SOCKSUsername, request.SOCKSPassword}})

	port, err := reserveLoopbackPort()
	if err != nil {
		writeLine(stderr, "[Error] cannot reserve VLESS helper listener")
		return errors.New("cannot reserve VLESS helper listener")
	}
	config, err := BuildClientConfig(request.Client, port, request.SOCKSUsername, request.SOCKSPassword, request.HandshakeTimeout, request.TestTLSPin)
	if err != nil {
		writeLine(stderr, "[Error] cannot build VLESS helper client")
		return errors.New("cannot build VLESS helper client")
	}
	instance, err := xraycore.New(config)
	if err != nil {
		writeLine(stderr, "[Error] cannot create VLESS helper client")
		return errors.New("cannot create VLESS helper client")
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		writeLine(stderr, "[Error] cannot start VLESS helper client")
		return errors.New("cannot start VLESS helper client")
	}
	if err := json.NewEncoder(output).Encode(vlesswire.Ready{Port: port}); err != nil {
		_ = instance.Close()
		return errors.New("cannot report VLESS helper listener")
	}

	inputClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(inputClosed)
	}()
	timer := time.NewTimer(request.HardTimeout)
	defer timer.Stop()
	select {
	case <-inputClosed:
	case <-timer.C:
	}
	// No instance.Close(): the process exits right after Run returns, which frees every socket and goroutine at once and cannot
	// hang on a stuck transport (a graceful Close made the panel wait for its kill timeout under the race detector).
	return nil
}

func validateRequest(request vlesswire.Request) error {
	if request.Client.UUID == "" || request.SOCKSUsername == "" || request.SOCKSPassword == "" ||
		len(request.SOCKSUsername) > 255 || len(request.SOCKSPassword) > 255 ||
		request.HandshakeTimeout <= 0 || request.HandshakeTimeout > time.Minute ||
		request.HardTimeout <= 0 || request.HardTimeout > 2*time.Minute {
		return errors.New("invalid helper request fields")
	}
	return nil
}

func readRequestLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > maxRequestBytes {
			return nil, errors.New("helper request too large")
		}
		line = append(line, part...)
		if err == nil {
			return bytes.TrimSpace(line), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func reserveLoopbackPort() (uint16, error) {
	// Xray treats TCP port zero as a Unix listener, so use the OS-assigned loopback port in its receiver config.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

type stderrLogger struct {
	writer  io.Writer
	secrets []string
}

func (l *stderrLogger) Handle(message xraylog.Message) {
	var line string
	switch message := message.(type) {
	case *xraylog.GeneralMessage:
		line = message.String()
		// Warnings and errors are always useful. Of the info lines only failures and the tunnel marker are: the rest is per-request noise.
		if message.Severity != xraylog.Severity_Error && message.Severity != xraylog.Severity_Warning &&
			!(message.Severity == xraylog.Severity_Info && (strings.Contains(line, "failed") || strings.Contains(line, vlesswire.TunnelMarker))) {
			return
		}
	case *xraylog.AccessMessage:
		if message.Status != xraylog.AccessRejected {
			return
		}
		line = "[Warning] " + message.String()
	default:
		return
	}
	line = strings.Join(strings.Fields(line), " ")
	for _, secret := range l.secrets {
		if secret == "" {
			continue
		}
		line = strings.ReplaceAll(line, secret, "[redacted]")
		line = strings.ReplaceAll(line, strings.ToUpper(secret), "[redacted]")
	}
	if runes := []rune(line); len(runes) > 512 {
		line = string(runes[:512])
	}
	writeLine(l.writer, line)
}

func writeLine(w io.Writer, line string) {
	line = strings.Join(strings.Fields(line), " ")
	if line != "" {
		_, _ = fmt.Fprintln(w, line)
	}
}
