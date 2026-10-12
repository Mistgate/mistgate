//go:build js

package provision

import "time"

// cancelPoll is how often a running job looks at its stored state for a cancellation or a retired node.
// ponytail: D1 allows 1000 queries per Worker invocation; 200 ms would spend about 2400 on an 8-minute install, 2 s
// about 240. Lower it only together with a smaller query budget elsewhere in the job.
const cancelPoll = 2 * time.Second
