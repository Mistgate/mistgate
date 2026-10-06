//go:build js

package health

import (
	"context"
	"errors"
)

var errTunnelProbesOnNodes = errors.New("tunnel probes run on nodes in this edition")

func dialHysteria2(context.Context, Target, DialOptions) (Tunnel, error) {
	return nil, errTunnelProbesOnNodes
}

func dialAWG(context.Context, Target, DialOptions) (Tunnel, error) {
	return nil, errTunnelProbesOnNodes
}
