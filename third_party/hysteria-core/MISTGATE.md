# Hysteria core, patched for Mistgate

A copy of `github.com/apernet/hysteria/core/v2` **v2.12.3** (MIT, `LICENSE.md`), used through a `replace` in Mistgate's
`go.mod`. Everything is upstream except one patch, meant to go upstream as a pull request:

- `server/config.go`: `ConnEventLogger`, an optional extension of `EventLogger` with `Accepted(conn)`,
  `ConnConnect(conn, id, tx)` and `ConnDisconnect(conn, id, err)`.
- `server/server.go`: `handleClient` calls `Accepted` and `ConnDisconnect`, the auth handler calls `ConnConnect`, when
  the configured `EventLogger` implements `ConnEventLogger`; otherwise `Connect` and `Disconnect` as before.

Why: the node agent needs the QUIC connection itself — its statistics (RTT, packets sent, received and lost) tell a
frozen path (the node keeps sending, nothing comes back) from a client that simply went quiet, and its address cannot
name a connection because it moves with port hopping (`DisablePathManager`). See
`design/connection-diagnostics-gaps.md` (B1, B2, step S2).

To move to a new upstream version: copy the new `core/v2` module from the Go module cache over this directory, apply the
two hunks above again (they are small and marked by `ConnEventLogger`), update the version here and in `go.mod`'s
`require`, and run `go build ./...` and the Hysteria2 engine tests (`go test ./internal/node/hysteria2/`).
