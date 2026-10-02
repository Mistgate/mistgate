package main

import (
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
