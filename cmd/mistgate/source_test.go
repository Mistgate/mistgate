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
