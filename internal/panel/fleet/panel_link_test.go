package fleet

import (
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// No address set (every panel today): no agent, capable or not, is sent a link address, and no settings signature
// changes. A set address reaches only agents with panel-link/1.
func TestPanelLinkSettingsAndSignature(t *testing.T) {
	node := store.NodeRow{DialTimeoutS: 30, CountryCode: "DE", AwgBackend: "userspace"}
	link := "wss://panel.example.com/" + strings.Repeat("n", 24) + "/"
	capable := []string{agentcap.PanelLink}

	f := &Fleet{}
	base := settingsSig(f.nodeSettings(node, nil))
	for _, caps := range [][]string{nil, capable} {
		s := f.nodeSettings(node, caps)
		if s.PanelLinkUrl != "" || settingsSig(s) != base {
			t.Fatalf("never set, caps %v: link %q, signature %q want %q", caps, s.PanelLinkUrl, settingsSig(s), base)
		}
	}
	f.SetPanelLink("")
	for _, caps := range [][]string{nil, capable} {
		s := f.nodeSettings(node, caps)
		if s.PanelLinkUrl != "" || settingsSig(s) != base {
			t.Fatalf("set to empty, caps %v: link %q, signature %q want %q", caps, s.PanelLinkUrl, settingsSig(s), base)
		}
	}

	f.SetPanelLink(link)
	s := f.nodeSettings(node, capable)
	if s.PanelLinkUrl != link {
		t.Fatalf("panel-link/1 agent: link %q, want %q", s.PanelLinkUrl, link)
	}
	if settingsSig(s) == base {
		t.Fatal("a set link address did not change the panel-link/1 agent's signature")
	}
	old := f.nodeSettings(node, nil)
	if old.PanelLinkUrl != "" || settingsSig(old) != base {
		t.Fatalf("agent without panel-link/1: link %q, signature %q want %q", old.PanelLinkUrl, settingsSig(old), base)
	}
}
