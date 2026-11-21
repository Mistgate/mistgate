//go:build !js

package health

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	mp "github.com/mistgate/mistgate/internal/mtproto"
	mtclient "github.com/mistgate/mistgate/internal/mtproto/client"
	"github.com/mistgate/mistgate/internal/webproxy"
)

func dialMTProtoWeb(ctx context.Context, t Target, o DialOptions) (tunnel Tunnel, retErr error) {
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
	host := settings.Domains[0]
	capability, err := webproxy.DeriveCapability(host, credential.Key[:])
	if err != nil {
		return nil, &ProbeError{Code: "auth", Detail: "MTProto Web probe capability could not be derived"}
	}

	address := net.JoinHostPort(strings.Trim(strings.TrimSpace(t.Node.Address), "[]"), strconv.Itoa(int(t.Spec.Listen.Port)))
	var connected sync.Once
	var connectMS atomic.Uint32
	dialer := &net.Dialer{}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if o.ProbeTLS != nil {
		tlsConfig = o.ProbeTLS.Clone()
		tlsConfig.ServerName = host
		tlsConfig.InsecureSkipVerify = false
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	transport := &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: handshakeTimeout,
		ForceAttemptHTTP2:   true,
		DialContext: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
			conn, err := dialer.DialContext(dialCtx, network, address)
			if err == nil {
				connected.Do(func() { connectMS.Store(elapsedMS(start)) })
			}
			return conn, err
		},
	}
	defer transport.CloseIdleConnections()
	client, err := webproxy.NewClient("https://"+host, capability, transport)
	if err != nil {
		return nil, &ProbeError{Code: "auth", Detail: "MTProto Web probe client configuration is invalid"}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
		defer cancel()
		if err := client.DeleteSession(cleanupCtx); err != nil && retErr == nil {
			tunnel = nil
			retErr = mtprotoWebError(cleanupCtx, err, "session", "MTProto Web session could not be deleted")
		}
	}()

	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, handshakeTimeout)
	defer cancelHandshake()
	if err := client.Bootstrap(handshakeCtx); err != nil {
		return nil, mtprotoWebError(handshakeCtx, err, "tls", "MTProto Web bridge request failed")
	}
	connectTime := connectMS.Load()
	if connectTime == 0 {
		connectTime = elapsedMS(start)
	}
	stream, err := client.OpenStream(handshakeCtx)
	if err != nil {
		return nil, mtprotoWebError(handshakeCtx, err, "session", "MTProto Web stream could not be opened")
	}

	type headerResult struct {
		client *mtclient.StreamClient
		err    error
	}
	headerDone := make(chan headerResult, 1)
	go func() {
		streamClient, err := mtclient.StartStreamClient(stream, credential.Key, mtclient.TagAbridged, mtprotoDC)
		headerDone <- headerResult{client: streamClient, err: err}
	}()
	var streamClient *mtclient.StreamClient
	select {
	case result := <-headerDone:
		if result.err != nil {
			return nil, mtprotoWebError(handshakeCtx, result.err, "session", "MTProto Web obfuscated stream could not be started")
		}
		streamClient = result.client
	case <-handshakeCtx.Done():
		return nil, mtprotoWebError(handshakeCtx, handshakeCtx.Err(), "session", "MTProto Web stream setup timed out")
	}

	probeCtx, cancelProbe := context.WithTimeout(ctx, probeTimeout)
	defer cancelProbe()
	type exchangeResult struct {
		responseMS uint32
		err        error
	}
	exchangeDone := make(chan exchangeResult, 1)
	go func() {
		responseMS, err := exchangeMTProtoProbe(streamClient)
		exchangeDone <- exchangeResult{responseMS: responseMS, err: err}
	}()
	var responseMS uint32
	select {
	case result := <-exchangeDone:
		if probeCtx.Err() != nil {
			return nil, mtprotoWebError(probeCtx, probeCtx.Err(), "timeout", "MTProto Web DC response timed out")
		}
		if result.err != nil {
			var exchangeErr *mtprotoExchangeError
			if errors.As(result.err, &exchangeErr) {
				switch exchangeErr.stage {
				case "resPQ validation":
					return nil, &ProbeError{Code: "exit_unreachable", Detail: "DC 2 did not return a valid resPQ response"}
				case "req_pq_multi response":
					if errors.Is(exchangeErr.err, io.EOF) || errors.Is(exchangeErr.err, io.ErrUnexpectedEOF) {
						return nil, &ProbeError{Code: "exit_unreachable", Detail: "MTProto DC 2 closed the probe stream before resPQ"}
					}
				}
				return nil, mtprotoWebError(probeCtx, exchangeErr.err, "session", "MTProto Web stream exchange failed")
			}
			return nil, mtprotoWebError(probeCtx, result.err, "session", "MTProto Web stream exchange failed")
		}
		responseMS = result.responseMS
	case <-probeCtx.Done():
		return nil, mtprotoWebError(probeCtx, probeCtx.Err(), "timeout", "MTProto Web DC response timed out")
	}

	return &mtprotoWebTunnel{result: Result{
		Status: adminv1.CheckStatus_CHECK_STATUS_OK, LatencyMS: elapsedMS(start), ConnectMS: connectTime,
		ResponseMS: responseMS,
	}}, nil
}

func mtprotoWebError(ctx context.Context, err error, fallbackCode, fallbackDetail string) *ProbeError {
	if isTimeout(ctx, err) {
		return &ProbeError{Code: "timeout", Detail: "MTProto Web probe timed out"}
	}
	if detail, ok := mtprotoWebCertificateDetail(err); ok {
		return &ProbeError{Code: "tls", Detail: detail}
	}
	var bootstrapErr *webproxy.BootstrapError
	if errors.As(err, &bootstrapErr) {
		switch bootstrapErr.Stage {
		case webproxy.BootstrapStageBridge:
			return &ProbeError{Code: "auth", Detail: "MTProto Web endpoint answered without a bridge page"}
		case webproxy.BootstrapStageSession:
			return &ProbeError{Code: "session", Detail: "MTProto Web session could not be created"}
		}
	}
	code, _ := classify(err)
	if code == "refused" {
		return &ProbeError{Code: "refused", Detail: "MTProto Web TCP connection was refused"}
	}
	return &ProbeError{Code: fallbackCode, Detail: fallbackDetail}
}

func mtprotoWebCertificateDetail(err error) (string, bool) {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return "MTProto Web TLS certificate has not been issued yet", true
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return "MTProto Web TLS certificate does not match the inbound name", true
	}
	return "", false
}

type mtprotoWebTunnel struct {
	result Result
}

func (t *mtprotoWebTunnel) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("MTProto Web health check does not provide a generic TCP tunnel")
}
func (*mtprotoWebTunnel) Close() error          { return nil }
func (t *mtprotoWebTunnel) ProbeResult() Result { return t.result }

var _ Tunnel = (*mtprotoWebTunnel)(nil)
var _ interface{ ProbeResult() Result } = (*mtprotoWebTunnel)(nil)
