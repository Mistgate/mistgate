//go:build !linux

package main

import (
	"fmt"
	"os"
)

func cmdAwg([]string) int {
	fmt.Fprintln(os.Stderr, "awg: the AmneziaWG kernel module exists on Linux only")
	return 1
}
