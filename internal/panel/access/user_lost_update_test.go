package access

import (
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// An edit of a person's name races with another admin switching the person off: the switch-off that was reported done must
// still be there after the edit; an edit that read the row before the switch is refused (Aborted, stale_user), never
// saved over it.
func TestUpdateUserNeverSwitchesBackOnAPersonWhoWasSwitchedOff(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice", e.group("g1"), nil).User
	refused := 0
	for i := 0; i < 150; i++ {
		var wg sync.WaitGroup
		var editErr, offErr error
		start := make(chan struct{})
		name := "alice" + string(rune('a'+i%26))
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, editErr = e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, Name: &name}))
		}()
		go func() {
			defer wg.Done()
			<-start
			_, offErr = e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{u.Id}, Enabled: false}))
		}()
		close(start)
		wg.Wait()
		if offErr != nil {
			t.Fatalf("round %d: switch-off: %v", i, offErr)
		}
		if editErr != nil {
			wantCode(t, editErr, connect.CodeAborted)
			wantMsg(t, editErr, "aborted: stale_user")
			refused++
		}
		got := must(e.st.Access().User(e.ctx, u.Id))
		if !got.Disabled {
			t.Fatalf("round %d: the person was switched off and then back on by an edit of the name (edit error: %v)", i, editErr)
		}
		must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{u.Id}, Enabled: true})))
	}
	t.Logf("refused %d of 150 edits", refused)
}

// Two extensions of one person at once both count: the one that read the term end before the other wrote it reads again.
func TestExtendUsersConcurrentExtensionsBothCount(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice", e.group("g1"), nil).User
	for i := 0; i < 60; i++ {
		base := e.clock.AddDate(0, 0, 3).Unix()
		e.sql(`UPDATE user SET expires_at = ? WHERE id = ?`, base, u.Id)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for n, days := range []uint32{10, 20} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[n] = e.s.ExtendUsers(e.ctx, req(&adminv1.ExtendUsersRequest{UserIds: []string{u.Id}, Days: days}))
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
		}
		want := time.Unix(base, 0).UTC().AddDate(0, 0, 30).Unix()
		if got := must(e.st.Access().User(e.ctx, u.Id)).ExpiresAt.Unix(); got != want {
			t.Fatalf("round %d: term end %d, want %d (both extensions)", i, got, want)
		}
	}
}
