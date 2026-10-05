//go:build linux

package hostctl

import (
	"context"
	"slices"
	"testing"
)

func TestCleanupRemovesOnlyTheTaggedProvisionRules(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "yes")
	fw := &fakeUFW{t: t, rules: []string{
		"allow 22/tcp",
		"allow 80/tcp", // the owner's own: untagged
		"allow 443/tcp comment 'mistgate-node-provision-v1'", // the install's
		"allow 443/udp comment 'mistgate-node-provision-v1'",
	}}
	h.run = fw.run // fakeUFW fails the test if ufw runs inside the sandbox or without a timeout
	if err := h.removeProvisionUFWRules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"allow 22/tcp", "allow 80/tcp"}; !slices.Equal(fw.rules, want) {
		t.Fatalf("UFW rules after cleanup: %q, want %q", fw.rules, want)
	}
}

func TestCleanupLeavesAnInactiveUFWAlone(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "no")
	fw := &fakeUFW{t: t, rules: []string{"allow 443/tcp comment 'mistgate-node-provision-v1'"}}
	h.run = fw.run
	if err := h.removeProvisionUFWRules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fw.calls) != 0 || len(fw.rules) != 1 {
		t.Fatalf("UFW was run while off: calls %q, rules %q", fw.calls, fw.rules)
	}
}
