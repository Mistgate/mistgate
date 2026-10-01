//go:build !linux

package doctor

import "errors"

// Dev machines (Windows, macOS): the doctor builds, every check is SKIP "not a Linux host".
const hostSupported = false

func statfs(string) (FSStat, error) { return FSStat{}, errors.New("statfs: not Linux") }
