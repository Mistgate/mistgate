//go:build !linux

package main

import "context"

// warpTraces: the WARP manager installs nothing outside Linux.
func warpTraces(context.Context) bool { return false }
