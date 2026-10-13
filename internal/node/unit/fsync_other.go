//go:build !linux

package unit

func syncDir(string) error { return nil }
