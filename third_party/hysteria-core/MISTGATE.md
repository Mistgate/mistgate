# Hysteria core, patched for Mistgate

A copy of `github.com/apernet/hysteria/core/v2` **v2.13.0** (MIT, `LICENSE.md`), used through a `replace` in Mistgate's
`go.mod`. Everything is upstream except a few small patches, meant to go upstream as pull requests:

- `server/config.go`: `ConnEventLogger`, an optional extension of `EventLogger` with `Accepted(conn)`,
  `ConnConnect(conn, id, tx)` and `ConnDisconnect(conn, id, err)`.
- `server/server.go`: `handleClient` calls `Accepted` and `ConnDisconnect`, the auth handler calls `ConnConnect`, when
  the configured `EventLogger` implements `ConnEventLogger`; otherwise `Connect` and `Disconnect` as before.
- `internal/congestion/utils.go`, `server/config.go`, `server/server.go`: `server.LostPackets(conn)` (the server sets its
  controllers with `UseConfiguredCounting` and `UseBrutalCounting`). quic-go counts lost packets into
  `ConnectionStats.PacketsLost` only in its own congestion controller; BBR and Brutal are wrapped in a counter of
  `OnCongestionEvent` (called once per lost packet whatever the controller), dropped when `handleClient` ends. quic-go's
  own count, which covers the handshake and the authentication, stays and is added.
- `server/config.go`, `server/server.go`: `QUICConfig.ConnContext`, passed to `quic.Transport.ConnContext`. quic-go calls it
  for every connection the server creates, before the handshake, and ends the context it hands in when the connection
  ends, with the close error as its cause, also for a handshake that never completed (a timeout, a TLS alert either way).
  That is the only place a failed handshake shows: the listener never returns such a connection. One callback per
  connection, nothing per packet (a `quic.Config.Tracer` would build an event for every packet).
- `server/server.go`: `handleTCPRequest` counts the bytes the `RequestHook` puts back (`LogTraffic(id, n, 0)`), which upstream only
  adds to its stream statistics. A leftover of 3 bytes per stream in v2.12.3, up to several KB (what the v2.13.0 sniffer reads
  before it can tell the protocol) in v2.13.0, it would otherwise be missing from the user's counted upload.
- `client/client.go`: the client sets the counting controllers too, `ConnectionStats()` on the client returns the QUIC
  statistics with these lost packets, and `Close` drops the connection's counter (the panel's health checks report what
  their client lost).

Why: the node agent needs the QUIC connection itself — its statistics (RTT, packets sent, received and lost) tell a
frozen path (the node keeps sending, nothing comes back) from a client that simply went quiet, and its address cannot
name a connection because it moves with port hopping (`DisablePathManager`). See
`design/connection-diagnostics-gaps.md` (B1, B2, step S2). `ConnContext` gives the reason a handshake failed (links/3: timeouts,
TLS alerts sent and received), which tells a path that drops the answer from a client that refuses the certificate.

Upstream v2.13.0 over v2.12.3 touched `server/udp.go`, `server/config.go` (the `RequestHook` doc and `UDP` signature), `server/server.go`
(`udpIOImpl.Hook`), the UDP mocks and tests, and bumped quic-go; none of the hunks above overlap with it, they were carried over
unchanged. Since v2.13.0 `RequestHook.UDP(packets [][]byte, reqAddr *string) (done bool, err error)` holds a session's first
UDP packets until the hook is done (the QUIC sniffer asks for more while a ClientHello spans several packets); Mistgate's
hook (`internal/node/hysteria2/sniffing.go`) answers done at once for an IP-literal destination, which it never rewrites.

To move to a new upstream version: copy the new `core/v2` module from the Go module cache over this directory, apply the
hunks above again (they are small and marked by `ConnEventLogger`, `LostPackets` and `ConnContext`), update the version here and in `go.mod`'s
`require`, and run `go build ./...` and the Hysteria2 engine tests (`go test ./internal/node/hysteria2/`).
