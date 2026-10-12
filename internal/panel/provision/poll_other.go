//go:build !js

package provision

import "time"

// cancelPoll is how often a running job looks at its stored state for a cancellation or a retired node.
const cancelPoll = 200 * time.Millisecond
