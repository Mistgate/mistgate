# 0010. MTProto proxy: an own Go implementation built on telemt's protocol

Status: accepted, 2026-11-01.

## Context

Telegram clients need a proxy that looks like TLS to a network observer. Telemt 3.5.16 is the reference implementation:
Fake TLS, obfuscated2, a middle-proxy pool and a large management surface. It is Rust, so using it inside Mistgate means
a sidecar process, a second configuration language and two release chains. Vendoring mtglib was rejected: the proxy
must be our Go code.

Telemt's issue tracker shows where a port must not repeat its failures:

- handshake timeouts and reconnects on mobile and Russian networks (#617, #582, #805, #115);
- middle-proxy writers that stall, do not recover or disappear, leaving clients with zero downstream bytes
  (#879, #941, #142, #126, #868);
- frame lengths trusted before allocation (#201);
- inaccurate active connections and session accounting (#107, #90);
- updates that overwrite listener ports (#924), per-listener PROXY protocol missing (#777, #105);
- no first-class groups, quotas and central secret storage (#488, #496, #510);
- DC 203 missing from the built-in DC map (#19).

The license is `TELEMT LICENSE 3.3`, not an OSI license. It allows derivative works if the copyright, license and
attribution are kept, and it requires modified versions to be marked as modified and not presented as official Telemt.
It grants no trademark rights.

## Decision

- The proxy is an own Go package, `internal/mtproto`. It has no Rust sidecar, no cgo and no mtglib. Telemt is a
  protocol reference and a source of design lessons, not a runtime dependency.
- Phase 1, the working MVP, is Fake TLS (`ee` secret) plus obfuscated2 plus direct connections to the Telegram DCs,
  with per-credential traffic and session accounting. This change also adds the node engine
  (`internal/node/mtproto`), the panel protocol (`internal/panel/protocols/mtproto`), a Telegram app toggle and a
  Telegram tab. The first version fixes the Fake TLS SNI to `www.google.com` so one credential works on every MTProto
  profile of a user's groups; per-profile SNI needs profile-scoped credentials and is later.
- The client handshake follows telemt exactly: a 32-byte digest in the ClientHello `random` field at offset 11,
  HMAC-SHA256 over the whole record with the digest zeroed, the first 28 bytes compared in constant time, and the last
  four bytes carrying the timestamp XORed with the HMAC. The accepted timestamp window is 120 seconds; timestamps below
  120 seconds are accepted as boot-time values, as telemt does. A 16-byte prefix of the digest is claimed in a replay
  cache before the ServerHello is sent.
- The ServerHello mirrors the client's offered TLS 1.3 key share: X25519MLKEM768 when offered (a real ML-KEM
  encapsulation plus an X25519 share), otherwise X25519. The cipher suite is the client's first supported TLS 1.3 suite,
  preferring AES-128-GCM. The response digest is HMAC-SHA256 over the client digest and the response with a zeroed
  random field.
- After the fake handshake, the stream is TLS ApplicationData records around obfuscated2. The proxy unwraps records,
  skips ChangeCipherSpec, treats an alert as EOF and refuses unexpected handshake records. Record lengths are checked
  before any allocation and capped at 16 KiB of plaintext / 16.6 KiB on the wire.
- The client's 64-byte obfuscated2 frame is decrypted with `SHA256(prekey || secret)` and its reverse for the other
  direction. The DC is read as a little-endian int16; negative values mean the media DC and are kept. The built-in DC
  map includes 1-5 and 203, IPv4 and IPv6, with a per-node override hook.
- The proxy opens its own obfuscated2 connection to the DC with fresh key material. It never reuses the client's keys
  for the upstream leg. Both directions are copied as plaintext after their own cipher, which also makes the `secure`
  (padded intermediate) tag a pass-through.
- The handshake has a hard deadline. Authentication failures are closed or handed to an optional fallback; they never
  log a secret, a digest or a decrypted frame. Metrics count plaintext bytes, not TLS or cipher overhead.
- The middle-proxy pool is phase 2. The MVP uses direct DC connections, a per-DC address map and a dial timeout. The
  pool will be a bounded writer set with explicit health, a circuit breaker and a direct fallback, not a copy of
  telemt's adaptive floor and generation machinery.
- Upstream parity is explicit: the telemt commit and the ported features live in a parity document, and a sync tool
  reports commits that touch the protocol, transport and configuration paths. The pin is a commit, not a moving
  branch.
- `NOTICE` and the license text ship with the repository. The port document says which files are derived from telemt
  and that the Go implementation is a modified work, not an official Telemt release.

## Consequences

- The first release has a small, testable protocol core with no external process. It can be exercised with an
  in-process fake DC before any Mistgate UI exists.
- Plain `dd` obfuscated2 without Fake TLS, the middle-proxy pool, TLS fronting, a decoy TCP backend and per-user
  quotas are deliberately later. The `ee` link is the only format the panel issues in phase 1.
- The preflight that checks real DC reachability (`req_pq_multi` -> `resPQ`) is designed with this package but lands
  with the engine, not in the protocol core.
- A telemt update is a diff to review, not a dependency bump. The parity document records what is ported, what is
  intentionally different and why.
