//go:build !linux

package main

import "github.com/mistgate/mistgate/internal/node/update"

func newUnitRefresh(string, *update.Updater) unitRefreshConfig {
	return unitRefreshConfig{reason: "not_systemd"}
}
