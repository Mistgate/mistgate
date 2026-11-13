package auth

import (
	"context"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Removing a passkey ends the admin's other sessions (a session opened with the removed key must not outlive it), like
// ChangePassword and the authenticator re-bind; the session that removed it stays.
func TestRemovePasskeyEndsOtherSessions(t *testing.T) {
	s, st, _ := newTestService(t)
	admin, ownerCookie, _ := passkeyAdmin(t, s, st)
	ctx := context.Background()

	if err := st.AddPasskey(ctx, store.Passkey{ID: "pk_spare", AdminID: admin.ID, CredentialID: []byte("spare"), PublicKey: []byte("k")}, s.now()); err != nil {
		t.Fatal(err)
	}
	other, err := s.newSession(ctx, admin.ID, "198.51.100.7", "other-browser")
	if err != nil {
		t.Fatal(err)
	}
	pks, err := st.PasskeysByAdmin(ctx, admin.ID)
	if err != nil || len(pks) != 2 {
		t.Fatalf("passkeys %d: %v", len(pks), err)
	}
	var removed string
	for _, p := range pks {
		if p.ID != "pk_spare" {
			removed = p.ID
		}
	}
	if _, err := s.RemovePasskey(authed(t, s, ownerCookie), cookieReq(&adminv1.RemovePasskeyRequest{Id: removed}, ownerCookie)); err != nil {
		t.Fatalf("RemovePasskey: %v", err)
	}
	if _, _, err := s.resolveSession(ctx, other.Value); err == nil {
		t.Error("another session of the admin survived the removal of a passkey")
	}
	if _, _, err := s.resolveSession(ctx, ownerCookie); err != nil {
		t.Errorf("the session that removed the passkey ended: %v", err)
	}
}
