//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestSyncInboundUDPPortsOwnsExactUFWRules(t *testing.T) {
	h, calls := testHost(t)
	owned := map[string]bool{"20000:30000": true}
	var untagged = []string{"ufw allow 443/udp"}
	h.run = func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{name: name, args: strings.Join(args, " ")})
		if name == "firewall-cmd" {
			return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		if name != "ufw" {
			return nil, errors.New("unexpected command")
		}
		switch strings.Join(args, " ") {
		case "status":
			return []byte("Status: active\n"), nil
		case "show added":
			lines := append([]string{"Added user rules:"}, untagged...)
			for expr := range owned {
				lines = append(lines, fmt.Sprintf("ufw allow %s/udp comment '%s%s'", expr, ufwInboundCommentPrefix, expr))
			}
			return []byte(strings.Join(lines, "\n")), nil
		default:
			if len(args) == 5 && args[0] == "delete" && args[1] == "allow" && args[3] == "comment" {
				expr := strings.TrimSuffix(args[2], "/udp")
				delete(owned, expr)
				return []byte("Rule deleted"), nil
			}
			if len(args) == 4 && args[0] == "allow" && args[2] == "comment" {
				expr := strings.TrimSuffix(args[1], "/udp")
				owned[expr] = true
				return []byte("Rule added"), nil
			}
			return nil, fmt.Errorf("unexpected ufw args %q", args)
		}
	}

	want := []UDPInboundPort{{Port: 51820}, {From: 20000, To: 20010}}
	if err := h.SyncInboundUDPPorts(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if owned["20000:30000"] || !owned["51820"] || !owned["20000:20010"] {
		t.Fatalf("owned UFW rules after sync: %v", owned)
	}
	if len(untagged) != 1 || untagged[0] != "ufw allow 443/udp" {
		t.Fatalf("untagged user rule changed: %v", untagged)
	}
	var mutations int
	for _, c := range *calls {
		if c.name == "ufw" && c.args != "status" && c.args != "show added" {
			mutations++
			if strings.Contains(c.args, "10000:60000") {
				t.Fatalf("wide UDP allow issued: %+v", c)
			}
		}
	}
	if mutations != 3 { // remove the old range, then add the exact listener and current hop range
		t.Fatalf("mutations=%d calls=%+v", mutations, *calls)
	}

	before := len(*calls)
	if err := h.SyncInboundUDPPorts(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if got := len(*calls) - before; got != 3 { // status + inventory + firewalld check; no duplicate allow rules
		t.Fatalf("second sync changed UFW again: %+v", (*calls)[before:])
	}

	if err := h.SyncInboundUDPPorts(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(owned) != 0 {
		t.Fatalf("removal left Mistgate-owned rules: %v", owned)
	}
	if len(untagged) != 1 {
		t.Fatalf("removal changed user rules: %v", untagged)
	}
}

func TestSyncInboundUDPPortsLeavesInactiveFirewallAlone(t *testing.T) {
	h, calls := testHost(t)
	h.run = func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{name: name, args: strings.Join(args, " ")})
		if name == "firewall-cmd" {
			return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		if name == "ufw" && strings.Join(args, " ") == "status" {
			return []byte("Status: inactive\n"), nil
		}
		return nil, errors.New("unexpected command")
	}
	if err := h.SyncInboundUDPPorts(context.Background(), []UDPInboundPort{{Port: 443}}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0].args != "status" || (*calls)[1].name != "firewall-cmd" || (*calls)[1].args != "--state" {
		t.Fatalf("inactive UFW was changed or inspected further: %+v", *calls)
	}
}

func TestSyncInboundUDPPortsReportsFirewalldWithoutMutatingIt(t *testing.T) {
	h, calls := testHost(t)
	h.run = func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{name: name, args: strings.Join(args, " ")})
		switch {
		case name == "ufw" && strings.Join(args, " ") == "status":
			return []byte("Status: inactive\n"), nil
		case name == "firewall-cmd" && strings.Join(args, " ") == "--state":
			return []byte("running\n"), nil
		default:
			return nil, errors.New("unexpected mutating command")
		}
	}
	err := h.SyncInboundUDPPorts(context.Background(), []UDPInboundPort{{Port: 443}, {From: 20000, To: 20010}})
	if err == nil || !strings.Contains(err.Error(), "firewalld is active") || !strings.Contains(err.Error(), "443/udp") || !strings.Contains(err.Error(), "20000-20010/udp") {
		t.Fatalf("firewalld result: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("firewalld was mutated: %+v", *calls)
	}
}

func TestSyncInboundUDPPortsRejectsInvalidRangesBeforeCommands(t *testing.T) {
	h, calls := testHost(t)
	err := h.SyncInboundUDPPorts(context.Background(), []UDPInboundPort{{From: 10000, To: 60000}})
	if err == nil {
		t.Fatal("wide hop range accepted")
	}
	if len(*calls) != 0 {
		t.Fatalf("commands ran for invalid rules: %+v", *calls)
	}
}
