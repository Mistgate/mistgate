package access

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestTokenIndexFollowsCreateRotateAndDelete(t *testing.T) {
	f := newFixture(t)
	e := f.e
	ctx := e.ctx
	// Load the index while it holds nobody, so everything below has to come from the hooks and not from a reload.
	if e.s.KnownSubscriptionToken(ctx, strings.Repeat("z", 43)) {
		t.Fatal("a token nobody has is known")
	}
	r := e.userBare("alice", f.group, nil)
	tok := e.tokenOf(r.User.Id)
	if !e.s.KnownSubscriptionToken(ctx, tok) {
		t.Fatal("the token of a new user is not known at once")
	}

	rot := must(e.s.GetSubscriptionLink(ctx, req(&adminv1.GetSubscriptionLinkRequest{UserId: r.User.Id, Rotate: true}))).Msg.Url
	next := rot[strings.LastIndex(rot, "/")+1:]
	if !e.s.KnownSubscriptionToken(ctx, next) {
		t.Fatal("the new link after a rotate is not known at once")
	}
	if e.s.KnownSubscriptionToken(ctx, tok) {
		t.Fatal("the rotated-out link is still known")
	}

	// Disabling a user keeps the link (the page still answers it); deleting one makes it a stranger once it is looked up.
	must(e.s.SetUsersEnabled(ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{r.User.Id}, Enabled: false})))
	if !e.s.KnownSubscriptionToken(ctx, next) {
		t.Fatal("a disabled user's link is not known")
	}
	must(e.s.DeleteUsers(ctx, req(&adminv1.DeleteUsersRequest{UserIds: []string{r.User.Id}})))
	if _, err := e.s.Subscription(ctx, next); err != ErrUnknownToken {
		t.Fatalf("deleted user's link: %v", err)
	}
	if e.s.KnownSubscriptionToken(ctx, next) {
		t.Fatal("a deleted user's link is known after its lookup")
	}
}

// A token made behind the process's back (another panel on the database, a restore) is found by a reload, and reloads are
// rare however many tokens ask, so a stream of guesses cannot make the database read the users again and again.
func TestTokenIndexReloadsRarelyAndRetriesAfterAFailure(t *testing.T) {
	f := newFixture(t)
	e := f.e
	ctx := e.ctx
	if e.s.KnownSubscriptionToken(ctx, strings.Repeat("y", 43)) { // the first load
		t.Fatal("a token nobody has is known")
	}
	r := e.userBare("outside", f.group, nil)
	const outside = "outside-token-0000000000000000000000000"
	if err := e.st.Access().SetSubToken(ctx, r.User.Id, hashOf(outside), []byte{0}); err != nil { // not through the service
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if e.s.KnownSubscriptionToken(ctx, outside) {
			t.Fatal("the user made behind the process's back was found before the refresh interval")
		}
	}
	e.clock = e.clock.Add(tokenIndexRefresh + time.Second)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e.s.KnownSubscriptionToken(cancelled, outside) {
		t.Fatal("a load that failed found the token")
	}
	if e.s.KnownSubscriptionToken(ctx, outside) {
		t.Fatal("a failed load was retried at once")
	}
	e.clock = e.clock.Add(tokenIndexRetry + time.Second)
	if !e.s.KnownSubscriptionToken(ctx, outside) {
		t.Fatal("the reload after the retry delay did not find the token")
	}
}

func hashOf(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
