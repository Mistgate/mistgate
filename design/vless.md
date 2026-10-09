# VLESS protocol plugin: design

Status: accepted by the owner 2026-10-09; R1 and R2 implemented 2026-10-09; §7 (Hysteria2 and VLESS on one node, node decoy
choice) agreed 2026-10-09. Base: main 0a3c16a. Reference versions: xray-core v26.3.27 (module v1.260327.0, the
latest stable; later tags are pre-releases), Mihomo v1.19.32 (already used by scripts/e2e/m3.sh).

## 0. Decisions in one screen

- **Engine.** The node agent embeds xray-core as a Go library. Each VLESS inbound gets its own `core.Instance`. We build
  the `core.Config` protobuf ourselves and do not use `infra/conf`.
- **Outbound.** One outbound handler, our own, on top of `engine.Egress`: it keeps everything the node already
  guarantees (the node's DNS, the private and own-address block, the "IPv6 for clients" switch, the WARP device binding,
  torrentguard) and holds the per-credential byte counters, sessions, rate limit and kick. Xray's stats app, router and
  freedom outbound are not used.
- **Users** change at runtime through the in-process `proxy.UserManager` (what HandlerService `AlterInbound` calls
  internally). No gRPC.
- **Panel side.** A new built-in plugin `internal/panel/protocols/vless` with no xray imports, so it still builds for
  js/wasm and the edge edition gets it unchanged. v1 renders only for the existing "link apps" toggle: Happ through the
  URI list, plus Mihomo YAML.
- **Spike** (scratch tree + standalone program in WSL with a real Xray client): the whole tree builds with xray-core
  added (AWG netstack, WARP and the panel included). Node binary linux/amd64 stripped 16.5 MB → 20.8 MB with the
  selected packages (36.7 MB with infra/conf). Idle RSS 12.3 → 13.6 MB with two instances; 1000 users added per
  instance at runtime in 6 ms, +3.9 MB. 20 MB downloads through REALITY+Vision and XHTTP; our counters read
  20,038,xxx bytes down. `RemoveUser` refuses new connections at once; re-add works. Two findings:
  - xray's `common/log` registers a stdout logger at all severities in `init()`, including access lines with
    destinations and the user email — we must replace it.
  - `RemoveUser` does not close open connections; ending a credential's open sessions needs our own mechanism, proven
    by a test (R2).

## 1. Scope

v1 combinations (one inbound = one port = one combination):

| transport | security | flow | use |
|---|---|---|---|
| `xhttp` | `reality` | none | default; direct to the node |
| `tcp` (RAW) | `reality` | `xtls-rprx-vision` (always) | direct, maximum throughput |
| `xhttp` | `tls` | none | behind a CDN, or direct with a Let's Encrypt name |
| `ws` | `tls` | none | CDN fallback for CDNs without streamed POST bodies |

Features in v1: several REALITY `server_names` and `short_ids` (each credential gets one, chosen deterministically);
client fingerprint `fp` in links; XHTTP `mode` auto / packet-up / stream-up / stream-one in the link (the server
accepts all); `x_padding_bytes`; a CDN address override (`cdn.host`, `cdn.port`); sniffing (http, tls, quic) so the
node resolves sniffed names; egress direct or WARP; per-credential rate limit, hard expiry and kick; torrent guard parity
with hysteria2.

Explicitly later: Xray JSON and sing-box subscription formats (the URI list carries everything v1 uses and Happ reads it
today); VLESS `encryption` (mlkem768x25519plus), REALITY `mldsa65`, VLESS fallbacks; XHTTP `downloadSettings`, xmux
tuning, header/cookie placement, HTTP/3; gRPC, httpupgrade, mKCP; per-inbound CDN hosts; a panel-side synthetic VLESS
check (`health.Dialers` has none, so these inbounds are skipped; adding one costs ~5 MB on the panel binary); a decoy
site for unknown XHTTP paths (Xray answers a bare 404); a TCP port-delivery check.

## 2. Node side: `internal/node/vless`

Files: `vless.go` (engine), `config.go` (spec → core.Config), `egress.go` (outbound handler, credState, UDP, kick),
`settings.go` (parse and validate, the second line of defence), `front.go` (TLS front, R3), `log.go` (Xray log
handler). Registered in `cmd/mistgate-node/wire.go` as `"vless": vless.Factory`; the agent lists `vless/1` in
`Hello.capabilities` when the engine is present (`internal/node/agent/l3.go` `capabilities()`).

Settings on the wire (`InboundSpec.settings_json` from the panel's `NodeSettings`; fixed field order, hashed; unknown
keys ignored):

```json
{"transport":"xhttp","security":"reality","flow":"",
 "reality":{"target":"www.example.org:443","server_names":["www.example.org"],"short_ids":["6ba85179","0f1e2d3c4b5a6978"],"private_key":"<base64url x25519>"},
 "xhttp":{"path":"/q8x2kd7w","host":"","mode":"auto","x_padding_bytes":"100-1000"},
 "ws":{"path":"","host":""}}
```

`Listen{network:"tcp", port}` and `Tls{mode, server_name}` are first-class fields; Tls only for `security: tls`.
`Credential.data_json` is `{"id":"<uuid>"}` — VLESS matches the UUID itself, so it reaches the node (same trust class as
the AWG PSK); document it in agent.proto next to the AWG `data_json` line.

Instance per inbound: `core.New(&core.Config{...})` with apps `dispatcher.Config{}`, `proxyman.InboundConfig{}`,
`proxyman.OutboundConfig{}`, `policy.Config{Level: {0: {Buffer: {Connection: 64 KiB}}}}` (explicit). One inbound tagged
with the inbound id: `ReceiverConfig{PortList, Listen, StreamSettings, SniffingSettings{Enabled, DestinationOverride:
http,tls,quic}}`, `vlessin.Config{Clients, Decryption:"none"}`. Stream settings: `ProtocolName "splithttp"` with
`splithttp.Config{Path, Host, Mode:"auto", XPaddingBytes}`, or `"tcp"`; REALITY `reality.Config{Dest: target,
Type:"tcp", ServerNames, PrivateKey, ShortIds}`; ws `websocket.Config{Path, Host}`. After `core.New`,
`outbound.Manager.AddHandler(ctx, egressHandler)` then `Start()`; the first handler added is the default, no router.
Blank imports: `app/{dispatcher,policy,proxyman/inbound,proxyman/outbound}`, `proxy/vless/inbound`,
`transport/internet/{tcp,splithttp,websocket,reality,tls}`. One instance per inbound (not shared): a spec change, key
change or failure touches only that inbound (mirrors hysteria2); ~0.6 MB per idle instance.

Engine contract (`internal/node/engine/engine.go`):
- Apply, same spec hash (users-only): diff by cred_id → `um.AddUser(ctx, &protocol.MemoryUser{Email: cred_id, Account:
  &vless.MemoryAccount{ID, Flow}})` / `um.RemoveUser(ctx, cred_id)` (a changed UUID = remove + add); `um` from
  `inbound.Manager.GetHandler(ctx, id).(proxy.GetInbound).GetInbound().(proxy.UserManager)`; removed credentials are
  kicked and their counters retired (as hysteria2 `retireMissing`). No restart.
