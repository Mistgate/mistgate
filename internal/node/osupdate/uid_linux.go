//go:build linux

package osupdate

import "os"

func currentUID() int { return os.Geteuid() }
