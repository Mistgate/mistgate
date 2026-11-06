//go:build !linux && !js

package update

import "errors"

func renameExchange(string, string) error        { return errors.ErrUnsupported }
func isRenameExchangeUnsupported(err error) bool { return errors.Is(err, errors.ErrUnsupported) }