- Apply, different spec hash: close the old instance, build a new one, report Restarted. `Enabled=false`: stop.
- Traffic: `egressHandler.Dispatch(ctx, link)` takes the destination from `session.OutboundsFromContext(ctx)` (last
  entry's Target) and the credential from `session.InboundFromContext(ctx).User.Email`; copies `link.Reader` →
  `Egress.TCP(addr)` and back through counting writers on `credState.up/down` (Up = client → internet); UDP/XUDP
  (`b.UDP` per buffer) through one `Egress.UDP` per link; torrentguard wrappers on both (move `torrentTCPConn` /
  `torrentUDPConn` from `internal/node/hysteria2/torrent.go` into `internal/node/torrentguard`). `Collect` swaps the
  counters to zero; a session = a credential with ≥1 open link, `Since` = when the first opened.
- Kick and removal: a cancel func per link; cancel closes the egress connection and interrupts `link.Reader` and
  `link.Writer` (`common.Interrupt`). A test must prove the client's session ends within 5 s for RAW+Vision and XHTTP
  (closing the egress socket alone is not enough).
- Capabilities `{RateLimitPerCred: true, HardExpiry: true}`; move hysteria2's token bucket (`index.go` `newLimiter`) to
  a shared spot.
- Health per inbound: state, detail, restarts, cert pin (TLS only); details `listen: …`, `reality_target:
  unreachable|no_tls13|no_mlkem` (the target probed once at start with a TLS 1.3 handshake offering X25519MLKEM768; a
  bad target is a warning, never a failure).
- Version `"xray-core v26.3.27"` from `debug.ReadBuildInfo`.
- Logs: `xlog.RegisterHandler` once in `New`; `*GeneralMessage` ≤ Warning → slog `source=xray`; access messages and
  everything below Warning dropped (they carry destinations and credential ids).
- No Vision splice (needs xray's freedom outbound); Vision copies through user space; the CPU cost is accepted.
- REALITY keys: per inbound from the panel's `InboundInitializer` (§3); a compromised node exposes only its own key.

Ports, TLS and the firewall:
- TCP 443 and the hysteria2 decoy (both default to 443, `internal/node/hysteria2/settings.go` `defaultTCPPort`): new
  optional engine interface in the agent `TCPPortUser{ TCPClaims(ctx, claimed map[uint16]string) }`; `reconcile` calls
  it after the removal loop and after the apply loop; `claimed` = the Listen port of every enabled tcp inbound in
  `next`; hysteria2 closes its `tcpMasq` on a claimed port (QUIC untouched; detail `masq_tcp: port 443 serves inbound
  <id>`) and rebinds on a port that became free; no-op without TCP inbounds. REALITY on 443 is itself a decoy
  (unauthenticated clients are forwarded to the target).
- Port 80 refused for VLESS (ACME HTTP-01).
- TLS mode (R3): xray's `tls.Config.GetTLSConfig` cannot take an external `GetCertificate`, so the engine runs its own
  TLS front: `tls.Listen` on the inbound port with `Env.Certs.Acquire(...).GetCertificate` and ALPN h2, http/1.1,
  acme-tls/1; each connection spliced to the inbound's xray instance listening with `security none` on the abstract unix
  socket `@mistgate-vless-<inbound_id>` (xray's XHTTP server speaks HTTP/1.1 and h2c there; WS works too). Lazy ACME
  issuance, renewal without restart and pin reporting then work as in hysteria2.
- Firewall: `hostctl.SyncInboundUDPPorts` → `SyncInboundPorts(udp, tcp)`, UFW comment prefix
  `mistgate-node-managed-tcp-v1-`; the provisioning 443/tcp rule (`ProvisionUFWTag`) counts as already open; doctor
  `port_conflicts` also covers TCP.
- Budget: +4.3 MB binary; ~+0.6 MB RSS per idle inbound, ~+1.9 KB per credential per inbound; up to 64 KiB per open
  link under load; the e2e idle check (node ≤ 100 MB) keeps guarding it (re-measure in R5 with hy2 + AWG + 3 VLESS).

## 3. Panel side: `internal/panel/protocols/vless`

Schema (x-group, x-order, x-critical as hysteria2): `port` (default 443, 1–65535, not 80); `transport` xhttp | tcp | ws
(default xhttp); `security` reality | tls (default reality). REALITY: `reality.target` host:port required, no default
(hint: TLS 1.3, X25519MLKEM768, preferably a site in the same ASN); `reality.server_names[]` (default [host of target],
1–8, hostnames, no wildcards); `reality.short_ids[]` (two generated, 8 and 16 hex; 1–8, unique, even length, ≤ 16 hex);
`reality.fingerprint` chrome | firefox | safari | ios | edge | random (default chrome). XHTTP/WS: `xhttp.path`
(generated `/` + 10 URL-safe chars; starts with `/`, ≤ 64, `[A-Za-z0-9/_-]`); `xhttp.host` (""); `xhttp.mode`
(default auto); `xhttp.x_padding_bytes` "a-b" 1 ≤ a ≤ b ≤ 4096 (default 100-1000); `ws.path` (generated), `ws.host`
(""). TLS only: `tls_mode` acme_domain | self_signed, `sni`, `cdn.host` ("" = the node address), `cdn.port` (443).
Egress direct | warp. Rules (Validate, re-checked in node `parseSpec`): tcp requires reality; ws requires tls;
self_signed requires cdn.host; flow is derived (Vision always for tcp + reality).

`InitInbound`: X25519 key via `crypto/ecdh`; State = private key; Public = `{"public_key":"<base64url>"}`; re-adding an
inbound reuses the parked key (`profile_rpc.go` `RetainedKey`). `BuildInbound`: `Listen{tcp, port}` with
`PortOverride`; `TLS` only for tls (hysteria2's server-name rules, `NeedsDomainError`); `NodeSettings` as §2; private key
from `PluginState`. `IssueCredential`: UUID v4 from crypto/rand; Secret = UUID, NodeData = `{"id":uuid}`; one
credential per device for all VLESS inbounds (like hysteria2), minted lazily; no PerDevice. `Clients`: Happ →
uri-list, Mihomo → mihomo-yaml; rides the existing link-apps toggle (`AppHapp`).

URI (keys alphabetical; `path`, `host`, `spx`, `extra` and the remark `url.QueryEscape`d; host = node address or
`cdn.host`, port = inbound port or `cdn.port`):

```
tcp+reality:   vless://<uuid>@<host>:<port>?encryption=none&flow=xtls-rprx-vision&fp=chrome&pbk=<pub>&security=reality&sid=<sid>&sni=<sn>&spx=<spx>&type=tcp#<name>
xhttp+reality: vless://<uuid>@<host>:<port>?encryption=none&fp=chrome[&extra=<json>][&host=<h>]&mode=auto&path=<p>&pbk=<pub>&security=reality&sid=<sid>&sni=<sn>&spx=<spx>&type=xhttp#<name>
xhttp+tls:     vless://<uuid>@<host>:<port>?encryption=none&fp=chrome[&extra=<json>]&host=<cdn.host|sni>&mode=<m>&path=<p>&security=tls&sni=<cdn.host|sni>&type=xhttp#<name>
ws+tls:        vless://<uuid>@<host>:<port>?encryption=none&fp=chrome&host=<h>&path=<p>&security=tls&sni=<sn>&type=ws#<name>
```

`sid`/`sni` index `fnv32(device_id) % n` (stable subscription bytes); `spx` = `/` + first 8 hex of
`sha256(device_id)`; `extra` only non-default fields (today `{"xPaddingBytes":"…"}` when not 100-1000 — literal
defaults in links are a fingerprint); name `<node> · vless · <port>`; no URI for an address that is not a plain host.

Mihomo (yaml.v3 nodes like `hysteria2/render_mihomo.go`): `name`, `type: vless`, `server`, `port`, `uuid`, `udp: true`,
`packet-encoding: xudp`, `tls: true`, `servername`, `client-fingerprint`, `skip-cert-verify: false`; per transport
`network: tcp` + `flow: xtls-rprx-vision`, `network: xhttp` + `xhttp-opts: {path, mode, host?, x-padding-bytes?}`,
`network: ws` + `ws-opts: {path, headers: {Host}}`; REALITY `reality-opts: {public-key, short-id}`.

Framework changes: `access/sub.go` passes `InboundPublic` for every protocol (also the profile preview);
`fleet/desired.go` `needsAWG` → `requiredCap(spec)` (awg → awg/1, vless → vless/1), generic withheld text;
`listenOverlap` compares Network (a TCP inbound on 443 next to hysteria2 is not a clash; info reason
`hy2_decoy_moves`). Doctor: `Protocol.Doctor()` is never called today, so v1 returns nil; checks live on the node
(health detail codes, TCP `port_conflicts`), i18n for the new detail codes.

## 4. Behind a CDN (any CDN)

Client → CDN (`cdn.host`, the CDN's TLS) → origin (the node's DNS-only name, e.g. de1.example.com:443, our TLS front)
→ xray XHTTP. Origin certificate `acme_domain`; `self_signed` only when the CDN accepts unverified origins. No
plain-HTTP origins (the VLESS header would cross in clear). Mode `packet-up` (the default when `cdn.host` is set): many
CDNs buffer whole request bodies; packet-up sends small POSTs (≤ `scMaxEachPostBytes`, default 1 MB, under the CDN's
body limit). `stream-up` only on CDNs that stream request bodies. Downlink is one long GET: keep Xray's defaults
(`Content-Type: text/event-stream`, `X-Accel-Buffering: no`, `Cache-Control: no-store`) — cdnprobe found workerd
buffered and compressed text/plain but streamed event streams. Idle timeouts (e.g. 100 s) are covered by Xray's server
padding keepalive (`scStreamUpServerSecs`, random 20–80 s) — leave it on. Leave the server's `host` empty (CDNs may
rewrite Host); clients send host = sni = cdn.host. Path random per profile; the CDN must allow GET and POST and not
cache it. ALPN h2, no h3 in v1. WS (`ws+tls`) is the fallback for CDNs that support Upgrade but break streamed POSTs.
Accounting unchanged: the node counts payload; CDN egress is billed separately.

## 5. Rounds

Each round merges with the gate green; existing VPS behaviour is unchanged until a VLESS profile exists; release only on
the owner's "да".

| Round | Changes | Proof | Risks |
|---|---|---|---|
| R1 TCP inbound groundwork (no xray; implemented 2026-10-09) | `TCPClaims` in `agent/apply.go` + hysteria2; `listenOverlap` network-aware + the decoy info reason; `hostctl` TCP firewall sync; doctor `port_conflicts` covers TCP | unit: a hy2 decoy released and rebound under a fake claim, both call orders; UFW golden tests (tcp/udp, provision rule present); overlap table test; WSL e2e `--m3` unchanged | UFW edits — same ownership-tag discipline as UDP |
| R2 engine with REALITY (RAW+Vision, XHTTP; implemented 2026-10-09) | go.mod xray-core v1.260327.0 (MVS raises gvisor, wireguard, utls 1.8.3-pre); `internal/node/vless`; wire.go; `vless/1`; torrentguard wrappers and limiter shared | in-process xray client only in `_test.go`: traffic through both, counters ±1 %; users-only Apply no restart, sessions kept; add/remove/kick end sessions ≤ 5 s; expiry, rate limit, private-destination block; WARP egress fails closed; spec change restarts only that inbound; log handler drops access lines; Observed hash test; schema-to-engine test; WARP registration tests re-run (utls bump) | xray API churn (compile errors — pin and bump deliberately); kick semantics; Vision CPU |
| R3 TLS front, XHTTP+TLS, WS+TLS | `front.go`, abstract unix sockets (temp-dir socket on non-Linux test hosts) | a fake CertSource rotates the cert under a live session: no drop, new pin reported; h2 and http/1.1; WS upgrade; ACME lazy issuance via the certs tests | the extra copy per byte on TLS inbounds — measure in WSL |
| R4 panel plugin | schema, Validate, BuildInbound, InitInbound, IssueCredential, URI + Mihomo render; builtin registry; requiredCap; InboundPublic in sub.go and preview; docs en/ru; agent.proto comments; `hy2_decoy_moves` uses Hysteria2's effective `masquerade.tcp_port` (absent means 443; 0 means no decoy), parsed like `hy2TCPPort` in `agent/doctor.go:157`, and is rendered in the inbound-check UI; `host_firewall_sync_failed` en/ru text covers TCP as well as UDP because `ports` may contain `tcp:8443`; `docs/en/getting-started/requirements.md` and `docs/ru/getting-started/requirements.md` describe both tagged transports | golden URI and YAML per combination incl. hostile node names; Validate tables; GOOS=js build; prod-copy subscription diff with the owner's OK (identical for users without a VLESS profile) | every implicit device gains one VLESS credential row on its next fetch (one write per user, D1 too) |
| R5 e2e | `m3_vless` in `scripts/e2e/m3.sh` (`--vless`, also in `--m3`); `xray_bin` from `github.com/xtls/xray-core/main@v1.260327.0` into `$M3_CACHE`; `scripts/e2e/tlsdest` local TLS 1.3 REALITY target | profiles xhttp+reality, tcp+reality, xhttp+tls (self-signed, client pinned); Happ fetch → 3 vless:// lines → python → xray client with socks; 20 MB each, usedBytes 0.95–1.10; disable kills the running download and refuses new, enable restores; Mihomo profile `mihomo -t` + traffic per proxy; RSS with all inbounds, node idle ≤ 100 MB; step 11 greps logs for UUIDs | e2e +~2 min |
| R6 CDN and polish | `reality_target` detail in the UI, "VLESS behind a CDN" docs page, form hints | manual CDN test by the owner (not CI) | — |

R1 implementation note: TCP firewall synchronization covers direct TCP listener ports; R1 introduces no TCP port-hop ranges.

R2 implementation notes: short IDs are zero-padded to Xray's required 8-byte representation.
R2 round 2, item 1: TCP copies track upload and download ends independently. When the destination closes, the client writer is closed at once and the upload gets 1 s without traffic (xray's UplinkOnly). When the client half-closes, the answer gets 30 s without traffic (not xray's 1 s DownlinkOnly, which cuts a slow answer; not the full idle timeout, since xray-based clients never half-close and an upload end mostly means a client that has gone).
R2 round 2, item 2: inbound stop rejects new dispatches, cancels links, and closes tracked raw TCP connections keyed by inbound generation.
R2 round 2, item 3: connection-scoped Xray logs are dropped, and forwarded messages have UUIDs and IP addresses scrubbed.
R2 round 2, item 4: VLESS links use a 300-second activity timeout updated by both copy directions; tests can inject a shorter timeout. An idle link ends like a natural one: its mux siblings keep the shared connection. The timer checks once per interval, so a link can live up to twice the timeout.
R2 round 2, item 5: REALITY probe handshake timeouts report unreachable, while inbound cancellation suppresses the probe result.
R2 round 2, item 6: the XHTTP server always uses auto mode; the client profile continues to choose its own mode.
R2 round 2, item 7: natural link completion closes that link and its egress without closing the inbound connection or sibling flows.
R2 round 2, item 8: the outbound handler, Xray instance, and user manager are assigned before the instance starts.
R2 round 2, item 9: torrentguard TCP CloseWrite flushes any held prefix before half-closing, and wrapper tests live with torrentguard.
R2 round 2, item 10: Xray start messages and closed-network XHTTP accept errors are filtered.
R2 round 2, item 11: Xray v1.260327.0 does not expose an ErrorLog hook for its XHTTP HTTP server; no global logger override is installed, so net/http diagnostics may still include client IPs on the standard logger.
R2 round 2, item 12: torrent-guard setting changes no longer restart VLESS inbounds.
R2 round 2, item 13 (corrected in round 3): kicking an XHTTP client that stopped reading ends its session at once (Kick returns, Collect drops it, the destination socket closes), but one goroutine stays in xray's blocked `httpServerConn.Write` until the client reads again or its TCP gives up. Only an inbound stop closes the raw connection; mapping a session to its raw connection is not safe (xray fills `splitConn.remoteAddr` from the client's `X-Forwarded-For`). Accepted.
R2 round 3: a link is interrupted only through `common.Interruptible`, never `Close`. On XHTTP the writer is xray's BufferedWriter, whose `Close` waits for the lock that a write blocked on a non-reading client holds, so a kick, removal or expiry hung the engine. The connection tracker's wrapper implements `finalmask.TcpMaskConn`, so Vision keeps readv/writev on the raw socket. Generations are numbered process-wide (the tracker map is global).
REALITY first connections: xtls/reality measures the target's post-handshake records once per process for each target, SNI and ALPN class. The measurement takes at least 5 s (it reads to a 5 s deadline). An authenticated client that arrives before the result is stored sleeps 5 s and checks again (`tls.go`). So in the first seconds after an agent start, or after a new target or SNI appears, handshakes take 5-10 s longer, once. A target that is down during the measurement leaves empty lengths for the life of the process (no post-handshake records until the next agent restart). Accepted for R2; measure the client-visible effect in R5 before adding anything (e.g. persisting the lengths). The test fixture stores the result for its local target up front.
Upstream on Windows: xray's readv there is a blocking WSARecv (`common/buf/readv_windows.go`), and closing that socket waits for the read. An xray client on Windows using Vision can therefore keep an app connection open after our server ended the session, as long as the app sends nothing. The package's TestMain runs the tests on Windows with `XRAY_BUF_READV=disable`. Nodes run Linux, where Close interrupts the read.

## 6. Decisions on the open questions

1. Xray version: stable v26.3.27, bumped deliberately (not v26.9.x pre-releases). — owner: yes (2026-10-09)
2. REALITY target: no default; required; the node probe checks TLS 1.3 and MLKEM; no shared-CDN sites (can be abused as
   a forwarder). — owner: yes (2026-10-09)
3. CDN host: profile-level `cdn.host`, one origin node per CDN profile in v1; per-inbound later. — owner: yes (2026-10-09)
4. Self-signed origin only behind a CDN; direct `tls` needs Let's Encrypt. — owner: yes (2026-10-09)
5. Client UUIDs in clear in the node's state file: accept and document (inherent to VLESS). — owner: yes (2026-10-09)
6. Xray JSON for Happ: not in v1; add with sing-box when a feature needs it. — owner: yes (2026-10-09)
7. REALITY on TCP 443 moves the hysteria2 TCP decoy aside on the same node: yes (REALITY forwards probes to a real
   site). — owner: yes (2026-10-09)
8. Panel synthetic VLESS check (+~5 MB panel binary): later. — owner: yes (2026-10-09)

## 7. Hysteria2 and VLESS on one node (added 2026-10-09)

Agreed with the owner on 2026-10-09. Where this section and §1-§6 disagree, this section wins. It replaces these lines:

| Where | Old | New |
|---|---|---|
| §1 "Explicitly later" | "...; a TCP port-delivery check." | Removed (now §7.4). Added to "later": a TCP probe from a sender node that completes a real REALITY handshake (needs xray on the sender); a Mihomo `fallback` group per server (hy2 first, then VLESS), §7.6. |
| §2, `Tls{...}` sentence | "Tls only for `security: tls`" | "Tls for `security: tls`, and for `reality` with `target: "self"` (the ACME certificate of the node's own site, §7.5)" |
| §2, Health line | `reality_target: unreachable\|no_tls13\|no_mlkem` | Add: "not probed for `self`; cert pin and expiry are reported for `tls` and `self`" |
| §2, Ports bullet 1, last sentence | "REALITY on 443 is itself a decoy (unauthenticated clients are forwarded to the target)." | Add: "With `target: "self"` the VLESS engine serves the node's own site behind REALITY (§7.5). `TCPClaims` stays as R1 made it." |
| §2, TLS mode (R3) | ALPN `h2, http/1.1, acme-tls/1` | Add: "acme-tls/1 hellos are answered for every ACME name of the node (§7.3)" |
| §3, schema, REALITY | "`reality.target` host:port required, no default; `reality.server_names[]` (default [host of target] ..." | "`reality.target_kind` self \| site (default self). `reality.target` (host:port, required) and `reality.server_names[]` exist only with site; their rules are unchanged." |
| §3, framework changes | info reason `hy2_decoy_moves` | `hy2_decoy_paused` (§7.1) |
| §5 rows R3-R6 | - | §7.8 |
| §6 decision 2 | "REALITY target: no default; required; ..." | "Default `self` (the node's own site; needs a domain). Another site stays possible and is then required; the probe and the no-shared-CDN rule apply to it." |
| §6 decision 7 | "...moves the hysteria2 TCP decoy aside..." | "...pauses the Hysteria2 TCP site while it holds the port. With target self, REALITY shows the same site." |
| Final "Note (R1)" | - | Replaced by §7.1 |

### 7.1 The reason code (R4)

Rename `hy2_decoy_moves` to `hy2_decoy_paused`. The site does not move anywhere: hysteria2 closes its TCP site listener while an
enabled TCP inbound claims that port and binds it again once the port is free (`internal/node/hysteria2/hysteria2.go` `TCPClaims`,
`internal/node/agent/apply.go` `syncTCPClaims`). Renaming costs nothing now: only `internal/panel/access/profile_rpc.go` and
`inbound_check_test.go` use the code; no UI shows it and nothing stores it.

When it fires: `inboundWarnings` (`profile_rpc.go`) checks both directions: the inbound being added or enabled listens on TCP P
and an enabled hysteria2 neighbour's site port is P; or the inbound is hysteria2 with site port P and an enabled TCP neighbour
listens on P. The site port is `masquerade.tcp_port` of the hy2 spec: absent means 443, 0 means none (the rule of
`agent/doctor.go` `hy2TCPPort`). This replaces the hard-coded `Port == 443`. The panel never sends `tcp_port`
(`protocols/hysteria2/hysteria2.go`), so in practice the port is 443. `nodeListen` also keeps the neighbour's built `spec`,
which `nodeInbounds` already builds.

Params: `node` (node name), `port` (the shared TCP port), `hy2` (the hy2 profile name), `vless` (the TCP profile name), `via`
(`self` | `site` | `tls`, from the VLESS NodeSettings `security` and `reality.target`), `target` (host of the REALITY target;
only with `site`), `port80` (`open` | `closed` | `unchecked`; only when `via=site` and the hy2 spec is `acme_domain`, since no
other case depends on :80, §7.3; R4 always sends `unchecked`, R4.1 reads the stored port-80 row, §7.4).

Texts in `web/src/i18n/ops.ts`, next to `node.check.warp.*`. The UI shows the main sentence, then the port80 sentence if any,
then the docs link.

| key | en | ru |
|---|---|---|
| `node.check.decoy.self` | TCP {port} on {node} goes to "{vless}". The site of "{hy2}" stays there: REALITY shows it with the node's own certificate, and that certificate keeps renewing. | TCP {port} на {node} отходит «{vless}». Сайт «{hy2}» там остаётся: его показывает REALITY с собственным сертификатом ноды, и сертификат продолжает обновляться. |
| `node.check.decoy.tls` | TCP {port} on {node} goes to "{vless}". The site of "{hy2}" is off while it does and comes back when the port is free; meanwhile TCP {port} answers a plain 404. The certificate keeps renewing through "{vless}". | TCP {port} на {node} отходит «{vless}». Сайт «{hy2}» на это время выключается и вернётся, когда порт освободится; пока TCP {port} отвечает пустой страницей 404. Сертификат продолжает обновляться через «{vless}». |
| `node.check.decoy.site` | TCP {port} on {node} goes to "{vless}". The site of "{hy2}" is off while it does and comes back when the port is free; visitors of TCP {port} see {target} instead (REALITY passes them on). | TCP {port} на {node} отходит «{vless}». Сайт «{hy2}» на это время выключается и вернётся, когда порт освободится; на TCP {port} вместо него откроется {target} — REALITY передаёт посетителей туда. |
| `node.check.decoy.port80.open` | Port 80 reaches Mistgate here, so the Hysteria2 certificate renews over it. | Порт 80 до Mistgate здесь доходит — сертификат Hysteria2 обновится через него. |
| `node.check.decoy.port80.closed` | Port 80 does not reach Mistgate here, so the Hysteria2 certificate will stop renewing. Open TCP 80 at the provider, move "{vless}" to 8443, or make its REALITY target "This node's site". | Порт 80 до Mistgate здесь не доходит — сертификат Hysteria2 перестанет обновляться. Открой TCP 80 у провайдера, перенеси «{vless}» на 8443 или поставь ему цель REALITY «Сайт этой ноды». |
| `node.check.decoy.port80.unchecked` | The Hysteria2 certificate now renews only through TCP 80. Check the ports on the node's Profiles tab to see whether it is open. | Сертификат Hysteria2 теперь обновляется только через TCP 80. Проверь порты на вкладке «Профили» ноды — открыт ли он. |
| `docs.vlessHy2` | VLESS and Hysteria2 on one node | VLESS и Hysteria2 на одной ноде |
| `docs.vlessHy2.url` | https://mistgate.app/guide/vless-and-hysteria2/ | https://mistgate.app/ru/guide/vless-and-hysteria2/ |

### 7.2 Warnings in three places, plus a docs page (R4, UI by Claude)

(a) VLESS profile form: in `web/src/screens/profiles/editor.tsx` `extra(f)`, when `info.id === "vless" && f.id === "port"` and
the port is 443: target `self` → muted tone `profiles.vless.port443.self`; target `site` → warn tone `profiles.vless.port443.site`;
both followed by the docs link. Keys in `web/src/i18n/profiles.ts`:

| key | en | ru |
|---|---|---|
| `profiles.vless.port443.self` | On a node with Hysteria2 this profile takes TCP 443 from its site and shows the same site through REALITY. Nodes need a domain. | На ноде с Hysteria2 этот профиль забирает TCP 443 у её сайта и показывает тот же сайт через REALITY. Нодам нужен домен. |
| `profiles.vless.port443.site` | On a node with Hysteria2 this profile takes TCP 443: the Hysteria2 site there is off while it does, and the Hysteria2 certificate then renews only through TCP 80. "This node's site" as the target, or port 8443, keeps both. | На ноде с Hysteria2 этот профиль забирает TCP 443: сайт Hysteria2 там на это время выключается, а её сертификат обновляется только через TCP 80. Цель «Сайт этой ноды» или порт 8443 сохраняют и то и другое. |

(b) Attaching to a node (inbound check): a new `DecoyNotice` in `web/src/screens/node/inbound-check.tsx`, modelled on
`WarpWarnings` and rendered next to it (`add-inbound.tsx`, `node/profiles.tsx`). Rows of the bulk "put on nodes" dialog
(`profiles/deploy.tsx`) get the same sentence through `portNotes` (`web/src/lib/port-check.ts`). One text builder,
`decoyNote(t, params)` in `port-check.ts`, serves both.

(c) Hysteria2 form: there is no decoy-port field (`masquerade.tcp_port` is a node-only key the panel never sends), so the hint goes
on the Masquerade field. Replace `profiles.f.hysteria2.masquerade.type.hint`:
- en: "What the node answers to anything that is not a Hysteria2 client: over HTTP/3 on the profile's port and over HTTPS on TCP 443. The site stays on 443 on purpose, a site on an odd port hides little; a VLESS profile on TCP 443 of the same node takes the port over."
- ru: "что нода отвечает всем, кто не клиент Hysteria2: по HTTP/3 на порту профиля и по HTTPS на TCP 443. Сайт нарочно стоит на 443 — сайт на нестандартном порту почти ничего не скрывает; VLESS-профиль на TCP 443 той же ноды забирает этот порт себе"
and add the docs link through `extra` for `info.id === "hysteria2" && f.id === "masquerade.type"`. We do not add the field: moving
the site is the wrong fix, and the field would need `omitempty` for 443, otherwise every hy2 spec hash changes and every hy2
inbound restarts.

Docs page: `docs/en/guide/vless-and-hysteria2.md` and `docs/ru/guide/vless-and-hysteria2.md` with front matter `title` and
`description`, listed under Guide in `docs/{en,ru}/index.md` (otherwise `site/build.mjs` leaves it out of the sidebar). Sections:
1. What listens where: hy2 on UDP 443 (QUIC) plus its site on TCP 443; VLESS on its TCP port; TCP 80 for Let's Encrypt.
2. Three ways to share, as a table: REALITY with this node's site on 443 (recommended, needs a domain); REALITY with another site
   on 443 (site paused, renewal only through 80); VLESS on 8443 (both keep 443; 8443 must be open at the provider). TLS/CDN mode in one line.
3. Certificates: the §7.3 rule and the `cert_expiry` alert.
4. The TCP rows of the port check.
5. Trying VLESS with a few people first (§7.6).
Also edit `docs/{en,ru}/guide/hysteria2.md` (the "keep TCP 443 free" sentence) to link the page, and `docs/{en,ru}/guide/profiles.md`
("Today there is one: `warp_missing`" becomes two warnings).

### 7.3 Certificate renewal when VLESS holds TCP 443 (R3 node, R4/R4.1 panel)

A node's Let's Encrypt certificates renew when TCP 80 reaches Mistgate (HTTP-01), or TCP 443 is held by a Mistgate TLS listener
that answers `acme-tls/1` (TLS-ALPN-01): the hy2 site, the VLESS TLS front, or VLESS REALITY with self. REALITY with another site
on 443 forwards Let's Encrypt's validation to that site, which leaves only HTTP-01. autocert tries tls-alpn-01 first and switches to
http-01 on a new order (x/crypto `acme/autocert` `verifyRFC`, `supportedChallengeTypes`), so in that setup each issuance burns one
failed validation (Let's Encrypt allows 5 per name per hour); acceptable.

Changes:
1. R3, `internal/node/certs/certs.go` `Acquire`: the returned `GetCertificate` sends any hello whose only ALPN is `acme-tls/1` to
   the ACME entry of its SNI (`s.entryFor(hello.ServerName)`), whatever the inbound's own mode; everything else keeps today's path.
   Any of our TLS listeners on 443 then answers TLS-ALPN-01 for every name on the node. This also fixes an existing gap: two hy2
   profiles with different names on one node share a single site listener (`tcpMasqOwner`), so today the second name relies on :80.
2. R3, doctor: today an ACME hy2 certificate on a node where VLESS holds the site port gets no expiry alert: `TLSDown` is set
   (`agent/doctor.go`), so `checks_cert.go` skips reading the served certificate and the ACME expiry stays "unknown"; the panel's
   fallback (`health/eval.go`, `!hasCert`) never runs because the doctor still reports `cert_expiry`. Fix: `agent/doctor.go` fills
   `Inbound.CertNotAfter` with the later of the apply-time value and the live `Health().CertNotAfter`; `checkCertExpiry` falls back to
   it for every mode whenever the served certificate cannot be read. Ceiling: the live value is the leaf last handed to a client
   handshake, so after a background renewal with no client since it lags; worst case a false "expiring" warning, cleared by the next client.
3. R3: `tlsPortOf(spec)` replaces `hy2TCPPort`: hy2 → its site port; VLESS `tls` → its listen port; VLESS `self` → 0 (its
   certificate is behind REALITY, and before issuance the fallback self-signed certificate would read as a healthy 10-year one).
4. R4/R4.1: the `port80` warning param (§7.1), the renewal line of the port check (§7.4), the docs.
VLESS on port 80 stays refused (§2).

### 7.4 TCP part of the port check (UDP gating in R4; the TCP check is step R4.1)

R4 must fix first: the UDP check ignores the network. `fleet/ports.go` `udpCheckPorts` and `portLossyEvents` read `port` from any
inbound's settings; `access` `checkPort` (`port_check.go`), `cachedPortLossy`/`portLossyRefusal` in CreateInbound, UpdateInbound,
UpdateProfile and `freePortFor` run on every inbound. A VLESS port would be UDP-checked, refused for UDP loss and raise
`port_lossy`. Gate all of them on `Listen.Network == "udp"`; in fleet, build the specs with `f.buildSpec` instead of reading the raw `port`.

How it proves that our process answered (the nftables counter used for UDP would also count a foreign nginx):

| Port and role | Probe | "ours" when |
|---|---|---|
| 80, `acme` (only if the node has an ACME inbound) | The target agent arms a one-time mark; the sender sends `GET /.well-known/acme-challenge/<hex token>` (Host = node address). This is Let's Encrypt's own path, so a provider filter on it shows up too. | Status 200 and body = `<hex answer>` |
| hy2 site port (when not claimed), `site` | TLS handshake, SNI = spec server name | Chain valid for the SNI (system roots), or leaf sha256 = the reported pin (self-signed) |
| VLESS port, `vless` (tls/self) | Same | Same |
| VLESS port, `vless` (REALITY with another site) | TLS handshake, SNI = `server_names[0]` | Chain valid for the target name, so our REALITY forwarded it. A full REALITY-authenticated probe stays "later". |

No static marker path, so the node gets no fingerprint; each mark is one-shot and expires.

Proto `agent.v1` (additive; `api_version` stays 1; section "TCP DELIVERY CHECK"; capability `tcpcheck/1`; next free
`ConnectResponse` fields 28 and 29):

```proto
HttpMark http_mark = 28;  TcpProbe tcp_probe = 29;
message HttpMark { string request_id = 1; bytes token = 2; /*16*/ bytes answer = 3; /*16*/ uint32 hold_s = 4; /*1-60*/ }
// -> CommandResult{ok, params{"http01": "serving"|"busy"|"off"}}  busy = another program holds :80; off = no ACME name, :80 unused
message TcpProbe { string request_id = 1; string host = 2; repeated TcpTarget targets = 3; /*1-8*/ }
message TcpTarget { uint32 port = 1; string kind = 2; /*"http_mark"|"tls"*/ string sni = 3; string pin_sha256 = 4; bytes token = 5; bytes answer = 6; }
// -> CommandResult{ok, params{"family": "4"|"6", "t<port>": "ours"|"other"|"refused"|"timeout"}}; 5 s connect, 8 s per port, ports in parallel
```

Node: `certs.Source.Mark(token, answer, until)` and `HTTP01State()`, the mark checked in `serveHTTP` before the Host routing, one
mark at a time (else `busy`); agent handlers as `agent/udpcheck.go` (`bad_params`, `busy`, at most 4 probes at once); new package
`internal/tcpcheck` (crypto/tls and net/http only; no xray; builds for js) with `Probe`, used by the node and by the VPS panel.

Panel: `fleet` computes targets from the node's built specs with a pure `tcpTargets(specs, pins)` (80 first, then by port, at most
8), run after the UDP part in the same `checkPorts` run under the same node lock, so the scheduled `CheckPortsAsSystem` covers TCP
too. Sender: same order as UDP (`udpPortSenderOrder`), must list `tcpcheck/1`; the VPS panel only as a fallback (`panelSendsUDP`,
false in `ports_js.go`); the target never probes itself. Port 80 needs `tcpcheck/1` on the target (arming), else the row says
`agent_too_old`; TLS rows need nothing from the target.

Edge edition: the panel never sends, a node does. Workers `connect()` exists (plain TCP; ports 25 and Cloudflare ranges blocked;
bare-IP destinations not documented), but the Go wasm panel has no socket bridge and the TLS rows need pin and chain checks that
`connect()` with `secureTransport` does not expose. One node on an edge panel → `no_sender`, as UDP today.

Storage: migration `node_tcp_check(node_id → node ON DELETE CASCADE, port, role, alpn INTEGER, profile TEXT, verdict, reason,
sender, checked_at, PRIMARY KEY(node_id, port))`; `alpn` = the listener answers TLS-ALPN-01 (site / tls / self on 443).
`store.TCPChecks(ctx, nodeIDs...)`; `PutTCPChecks` replaces a node's rows in one batch. `GetNode` reads them (one extra query on the
node page, not a budgeted D1 path). `admin.v1`: `message TcpCheck { port, role, alpn, profile, verdict, reason, sender, checked_unix }`;
`CheckPortsResponse.tcp = 4`; `GetNodeResponse.tcp_checks` = next free number; update the CheckPorts comment.

UI: `web/src/screens/node/udp-ports.tsx` becomes "Ports" with TCP rows and one renewal line. `renewalOf(rows)` in
`web/src/lib/port-check.ts`: `ok80` if port 80 is `ours`; else `ok443` if any `alpn` row on 443 is `ours`; else `risk` (with the
VLESS profile if a `site` REALITY holds 443); else none (no ACME). Texts in `web/src/i18n/ports.ts`:
- `ports.title`: "Ports" / "Порты"
- `ports.tcp.ours`: "Reaches Mistgate" / "Доходит до Mistgate"
- `ports.tcp.other`: "Another server answers" / "Отвечает чужой сервер"
- `ports.tcp.busy`: "Held by another program on the node" / "Занят другой программой на ноде"
- `ports.tcp.refused`: "Closed" / "Закрыт"
- `ports.tcp.timeout`: "No answer (filtered)" / "Нет ответа (фильтруется)"
- `ports.renewal.ok80`: "Port 80 is open: Let's Encrypt certificates on this node renew." / "Порт 80 открыт — сертификаты Let's Encrypt на этой ноде обновляются."
- `ports.renewal.ok443`: "Port 80 is closed, but TCP 443 answers Let's Encrypt: certificates renew." / "Порт 80 закрыт, но TCP 443 отвечает Let's Encrypt — сертификаты обновляются."
- `ports.renewal.risk`: "Port 80 is closed and TCP 443 does not answer Let's Encrypt{why}: certificates on this node will stop renewing. Open TCP 80 at the provider, move VLESS to 8443, or make its REALITY target "This node's site"." / "Порт 80 закрыт, а TCP 443 не отвечает Let's Encrypt{why} — сертификаты на этой ноде перестанут обновляться. Открой TCP 80 у провайдера, перенеси VLESS на 8443 или поставь ему цель REALITY «Сайт этой ноды»."
- `ports.renewal.risk.vless` (fills {why}): " (it goes to "{profile}", REALITY with another site)" / " (его занимает «{profile}» — REALITY с другим сайтом)"

### 7.5 REALITY target "self" (R3 node, R4 panel)

Verified in the dependencies:
- Unix socket target: xray-core v1.260327.0 dials the REALITY target with `net.Dialer.DialContext(ctx, Type, Dest)`
  (`transport/internet/reality/config.go`), so `Type: "unix"` with `Dest: "@name"` (abstract socket on Linux) works.
- Let's Encrypt passes through: in `github.com/xtls/reality` (v0.0.0-20260322125925-9234c772ba8f) a hello that fails
  authentication or has an unknown SNI is spliced raw to the target (`tls.go`), the bytes already read mirrored by `MirrorConn`,
  so the TLS-ALPN-01 hello reaches our site listener unchanged.
- The target must always be up: `Server` dials the target for every connection before reading the hello; for authenticated
  clients it also waits for the target's ServerHello (TLS 1.3, X25519 or X25519MLKEM768 share) to copy its record lengths.
- Post-quantum key exchange is on: go.mod is `go 1.27` with no `godebug` or `CurvePreferences` override, so crypto/tls picks
  X25519MLKEM768 by default.

Where the site lives: in the VLESS engine, not in hysteria2. `TCPClaims` does not change (hy2 closes its TCP listener on the claimed
port, as R1 does). Moving hy2's listener onto loopback would tie every VLESS connection to hy2's lifecycle (a hy2 restart or
removal, or a node without hy2, would cut VLESS). The VLESS engine serves the same `Env.Masquerade(id)` handler with the same
certificate (certs.Source shares one ACME entry per name). One difference: no `Alt-Svc: h3` header (VLESS does not know hy2's QUIC
port); if a probe ever keys on it, the agent's `masquerade()` wraps the handler with the node's hy2 Alt-Svc.

Node (R3, `internal/node/vless/front.go`, reusing the R3 TLS listener code):
- Site listener per self inbound on `@mistgate-vless-site-<inbound_id>` (a temp-dir path on non-Linux test hosts): an `http.Server`
  set up like `hysteria2/masq.go` (handler `Env.Masquerade(id)`, ALPN `h2, http/1.1, acme-tls/1`, MinVersion TLS 1.2, same timeouts).
- Start it before `core.New`, stop it after the instance closes; `reality.Config{Dest: that, Type: "unix", ServerNames: [TLS.ServerName]}`.
- If it fails to listen, the inbound fails (with no target REALITY drops every connection).
- Certificate: `acme-tls/1` → `cert.GetCertificate` (routed, §7.3); an ACME leaf valid now → `cert.GetCertificate`; otherwise the
  persisted self-signed certificate for the same name (a second `Acquire` with `TLSSelfSigned`), because autocert blocks a first
  handshake for minutes while it issues and that would block real clients too.
- Record lengths: xray's REALITY learns the target's post-handshake record lengths once per process
  (`DetectPostHandshakeRecordsLens` when the listener starts; it takes at least 5 s, and an authenticated client arriving before
  the result sleeps 5 s, see the R2 notes). First check with a test whether the lengths of a Go TLS 1.3 site depend on the
  certificate at all (NewSessionTicket and h2 SETTINGS should not): if the self-signed and the ACME certificate give the same
  lengths, no re-detection is needed. If they differ, run the detection again after the first issuance and store the new lengths
  over the old ones in `reality.GlobalPostHandshakeRecordsLens` (exported). Never delete the old keys: that opens a window of at
  least 5 s in which every new authenticated handshake sleeps 5-10 s. Keep the order: the site listener is up before the
  REALITY listener starts, or the first measurement stores empty lengths for the life of the process.
- No `reality_target` probe for self; Health reports cert pin and expiry from `cert.Info()` (zero until issued).
- `parseSpec`: `reality.target == "self"` requires `TLS{acme_domain, server_name}` and `server_names == [server_name]`.

Wire settings: `"reality":{"target":"self","server_names":["de1.example.com"],"short_ids":[...],"private_key":"..."}` plus
`Tls{acme_domain, "de1.example.com"}`.

Panel (R4):
- Schema `reality.target_kind` self | site (default `self`, x-critical, segmented; "This node's site" / "Another site", ru «Сайт
  этой ноды» / «Другой сайт»). `reality.target` and `reality.server_names` only with site (the editor's `hidden` set hides them for self).
- `BuildInbound` for self: name = `TLSServerNameOverride`, else `Node.Address` if it is a hostname, else
  `NeedsDomainError{Address, Reality: true}`; `buildRefusal` maps it to a new refusal code `reality_needs_domain` (in `refusals` of
  `inbound-check.tsx` and the reader in `deploy.tsx`). en: "This node's site needs a Let's Encrypt certificate, which is issued for
  domains only, and the address of {node} is the IP {address}. Enter a domain whose A record points to this IP, or use a profile whose
  REALITY target is another site." ru: "Сайту этой ноды нужен сертификат Let's Encrypt, а его выдают только на домен; у {node} же
  адрес — IP {address}. Впиши домен, у которого A-запись указывает на этот IP, или возьми профиль, где цель REALITY — другой сайт."
- Deploy dialog: `editor.tsx` `certOf` passes `tlsMode: "acme_domain"` for VLESS self, so the existing `needsDomain` marks IP-only
  nodes before the click.
- URI: `sni` = the spec's server name; the rest as in §3.

What self gives: the SNI is the node's own name and resolves to the node's IP; TCP 443 and QUIC 443 show the same site with the same
Let's Encrypt certificate; renewal works without :80; no third-party site to pick, probe or forward strangers to.

### 7.6 Friends compare protocols (R4)

- Both protocols in the subscription: no new code. VLESS rides the existing link-apps toggle; a user gets VLESS when their group has
  the VLESS profile.
- Names come from the same pipeline for every protocol: template `{flag} {country} · {profile}` (`subsettings.DefaultNameTemplate`)
  plus " · N％" load for Happ only (`subs/names.go` `renderRemarks`). A new profile's name is `${displayName} · ${port}`
  (`profiles/after-create.tsx` `autoName`), so with DisplayName "VLESS" entries read "🇩🇪 DE · VLESS · 443 · 1％" next to
  "🇩🇪 DE · HY2 · 443 · 1％" (28 UTF-16 units, inside Happ's 30).
- Per-user traffic is already stored and shown: `traffic_bucket` is keyed by user, node, protocol and hour; the user page lists
  node × protocol for the current period (`store/access_user.go` `NodeTraffic` → `user_rpc.go` → `user-detail.tsx`); Overview
  charts split by protocol. Only labels are needed: `web/src/lib/series.ts` `protocolNames.vless = "VLESS"`,
  `protocolOrder = ["hysteria2", "vless", "amneziawg", "awg"]`.
- Per inbound/profile split is not stored (two VLESS profiles on one node merge); adding `profile_id` to the bucket key rebuilds the
  hottest table: not now. To compare REALITY-tcp with XHTTP, put them on different nodes.
- Rollout to a test group: a user is in exactly one group. Create a group, e.g. "VLESS test", with the existing hy2 profile and the
  new VLESS profile, and move alice and bob into it. The "Right after it is created" block gives a new profile to every group that has
  none of its protocol (`after-create.tsx` `lacking`), which for the first VLESS profile means all groups: the docs page says to leave
  only the test group switched on.
- Later (not v1): a Mihomo `fallback` group per server in `subs/mihomo.go` `build` (that server's hy2 entries, then its VLESS
  entries; `url` generate_204; `interval` 300), listed first in the select group.

### 7.7 Subscription order

Merged separately (5db8209): entries by country, location, node name, then protocol hysteria2 > vless > awg, direct before warp,
then profile name; repeated names numbered in creation order. VLESS entries need nothing beyond `vless` taking its rank.

### 7.8 Rounds (replace rows R3-R6 of §5)

Edge budgets (Happ 5, Mihomo 7, page 5/3/4, AWG 7) do not change in any of these rounds: new reads are on admin paths only (`GetNode`;
the inbound check when `via=site`). The panel stays free of xray.

R3 (node), in addition to the TLS front, XHTTP+TLS and WS+TLS:
- Changes: `front.go` site listener for `target: self`; cert fallback; record-length re-detection; `parseSpec` self rules; certs
  routes acme-tls/1 by SNI; doctor uses the live expiry fallback; `tlsPortOf`.
- Proof: in-process xray client through a self inbound (fake CertSource); a plain TLS client gets the site page and the node
  certificate through REALITY; a hello with ALPN `acme-tls/1` and the node's SNI reaches CertSource; before issuance the self-signed
  fallback serves and authenticated clients connect; a site listener that cannot bind fails the inbound; record lengths are non-empty
  after the first issuance; hy2 + self on one agent: hy2 closes its site listener, VLESS serves the same handler, disabling VLESS brings
  hy2's listener back; certs router table (another name, normal hello, self-signed inbound); doctor: claimed hy2 ACME uses the live
  expiry and warns at < certWarn.
- Risks: record-length detection reaches into xtls/reality globals (pin the version; test); the self-signed certificate shows to
  probers in the first minutes.

R4 (panel plugin and UI; UI by Claude), in addition to the existing R4 work:
- Changes: schema `reality.target_kind` and self BuildInbound; `reality_needs_domain`; `hy2_decoy_paused` (both directions, params,
  `nodeListen.spec`); UDP check gated on `Listen.Network`; UI (a)(b)(c) and deploy notes; `series.ts` labels; docs page en/ru,
  `index.md`, `hysteria2.md`, `profiles.md`.
- Proof: `inbound_check_test.go` table (via self/site/tls, `tcp_port` 0 and 8443, reverse direction, disabled neighbour); Validate and
  BuildInbound for self (domain, IP → refusal); golden self URI; a TCP inbound never triggers a UDP run or a `port_lossy` refusal
  (CreateInbound, UpdateInbound, UpdateProfile, `udpCheckPorts`); web: `decoyNote` and `portNotes` texts, inbound-check notice with
  link, editor port-443 note per target kind, hy2 Masquerade link; `pnpm typecheck` (ru ≡ en); site build link check; `names_test`
  VLESS remark within 30.
- Risks: the self default refuses IP-only nodes (the message says what to do); the after-create default gives VLESS to every group
  unless unticked.

R4.1 (TCP check), new:
- Changes: proto `HttpMark`/`TcpProbe`, `tcpcheck/1`; certs `Mark`/`HTTP01State`; agent handlers; `internal/tcpcheck`; `fleet`
  `tcpTargets` + run + store; migration `node_tcp_check`; `admin.v1` `TcpCheck`; Ports block; `port80` param.
- Proof: `tcpcheck` against httptest (mark ours/other, refused, timeout; TLS chain, pin, wrong cert); certs mark (one-shot, expiry,
  busy); agent command tests (as `udpcheck_test.go`); `tcpTargets` table (no ACME → no 80; claimed hy2 site not probed; cap 8); a
  fake-sender run stores rows; js edition uses a node sender only; store tests for SQLite and D1, migration up and down; `renewalOf` table.
- Risks: a mark lost to an agent restart between arm and probe reads as "other" (re-run); a Docker DNAT on :80 sending traffic
  elsewhere shows as "other" (correct).

R5 (e2e), in addition: `--m3` runs Check ports with hy2 + VLESS (site) on 443 and asserts the 80 and 443 rows are `ours`. Self is not
in e2e (the e2e node has no ACME directory and no public name; the R3 unit tests cover it). +~10 s.

R6 (CDN and polish): unchanged, minus the form hints that move to R4. The Mihomo fallback group stays "later".

### 7.9 Node decoy site: a choice per node (owner, 2026-10-09; a step next to R3/R4)

Today every node serves one fixed built-in site (`internal/decoy`), the same on every installation, so it is recognisable;
REALITY with target self (§7.5) would serve it too. Each node gets a setting "decoy site" with three modes:
1. `builtin`: today's page (recognisable; nothing to do).
2. `panel`: the panel's own decoy bundle (`--decoy-dir`), the default when the panel has one. For one domain with
   subdomains: the subdomains are linked anyway (Certificate Transparency logs list them), and one company's site on its
   regional hosts is natural.
3. `own`: a bundle uploaded for this node only (static files, <= 2 MB; the same serving rules as `--decoy-dir`). For
   nodes on separate domains or bare IPs that must not be linkable to each other.
The panel stores the bundles and pushes a node only its own (content-addressed, so an unchanged bundle is not resent).
The node serves the chosen site wherever the built-in one is served today: hy2 masquerade (HTTP/3 and its TCP site), and
the VLESS site for REALITY self. Bulk change on the Nodes page; an MCP tool pair `node_decoy_plan`/`node_decoy_apply`
lets an agent upload one generated site per node (the docs' prompt gains a rule: never repeat the subject or the look of
the sites already made for other nodes). Docs: guide/decoy-site.md gains "Nodes: three options" (when to pick which).

Release checklist for the first release with VLESS (owner's request, 2026-10-09): README.md and README.ru.md (VLESS moves
from the "Later" roadmap row into the feature list and the protocol table); docs/{en,ru}/index.md and
getting-started/overview.md (protocol lists) plus a guide page per transport; the site mistgate.app (built from docs/ and
deployed by the release routine); the GitHub wiki (generated by site/wiki.mjs); the GitHub repository's About text and
topics (vless, xray, reality).

Release note: R2 and R3 ship together; if R2 is ever released alone, an R2 node would reject `target: "self"` (then gate self behind
a `vless-self/1` capability).
