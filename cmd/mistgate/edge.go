package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/edgedeploy"
)

const edgeUsage = `usage:
  mistgate edge deploy --host panel.example.com [--name mistgate-edge] [--d1-location weur] [--bundle DIR | --version vX.Y.Z] [--resume] [--dry-run] [--insecure-skip-signature]
  mistgate edge setup-link --file mistgate-edge-<name>.json

The Cloudflare API token is read only from CLOUDFLARE_API_TOKEN.
Edge deploy requires Cloudflare Workers Paid and the host's DNS zone on Cloudflare.
`

// releaseVersionRe matches the stamp of a release build (the tag, with or without the v); a development build
// ("0.1.0-dev", a commit hash) does not.
var releaseVersionRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// edgeDefaultVersion is the edge release that goes with this binary, vX.Y.Z; empty for a development build.
func edgeDefaultVersion() string {
	if !releaseVersionRe.MatchString(buildinfo.Version) {
		return ""
	}
	return "v" + strings.TrimPrefix(buildinfo.Version, "v")
}

func runEdge(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(edgeUsage)
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(out, edgeUsage)
		return nil
	case "deploy":
		return runEdgeDeploy(args[1:], out)
	case "setup-link":
		return runEdgeSetupLink(args[1:], out)
	default:
		return errors.New(edgeUsage)
	}
}

func runEdgeDeploy(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mistgate edge deploy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	host := fs.String("host", "", "public panel hostname, without scheme or path")
	name := fs.String("name", "mistgate-edge", "main Worker script name")
	d1Location := fs.String("d1-location", "weur", "Cloudflare D1 location")
	bundleDir := fs.String("bundle", "", "local edge release directory")
	version := fs.String("version", "", "edge release tag, vX.Y.Z (default: this binary's own release)")
	resume := fs.Bool("resume", false, "resume using the local deployment file")
	dryRun := fs.Bool("dry-run", false, "print the plan and make GET requests only")
	skipSignature := fs.Bool("insecure-skip-signature", false, "skip signature verification for a local development bundle")
	if err := fs.Parse(args); err != nil {
		return edgeParseError(err, out)
	}
	if fs.NArg() != 0 || *host == "" {
		return errors.New(edgeUsage)
	}
	if *skipSignature && *bundleDir == "" {
		return errors.New("--insecure-skip-signature is only available with --bundle DIR")
	}
	if *skipSignature {
		fmt.Fprintln(out, "WARNING: signature verification is disabled for this local development bundle.")
	}
	pub, keyErr := buildinfo.ReleasePublicKey()
	if keyErr != nil && !*skipSignature && !errors.Is(keyErr, buildinfo.ErrUnsignedBuild) {
		return fmt.Errorf("compiled release key: %w", keyErr)
	}
	// No overall timeout covers the uploads; Ctrl+C is the way out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return edgedeploy.Deploy(ctx, edgedeploy.Options{
		Host: *host, Name: *name, D1Location: *d1Location, BundleDir: *bundleDir, Version: *version, DefaultVersion: edgeDefaultVersion(),
		Resume: *resume, DryRun: *dryRun, InsecureSkipSignature: *skipSignature,
		Token: os.Getenv("CLOUDFLARE_API_TOKEN"), PublicKey: pub,
	}, out)
}

// edgeParseError prints the usage for -h and returns the parse error otherwise (flag output is discarded, so -h would be silent).
func edgeParseError(err error, out io.Writer) error {
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(out, edgeUsage)
		return nil
	}
	return fmt.Errorf("%w\n%s", err, edgeUsage)
}

func runEdgeSetupLink(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mistgate edge setup-link", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "", "local edge deployment file")
	if err := fs.Parse(args); err != nil {
		return edgeParseError(err, out)
	}
	if fs.NArg() != 0 || *file == "" {
		return errors.New(edgeUsage)
	}
	return edgedeploy.SetupLink(*file, out)
}
