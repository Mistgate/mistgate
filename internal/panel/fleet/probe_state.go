package fleet

import (
	"sync"
	"time"
)

const maxActiveProbes = 8

// probeState is the limiter of the on-demand probes (path_probe, node_trace, node_dns_check). The code that uses it is in
// path_probe.go, which is not in the Cloudflare edition; the type stays here so that Fleet has the same fields in both builds
// (an empty struct costs the Workers nothing).
type probeState struct {
	mu     sync.Mutex
	active int                    // probes and speed tests currently running across the fleet
	busy   map[string]struct{}    // unordered node pair -> a probe between them is running
	caller map[string][]time.Time // actor -> when its probes started, inside the window
	node   map[string][]time.Time // node id -> when probes touching it started, inside the window

	// path_speed (path_speed.go): a node runs one test at a time, and starts are counted over a day per node and per caller.
	speedBusy   map[string]struct{}
	speedCaller map[string][]time.Time
	speedNode   map[string][]time.Time
}
