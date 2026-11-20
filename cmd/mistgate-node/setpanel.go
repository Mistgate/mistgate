package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mistgate/mistgate/internal/node/agent"
)

// cmdSetPanel is `set-panel`: it saves a new panel link address, which the agent tries next to its current one and keeps
// once a session over it succeeds.
func cmdSetPanel(args []string) int {
	fs := flag.NewFlagSet("set-panel", flag.ContinueOnError)
	linkURL := fs.String("link-url", "", "the panel's new link address (wss://host/<secret-prefix>/)")
	stateDir := fs.String("state-dir", envOr("MISTGATE_NODE_STATE_DIR", defaultStateDir), "state directory")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: mistgate-node set-panel --link-url <wss://host/prefix/> [--state-dir dir]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *linkURL == "" || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if err := agent.SetPanelLink(*stateDir, *linkURL); err != nil {
		fmt.Fprintln(os.Stderr, "set-panel:", err)
		return 1
	}
	fmt.Println("Saved. Restart the agent: systemctl restart mistgate-node")
	return 0
}
