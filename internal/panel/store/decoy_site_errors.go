package store

import "errors"

// Refusals of the decoy library. They are here, not in decoy_site.go, because the fleet code that words them is built for
// the edge too (which keeps no library: the code that returns them is VPS-only).
var (
	ErrDecoySiteNotFound    = errors.New("decoy site not found")
	ErrDecoyNameTaken       = errors.New("decoy site name is taken")
	ErrDecoyNameInvalid     = errors.New("decoy site name is empty, too long or has control characters")
	ErrDecoyNoteInvalid     = errors.New("decoy site note is too long or has control characters")
	ErrDecoyLibraryFull     = errors.New("decoy library is full")
	ErrDecoySiteDraft       = errors.New("decoy site is a draft")
	ErrDecoyVersionNotFound = errors.New("decoy site version not found")
	ErrDecoyNotAssigned     = errors.New("decoy site does not stand on this node")
)

// DecoySiteInUseError says where a site stands, which is why it cannot be deleted or put back to a draft.
type DecoySiteInUseError struct {
	NodeIDs []string
	Panel   bool
}

func (e *DecoySiteInUseError) Error() string { return "decoy site is in use" }
