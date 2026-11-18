//go:build js

package telegram

// The Cloudflare edition never raises the WARP reach alert (format_warpreach.go).

func warpReachTitle(L) string { return "" }

func warpReachReason(L, map[string]string) string { return "" }
