# 0010. MTProto proxy: a clean-room core, Fake TLS only, a real site for everyone else

Status: accepted, 2026-11-02.

## Context

The MTProto inbound (Telegram's proxy protocol) came in with the Telegram Proxy UI, and its protocol core was a
translation of another project's code under a licence that AGPL-3.0-only cannot take. That core was removed; this
record is its replacement, written from public descriptions of the protocol (provenance below).

The protocol in one paragraph. A Telegram client with an "ee" secret (16-byte key and a domain) opens what looks like
TLS 1.3 to that domain: its ClientHello carries HMAC-SHA256(key, hello) in the random field, with the client's clock
folded into the last four bytes. The proxy answers a ServerHello the client checks the same way, then both wrap
obfuscated2 in TLS application data records: 64 bytes that give the AES-256-CTR keys of the client's leg, the transport
tag and the DC, then the MTProto transport. The proxy connects to that DC with obfuscated2 of its own and relays.

What shapes the design: censors probe proxies. They replay recorded hellos, connect with browsers and scanners and
compare what comes back with the site the domain names; and any unauthenticated hello for the domain costs the proxy
one HMAC per configured key. The owner decided that the domain is a choice per profile: the node's own domain (a
stranger sees the node's decoy site) or someone else's (a stranger is spliced to that site).

## Decision

- **Two packages.** `internal/mtproto` owns one connection from its first byte to its last (Fake TLS, obfuscated2, DC,
  relay, the limits of unauthenticated work). `internal/node/mtproto` is the engine: listeners, credentials, counters,
  and the fallback for strangers. `internal/mtproto/mtprototest` is a client and a fake DC written from the spec, so the
  tests run a real client's half of both handshakes against the server without reusing its code.
- **Fake TLS only.** The bare and "dd" secret forms parse to the same key (`ParseCredential`), but the server serves
  only "ee": plain obfuscated2 on a port that also shows a website would be a stream of noise nothing else explains.
- **The handshake as clients speak it.** A hello is read only when its record header says handshake/TLS 1.0 and the
  record is at most 4096 bytes (MTProxy's limit; checked before allocating); it must be one well-formed ClientHello with
  a 32-byte session id, a TLS 1.3 suite and an SNI that is one of the configured domains (no HMAC otherwise). The first
  28 bytes of the HMAC must match; the last four give the client's unix time, accepted within half of the replay window
  from the node's clock (default ±5 min). The answer is ServerHello (session id echoed, the client's first TLS 1.3 suite,
  supported_versions then an X25519 key share from a real key pair, Go's order), ChangeCipherSpec and one application
  data record of 2500 to 4699 random bytes (MTProxy's and mtg's default range), with
  HMAC(key, client random || answer) in the ServerHello random. Afterwards one ChangeCipherSpec from the client, then
  application data records only (up to 2^14+256 read, 2^14 written).
- **DCs directly, no middle proxy.** Telegram's published production addresses (DC 1-5 and 203, IPv4 and IPv6, port
  443; IPv6 first with `prefer_ipv6`); a negative DC (media) goes to the main address of the same DC, 0 to DC 2, anything
  else is refused. `Config.DCAddress` overrides an address. The addresses are tried in turn, each with an equal share of
  what is left of the handshake deadline, so one that drops packets cannot use up the time of the others. The DC leg
  gets a fresh random header: the client's keys never leave the node. The transport tag (abridged, intermediate,
  padded) is passed on unchanged.
