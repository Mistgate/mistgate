# 0003. One agent link for both editions: WebSocket with a signed challenge

Status: accepted, 2026-10-06.

## Context

Agents hold one long-lived bidirectional Connect stream to the panel over HTTP/2 with mutual TLS (the panel's own CA,
selected by a secret SNI). On Cloudflare neither part works: mTLS with a custom CA is Enterprise-only, and a Worker gets
the request body of an HTTP/2 stream only after it ends (measured: response headers arrived after the 16-second body).
Two different agent transports, one per edition, would make the agents diverge.

## Decision

- Add a second transport, used by both editions: a WebSocket with binary frames, one `ConnectRequest` /
  `ConnectResponse` per frame — the same messages and the same seq / Ack / dedupe / desired-state rules as today.
- Authentication by a signed challenge: the panel sends a nonce and its audience; the agent signs
  `"mistgate-agent-link/1\0" + node_id + "\0" + audience + "\0" + nonce` with its enrolment key (the key of the CSR it
  already sends at `Enroll`); the panel verifies with the stored public key and refuses retired nodes and revoked keys.
- On the edge each node's link is a Durable Object (hibernating WebSocket, its own SQLite for seq and desired state).
- Agents advertise `ws-link/1`; the panel says which transport to prefer. mTLS stays for backward compatibility until
  every node runs the WebSocket link, plus two releases, then both editions retire it by date.

## Consequences

- Agents behave the same with either panel, which is what makes moving between editions possible
  (see "change panel address for nodes" in the plan).
- Server-streaming calls (`FetchUpdate`, admin `StreamLogs`) keep working over plain HTTP; update bundles come from R2 on
  the edge.
- The secret SNI split goes away on the edge; the agent path is a secret URL prefix instead.
