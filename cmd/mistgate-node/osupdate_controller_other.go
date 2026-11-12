//go:build !linux

package main

import (
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/node/update"
)

func newOSUpdate(string, *update.Updater) *osupdate.Controller { return nil }
