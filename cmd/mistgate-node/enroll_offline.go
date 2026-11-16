package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"

	"github.com/mistgate/mistgate/internal/node/agent"
)

// maxOfflinePEM bounds what --offline-finish reads from stdin: the tunnel line and two certificates.
const maxOfflinePEM = 64 << 10

// enrollOfflineCSR is `enroll --offline-csr`: the key stays in the state directory, the CSR goes to the panel.
func enrollOfflineCSR(stateDir string, force bool) int {
	csr, err := agent.OfflineCSR(stateDir, force)
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		return 1
	}
	fmt.Println("csr=" + base64.StdEncoding.EncodeToString(csr))
	return 0
}

// enrollOfflineFinish is `enroll --offline-finish`: stdin holds the line `tunnel-sni=<name>` (the tunnel secret, kept
// out of the command line, sudo's log and /proc), then the node certificate and the CA certificate.
func enrollOfflineFinish(in io.Reader, cfg agent.OfflineEnrollConfig) int {
	if cfg.NodeID == "" || cfg.CASHA256 == "" || cfg.LinkURL == "" || cfg.TunnelName == "" {
		fmt.Fprintln(os.Stderr, "enroll: --offline-finish needs --node-id, --ca-sha256, --link-url and --tunnel-name")
		return 2
	}
	b, err := io.ReadAll(io.LimitReader(in, maxOfflinePEM+1))
	if err != nil || len(b) > maxOfflinePEM {
		fmt.Fprintln(os.Stderr, "enroll: could not read the certificates from stdin")
		return 2
	}
	line, rest, _ := bytes.Cut(b, []byte("\n"))
	sni, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("tunnel-sni="))
	if !ok || len(sni) == 0 {
		fmt.Fprintln(os.Stderr, "enroll: stdin must start with the line tunnel-sni=<name>")
		return 2
	}
	cfg.TunnelSNI, cfg.PEM = string(sni), rest
	meta, err := agent.EnrollOffline(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		return 1
	}
	sd := ""
	if cfg.StateDir != defaultStateDir {
		sd = " --state-dir " + cfg.StateDir
	}
	fmt.Printf("enrolled as %s (subscription host); state in %s\nnext: mistgate-node install%s   (or, in the foreground: mistgate-node run%s)\n",
		meta.NodeID, cfg.StateDir, sd, sd)
	return 0
}
