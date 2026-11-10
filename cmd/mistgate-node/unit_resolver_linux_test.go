package main

import (
	"strings"
	"testing"
)

func TestRenderedUnitKeepsGenerationWithoutResolverWriteGrant(t *testing.T) {
	unit, err := renderUnit("/usr/local/bin/mistgate-node", "/var/lib/mistgate-node", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, "ReadWritePaths=/var/lib/mistgate-node /usr/local/bin /etc/sysctl.d /etc/systemd/journald.conf.d\n") {
		t.Fatalf("unit write paths changed unexpectedly:\n%s", unit)
	}
	if !strings.Contains(unit, "Environment=MISTGATE_UNIT_GEN=3\n") {
		t.Fatal("the unit generation changed")
	}
	if strings.Contains(unit, "/etc/systemd/resolved.conf.d") {
		t.Fatal("the resolver drop-in path was granted directly to the service")
	}
}
