package fleet

import (
	"errors"
	"net/url"
	"strings"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// decoyLibraryError words a refusal of the decoy library with a stable code (proto/mistgate/decoysite/v1); nil when err is not one.
func decoyLibraryError(err error) error {
	var inUse *store.DecoySiteInUseError
	switch {
	case errors.Is(err, store.ErrDecoySiteNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("decoy_site_not_found"))
	case errors.Is(err, store.ErrDecoyVersionNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("decoy_version_not_found"))
	case errors.Is(err, store.ErrDecoyNameTaken):
		return connect.NewError(connect.CodeAlreadyExists, errors.New("decoy_name_taken"))
	case errors.Is(err, store.ErrDecoyNameInvalid):
		return invalid("decoy_name_invalid")
	case errors.Is(err, store.ErrDecoyNoteInvalid):
		return invalid("decoy_note_invalid")
	case errors.Is(err, store.ErrDecoyLibraryFull):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("decoy_library_full"))
	case errors.Is(err, store.ErrDecoySiteDraft):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("decoy_site_draft"))
	case errors.Is(err, store.ErrDecoyNotAssigned):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("node_not_assigned"))
	case errors.Is(err, decoy.ErrInvalidBundle):
		return invalid(decoy.ClientMessage(err))
	case errors.As(err, &inUse):
		msg := "decoy_site_in_use: nodes=" + url.QueryEscape(strings.Join(inUse.NodeIDs, ","))
		if inUse.Panel {
			msg += "&panel=1"
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(msg))
	}
	return nil
}
