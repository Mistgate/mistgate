//go:build !linux

package ownprobe

import "os/exec"

// The script tests run on Linux only (scriptShells skips them elsewhere); see script_lab_linux_test.go.
func (l *lab) startGroup() {}

func (l *lab) own(*exec.Cmd) {}
