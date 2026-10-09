package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveEnrollmentTokenFromStdin(t *testing.T) {
	token := "one-time-token"
	got, err := resolveEnrollmentToken(strings.NewReader(token+"\n"), "", "", true)
	if err != nil || got != token {
		t.Fatalf("stdin token = %q, err %v", got, err)
	}
}

func TestResolveEnrollmentTokenRejectsConflictsAndMalformedInput(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		explicit    string
		environment string
		fromStdin   bool
	}{
		{name: "explicit token conflict", input: "stdin-token\n", explicit: "flag-token", fromStdin: true},
		{name: "environment token conflict", input: "stdin-token\n", environment: "env-token", fromStdin: true},
		{name: "oversized stdin token", input: strings.Repeat("a", maxEnrollmentTokenBytes+1), fromStdin: true},
		{name: "embedded newline", input: "one\ntwo", fromStdin: true},
		{name: "whitespace", explicit: "token with space"},
		{name: "nul", explicit: "token\x00suffix"},
		{name: "oversized explicit token", explicit: strings.Repeat("a", maxEnrollmentTokenBytes+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveEnrollmentToken(strings.NewReader(tc.input), tc.explicit, tc.environment, tc.fromStdin)
			if err == nil || got != "" {
				t.Fatalf("resolved malformed token = %q, err %v", got, err)
			}
		})
	}
}

func TestResolveEnrollmentTokenUsesEnvironmentOnlyWhenFlagIsEmpty(t *testing.T) {
	got, err := resolveEnrollmentToken(strings.NewReader("unused"), "", "env-token", false)
	if err != nil || got != "env-token" {
		t.Fatalf("environment token = %q, err %v", got, err)
	}
}

func TestEnrollLinkURLFlags(t *testing.T) {
	link := "wss://de1.example.com/" + strings.Repeat("p", 24) + "/"
	dir := filepath.Join(t.TempDir(), "s")
	for name, args := range map[string][]string{
		"with --panel":       {"enroll", "--link-url", link, "--panel", "p:1", "--ca-sha256", "zz", "--token", "t", "--state-dir", dir},
		"with --sni":         {"enroll", "--link-url", link, "--sni", "x.invalid", "--ca-sha256", "zz", "--token", "t", "--state-dir", dir},
		"without a pin":      {"enroll", "--link-url", link, "--token", "t", "--state-dir", dir},
		"without a token":    {"enroll", "--link-url", link, "--ca-sha256", "zz", "--state-dir", dir},
		"mTLS without --sni": {"enroll", "--panel", "p:1", "--ca-sha256", "zz", "--token", "t", "--state-dir", dir},
	} {
		if got := dispatch(args); got != 2 {
			t.Errorf("%s: exit %d, want 2", name, got)
		}
	}
	// A panel address in the environment is not a request to combine; the run gets as far as the bad pin.
	t.Setenv("MISTGATE_PANEL", "p:1")
	t.Setenv("MISTGATE_AGENT_SNI", "x.invalid")
	if got := dispatch([]string{"enroll", "--link-url", link, "--ca-sha256", "zz", "--token", "t", "--state-dir", dir}); got != 1 {
		t.Errorf("link enrol with MISTGATE_PANEL set: exit %d, want 1 (bad pin)", got)
	}
}