- **Everyone else gets a real site.** Another SNI, a failed HMAC, a replay, a hello outside the clock window, plain HTTP,
  a browser: the core hands the connection to `Config.Fallback` with every byte it has read. The engine has two:
  - `decoy` (the default): the domain is the node's own. The connection goes to an `http.Server` serving
    `Env.Masquerade` (the site Hysteria2 and VLESS "self" show) with the certificate of the node's name from `Env.Certs`
    (`spec.TLS` when the panel sets it, else ACME for the first domain; a self-signed certificate of the same name until
    the first issuance, `certs.SiteCertificate`, now shared with VLESS). `acme-tls/1` is offered and the certificate
    source routes it by name, so Let's Encrypt's TLS-ALPN-01 on 443 keeps working while MTProto holds the port.
  - `site`: the domain is someone else's. The connection is spliced raw to the hello's SNI (when it is one of the
    domains, else the first domain), port 443, resolved by the node, so a prober completes a real handshake with that
    site and sees its certificate. Bounded: 256 forwards per inbound and 16 per peer (IPv4 address or IPv6 /64), 60 s
    idle (nothing either way), 5 min in total. It dials only public unicast addresses that are not the node's: not
    loopback, an interface address, private (RFC 1918, ULA), link-local, CGNAT, documentation, benchmarking or
    otherwise reserved (a name pointed there would loop or open the node's own networks to strangers), and forwards no
    connection that comes from the node. A loop through a NAT that hides the node's public address is cut by the
    per-peer limit, as every hop comes from one address.
- **Settings the node reads** (`InboundSpec.Settings`): `domains` (required when enabled: the domains of the links,
  compared lower case without a trailing dot), `fallback` (`decoy` or `site`) and `idle_timeout_secs` (60-86400,
  default 600), next to the existing `handshake_timeout_secs` (default now 10 s), `replay_window_secs`,
  `ignore_time_skew` and `prefer_ipv6`.
- **What a stranger may cost.** Handshake slots: 1024 per inbound and 32 per peer, taken in the accept loop before a
  goroutine is started (`ServeListener`); over the limit a connection is closed at once. A hard handshake deadline from
  the first byte to the DC connection. A failure budget per peer: a scan that ends without a match (or a replayed hello)
  spends one unit, 16 at once and one more every 4 s; with none left the hello goes to the fallback without a scan, and
  a scan that authenticates is refunded. The budgets live in 8192 slots, two per address chosen by a keyed hash, and an
  address's budget is the fuller of its two (count-min with conservative update): memory is constant (64 KB) whatever
  the number of addresses, and another address limits a client only when both its slots are spent. Hellos for another
  SNI never cost a scan.
- **What a key holder may cost.** Sessions: 64 open per credential (one device); a client keeps a main and a media
  connection per DC it uses and a few more for uploads and downloads, about 10-15 per account at the busiest, and one
  app may hold several accounts behind one proxy; 64 leaves room for that and stops a leaked key from carrying more
  than a handful of people. A session with no byte either way for 10 minutes (`idle_timeout_secs`) is closed; clients
  ping about once a minute.
- **Replays.** The first 16 bytes of an authenticated hello's random are kept for the replay window, checked before the
  HMAC and added after it (only a key holder can add). The cache is a FIFO with a hard size (default 2^17 entries, about
  8 MB): full of live entries it refuses new hellos (they go to the fallback, with a warning at most once a minute)
  instead of dropping a live entry or growing. Each credential may hold at most 1/64 of it (2048 hellos per window, over
  3 a second where a client makes a few a minute), so one key holder making fresh hellos fills only its own share and
  only its own new clients are refused.
- **Revocation.** A handshake becomes a session under the lock `SetCredentials` and `CloseCredential` take: the
  credential must still be there with the same key, and not kicked after this connection was accepted (a kick records
  the accept sequence). `SetCredentials` closes the sessions of credentials that left or changed key; a session closed
  while its DC dial was in flight is not attached.
- **Relay.** Plaintext between the two legs is what the Observer counts (every `StatsInterval` and at the end, the last
  count before `Closed`). A clean end of one direction half-closes the other side's write and the reverse copy goes on;
  an error closes both.
- **Engine races.** The credential index is immutable once published; Apply and Collect replace it (copy on write). A
  session keeps the credential state it was opened with, and its traffic is reported by session, so a key swap during a
  session neither leaks a retired state, nor drives the new one negative, nor puts the old session's bytes on the new
  key. On a credential change the index is swapped before the core's credentials, so a session under a new credential
  always finds its state. An inbound that stops (a restart on a spec change, a disable, a removal) waits for its
  connections to end and keeps what its counters hold, last flushes included, for the next Collect.
- **Errors and logs** carry no key, digest or stream bytes: credential ids, DC numbers, addresses and sizes only.

### Port 443 and the node's site

An enabled TCP inbound claims its port (`tcpPortClaims` in the agent), so a Hysteria2 site on the same TCP port closes
for it, as it does for VLESS. With `decoy` nothing is lost: the visitor of 443 gets the same handler with the same
certificate, and TLS-ALPN-01 is answered, the same case as VLESS "self" (no `hy2_decoy_paused` warning is needed). With
`site` the node's own site is not on that port any more and TLS-ALPN-01 for the node's name is spliced to the other
site and fails; a node that renews a certificate then needs HTTP-01 on port 80. Warning the admin about that is the
panel's part and is not built here.

## Provenance

Sources, read on the web; nothing was downloaded or cloned and no code was copied.

- Telegram, core.telegram.org "MTProto transport" and its "Transport obfuscation" section: the obfuscated2 header
  (offsets, forbidden starts, the reversed key, SHA-256 with the proxy key, the DC at 60 and negative for media), the
  three transports.
- tdlib (Boost licence), `td/mtproto/TlsInit.cpp` and `TcpTransport.cpp`: the client's hello (HMAC over the whole
  record with a zero random, the time XORed into the last four bytes, little-endian), how the client checks the answer
  (the two record prefixes, HMAC over client random and answer), the client's ChangeCipherSpec before its first record,
  the tags it sends.
