//go:build !linux

package main

import "context"

func newSecurityUpdatesLauncher(string) func(context.Context) error { return nil }
