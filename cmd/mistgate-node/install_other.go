//go:build !linux

package main

import (
	"fmt"
	"os"
)

func cmdInstall([]string) int {
	fmt.Fprintln(os.Stderr, "install: the systemd unit can only be installed on Linux")
	return 1
}