- MTProxy (GPL-2.0), `net/net-tcp-rpc-ext-server.c`, read to understand the protocol: the server's checks (record
  limit, SNI lookup, 28-byte match, clock window, replay key and when it is stored), the ServerHello layout and cipher
  suite choice, the default length of the fake certificate record, proxying strangers to the domain.
- mtg (MIT), `mtglib/obfuscation`, `mtglib/internal/tls/fake`, `mtglib/proxy.go`, `essentials/addresses.go`: the frame
  constants, negative DC to its absolute value and 0 to DC 2, domain fronting with the bytes read, the extension order
  and the DC address list (facts, not code). No mtg code is adapted, so there is no NOTICE.

Not read: the removed core, its design notes, and the project it was translated from (telemt).

## Consequences

- A careful prober can still tell the fake flight from a real one: one application data record of random length where
  a real TLS 1.3 server (and the node's own decoy, a Go server) sends four, and an X25519 key share where Go would pick
  X25519MLKEM768 for a client that offers it.
- A client whose two budget slots are both shared with spent ones, or who shares a NAT with someone sending bad hellos
  for the domain, is turned away until the budget refills. A botnet with many addresses still costs CPU; a global budget
  would instead turn every client away during it.
- The replay cache lives in memory; after a restart only the clock window protects (and nothing with
  `ignore_time_skew`). 64 key holders making fresh hellos at full rate can still fill it.

## Phase 2

- Middle proxies (Telegram's proxy config, the promoted-channel tag).
- Measure the real TLS flight of the fallback (the node's site or the target site) and shape the fake answer after it:
  record count and lengths, the key share group.
- Per-credential and per-user rate limits (`RateLimitPerCred`, `RateLimitPerUser`); the session cap as an inbound
  setting.
- Answer `acme-tls/1` for the node's own name locally in `site` mode.
- DC addresses from Telegram's published config instead of only the built-in table; test DCs.
- Connection counters (hellos, rejects, replays) as `LinkStat`.
- The panel warning for `site` on a port where the node shows its site or renews a certificate.

## Alternatives rejected

- **Close strangers' connections**: an HTTPS port that drops browsers is itself the signal a censor looks for.
- **Serve plain obfuscated2 too**: see "Fake TLS only".
- **Evict live replay entries when the cache is full**: a recorded hello could then be replayed after enough new
  handshakes; growing the cache makes its memory unbounded.
- **A global failure budget**: bounds CPU under a botnet, at the price of turning every client away while it lasts.
