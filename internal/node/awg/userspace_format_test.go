package awg

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownInitiationLogFormatMatchesPinnedAmneziawg(t *testing.T) {
	const (
		modulePath    = "github.com/amnezia-vpn/amneziawg-go/v3"
		moduleVersion = "v3.1.20260828"
	)
	if !isUnknownInitiationLog(unknownInitiationLogFormat) || isUnknownInitiationLog(unknownInitiationLogFormat+" extra") {
		t.Fatal("unknown initiation matcher must compare the complete format string")
	}

	out, err := exec.Command("go", "list", "-m", "-f={{.Dir}}\t{{.Version}}", modulePath).Output()
	if err != nil {
		t.Fatalf("locate pinned amneziawg-go module: %v", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(parts) != 2 || parts[1] != moduleVersion {
		t.Fatalf("amneziawg-go module = %q, want %s", strings.TrimSpace(string(out)), moduleVersion)
	}
	source, err := os.ReadFile(filepath.Join(parts[0], "device", "receive.go"))
	if err != nil {
		t.Fatalf("read pinned receive.go: %v", err)
	}
	want := `Verbosef("` + unknownInitiationLogFormat + `",`
	if !strings.Contains(string(source), want) {
		t.Fatalf("pinned amneziawg-go %s no longer logs %q using the expected format", moduleVersion, unknownInitiationLogFormat)
	}
}

func TestUserspaceErrorLogMessageKeepsTemplateAndFormatsLiveText(t *testing.T) {
	message, template := userspaceErrorLogMessage("send to %s failed: %v", "203.0.113.7:51820", "closed")
	if want := "amneziawg-go: send to %s failed: %v"; template != want {
		t.Fatalf("template = %q, want %q", template, want)
	}
	if want := "amneziawg-go: send to 203.0.113.7:51820 failed: closed"; message != want {
		t.Fatalf("message = %q, want %q", message, want)
	}
}
