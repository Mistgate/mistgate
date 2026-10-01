package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/mistgate/mistgate/internal/panel/mcp"
)

const mcpUsage = `usage: mistgate mcp --url <admin url> --token-file <file>

  Runs a local MCP server on stdin/stdout for agent clients that cannot speak HTTP, and forwards every message to the
  panel's MCP endpoint (the admin URL plus "mcp"). It decides nothing: the panel answers, with the token's profile.

  --url          the admin URL "mistgate setup" printed, like https://panel.example.com/<prefix>/   (env MISTGATE_URL)
  --token-file   a file whose first line is an API token made in the admin panel (Settings, Tokens)   (env MISTGATE_TOKEN_FILE)

  The token is read from the file only: never from the command line or the environment, and never printed.
  Plain http is refused unless the host is localhost.
`

// runMCP implements `mistgate mcp`: the stdio proxy to the panel's MCP endpoint. stdout carries only
// protocol messages; everything else goes to stderr.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, mcpUsage) }
	url := fs.String("url", os.Getenv("MISTGATE_URL"), "admin URL of the panel")
	tokenFile := fs.String("token-file", os.Getenv("MISTGATE_TOKEN_FILE"), "file whose first line is the API token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return mcp.RunProxy(ctx, *url, *tokenFile, stdin, stdout, stderr)
}
