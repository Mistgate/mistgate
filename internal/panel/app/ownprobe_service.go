package app

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
)

// ownProbeService is the owner-managed probes (ownprobe): the owner's RPCs, the public endpoint of the probes under their secret
// prefix, and what the health module needs to know of them.
type ownProbeService interface {
	Handler(opts ...connect.HandlerOption) (string, http.Handler)
	PublicHandler(decoyDir string) http.Handler
	// SourceState says whether the source is on and how old a point may be before it stops counting.
	SourceState(ctx context.Context) (on bool, maxAge time.Duration, err error)
}
