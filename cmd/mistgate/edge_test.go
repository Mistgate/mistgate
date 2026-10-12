package main

import (
	"bytes"
	"strings"
	"testing"
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
