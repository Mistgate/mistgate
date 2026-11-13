package subs_test

import (
	"strings"
	"testing"
)

// With the page password on, the device calls of a token nobody holds are answered like every other route answers it: the
// decoy. "locked" belongs to a token that exists.
func TestGatedPageAnswersUnknownTokenWithTheDecoy(t *testing.T) {
	g := newGate(t, nil)
	_, tok := g.newUser("alice", nil)
	nobody := strings.Repeat("z", 43)

	if rec := (call{g.h, t, nobody}).post("/devices", `{}`); rec.Code != 404 || strings.Contains(rec.Body.String(), "locked") {
		t.Fatalf("unknown token, POST /devices: %d %s", rec.Code, rec.Body.String())
	}
	if rec := (call{g.h, t, nobody}).post("/unlock", `{"password":"x"}`); rec.Code != 404 {
		t.Fatalf("unknown token, POST /unlock: %d", rec.Code)
	}
	if rec := (call{g.h, t, tok}).post("/devices", `{}`); rec.Code != 401 || !strings.Contains(rec.Body.String(), "locked") {
		t.Fatalf("known token, POST /devices: %d %s", rec.Code, rec.Body.String())
	}
}
