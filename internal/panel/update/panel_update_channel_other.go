//go:build !linux

package update

import (
	"errors"
	"fmt"
	"os"

	"github.com/mistgate/mistgate/internal/release"
)

// The self-update helper is Linux-only. A missing policy still has the stable default so package tests and builds on
// other platforms retain the same channel semantics; a present file cannot be trusted without Linux ownership checks.
func readRootAllowedPanelUpdateChannel(path string) (release.Channel, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return release.ChannelStable, nil
	} else if err != nil {
		return "", fmt.Errorf("%w: inspect %s: %w", ErrPanelUpdateChannelConfig, path, err)
	}
	return "", fmt.Errorf("%w: the channel policy can only be validated on Linux", ErrPanelUpdateChannelConfig)
}
