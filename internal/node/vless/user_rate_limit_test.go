package vless

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"

	"github.com/mistgate/mistgate/internal/node/ratelimit"
	"github.com/mistgate/mistgate/internal/plugin"
)

func withPerson(userID string, bps uint64) func(*plugin.UserCred) {
	return func(c *plugin.UserCred) { c.UserID, c.UserRateLimitBps = userID, bps }
}

func personCred(id, uuid string, mods ...func(*plugin.UserCred)) plugin.UserCred {
	c := testCredential(id, uuid)
	for _, m := range mods {
		m(&c)
	}
	return c
}

// Credentials of one user on a node draw on one bucket (across engines when they share the registry), another user's on
// another; the bucket follows the value and is freed with the last credential or the inbound.
func TestPersonRateLimitBucketLifecycle(t *testing.T) {
	f := newRuntimeFixture(t, false)
	if !f.engine.Capabilities().RateLimitPerUser {
		t.Fatal("the capability must be advertised")
	}
	spec := f.spec("node-a", "tcp")
	const a1, a2, b1 = "66ad4540-b58c-4ad2-9926-ea63445a9b57", "7c9e6679-7425-40de-944b-e07fc1f90ae7", "0f8fad5b-d9cb-469f-a165-70867728950e"
	apply := func(creds ...plugin.UserCred) {
		t.Helper()
		if _, err := f.engine.Apply(context.Background(), spec, creds); err != nil {
			t.Fatal(err)
		}
	}
	bucket := func(id string) *ratelimit.Limiter {
		t.Helper()
		cs := f.engine.inbounds[spec.ID].idx.Load().byID[id]
		if cs == nil {
			t.Fatalf("no credential %s", id)
		}
		return cs.person.Load()
	}
	users := f.engine.users

	apply(personCred("a1", a1, withPerson("usr_a", 8_000_000)), personCred("a2", a2, withPerson("usr_a", 8_000_000)), personCred("b1", b1, withPerson("usr_b", 8_000_000)))
	if bucket("a1") == nil || bucket("a1") != bucket("a2") || bucket("b1") == nil || bucket("b1") == bucket("a1") {
		t.Fatalf("buckets: a1=%p a2=%p b1=%p", bucket("a1"), bucket("a2"), bucket("b1"))
	}
	shared := bucket("a1")
	apply(personCred("a1", a1, withPerson("usr_a", 16_000_000)), personCred("a2", a2, withPerson("usr_a", 16_000_000)), personCred("b1", b1))
	if bucket("a1") != shared || shared.BPS() != 16_000_000 {
		t.Fatalf("the bucket must follow the new value, bps = %d", shared.BPS())
	}
	if bucket("b1") != nil || users.Limiter("usr_b") != nil {
		t.Fatal("b1 lost its limit: its bucket must be gone")
	}
	apply(personCred("a1", a1, withPerson("usr_a", 16_000_000)))
	if users.Limiter("usr_a") == nil {
		t.Fatal("a1 still holds the bucket")
	}
	apply()
	if users.Limiter("usr_a") != nil {
		t.Fatal("the last credential of the person is gone: the bucket must be dropped")
	}
	apply(personCred("a1", a1, withPerson("usr_a", 16_000_000)))
	if err := f.engine.Remove(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if users.Limiter("usr_a") != nil {
		t.Fatal("removing the inbound must free its buckets")
	}
}

// A write waits for the person bucket as well: after one device has used it up, another device of the same person
// waits, a device of another person does not.
func TestPayloadWaitsForThePersonBucket(t *testing.T) {
	reg := ratelimit.NewRegistry()
	b := reg.Sync("inb", []ratelimit.Holder{{CredID: "a1", UserID: "usr_a", BPS: 8}, {CredID: "a2", UserID: "usr_a", BPS: 8}, {CredID: "b1", UserID: "usr_b", BPS: 8}})
	credOf := func(id string) *credState {
		c := &credState{}
		c.person.Store(b[id])
		return c
	}
	write := func(c *credState, ctx context.Context) error {
		w := countWriter{Writer: buf.NewWriter(io.Discard), cred: c, up: true, ctx: ctx}
		return w.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(make([]byte, 64*1024))})
	}
	if err := write(credOf("a1"), context.Background()); err != nil { // the whole 64 KiB burst
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := write(credOf("a2"), ctx); err == nil {
		t.Fatal("a second device of the person got through an empty person bucket")
	}
	if err := write(credOf("b1"), context.Background()); err != nil {
		t.Fatalf("another person must not be held back: %v", err)
	}
}
