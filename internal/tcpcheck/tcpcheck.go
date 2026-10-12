// Package tcpcheck probes bounded TCP targets and distinguishes a Mistgate answer from another listener.
package tcpcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	KindHTTPMark = "http_mark"
	KindTLS      = "tls"

	VerdictOurs    = "ours"
	VerdictOther   = "other"
	VerdictRefused = "refused"
	VerdictTimeout = "timeout"
	MaxTargets     = 8
	markSize       = 16
)

var (
	ErrBadParams       = errors.New("tcpcheck: bad parameters")
	ErrUnsupportedHost = errors.New("tcpcheck: unsupported destination")
	ErrNoRoute         = errors.New("tcpcheck: no route")
)

const (
	defaultConnectTimeout = 5 * time.Second
	defaultRequestTimeout = 8 * time.Second
)

type Target struct {
	Port      uint16
	Kind      string
	SNI       string
	PinSHA256 string
	Token     []byte
	Answer    []byte
}

type probeOptions struct {
	rootCAs        *x509.CertPool
	connectTimeout time.Duration
	requestTimeout time.Duration
}

// Validate checks the target count, ports, and probe-specific parameters before opening sockets.
func Validate(host string, targets []Target) error {
	if strings.TrimSpace(host) == "" || len(targets) == 0 || len(targets) > MaxTargets {
		return ErrBadParams
	}
	seen := make(map[uint16]struct{}, len(targets))
	for _, target := range targets {
		if target.Port == 0 {
			return ErrBadParams
		}
		if _, ok := seen[target.Port]; ok {
			return ErrBadParams
		}
		seen[target.Port] = struct{}{}
		switch target.Kind {
		case KindHTTPMark:
			if len(target.Token) != markSize || len(target.Answer) != markSize || target.SNI != "" || target.PinSHA256 != "" {
				return ErrBadParams
			}
		case KindTLS:
			if strings.TrimSpace(target.SNI) == "" || len(target.Token) != 0 || len(target.Answer) != 0 {
				return ErrBadParams
			}
			if target.PinSHA256 != "" {
				pin, err := hex.DecodeString(target.PinSHA256)
				if err != nil || len(pin) != sha256.Size {
					return ErrBadParams
				}
			}
		default:
			return ErrBadParams
		}
	}
	return nil
}

// Probe resolves host once, preferring IPv4, then probes each target concurrently. A failed port is a verdict; only
// invalid input or failure to resolve the destination fails the whole call.
func Probe(ctx context.Context, host string, targets []Target) (family string, verdicts map[uint16]string, err error) {
	return probe(ctx, host, targets, probeOptions{
		connectTimeout: defaultConnectTimeout,
		requestTimeout: defaultRequestTimeout,
	})
}

func probe(ctx context.Context, host string, targets []Target, options probeOptions) (string, map[uint16]string, error) {
	if err := Validate(host, targets); err != nil {
		return "", nil, err
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if options.connectTimeout <= 0 {
		options.connectTimeout = defaultConnectTimeout
	}
	if options.requestTimeout <= 0 {
		options.requestTimeout = defaultRequestTimeout
	}
	ip, err := resolve(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		return "", nil, fmt.Errorf("%w: %v", ErrNoRoute, err)
	}
	if unsupportedDestination(ip) {
		return "", nil, fmt.Errorf("%w: %s", ErrUnsupportedHost, ip)
	}
	family := "4"
	if ip.Is6() {
		family = "6"
	}

	verdicts := make(map[uint16]string, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			verdict := probeTarget(ctx, ip, host, target, options)
			mu.Lock()
			verdicts[target.Port] = verdict
			mu.Unlock()
		}()
	}
	wg.Wait()
	return family, verdicts, nil
}

func probeTarget(ctx context.Context, ip netip.Addr, host string, target Target, options probeOptions) string {
	probeCtx, cancel := context.WithTimeout(ctx, options.requestTimeout)
	defer cancel()
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(int(target.Port)))
	dialer := &net.Dialer{Timeout: options.connectTimeout}
	if target.Kind == KindHTTPMark {
		return probeHTTPMark(probeCtx, dialer, addr, host, target)
	}
	return probeTLS(probeCtx, dialer, addr, target, options.rootCAs)
}

func probeHTTPMark(ctx context.Context, dialer *net.Dialer, addr, host string, target Target) string {
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", addr)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u := url.URL{Scheme: "http", Host: addr, Path: "/.well-known/acme-challenge/" + hex.EncodeToString(target.Token)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return VerdictOther
	}
	// The node address is used as Host, independent of whether it is a DNS name or an IP literal.
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return verdictForError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(target.Answer)+1)))
	if err != nil {
		return verdictForError(err)
	}
	if resp.StatusCode == http.StatusOK && bytes.Equal(body, target.Answer) {
		return VerdictOurs
	}
	return VerdictOther
}

func probeTLS(ctx context.Context, dialer *net.Dialer, addr string, target Target, roots *x509.CertPool) string {
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return verdictForError(err)
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if ok {
		_ = conn.SetDeadline(deadline)
	}
	tlsConn := tls.Client(conn, tlsConfig(target, roots))
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return verdictForError(err)
	}
	return VerdictOurs
}

func tlsConfig(target Target, roots *x509.CertPool) *tls.Config {
	cfg := &tls.Config{ServerName: target.SNI, RootCAs: roots, MinVersion: tls.VersionTLS12}
	if target.PinSHA256 == "" {
		return cfg
	}
	// A reported pin is an alternative to normal system-root validation for a self-signed listener.
	cfg.InsecureSkipVerify = true //nolint:gosec -- VerifyConnection below applies both chain and exact-pin checks.
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("tcpcheck: server sent no certificate")
		}
		leaf := cs.PeerCertificates[0]
		intermediates := x509.NewCertPool()
		for _, cert := range cs.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: target.SNI,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
			return nil
		}
		sum := sha256.Sum256(leaf.Raw)
		pin, _ := hex.DecodeString(target.PinSHA256)
		if bytes.Equal(sum[:], pin) {
			return nil
		}
		return errors.New("tcpcheck: certificate is neither trusted nor pinned")
	}
	return cfg
}

func verdictForError(err error) string {
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "refused") {
		return VerdictRefused
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		(errors.As(err, &netErr) && netErr.Timeout()) || errors.Is(err, os.ErrDeadlineExceeded) {
		return VerdictTimeout
	}
	return VerdictOther
}

func resolve(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, ip := range ips {
		if ip.Unmap().Is4() {
			return ip.Unmap(), nil
		}
	}
	return ips[0].Unmap(), nil
}

func unsupportedDestination(ip netip.Addr) bool {
	return !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}
