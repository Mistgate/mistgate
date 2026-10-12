package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/edgedeploy"
)

const edgeUsage = `usage:
  mistgate edge deploy --host panel.example.com [--name mistgate-edge] [--d1-location weur] [--bundle DIR | --version vX.Y.Z] [--resume] [--dry-run] [--insecure-skip-signature]
  mistgate edge setup-link --file mistgate-edge-<name>.json

The Cloudflare API token is read only from CLOUDFLARE_API_TOKEN.
Edge deploy requires Cloudflare Workers Paid and the host's DNS zone on Cloudflare.
`

func runEdge(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(edgeUsage)
	}
	switch args[0] {
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
	version := fs.String("version", "", "edge release version (default: latest)")
	resume := fs.Bool("resume", false, "resume using the local deployment file")
	dryRun := fs.Bool("dry-run", false, "print the plan and make GET requests only")
	skipSignature := fs.Bool("insecure-skip-signature", false, "skip signature verification for a local development bundle")
	if err := fs.Parse(args); err != nil {
		return err
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
	return edgedeploy.Deploy(context.Background(), edgedeploy.Options{
		Host: *host, Name: *name, D1Location: *d1Location, BundleDir: *bundleDir, Version: *version,
		Resume: *resume, DryRun: *dryRun, InsecureSkipSignature: *skipSignature,
		Token: os.Getenv("CLOUDFLARE_API_TOKEN"), PublicKey: pub,
	}, out)
}

func runEdgeSetupLink(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mistgate edge setup-link", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "", "local edge deployment file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *file == "" {
		return errors.New(edgeUsage)
	}
	return edgedeploy.SetupLink(*file, out)
}
