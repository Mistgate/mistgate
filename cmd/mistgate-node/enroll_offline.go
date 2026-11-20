package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mistgate/mistgate/internal/node/agent"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/plugin"
)

// maxOfflineInput bounds what --offline-finish reads from stdin: the role line and two certificates.
const maxOfflineInput = 64 << 10

// enrollOfflineCSR is `enroll --offline-csr`: the key stays in the state directory, the CSR goes to the panel.
func enrollOfflineCSR(stdout io.Writer, stateDir string, force bool) int {
	csr, err := agent.OfflineCSR(stateDir, force)
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		return 1
	}
	fmt.Fprintln(stdout, "csr="+base64.StdEncoding.EncodeToString(csr))
	return 0
}

// enrollOfflineFinish is `enroll --offline-finish`: stdin holds one JSON line with the link address and the subscription host
// role (both hold secrets, so they stay out of the command line, sudo's log and /proc), then the node certificate and the CA
// certificate. It prints only the pin of the host site's self-signed certificate (pin=<hex>), which the panel verifies the
// host by until the site has a public certificate.
func enrollOfflineFinish(in io.Reader, stdout io.Writer, cfg agent.OfflineEnrollConfig) int {
	if cfg.NodeID == "" || cfg.CASHA256 == "" {
		fmt.Fprintln(os.Stderr, "enroll: --offline-finish needs --node-id and --ca-sha256")
		return 2
	}
	b, err := io.ReadAll(io.LimitReader(in, maxOfflineInput+1))
	if err != nil || len(b) > maxOfflineInput {
		fmt.Fprintln(os.Stderr, "enroll: could not read the role and the certificates from stdin")
		return 2
	}
	line, rest, _ := bytes.Cut(b, []byte("\n"))
	if err := json.Unmarshal(bytes.TrimSpace(line), &cfg.Role); err != nil {
		fmt.Fprintln(os.Stderr, "enroll: stdin must start with the role line (JSON)")
		return 2
	}
	cfg.PEM = rest
	meta, err := agent.EnrollOffline(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		return 1
	}
	if name := meta.SubHost.SiteName; name != "" {
		// The agent's own site on 443 presents this self-signed certificate until ACME issues one (the same file: certs keeps
		// self-signed certificates by name).
		cert, err := certs.New(filepath.Join(cfg.StateDir, "certs")).Acquire(context.Background(), "offline-enroll",
			plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: name})
		if err != nil || cert.Info == nil {
			fmt.Fprintln(os.Stderr, "enroll: the site certificate could not be made:", err)
			return 1
		}
		fmt.Fprintln(stdout, "pin="+cert.Info().PinSHA256)
	}
	return 0
}
