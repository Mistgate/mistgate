package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/buildinfo"
)

func TestEdgeDeployDoesNotAcceptOrEchoTokenFlags(t *testing.T) {
	secret := "token-value-must-not-appear"
	err := runEdgeDeploy([]string{"--host", "panel.example.com", "--token", secret}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("accepted a Cloudflare API token flag")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token flag value appeared in error: %v", err)
	}
}

func TestEdgeHelpPrintsUsage(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"deploy", "-h"}, {"deploy", "--help"}, {"setup-link", "-h"}, {"reset-login", "-h"}} {
		var out bytes.Buffer
		if err := runEdge(args, &out); err != nil || !strings.Contains(out.String(), "mistgate edge deploy") {
			t.Errorf("%v: err=%v output=%q", args, err, out.String())
		}
		if len(args) > 0 && args[0] == "reset-login" && (!strings.Contains(out.String(), "admin login") || !strings.Contains(out.String(), "admin ID")) {
			t.Errorf("reset-login help does not distinguish edge login from VPS admin ID: %q", out.String())
		}
	}
}

func TestEdgeResetLoginReadsTokenOnlyFromEnvironment(t *testing.T) {
	secret := "token-value-must-not-appear"
	if err := runEdgeResetLogin([]string{"--file", "unused.json", "--token", secret}, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("reset-login accepted or exposed a token flag: %v", err)
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	err := runEdgeResetLogin([]string{"--file", "unused.json"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN is required") {
		t.Fatalf("reset-login without the environment token: %v", err)
	}
}

func TestEdgeDeploySkipSignatureNeedsABundle(t *testing.T) {
	err := runEdgeDeploy([]string{"--host", "panel.example.com", "--insecure-skip-signature"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "only available with --bundle") {
		t.Fatalf("flag without --bundle: %v", err)
	}
}

func TestEdgeDefaultVersionIsTheBinarysRelease(t *testing.T) {
	old := buildinfo.Version
	defer func() { buildinfo.Version = old }()
	for stamp, want := range map[string]string{"v0.1.4": "v0.1.4", "0.1.4": "v0.1.4", "0.1.0-dev": "", "0.1.0-abc1234": "", "": "", "v1.2": ""} {
		buildinfo.Version = stamp
		if got := edgeDefaultVersion(); got != want {
			t.Errorf("version stamp %q: default %q, want %q", stamp, got, want)
		}
	}
}

func TestEdgeDeployOfADevelopmentBuildNeedsAVersion(t *testing.T) {
	old := buildinfo.Version
	buildinfo.Version = "0.1.0-dev"
	defer func() { buildinfo.Version = old }()
	t.Setenv("CLOUDFLARE_API_TOKEN", "token-value-must-not-appear")
	t.Chdir(t.TempDir())
	err := runEdgeDeploy([]string{"--host", "panel.example.com"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--version vX.Y.Z or --bundle") || strings.Contains(err.Error(), "token-value") {
		t.Fatalf("development build without a version: %v", err)
	}
}

func TestEdgeRollbackIsAFlagAndNeedsTheKeyFile(t *testing.T) {
	var usage bytes.Buffer
	if err := runEdge([]string{"help"}, &usage); err != nil || !strings.Contains(usage.String(), "--rollback") {
		t.Fatalf("usage lacks --rollback: %v %q", err, usage.String())
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "token-value-must-not-appear")
	t.Chdir(t.TempDir())
	err := runEdgeDeploy([]string{"--host", "panel.example.com", "--rollback"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--rollback requires the local deployment file") || strings.Contains(err.Error(), "token-value") {
		t.Fatalf("rollback without a key file: %v", err)
	}
	err = runEdgeDeploy([]string{"--host", "panel.example.com", "--rollback", "--resume"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be used with") {
		t.Fatalf("rollback with --resume: %v", err)
	}
}
