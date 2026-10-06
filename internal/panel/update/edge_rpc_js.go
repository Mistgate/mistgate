//go:build js

package update

import (
	"errors"

	"connectrpc.com/connect"
)

func edgeFileRPCUnavailable() error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("update bundle files are managed differently in the edge edition"))
}
