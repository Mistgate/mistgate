//go:build !js

package store

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

// The Mistgate app switch is stored as "off", so a person that existed before the column came keeps what they had: the app on.
func TestMistgateAppMigrationKeepsExistingPeopleOn(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	rewriteAccessGroup(t, st, "grp_old", "Old")
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, st.W, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 127); err != nil {
		t.Fatalf("down to the schema before the switch: %v", err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at)
		VALUES ('usr_old', 'alice', 'grp_old', 1, X'01', X'02', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 128); err != nil {
		t.Fatalf("apply the switch: %v", err)
	}
	u, err := st.Access().User(ctx, "usr_old")
	if err != nil {
		t.Fatal(err)
	}
	if u.AppMistgateOff {
		t.Fatal("a person from before the switch has the app off")
	}
	if got, err := st.Access().ClientAPIStats(ctx); err != nil || got.People != 1 || got.PeopleOn != 1 {
		t.Fatalf("stats after the migration = %+v, %v; want one person, the app on", got, err)
	}
}

// The switch round-trips through the user row, and the summary counts the people who have the app on (the link on too) and the
// devices that hold a key and are not removed.
func TestMistgateAppSwitchAndStats(t *testing.T) {
	ctx := context.Background()
	s := clientDeviceKeyFixture(t) // alice, on
	rewriteAccessUser(t, s, "usr_bob", "bob", "grp_link")
	rewriteAccessUser(t, s, "usr_cara", "cara", "grp_link")

	bob, err := s.Access().User(ctx, "usr_bob")
	if err != nil {
		t.Fatal(err)
	}
	bob.AppMistgateOff = true
	if err := s.Access().UpdateUser(ctx, bob, false); err != nil {
		t.Fatal(err)
	}
	if back, _ := s.Access().User(ctx, "usr_bob"); !back.AppMistgateOff {
		t.Fatal("the switch was not stored")
	}
	// cara has the subscription link off (and AmneziaVPN on): the app rides on the link, so she is not counted
	cara, _ := s.Access().User(ctx, "usr_cara")
	cara.AppHapp = false
	if err := s.Access().UpdateUser(ctx, cara, false); err != nil {
		t.Fatal(err)
	}

	hw := func(n string) []byte { return HWIDHash("usr_alice", n+"aaaaaaaaaaaaaaaa") }
	live, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "live", hw("1")), 10, linkNow, linkCreds("live", "usr_alice"))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := s.Access().EnrolLinkDevice(ctx, enrolDevice("usr_alice", "gone", hw("2")), 10, linkNow, linkCreds("gone", "usr_alice"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Access().RevokeEnrolledLinkDevice(ctx, gone.Device, linkNow); err != nil {
		t.Fatal(err)
	}
	// a device without a key (an app on the plain link) is not a Mistgate app device
	if _, err := s.Access().EnsureLinkDevice(ctx, linkDevice("usr_alice", "plain", HWIDHash("usr_alice", "pppppppppppppppp")), 10, linkNow, linkCreds("plain", "usr_alice")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Access().ClientAPIStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != (ClientAPIStats{People: 3, PeopleOn: 1, Devices: 1}) {
		t.Fatalf("stats = %+v, want 3 people, 1 with the app on, 1 device with a key", got)
	}
	_ = live
}
