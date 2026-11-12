//go:build !linux

package osupdate

func freeMB(string) (int64, bool) { return 0, false }
