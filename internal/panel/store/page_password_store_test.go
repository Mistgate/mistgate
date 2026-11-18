package store

import (
	"context"
	"errors"
	"testing"
)

// The state of a person's page password (a generation and "removed") on a store: one statement for each change, the one-row read that
// the public handler makes to confirm a link, and a new link that starts the generation again. Run on SQLite (below) and on D1
// (d1_store_test.go).

func pagePasswordStoreProof(t *testing.T, open func(*testing.T) *Store) {
	ctx := context.Background()
	w := newEGWorld(t, open(t))
	alice := w.user("alice", "family")
	hash := []byte("tok-alice")
	state := func() PageState {
		t.Helper()
		st, found, err := w.a.PageStateByTokenHash(ctx, hash)
		if err != nil || !found {
			t.Fatalf("state: found %v, %v", found, err)
		}
		return st
	}

	// a person nobody touched: generation 0, the password asked for
	if alice.PagePassGen != 0 || alice.PageOpen || state() != (PageState{}) {
		t.Fatalf("the starting state: %+v %+v", alice, state())
	}
	if _, found, err := w.a.PageStateByTokenHash(ctx, []byte("no-such-token")); err != nil || found {
		t.Errorf("an unknown token: found %v, %v", found, err)
	}

	do := func(action string, wantChanged bool, want PageState) {
		t.Helper()
		changed, err := w.a.SetPagePassword(ctx, alice.ID, action)
		if err != nil || changed != wantChanged || state() != want {
			t.Fatalf("%s: changed %v (want %v), err %v, state %+v (want %+v)", action, changed, wantChanged, err, state(), want)
		}
		if got, err := w.a.User(ctx, alice.ID); err != nil || got.PagePassGen != want.Gen || got.PageOpen != want.Open {
			t.Fatalf("%s: the user reads %+v, %v", action, got, err)
		}
	}
	do(PagePassChange, true, PageState{Gen: 1})
	do(PagePassChange, true, PageState{Gen: 2})
	do(PagePassRestore, false, PageState{Gen: 2}) // nothing to bring back
	do(PagePassRemove, true, PageState{Gen: 2, Open: true})
	do(PagePassRemove, false, PageState{Gen: 2, Open: true}) // a repeat does nothing
	do(PagePassChange, false, PageState{Gen: 2, Open: true}) // a removed password cannot be changed
	do(PagePassRestore, true, PageState{Gen: 3})             // asked again, and a new one: the generation moved on

	if _, err := w.a.SetPagePassword(ctx, "usr_nobody", PagePassChange); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown person: %v", err)
	}
	if _, err := w.a.SetPagePassword(ctx, alice.ID, "show"); err == nil {
		t.Error("an unknown action was taken")
	}

	// two changes at once cannot lose one
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := w.a.SetPagePassword(ctx, alice.ID, PagePassChange); done <- err }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if st := state(); st.Gen != 5 {
		t.Errorf("two changes at once: generation %d, want 5", st.Gen)
	}

	// a new link starts the generation again and leaves "removed" as it was
	do(PagePassRemove, true, PageState{Gen: 5, Open: true})
	if err := w.a.SetSubToken(ctx, alice.ID, []byte("tok-alice-2"), []byte("sealed2")); err != nil {
		t.Fatal(err)
	}
	hash = []byte("tok-alice-2")
	if st := state(); st != (PageState{Gen: 0, Open: true}) {
		t.Errorf("after a new link: %+v", st)
	}
	if _, found, _ := w.a.PageStateByTokenHash(ctx, []byte("tok-alice")); found {
		t.Error("the old link still finds the person")
	}
}
