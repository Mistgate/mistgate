# Telemt parity

Upstream pin: `telemt/telemt` commit `84e5b827a6619fb2e09ad3d09f253cdffe04bc2f` (3.5.16, 2026-10-31).
The source snapshot used for the port is not vendored; this file records what the Go implementation took from it and
what it deliberately does not share.

## Ported in phase 1

- Fake TLS ClientHello authentication: digest at byte 11, HMAC-SHA256 over the whole record with a zeroed digest,
  first 28 bytes compared in constant time, timestamp XORed into the last four bytes, 120-second skew window and the
  boot-time compatibility bypass.
- Replay deduplication on the first 16 bytes of the digest.
- ServerHello shape: echoed session id, TLS 1.3 cipher suite selection, key_share mirroring with X25519MLKEM768
  (real ML-KEM encapsulation + X25519) or X25519, supported_versions, ChangeCipherSpec, opaque fake certificate and
  optional session tickets. The response digest is HMAC over the client digest and the zeroed response.
- TLS ApplicationData record wrapping and unwrapping for the client leg.
- Obfuscated2 client frame parsing (AES-256-CTR, reversed prekey for the other direction, abridged/intermediate/secure
  tags, little-endian DC with a negative media DC preserved) and a fresh upstream obfuscated2 handshake.
- Direct relay to Telegram DCs 1-5 and 203, IPv4 and IPv6.

## Deliberately different

- Go, not Rust; no sidecar, cgo or mtglib. The node engine owns the listener and the panel owns credentials.
- The first version has no middle-proxy pool, no TLS fronting, no decoy backend and no plain `dd` link. Those are
  phase 2 items, listed so a future port does not silently claim them.
- The Fake TLS SNI is fixed to `www.google.com` for all MTProto profiles in phase 1. A per-profile SNI needs a
  credential bound to the profile; the user table's `app_happ OR app_amnezia` check also keeps Telegram additive until
  that check can be changed without rebuilding a table that many foreign keys reference.
- DC 203 is included (telemt issue #19 is not repeated).
- Metrics count decrypted MTProto bytes, not TLS/obfuscation overhead.

## Tracking

A telemt update is reviewed as a diff against the pin above. Changes that touch `src/protocol`, `src/proxy/handshake`,
`src/proxy/direct_relay` or `src/transport/middle_proxy` are the ones to compare with `internal/mtproto` and
`internal/node/mtproto`.
