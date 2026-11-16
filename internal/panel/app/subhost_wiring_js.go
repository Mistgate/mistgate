//go:build js

package app

import "github.com/mistgate/mistgate/internal/panel/httpserver"

// The Cloudflare edition has no subscription host: it has a public address of its own, and its Workers keep no tunnel open.
// edition.go lists subscription_host as unavailable, and no endpoint is mounted.
func newSubHost(*core) subHostService { return nil }

func subHostAdminHandlers(subHostService) []httpserver.AdminHandler { return nil }
