//go:build !unix && !js

package store

func umask(int) int { return 0 }
