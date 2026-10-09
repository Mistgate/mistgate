package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mistgate/mistgate/internal/node/agent"
)

const defaultStateDir = "/var/lib/mistgate-node"

const maxEnrollmentTokenBytes = 256

func cmdEnroll(args []string) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	panel := fs.String("panel", envOr("MISTGATE_PANEL", ""), "panel address host:port")
	sni := fs.String("sni", envOr("MISTGATE_AGENT_SNI", ""), "secret agent SNI name of the panel")
	linkURL := fs.String("link-url", "", "enrol over the panel's public link instead (wss://host/<secret-prefix>/); not with --panel or --sni")
	pin := fs.String("ca-sha256", envOr("MISTGATE_CA_SHA256", ""), "SHA-256 fingerprint of the panel CA certificate (hex)")
	token := fs.String("token", "", "one-time enrollment token (or set MISTGATE_ENROLL_TOKEN)")
	tokenStdin := fs.Bool("token-stdin", false, "read the one-time enrollment token from stdin")
	stateDir := fs.String("state-dir", envOr("MISTGATE_NODE_STATE_DIR", defaultStateDir), "state directory")
	force := fs.Bool("force", false, "replace an existing identity")
	resumeKey := fs.Bool("resume-key", false, "persist the pending key so an interrupted SSH enrollment can be retried safely")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: mistgate-node enroll (--panel <host:port> --sni <name> | --link-url <wss://host/prefix/>) --ca-sha256 <hex> (--token <token> | --token-stdin) [--state-dir dir]\n"+
			"The panel prints the --token form when you add a node. Use --token-stdin to keep the token out of process arguments.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resolvedToken, err := resolveEnrollmentToken(os.Stdin, *token, os.Getenv("MISTGATE_ENROLL_TOKEN"), *tokenStdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		return 2
	}
	*token = resolvedToken
	if *linkURL != "" {
		explicit := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
		if explicit["panel"] || explicit["sni"] {
			fmt.Fprintln(os.Stderr, "enroll: --link-url cannot be combined with --panel or --sni")
			return 2
		}
		*panel, *sni = "", "" // MISTGATE_PANEL in the environment is not a request to combine them
		if *pin == "" || *token == "" {
			fmt.Fprintln(os.Stderr, "enroll: --link-url needs --ca-sha256 and a token source")
			return 2
		}
	} else if *panel == "" || *sni == "" || *pin == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "enroll: --panel, --sni, --ca-sha256 and a token source are required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	meta, err := agent.Enroll(ctx, agent.EnrollConfig{
		StateDir: *stateDir, Panel: *panel, SNI: *sni, LinkURL: *linkURL, CASHA256: *pin, Token: *token, Force: *force,
		ResumePendingKey: *resumeKey, Timeout: 30 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		return 1
	}
	sd := ""
	if *stateDir != defaultStateDir {
		sd = " --state-dir " + *stateDir
	}
	with := meta.Panel
	if meta.Link != "" {
		with = "the panel over its link" // the link address holds a secret path: not echoed
	}
	fmt.Printf("enrolled as %s with %s; state in %s\nnext: mistgate-node install%s   (or, in the foreground: mistgate-node run%s)\n",
		meta.NodeID, with, *stateDir, sd, sd)
	return 0
}

func resolveEnrollmentToken(input io.Reader, explicit, environment string, fromStdin bool) (string, error) {
	token := explicit
	if fromStdin {
		if explicit != "" || environment != "" {
			return "", errors.New("--token-stdin cannot be combined with --token or MISTGATE_ENROLL_TOKEN")
		}
		if input == nil {
			return "", errors.New("could not read token from stdin")
		}
		b, err := io.ReadAll(io.LimitReader(input, maxEnrollmentTokenBytes+1))
		if err != nil || len(b) > maxEnrollmentTokenBytes {
			return "", errors.New("could not read token from stdin")
		}
		token = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	} else if token == "" {
		token = environment
	}
	if strings.ContainsAny(token, "\x00\r\n \t") || len(token) > maxEnrollmentTokenBytes {
		return "", errors.New("invalid token")
	}
	return token, nil
}
