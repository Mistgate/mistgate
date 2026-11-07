//go:build !js

// Package reqid is the short request id the admin HTTP server stamps on every request: in the context, the log records, the
// audit rows and, once the caller is signed in, the X-Request-Id response header. It lets a person who sees "request code
// abc123" in the panel and an operator reading the logs talk about the same request. The id is random and only names the
// request; it is never a credential, and an id sent by the client is not used.
//
// The header is written only after the session or token middleware accepted the caller (MarkAuthed): an unauthenticated probe
// of the admin address must see what it sees on the decoy, and a header only the admin sends would tell it where the admin is.
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
)

// Header is the response header that carries the id.
const Header = "X-Request-Id"

type key struct{}

// Request is the id of one request and whether its caller was accepted.
type Request struct {
	ID     string
	authed atomic.Bool
}

// Authed reports whether the session or token middleware accepted the caller of the request.
func (r *Request) Authed() bool { return r.authed.Load() }

// New returns a fresh id: 8 lower-case hex characters.
func New() string {
	var b [4]byte
	rand.Read(b[:]) // never fails on supported platforms
	return hex.EncodeToString(b[:])
}

// With returns ctx carrying a new Request with the given id.
func With(ctx context.Context, id string) context.Context {
	ctx, _ = Start(ctx, id)
	return ctx
}

// Start returns ctx carrying a new Request with the given id, and the Request.
func Start(ctx context.Context, id string) (context.Context, *Request) {
	r := &Request{ID: id}
	return context.WithValue(ctx, key{}, r), r
}

// From returns the id of ctx, or "".
func From(ctx context.Context) string {
	if r, _ := ctx.Value(key{}).(*Request); r != nil {
		return r.ID
	}
	return ""
}

// MarkAuthed records that the caller of ctx's request was accepted, so the response may carry the header.
func MarkAuthed(ctx context.Context) {
	if r, _ := ctx.Value(key{}).(*Request); r != nil {
		r.authed.Store(true)
	}
}
