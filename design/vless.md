# VLESS protocol plugin: design

Status: accepted by the owner 2026-10-09; R1 implemented 2026-10-09. Base: main 0a3c16a. Reference versions: xray-core v26.3.27 (module v1.260327.0, the
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
| R2 engine with REALITY (RAW+Vision, XHTTP) | go.mod xray-core v1.260327.0 (MVS raises gvisor, wireguard, utls 1.8.3-pre); `internal/node/vless`; wire.go; `vless/1`; torrentguard wrappers and limiter shared | in-process xray client only in `_test.go`: traffic through both, counters ±1 %; users-only Apply no restart, sessions kept; add/remove/kick end sessions ≤ 5 s; expiry, rate limit, private-destination block; WARP egress fails closed; spec change restarts only that inbound; log handler drops access lines; Observed hash test; schema-to-engine test; WARP registration tests re-run (utls bump) | xray API churn (compile errors — pin and bump deliberately); kick semantics; Vision CPU |
| R3 TLS front, XHTTP+TLS, WS+TLS | `front.go`, abstract unix sockets (temp-dir socket on non-Linux test hosts) | a fake CertSource rotates the cert under a live session: no drop, new pin reported; h2 and http/1.1; WS upgrade; ACME lazy issuance via the certs tests | the extra copy per byte on TLS inbounds — measure in WSL |
| R4 panel plugin | schema, Validate, BuildInbound, InitInbound, IssueCredential, URI + Mihomo render; builtin registry; requiredCap; InboundPublic in sub.go and preview; docs en/ru; agent.proto comments; `hy2_decoy_moves` uses Hysteria2's effective `masquerade.tcp_port` (absent means 443; 0 means no decoy), parsed like `hy2TCPPort` in `agent/doctor.go:157`, and is rendered in the inbound-check UI; `host_firewall_sync_failed` en/ru text covers TCP as well as UDP because `ports` may contain `tcp:8443`; `docs/en/getting-started/requirements.md` and `docs/ru/getting-started/requirements.md` describe both tagged transports | golden URI and YAML per combination incl. hostile node names; Validate tables; GOOS=js build; prod-copy subscription diff with the owner's OK (identical for users without a VLESS profile) | every implicit device gains one VLESS credential row on its next fetch (one write per user, D1 too) |
| R5 e2e | `m3_vless` in `scripts/e2e/m3.sh` (`--vless`, also in `--m3`); `xray_bin` from `github.com/xtls/xray-core/main@v1.260327.0` into `$M3_CACHE`; `scripts/e2e/tlsdest` local TLS 1.3 REALITY target | profiles xhttp+reality, tcp+reality, xhttp+tls (self-signed, client pinned); Happ fetch → 3 vless:// lines → python → xray client with socks; 20 MB each, usedBytes 0.95–1.10; disable kills the running download and refuses new, enable restores; Mihomo profile `mihomo -t` + traffic per proxy; RSS with all inbounds, node idle ≤ 100 MB; step 11 greps logs for UUIDs | e2e +~2 min |
| R6 CDN and polish | `reality_target` detail in the UI, "VLESS behind a CDN" docs page, form hints | manual CDN test by the owner (not CI) | — |

R1 implementation note: TCP firewall synchronization covers direct TCP listener ports; R1 introduces no TCP port-hop ranges.

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

Note (R1): the panel returns the info reason `hy2_decoy_moves` already; the admin UI renders it (inbound-check, en/ru strings) in R4 together with the rest of the VLESS UI — before R4 no TCP inbound exists, so it cannot appear. hy2_decoy_moves is shown in the admin UI in R4.
