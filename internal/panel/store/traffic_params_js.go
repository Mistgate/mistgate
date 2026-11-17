//go:build js

package store

import (
	"context"
	"time"
)

// The traffic numbers on the node_down event and alerts are the VPS edition's (traffic_params.go): the Workers leave them out, and
// with them the query and the code that adds them.
func (s *Store) AddTrafficParams(context.Context, string, time.Time, map[string]string) error {
	return nil
}
