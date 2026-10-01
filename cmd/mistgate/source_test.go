package main

import (
	"os"
	"testing"
)

// --source-url defaults to the project's repository; MISTGATE_SOURCE_URL replaces it, and set to "" it hides the link.
func TestSourceURLDefault(t *testing.T) {
	t.Setenv("MISTGATE_SOURCE_URL", "")
	if got := sourceURLDefault(); got != "" {
		t.Errorf("MISTGATE_SOURCE_URL=\"\": default %q, want empty", got)
	}
	t.Setenv("MISTGATE_SOURCE_URL", "https://example.com/fork/mistgate")
	if got := sourceURLDefault(); got != "https://example.com/fork/mistgate" {
		t.Errorf("MISTGATE_SOURCE_URL set: default %q", got)
	}
	os.Unsetenv("MISTGATE_SOURCE_URL") // t.Setenv restores the outer value afterwards
	if got := sourceURLDefault(); got != "https://github.com/Mistgate/mistgate" {
		t.Errorf("unset: default %q, want the project's repository", got)
	}
}

// MISTGATE_ACME_HTTP set to "" turns the port-80 listener off, as --acme-http= does; unset, it is :80.
func TestACMEHTTPEnvEmptyTurnsItOff(t *testing.T) {
	t.Setenv("MISTGATE_ACME_HTTP", "")
	if got := envSetOr("MISTGATE_ACME_HTTP", ":80"); got != "" {
		t.Errorf("MISTGATE_ACME_HTTP=\"\": %q, want empty (off)", got)
	}
	t.Setenv("MISTGATE_ACME_HTTP", "127.0.0.1:8088")
	if got := envSetOr("MISTGATE_ACME_HTTP", ":80"); got != "127.0.0.1:8088" {
		t.Errorf("MISTGATE_ACME_HTTP set: %q", got)
	}
	os.Unsetenv("MISTGATE_ACME_HTTP")
	if got := envSetOr("MISTGATE_ACME_HTTP", ":80"); got != ":80" {
		t.Errorf("unset: %q, want :80", got)
	}
}
