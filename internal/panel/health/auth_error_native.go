//go:build !js

package health

import (
	"errors"

	coreErrs "github.com/apernet/hysteria/core/v2/errors"
)

func hysteriaAuthStatus(err error) (int, bool) {
	var authErr coreErrs.AuthError
	if !errors.As(err, &authErr) {
		return 0, false
	}
	return authErr.StatusCode, true
}
