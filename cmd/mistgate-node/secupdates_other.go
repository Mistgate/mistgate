//go:build !linux

package main

import (
	"fmt"
	"os"
)

func cmdHost([]string) int {
	fmt.Fprintln(os.Stderr, "host enable-security-updates: Linux only")
	return 1
}
