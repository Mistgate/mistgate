package subsettings

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func addUser(t *testing.T, st *store.Store, id string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Access().CreateGroup(ctx, store.AccessGroup{ID: "grp_" + id, Name: "g-" + id, CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	u := store.AccessUser{ID: id, Name: "n-" + id, GroupID: "grp_" + id, Status: "active", AppHapp: true, AppAmnezia: true, AllNodes: true,
		QuotaReset: "month", PeriodStart: time.Unix(1, 0), DeviceLimit: 5, SubTokenHash: []byte(id), SubTokenEnc: []byte("x"), CreatedAt: time.Unix(1, 0)}
	if err := st.Access().CreateUser(ctx, u, store.AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
}

func storedMode(t *testing.T, st *store.Store) string {
	t.Helper()
	v, err := st.Setting(context.Background(), store.SettingLinkDeviceLimit)
	if err != nil {
		return ""
	}
	return v
}

// A panel with people counts only, so an upgrade cuts nobody off; an empty panel refuses from the start. The setting is its own
// marker: what the owner chose later is never put back.
func TestMigrateLinkDeviceLimit(t *testing.T) {
	ctx := context.Background()

	empty := openStore(t)
	if err := MigrateLinkDeviceLimit(ctx, empty); err != nil || storedMode(t, empty) != store.LinkModeEnforce {
		t.Fatalf("an empty panel: %q, %v; want enforce", storedMode(t, empty), err)
	}

	used := openStore(t)
	addUser(t, used, "usr_a")
	if err := MigrateLinkDeviceLimit(ctx, used); err != nil || storedMode(t, used) != store.LinkModeTrack {
		t.Fatalf("a panel with users: %q, %v; want track", storedMode(t, used), err)
	}
	if s, err := Load(ctx, used); err != nil || s.GetLinkDeviceLimit() != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_TRACK {
		t.Fatalf("Load = %v, %v", s.GetLinkDeviceLimit(), err)
	}

	// The owner's choice stays, and so does a mode that was there before the migration ran.
	if err := used.SetSettings(ctx, map[string]string{store.SettingLinkDeviceLimit: store.LinkModeStrict}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLinkDeviceLimit(ctx, used); err != nil || storedMode(t, used) != store.LinkModeStrict {
		t.Fatalf("a second run: %q, %v; want strict kept", storedMode(t, used), err)
	}
	chosen := openStore(t)
	addUser(t, chosen, "usr_a")
	if err := chosen.SetSettings(ctx, map[string]string{store.SettingLinkDeviceLimit: store.LinkModeEnforce}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLinkDeviceLimit(ctx, chosen); err != nil || storedMode(t, chosen) != store.LinkModeEnforce {
		t.Fatalf("a mode that was already set: %q, %v", storedMode(t, chosen), err)
	}

	// Both editions start through NewCache.
	viaCache := openStore(t)
	NewCache(viaCache, nil)
	if storedMode(t, viaCache) != store.LinkModeEnforce {
		t.Fatalf("NewCache did not migrate: %q", storedMode(t, viaCache))
	}
}

// The mode lives in a setting of its own: the document keeps the live defaults of a panel that never saved one, and a save
// that sends no mode leaves the stored one alone.
func TestLinkModeIsSavedApartFromTheDocument(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	addUser(t, st, "usr_a")
	c := NewCache(st, nil)
	if got := c.Get(ctx).GetLinkDeviceLimit(); got != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_TRACK {
		t.Fatalf("mode of a panel with users = %v", got)
	}

	in := Defaults()
	in.LinkDeviceLimit = adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE.Enum()
	out, revoked, err := c.UpdateRevoking(ctx, in)
	if err != nil || revoked != 0 || out.GetLinkDeviceLimit() != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE || storedMode(t, st) != store.LinkModeEnforce {
		t.Fatalf("saving ENFORCE: %v, revoked %d, stored %q, %v", out.GetLinkDeviceLimit(), revoked, storedMode(t, st), err)
	}
	if doc, _ := st.Setting(ctx, Key); strings.Contains(doc, "link_device_limit") {
		t.Errorf("the mode is in the document: %s", doc)
	}
	if in.GetLinkDeviceLimit() != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE {
		t.Error("the input was changed")
	}

	in.LinkDeviceLimit = nil
	in.Title = "Mine"
	out, _, err = c.UpdateRevoking(ctx, in)
	if err != nil || out.GetLinkDeviceLimit() != adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE || storedMode(t, st) != store.LinkModeEnforce {
		t.Fatalf("a save without a mode: %v, stored %q, %v", out.GetLinkDeviceLimit(), storedMode(t, st), err)
	}
	if s, _ := Load(ctx, st); !proto.Equal(s, out) || !proto.Equal(c.Get(ctx), out) {
		t.Errorf("stored %v, cached %v, saved %v", s, c.Get(ctx), out)
	}

	// An explicit UNSPECIFIED is "not given" as well; an unknown value is refused and changes nothing.
	in.LinkDeviceLimit = adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_UNSPECIFIED.Enum()
	if _, err := c.Update(ctx, in); err != nil || storedMode(t, st) != store.LinkModeEnforce {
		t.Fatalf("UNSPECIFIED: stored %q, %v", storedMode(t, st), err)
	}
	bad := adminv1.LinkDeviceLimit(42)
	in.LinkDeviceLimit = &bad
	if _, err := c.Update(ctx, in); err == nil || storedMode(t, st) != store.LinkModeEnforce {
		t.Fatalf("an unknown mode: stored %q, %v", storedMode(t, st), err)
	}
}
