//go:build !linux

package osupdate

import "errors"

func holdAptLock() (func(), error) { return nil, errors.New("the dpkg lock exists on Linux only") }
