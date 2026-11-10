//go:build !js

package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type networkTableStub struct{}

func (networkTableStub) Built() time.Time    { return time.Unix(1_800_000_000, 0).UTC() }
func (networkTableStub) IPv4RangeCount() int { return 4 }
func (networkTableStub) IPv6RangeCount() int { return 3 }
func (networkTableStub) ASNCount() int       { return 2 }

func TestNetworkSettingsFactsAndModeChanges(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var stepUps int
	var changed []uint32
	var callbackStoredModes []uint32
	var e *env
	e = newEnv(t, func(c *Config) {
		c.StepUp = func(context.Context) error { stepUps++; return nil }
		c.NetworkTable = func() NetworkTable { return networkTableStub{} }
		c.NetworkBundle = func() (string, string) { return "mistgate-asndb-data-any", sha }
		c.NetworkSettingsChanged = func(mode uint32) {
			changed = append(changed, mode)
			if e != nil {
				persisted, err := e.st.NetworkMode(context.Background())
				if err != nil {
					t.Errorf("read mode inside pre-write callback: %v", err)
				}
				callbackStoredModes = append(callbackStoredModes, persisted)
			}
		}
		c.Now = func() time.Time { return time.Unix(1_800_000_100, 0).UTC() }
	})
	ctx := context.Background()
	for _, row := range []struct{ id, name, state, caps, digest string }{
		{"nod_de1", "de1", "active", "nets/1", sha},
		{"nod_nl1", "nl1", "pending", "nets/1", ""},
		{"nod_fi1", "fi1", "retired", "nets/1", sha},
		{"nod_old", "old", "active", "links/1", ""},
	} {
		if _, err := e.st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at, agent_caps, networks_db_sha256)
			VALUES (?, ?, '', ?, 1, ?, ?)`, row.id, row.name, row.state, row.caps, row.digest); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.cl.GetNetworkSettings(ctx, connect.NewRequest(&opsv1.GetNetworkSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.Mode != 1 || got.Msg.NodesWithNets != 2 || got.Msg.NodesWithMatchingTable != 1 || got.Msg.TableBuiltUnix != 1_800_000_000 ||
		got.Msg.Ipv4Ranges != 4 || got.Msg.Ipv6Ranges != 3 || got.Msg.Asns != 2 {
		t.Fatalf("GetNetworkSettings = %+v", got.Msg)
	}

	if err := e.st.SaveNetworkMode(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.ExecContext(ctx, `INSERT INTO network_hour (node_id, hour_start, inbound_id, asn, attempts, unfinished, connects, broken, people)
		VALUES ('nod_de1', 1800000000, 'inb_de1', 64496, 1, 0, 1, 0, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.ExecContext(ctx, `INSERT INTO user_network (user_id, asn, connects, last_hour) VALUES ('usr_alice', 64496, 1, 1800000000)`); err != nil {
		t.Fatal(err)
	}
	changedToOne, err := e.cl.SetNetworkSettings(ctx, connect.NewRequest(&opsv1.SetNetworkSettingsRequest{Mode: 1}))
	if err != nil || changedToOne.Msg.Settings.Mode != 1 {
		t.Fatalf("SetNetworkSettings(1) = %+v, %v", changedToOne, err)
	}
	if count := queryInt(t, e.st, `SELECT COUNT(*) FROM user_network`); count != 0 {
		t.Fatalf("user_network after 2 -> 1 = %d, want deleted", count)
	}
	if count := queryInt(t, e.st, `SELECT COUNT(*) FROM network_hour`); count != 1 {
		t.Fatalf("network_hour after 2 -> 1 = %d, want kept", count)
	}
	if _, err := e.st.W.ExecContext(ctx, `INSERT INTO user_network (user_id, asn, connects, last_hour) VALUES ('usr_bob', 64497, 1, 1800000000)`); err != nil {
		t.Fatal(err)
	}
	turnedOff, err := e.cl.SetNetworkSettings(ctx, connect.NewRequest(&opsv1.SetNetworkSettingsRequest{Mode: 0}))
	if err != nil || turnedOff.Msg.Settings.Mode != 0 {
		t.Fatalf("SetNetworkSettings(0) = %+v, %v", turnedOff, err)
	}
	for _, table := range []string{"network_hour", "user_network"} {
		if count := queryInt(t, e.st, `SELECT COUNT(*) FROM `+table); count != 0 {
			t.Errorf("%s after off = %d, want deleted", table, count)
		}
	}
	if stepUps != 2 || len(changed) != 2 || changed[0] != 1 || changed[1] != 0 {
		t.Fatalf("step-ups=%d settings callbacks=%v, want 2 and [1 0]", stepUps, changed)
	}
	if len(callbackStoredModes) != 2 || callbackStoredModes[0] != 2 || callbackStoredModes[1] != 1 {
		t.Fatalf("stored modes inside settings callbacks = %v, want prior modes [2 1]", callbackStoredModes)
	}
	audit, err := e.st.ListAudit(ctx, "", 0, 10)
	if err != nil || len(audit) != 2 {
		t.Fatalf("settings audit = %+v, %v", audit, err)
	}
	for _, row := range audit {
		if row.Action != "network_settings_set" || !strings.HasPrefix(row.Params, `{"mode":"`) || strings.Contains(row.Params, "user") || strings.Contains(row.Params, "network_hour") {
			t.Errorf("network setting audit contains more than the mode: %+v", row)
		}
	}
	if _, err := e.cl.SetNetworkSettings(ctx, connect.NewRequest(&opsv1.SetNetworkSettingsRequest{Mode: 3})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("mode 3 error = %v, want INVALID_ARGUMENT", err)
	}
}

func queryInt(t *testing.T, st *store.Store, query string) int {
	t.Helper()
	var count int
	if err := st.R.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
