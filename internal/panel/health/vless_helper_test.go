//go:build !js

package health

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	nodevless "github.com/mistgate/mistgate/internal/node/vless"
	"github.com/mistgate/mistgate/internal/panel/health/vlesshelper"
	"github.com/mistgate/mistgate/internal/plugin"
	xtlsreality "github.com/xtls/reality"
)

// vlessTestServerEnv makes the test binary run the node's VLESS engine instead of the tests. The engine is Xray as well
// and has its own data races (seen with -race in its XHTTP server); in a separate process they cannot fail the panel's
// tests, and the panel process stays free of any Xray code, which is the point of the check's design.
const vlessTestServerEnv = "MISTGATE_HEALTH_VLESS_TEST_SERVER"

// runVLESSHelperTestSubprocess serves the test binary's re-exec modes: the panel's client helper (real or fake) and the
// loopback VLESS server. It returns false for a normal test run.
func runVLESSHelperTestSubprocess() bool {
	if os.Getenv(vlessTestServerEnv) != "" {
		if err := runVLESSTestServer(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	mode := os.Getenv(vlessHelperTestModeEnv)
	if mode == "" {
		return false
	}
	switch mode {
	case "hang":
		_, _ = fmt.Fprintln(os.Stdout, `{"port":12345}`)
		select {}
	case "silent":
		// never reports a listener: the panel must give up and reap it
		select {}
	case "crash":
		_, _ = fmt.Fprintln(os.Stderr, "[Error] x509: certificate verification failed")
		os.Exit(1)
	case "real":
		if err := vlesshelper.Run(os.Stdin, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	default:
		os.Exit(2)
	}
	return true
}

type vlessTestServerRequest struct {
	Spec    plugin.InboundSpec
	Creds   []plugin.UserCred
	CertDER []byte
	KeyDER  []byte // PKCS#8
}

type vlessTestServerReady struct {
	PinSHA256 string
	NotAfter  int64
}

func runVLESSTestServer(input io.Reader, output io.Writer) error {
	var request vlessTestServerRequest
	decoder := json.NewDecoder(input)
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	key, err := x509.ParsePKCS8PrivateKey(request.KeyDER)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(request.CertDER)
	if err != nil {
		return err
	}
	certificate := &tls.Certificate{Certificate: [][]byte{request.CertDER}, PrivateKey: key, Leaf: leaf}
	sum := sha256.Sum256(request.CertDER)
	certs := &loopbackVLESSCerts{certificate: certificate, info: engine.CertInfo{PinSHA256: hex.EncodeToString(sum[:]), NotAfter: leaf.NotAfter}}
	nodevless.BindIP = net.IPv4(127, 0, 0, 1)
	direct := egress.New(func() []string { return nil }, egress.AllowPrivate())
	instance, err := nodevless.New(engine.Env{
		Certs: certs,
		Egress: func(name string) (engine.Egress, error) {
			if name != "" && name != "direct" {
				return nil, errors.New("test VLESS engine only supports direct egress")
			}
			return direct, nil
		},
		DNS: func() []string { return nil },
		Now: time.Now,
	})
	if err != nil {
		return err
	}
	report, err := instance.Apply(context.Background(), request.Spec, request.Creds)
	if err != nil {
		return fmt.Errorf("node VLESS engine rejected the panel spec: %w", err)
	}
	waitForSiteRecords(request.Spec)
	if err := json.NewEncoder(output).Encode(vlessTestServerReady{PinSHA256: report.Cert.PinSHA256, NotAfter: report.Cert.NotAfter.Unix()}); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, input) // serve until the test closes stdin
	return nil
}

// startVLESSTestServer runs the node engine for spec in a child process and returns its certificate report.
func startVLESSTestServer(t *testing.T, spec plugin.InboundSpec, creds []plugin.UserCred, certificate *tls.Certificate) vlessTestServerReady {
	t.Helper()
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(minimalHelperEnvironment(), vlessTestServerEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr tailBuffer
	stderr.limit = 8 << 10
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	if err := json.NewEncoder(stdin).Encode(vlessTestServerRequest{Spec: spec, Creds: creds, CertDER: certificate.Certificate[0], KeyDER: keyDER}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		ready vlessTestServerReady
		err   error
	}
	got := make(chan result, 1)
	go func() {
		var ready vlessTestServerReady
		err := json.NewDecoder(stdout).Decode(&ready)
		got <- result{ready, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("test VLESS server did not start: %v; stderr: %s", r.err, stderr.String())
		}
		return r.ready
	case <-time.After(20 * time.Second):
		t.Fatalf("test VLESS server did not start in time; stderr: %s", stderr.String())
		return vlessTestServerReady{}
	}
}

// waitForSiteRecords holds back the ready line of a REALITY inbound whose target is the node's own site until the node has
// stored the site's record lengths for REALITY (it does so in the background after Apply). A client that arrives earlier finds no record lengths and
// REALITY makes it wait 5 s, a start-up race that a real client never meets.
func waitForSiteRecords(spec plugin.InboundSpec) {
	if !strings.Contains(string(spec.Settings), `"self"`) {
		return
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		measured := 0
		xtlsreality.GlobalPostHandshakeRecordsLens.Range(func(_, value any) bool {
			if _, ok := value.([]int); ok {
				measured++
			}
			return true
		})
		if measured >= 3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
