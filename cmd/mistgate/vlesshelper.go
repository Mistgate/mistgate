//go:build !js

package main

import (
	"os"

	"github.com/mistgate/mistgate/internal/panel/health/vlesshelper"
)

// runVLESSHelper is the short-lived Xray client process of the VLESS health check (internal/panel/health/vless.go).
func runVLESSHelper() error { return vlesshelper.Run(os.Stdin, os.Stdout, os.Stderr) }
