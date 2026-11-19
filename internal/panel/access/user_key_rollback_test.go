package access

import (
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// UpdateUser makes the key of an app it switches on before it writes the person. A write that is refused (the name is taken, the
// person changed meanwhile) or a key that is refused (the device limit) leaves no key behind: the person has the app off, so a
// key would be held by nobody and sent to the nodes.
func TestUpdateUserRefusedWriteTakesBackTheTelegramKey(t *testing.T) {
	e := newEnv(t)
	g := e.group("g1")
	alice := e.user("alice", g, nil).User
	e.user("bob", g, nil)
	taken := "bob"
	apps := &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: true}
	_, err := e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: alice.Id, Name: &taken, Apps: apps}))
	wantMsg(t, err, "already_exists: name_taken")
	if got := must(e.st.Access().User(e.ctx, alice.Id)); got.AppTelegram {
		t.Fatal("the refused update switched Telegram on")
	}
	if _, _, err := e.st.Access().TelegramKey(e.ctx, alice.Id); err != store.ErrNotFound {
		t.Fatalf("the refused update left a Telegram key: %v", err)
	}
	// The same update with a free name keeps the key it made.
	free := "alice2"
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: alice.Id, Name: &free, Apps: apps})))
	if _, _, err := e.st.Access().TelegramKey(e.ctx, alice.Id); err != nil {
		t.Fatalf("the update that was written has no Telegram key: %v", err)
	}
}

// A key that is refused takes back the one made before it in the same update.
func TestUpdateUserRefusedTrustTunnelKeyTakesBackTheTelegramKey(t *testing.T) {
	r := newTrustTunnelKeyRig(t, 1, "acme_domain")
	e := r.e
	e.sql(`UPDATE user SET app_trusttunnel = 0 WHERE id = ?`, r.userID)
	if _, err := e.s.Subscription(e.ctx, r.token); err != nil { // the one device of the limit
		t.Fatal(err)
	}
	apps := &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: true, TrustTunnel: new(true)}
	_, err := e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: r.userID, Apps: apps}))
	if err == nil || !strings.HasPrefix(err.Error(), "failed_precondition: device_limit: 1/1") {
		t.Fatalf("update past the device limit = %v, want device_limit: 1/1", err)
	}
	if got := r.user(); got.AppTelegram || got.AppTrustTunnel {
		t.Fatalf("the refused update switched an app on: %+v", got)
	}
	if _, _, err := e.st.Access().TelegramKey(e.ctx, r.userID); err != store.ErrNotFound {
		t.Fatalf("the refused update left a Telegram key: %v", err)
	}
}
