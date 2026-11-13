//go:build !linux

package awgprep

func TryBuilderLock() (func(), bool, error) { return func() {}, true, nil }
