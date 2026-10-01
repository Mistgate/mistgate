package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mistgate/mistgate/internal/node/agent"
)

const defaultStateDir = "/var/lib/mistgate-node"

func cmdEnroll(args []string) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	panel := fs.String("panel", envOr("MISTGATE_PANEL", ""), "panel address host:port")
	sni := fs.String("sni", envOr("MISTGATE_AGENT_SNI", ""), "secret agent SNI name of the panel")
	pin := fs.String("ca-sha256", envOr("MISTGATE_CA_SHA256", ""), "SHA-256 fingerprint of the panel CA certificate (hex)")
	token := fs.String("token", "", "one-time enrollment token (or set MISTGATE_ENROLL_TOKEN, which keeps it out of the process list)")
	stateDir := fs.String("state-dir", envOr("MISTGATE_NODE_STATE_DIR", defaultStateDir), "state directory")
	force := fs.Bool("force", false, "replace an existing identity")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: mistgate-node enroll --panel <host:port> --sni <name> --ca-sha256 <hex> --token <token> [--state-dir dir]\n"+
			"The panel prints this command, with all four values filled in, when you add a node.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *token == "" {
		*token = os.Getenv("MISTGATE_ENROLL_TOKEN")
	}
	if *panel == "" || *sni == "" || *pin == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "enroll: --panel, --sni, --ca-sha256 and --token are required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	meta, err := agent.Enroll(ctx, agent.EnrollConfig{
		StateDir: *stateDir, Panel: *panel, SNI: *sni, CASHA256: *pin, Token: *token, Force: *force, Timeout: 30 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		return 1
	}
	sd := ""
	if *stateDir != defaultStateDir {
		sd = " --state-dir " + *stateDir
	}
	fmt.Printf("enrolled as %s with %s; state in %s\nnext: mistgate-node install%s   (or, in the foreground: mistgate-node run%s)\n",
		meta.NodeID, meta.Panel, *stateDir, sd, sd)
	return 0
}
