# REALITY, patched for Mistgate

A copy of `github.com/xtls/reality` at commit **4bb46b2afd8b** (module version `v0.0.0-20261108101329-4bb46b2afd8b`, the one
`github.com/xtls/xray-core` v26.11.9 requires), used through a `replace` in Mistgate's `go.mod`. The code is under the
Mozilla Public License 2.0 (`LICENSE`, `LICENSE-Go` for the parts derived from Go's `crypto/tls`); every file keeps its upstream
header and nothing of upstream was removed. The patch is one hunk in `tls.go` and one new file, both small. The source of this
directory is public with the rest of Mistgate.

`internal/vendorcheck` keeps this copy honest: it fails when the version above differs from the one `go.mod` requires (so a bump
of `xray-core` cannot silently go on building this copy), and when any file here differs from the upstream module in the Go module
cache except `tls.go` (by exactly the hunk below), `mistgate_compat.go` and this file.

## What is patched

Since 2026-11 (upstream commit 8cdf7bf9) `reality.Server` sends every ClientHello that does not carry an X25519MLKEM768 key share
before its optional X25519 share to the target site, as if it came from a stranger, and also turns away a hello with two such
shares or two X25519 shares. That excludes, by design of the library, the clients that never send the post-quantum share: every
sing-box (it strips the share), Mihomo without its post-quantum flag (and, with it, every fingerprint but `chrome`), Xray cores
older than 25.5.16 with `chrome` (older than 26.3.23 with `firefox` and `safari`), and any Xray with the `ios`, `edge` and `random`
fingerprints. Xray v26.3.27, the core Mistgate nodes ran before, accepted all of them. `reality.Config` has no field that turns
the check off.

The patch gives the rule of the library of Xray 26.3.27 (xtls/reality 9234c772, `tls.go` lines 214-228 there) back to every hello
the upstream rule turns away:

- the key share is **the first 32-byte X25519 share anywhere in the list**; if there is none, **the X25519 half of the first
  X25519MLKEM768 share of the right size** (1216 bytes). Everything else (GREASE, P-256, the Kyber draft share, shares of a wrong
  size, duplicates) is skipped. Nothing is turned away for its shape.
- a hello the upstream rule accepts is not touched: it takes the same share as the older rule would (the first X25519 share, else
  the post-quantum half), so the upstream path is unchanged for every client that works with upstream.
- the authentication that follows is unchanged: the share gives the auth key, the session id has to open (AEAD with the whole hello
  as additional data), the short id has to be one of the inbound's, the client version must be inside `MinClientVer` and
  `MaxClientVer` when they are set (Mistgate never sets them). `MaxTimeDiff` is 0 in Mistgate's config, so the client's clock is
  not checked. Whatever fails goes to the target, in both modes: an active prober sees no difference.

Files:

- `mistgate_compat.go` (new): `legacyPeerPub` (the older rule) and `SetRejectClassical(func(*Config) bool)`.
- `tls.go`, in `serverHandshake`'s hello loop:

  ```diff
  -			if peerPub2 == nil {
  -				break // reject outdated/strange Client Hello that doesn't have X25519MLKEM768 before optional X25519
  -			}
  +			if peerPub2 == nil {
  +				if peerPub = legacyPeerPub(config, hs.clientHello.keyShares); peerPub == nil { // Mistgate: the pre-2026-11 rule, mistgate_compat.go
  +					break // reject outdated/strange Client Hello that doesn't have X25519MLKEM768 before optional X25519
  +				}
  +			}
  ```

**The default is compatible.** Nothing has to set anything: with no policy installed the older rule applies, so a node cannot
come up strict by a missed `init`. `SetRejectClassical(f)` is the only switch, and it only turns the compatibility off: for a
config where `f(config)` is true the upstream rule stands. The strict mode ("post-quantum only" per profile, the plan's stage C4)
will be such a policy that consults the profile. `SetRejectClassical(nil)` returns to the default.

`minClientVer` is never set: upstream's JSON builder defaults it to the release (26.3.27), and every sing-box (it reports 1.8.1),
Mihomo (1.8.2) and older Xray would be turned away by it. Mistgate builds the protobuf config and leaves it empty;
`TestRealityConfigLeavesClientVersionsOpen` keeps it so.

## Tests

- `internal/node/vless/realitycompat_test.go`: `TestRealityKeyShareRuleAgainst2603` feeds nineteen shapes of key_share list to the
  library's `Server` (read through its `Show` output: whether an auth key was derived and its first 16 bytes) and compares with
  the 26.3.27 rule copied into the test, with the compatibility on (default) and off (upstream's behaviour, also copied).
- `TestRealityHelloKexPerKey` and `scripts/e2e/m3.sh` (`m3_vless_oldclients` and the steps after it) run real clients of older
  releases against a node.

## To move to a new upstream version

1. Copy the module that `go mod download -json github.com/xtls/reality@<version>` names (the version `xray-core`'s `go.mod`
   requires) over this directory, keeping `mistgate_compat.go` and this file.
2. Apply the hunk to `tls.go` again (search for `ensure order`; if upstream changed the rule, read the new one and decide what the
   older rule has to give back). Update the version above and in `go.mod`'s `require`.
3. `go mod tidy`, `go build ./...`, `go test -p 1 ./internal/node/vless/ ./internal/vendorcheck/`, then the old-client matrix of
   the m3 e2e.
4. Check `LICENSE`/`LICENSE-Go` and the licences of the library's new dependencies (no GPL in the node binary).
