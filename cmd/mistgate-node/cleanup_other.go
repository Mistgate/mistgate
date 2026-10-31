//go:build !linux

package main

import "context"

// warpTraces: the WARP manager installs nothing outside Linux.
func warpTraces(context.Context) bool { return false }

// cleanupOwnedIPv6: the agent holds no IPv6 addresses outside Linux.
func cleanupOwnedIPv6(context.Context, string) error { return nil }
