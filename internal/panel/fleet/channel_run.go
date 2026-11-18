//go:build !js

package fleet

import (
	"context"
	"sync"
	"time"
)

// The state of a channel measure (channel_measure.go): data only. The Workers have none of it (channel_run_js.go), so none of these
// types, with their field names and tags, is in the wasm.

// channelState is what a Fleet holds of them: the newest run of each node, running or over.
type channelState struct{ runs map[string]*channelRun }

type channelDirJSON struct {
	MbitS     float64 `json:"mbit_s"`
	Partner   string  `json:"partner,omitempty"`
	PartnerID string  `json:"partner_id,omitempty"`
	Streams   uint32  `json:"streams,omitempty"`
	Capped    bool    `json:"capped,omitempty"`
}

// channelLastJSON is the stored last measure of a node (the result_json of node_channel_measure) and the result of a run.
type channelLastJSON struct {
	Down     channelDirJSON `json:"down"`
	Up       channelDirJSON `json:"up"`
	Partners uint32         `json:"partners,omitempty"`
}

type channelStepState struct {
	Partner, PartnerID, Direction, State, Error string
	MbitS                                       float64
	Streams                                     uint32
	Capped                                      bool
}

// channelRun is one measure of one node, kept in memory while it runs and after it (the node page reads how it went); mu guards
// everything but done and cancel.
type channelRun struct {
	mu      sync.Mutex
	nodeID  string
	started time.Time
	state   string // running, done, failed, canceled
	err     string
	steps   []*channelStepState
	result  channelLastJSON
	cancel  context.CancelFunc
	done    chan struct{}
}
