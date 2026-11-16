//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestTrustTunnelAppToggleMigration(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	a := st.Access()
	if err := a.CreateGroup(ctx, AccessGroup{ID: "grp_tt", Name: "group", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	u := AccessUser{
		ID: "usr_alice", Name: "alice", GroupID: "grp_tt", Status: "active", AppHapp: true, AppAmnezia: true,
		AllNodes: true, QuotaReset: "month", PeriodStart: now, DeviceLimit: 2,
		SubTokenHash: make([]byte, 32), SubTokenEnc: []byte("sealed"), CreatedAt: now,
	}
	if err := a.CreateUser(ctx, u, AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := a.User(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AppTrustTunnel {
		t.Fatal("migration default enabled the TrustTunnel app")
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE user SET app_trusttunnel = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	got, err = a.User(ctx, u.ID)
	if err != nil || !got.AppTrustTunnel {
		t.Fatalf("TrustTunnel toggle round trip = %v, %v", got.AppTrustTunnel, err)
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE user SET app_trusttunnel = 2 WHERE id = ?`, u.ID); err == nil {
		t.Fatal("TrustTunnel toggle accepted a value outside 0/1")
	}
}

func ttKeyAdd(user, tag string, limit int) TrustTunnelKeyAdd {
	return TrustTunnelKeyAdd{
		Device: AccessDevice{ID: "dev_tt_" + tag, UserID: user, Platform: "other", Model: "TrustTunnel key", Client: "trusttunnel"},
		Cred:   AccessCred{ID: "crd_tt_" + tag, Protocol: TrustTunnelProtocol, SecretEnc: []byte("sealed-" + tag), DataJSON: `{"user":"u","pass_sha256":"` + tag + `"}`},
		Limit:  limit,
	}
}

// The key is a device of the person's limit: it is refused when the places are taken, takes one when it is made, and gives it
// back when revoked. A second key is never made.
func TestTrustTunnelKeyTakesAndFreesAPlace(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	a := s.Access()
	if _, err := ensureHWID(ctx, s, "usr_l", 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := a.AddTrustTunnelKey(ctx, ttKeyAdd("usr_l", "a", 2), linkNow); err != nil {
		t.Fatalf("key with a place free: %v", err)
	}
	if got := liveDevices(t, s, "usr_l"); got != 2 {
		t.Fatalf("devices after the key = %d, want 2", got)
	}
	if err := a.AddTrustTunnelKey(ctx, ttKeyAdd("usr_l", "b", 5), linkNow); !errors.Is(err, ErrAccessExists) {
		t.Fatalf("a second key = %v, want ErrAccessExists", err)
	}
	var limit *AccessLimitError
	if _, err := ensureHWID(ctx, s, "usr_l", 2, 2); !errors.As(err, &limit) || limit.Used != 2 {
		t.Fatalf("a link device after the key = %v, want a limit error with 2 used", err)
	}
	if had, err := a.RevokeTrustTunnelKey(ctx, "usr_l", linkNow); err != nil || !had {
		t.Fatalf("revoke = %v, %v", had, err)
	}
	if _, err := ensureHWID(ctx, s, "usr_l", 2, 2); err != nil {
		t.Fatalf("a link device after the revoke: %v", err)
	}
	if err := a.AddTrustTunnelKey(ctx, ttKeyAdd("usr_l", "c", 2), linkNow); !errors.As(err, &limit) {
		t.Fatalf("a key with the places taken = %v, want a limit error", err)
	}
}

// The Mistgate app's devices hold trusttunnel credentials of their own; they are not the person's key and do not stop one.
func TestTrustTunnelKeyIgnoresAppDeviceCredentials(t *testing.T) {
	ctx := context.Background()
	s := linkFixture(t, "usr_l")
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_l", "app", hwidOf("usr_l", 1)), 5, linkNow, func(string, map[string]bool) ([]AccessCred, error) {
		return []AccessCred{{ID: "crd_app_tt", UserID: "usr_l", Protocol: TrustTunnelProtocol, SecretEnc: []byte("sealed"), DataJSON: `{}`}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Access().TrustTunnelKey(ctx, "usr_l"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an app device's credential was taken for the key: %v", err)
	}
	if err := s.Access().AddTrustTunnelKey(ctx, ttKeyAdd("usr_l", "a", 5), linkNow); err != nil {
		t.Fatalf("key beside an app device's credential: %v", err)
	}
	if dev, cred, err := s.Access().TrustTunnelKey(ctx, "usr_l"); err != nil || dev.ID != "dev_tt_a" || cred.ID != "crd_tt_a" {
		t.Fatalf("key = %+v %+v, %v; want the key device's own", dev, cred, err)
	}
}

// One place left, a key and a link device racing for it: exactly one gets it, never both (the guard counts where it writes).
func TestTrustTunnelKeyAndLinkDeviceRaceForTheLastPlace(t *testing.T) {
	ctx := context.Background()
	for round := range 12 {
		user := fmt.Sprintf("usr_r%02d", round)
		s := linkFixture(t, user)
		if _, err := ensureHWID(ctx, s, user, 1, 2); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			errs[0] = s.Access().AddTrustTunnelKey(ctx, ttKeyAdd(user, "k", 2), linkNow)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, errs[1] = ensureHWID(ctx, s, user, 2, 2)
		}()
		close(start)
		wg.Wait()
		var limit *AccessLimitError
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.As(err, &limit):
				t.Fatalf("round %d: %v, want success or a limit error", round, err)
			}
		}
		if got := liveDevices(t, s, user); won != 1 || got != 2 {
			t.Fatalf("round %d: %d writers won, %d live devices; want one winner and the limit of 2 kept (%v)", round, won, got, errs)
		}
	}
}

// CreateUser stores the device it is given as it is: the shared device of a link stays a link device (CreateUserWithKeys must not
// turn it into a key).
func TestCreateUserKeepsTheDeviceAsGiven(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	rewriteAccessGroup(t, s, "grp_x", "x")
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	u := AccessUser{ID: "usr_x", Name: "x", GroupID: "grp_x", Status: "active", AppHapp: true, AppAmnezia: true, AllNodes: true, QuotaReset: "month",
		PeriodStart: now, DeviceLimit: 2, SubTokenHash: []byte("hash"), SubTokenEnc: []byte("sealed"), CreatedAt: now}
	dev := AccessDevice{ID: "dev_x", UserID: u.ID, Implicit: true, CreatedAt: now}
	cred := AccessCred{ID: "crd_x", DeviceID: dev.ID, UserID: u.ID, Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: now}
	if err := s.Access().CreateUser(ctx, u, dev, []AccessCred{cred}); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := s.R.QueryRowContext(ctx, `SELECT kind FROM device WHERE id = ?`, dev.ID).Scan(&kind); err != nil || kind != DeviceKindLink {
		t.Fatalf("kind = %q, %v; want %q", kind, err, DeviceKindLink)
	}
}
