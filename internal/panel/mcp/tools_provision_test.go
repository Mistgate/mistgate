package mcp

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
)

func TestGeneratedNodePasswordIsSentOnlyToRotatorAndNotInOutcome(t *testing.T) {
	var got string
	outcome, err := rotateGeneratedNodePassword(context.Background(), func(_ context.Context, password string) error {
		got = password
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Fatalf("generated password has unexpected format or length: %q", got)
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("generated password is not valid hex: %v", err)
	}
	if outcome.out.Code != "node_ssh_password_rotated" || !strings.Contains(outcome.text, "generated inside the panel") {
		t.Fatalf("MCP outcome does not describe the completed operation: %+v", outcome)
	}
	if strings.Contains(outcome.text, got) || strings.Contains(outcome.out.Code, got) {
		t.Fatalf("password appeared in MCP outcome: %+v", outcome)
	}
}
