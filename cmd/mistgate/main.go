// Command mistgate is the panel: `serve` runs it, `setup` prepares a new installation.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
)

const usage = `usage: mistgate <command> [flags]

commands:
  serve     run the panel (flags: -h for the list; env fallbacks MISTGATE_*)
  setup     create the data dir, master key and database; print the admin URL and a one-time setup link
  auth      operator commands on the panel server: "auth turnstile off" is the captcha kill switch,
            "auth reset-login <login>" a new password and authenticator app after a lost phone
  mcp       stdio proxy to the panel's MCP endpoint for agent clients: --url <admin url> --token-file <file>
  release   the owner's release key and signed bundles for node updates: "release keygen", "release sign"
  version   print the version, build time and the release key fingerprint
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	privateUmask()
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "setup":
		err = runSetup(os.Args[2:], os.Stdout)
	case "auth":
		err = runAuth(os.Args[2:], os.Stdout)
	case "release":
		err = runRelease(os.Args[2:], os.Stdout)
	case "mcp":
		// stdout is the protocol channel: nothing else may be written to it
		err = runMCP(os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
	case "version", "-v", "--version":
		fmt.Println("mistgate", buildinfo.Version)
		printBuild(os.Stdout)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "mistgate: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mistgate:", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// defaultSourceURL is where the panel's source code is published. The admin links to it next to the version.
const defaultSourceURL = "https://github.com/Mistgate/mistgate"

// sourceURLDefault is the default of serve --source-url: MISTGATE_SOURCE_URL when it is set, even to "" (no link),
// else defaultSourceURL.
func sourceURLDefault() string {
	if v, ok := os.LookupEnv("MISTGATE_SOURCE_URL"); ok {
		return v
	}
	return defaultSourceURL
}

func envBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// printBuild says when this binary was built and which release key it trusts.
func printBuild(w io.Writer) {
	if b := buildinfo.BuiltUnix(); b > 0 {
		fmt.Fprintf(w, "built %s\n", time.Unix(b, 0).UTC().Format(time.RFC3339))
	}
	switch key, err := buildinfo.ReleasePublicKey(); {
	case err == nil:
		fmt.Fprintf(w, "release key %s\n", buildinfo.KeyFingerprint(key))
	case errors.Is(err, buildinfo.ErrUnsignedBuild):
		fmt.Fprintln(w, "release key: none (unsigned build, nodes are updated by hand)")
	default:
		fmt.Fprintln(w, "release key: invalid")
	}
}
