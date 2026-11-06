//go:build js

package backup

import (
	"errors"
	"io"
)

func RunCLI([]string, io.Writer) error {
	return errors.New("backup CLI is unavailable in the js/wasm edition")
}
