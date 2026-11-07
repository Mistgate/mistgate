//go:build js

// The Cloudflare edition stamps no request ids (the middleware is the VPS edition's: internal/panel/httpserver/requestid.go), so
// the two calls the shared code makes are empty here and cost the admin Worker's wasm nothing.
package reqid

import "context"

// Header is the response header that carries the id.
const Header = "X-Request-Id"

// From returns "": there is no request id on the edge.
func From(context.Context) string { return "" }

// MarkAuthed does nothing on the edge.
func MarkAuthed(context.Context) {}
