//go:build js

package mcp

import (
	"context"
	"errors"
	"io"
)

// RunProxy is `mistgate mcp`, the stdio bridge of the VPS command; the Cloudflare build serves no MCP (ADR 0014), so the
// command only says so. It keeps cmd/mistgate building for js/wasm.
func RunProxy(_ context.Context, _, _ string, _ io.Reader, _, _ io.Writer) error {
	return errors.New("mistgate mcp is not available in this build")
}
