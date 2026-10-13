//go:build js

package main

import "errors"

// runVLESSHelper: the Cloudflare edition runs no tunnel checks, so it has no Xray client.
func runVLESSHelper() error { return errors.New("health-vless-helper is not part of this build") }
