//go:build js

package store

// The Cloudflare edition does not stamp request ids (internal/panel/httpserver/requestid_js.go): keeping the code out of the admin
// Worker's wasm is worth more than the id there.
func withRequestID(params, _ string) string { return params }
