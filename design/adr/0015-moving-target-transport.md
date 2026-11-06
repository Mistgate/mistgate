# 0015. A moving target: a dialect of its own for every server, rotated by the panel

Status: proposed, 2026-11-06.

## Context

How censors find circumvention traffic, roughly in the order they reach for it:

1. **Static signatures**: fixed bytes, fixed packet sizes, a handshake everybody shares.
2. **"Looks like nothing"**: the Great Firewall has blocked fully encrypted traffic since 2021 by cheap heuristics (the
   entropy and the share of set bits of the first packet, a printable prefix, the length), without knowing the protocol
   (Wu et al., *How the Great Firewall of China Detects and Blocks Fully Encrypted Traffic*, USENIX Security 2023).
3. **Active probing**: connect to a suspected server and see whether it answers like the protocol it claims to be.
4. **Statistics and ML** over sizes and timing (TLS-in-TLS and the like).
5. **Allow lists**: during shutdowns (mobile networks in Russian regions since 2025) only known services pass at all.

Secrecy of the protocol does not help against any of these: the traffic is what gets classified, and the node binary is on
every operator's server, so a censor reads it like anyone else. Polymorphic or metamorphic *binaries* (as malware uses
against signature scanners) change how the file looks, not what it does on the wire. The protection that works keeps the
design public and the secret per server (Kerckhoffs): keys, and parameters drawn from a per-server secret.

That is known to work on the wire. ScrambleSuit (2013) called itself a polymorphic protocol: each server's packet lengths and
timing come from distributions derived from its secret, so servers do not share a shape; obfs4 grew out of it. FTE (2013)
and Marionette (2015) make the format of the traffic a program or a grammar, so it can be changed. AmneziaWG does a simple
form: junk packets, header values and padding differ per server.

What Mistgate already has:

- **AmneziaWG**: every default is drawn per profile ("defaults that are the same for everybody become a fingerprint of their
  own", `internal/panel/protocols/awg/generate.go`), and the mimicry presets dress the junk as QUIC, DNS, STUN, WebRTC, SIP,
  NTP, RTP, SSDP or DTLS (`awg/mimicry`), DNS varying per device.
- **VLESS REALITY**: borrows the TLS handshake of a real site; **Hysteria2** with Salamander or Gecko obfuscation and port
  hopping; per-server keys everywhere; servers answer wrong keys with the decoy site.
- **Signals**: the Reachability page reads Globalping, the Russian block lists, the TSPU probe and the kl!ck reports
  (people's apps checking their servers), per country.
- **Delivery**: the apps that take the subscription link refresh it on their own (12 h by default, the owner's setting);
  the panel rolls node configs out in steps with rollback. AmneziaVPN keys and Telegram proxy links are static: a person
  imports them once.

What it lacks: a dialect is drawn once, when a profile is made, and stays until somebody edits it; nothing changes a dialect
across the fleet, on a schedule or when a signal says it is being cut.

## Decision

### A dialect, drawn from a seed

- A **dialect** is every wire parameter of one inbound that is not a key: for AmneziaWG the junk count and sizes, header
  values, padding and the mimicry chains; for Hysteria2 the obfuscation type and password and the port-hopping range; for
  VLESS REALITY the borrowed site and SNI candidates, short ids, the transport and its path, padding and ports.
- It is drawn by an open **generator** from a per-inbound **seed** sealed in the vault: `family + seed` reproduces it.
  A **family** is a versioned generator (`awg.quic/2`, `reality.tcp/1`, ...). New families arrive with releases.
- No two servers share a dialect. Values are drawn from the distributions of the protocol being imitated (real QUIC Initial
  sizes, real ClientHello shapes, real DNS answers), not uniformly: variation that real traffic never shows is a fingerprint
  too.
- **Mimicry first.** A family imitates something an allow list keeps (TLS to a real site, QUIC, DNS, WebRTC). No default
  family is "random bytes", which item 2 above blocks as a class.
- **Probe resistance is part of every family**: a connection without the right key gets what the imitated service would
  answer, or the decoy site.

### Rotation by the panel

- A rotation gives an inbound a new seed (and maybe a new family) and runs it **with an overlap**: the node accepts the old
  and the new dialect at once, the subscriptions serve the new one, and the old one goes when the overlap ends: at least two
  subscription refresh intervals, and not before the devices that used it have fetched the new one or gone quiet.
  - REALITY takes several site names and short ids natively: add, switch, remove.
  - Hysteria2 and AmneziaWG take one obfuscation per listener: the overlap is a second listener on another port.
- **Triggers**: the owner's schedule (off by default; 7 to 90 days), a signal (a Reachability source says the profile is
  cut from a country or provider, confirmed by a second source), or **Rotate now**.
- **Never everywhere at once**: canary nodes first, a reachability check, then the rest, with the rollout machinery's steps
  and rollback. A dialect that fails its checks is not spread.
- **Static clients**: AmneziaVPN keys and Telegram links do not refresh. Their inbounds rotate only by **Rotate now** or a
  confirmed signal, never on a schedule; the person's page then shows the new key, and the announcement and Telegram say
  so. The owner can exclude a profile from rotation.
- Every dialect is recorded: family, created, first served, devices on it, the first report that it is cut, retired. The
  owner sees which families last. Nothing leaves the panel; any shared statistics are a later, opt-in, aggregated decision.

### Openness

The generators, the families and the rotation are open source, like the rest. Secrecy lives only in the seeds and keys of
each panel. A new family may reach a paid early-access channel first and open for everyone after a fixed delay (ADR 0011
channels); fixes to security and to probe resistance are never delayed. Whether rotation by signals is part of a paid tier
is a separate decision.

## Consequences

- One fingerprint no longer blocks the fleet: a censor has to learn a family, from the traffic, while it moves.
- Nodes churn: more config pushes, a second port during overlaps, more state in the panel.
- Each family needs tests against the classifiers we can reproduce (the fully-encrypted heuristics, size and timing
  statistics, active probes) and interop tests with every app that carries it; the client matrix of the subscription formats
  limits which families an app can use.
- A wrong family spread fast hurts many people at once: the canary step and the checks are not optional.

## Nothing breaks

The first rule of every step below; a step that cannot keep it does not ship.

- **Off until turned on.** An updated panel changes no dialect by itself: rotation is opt-in per panel and per profile;
  today's profiles become "family as drawn, seed as stored" and keep every parameter they have.
- **A person never loses the connection to a rotation.** The old dialect keeps working until the overlap ends, the overlap
  outlives the app refresh interval, and a device that still uses the old dialect holds it open longer (up to a cap).
- **Static clients are not rotated behind their back**: AmneziaVPN keys and Telegram links only by hand or on a confirmed
  signal, with the new key shown first.
- **Canaries and rollback.** A rotation reaches a few nodes first; if their reachability or their health checks get worse,
  it stops and those nodes go back to the old dialect, as a failed node update does.
- **Same app matrix.** A family is offered only to apps whose formats carry it; an app that cannot carry the new dialect
  keeps a server it can use.
- **Tests before a family ships**: interop with each app that carries it, the classifier tests, and a rotation drill on a
  test fleet (rotate, overlap, retire, roll back).

## Our own protocol

A protocol of our own, when it comes, is designed as a family of this model from its first line, not retrofitted:

- **Metamorphic by construction.** Its wire format is generated from the seed: framing, header layout, padding and the
  size and timing shaping are parameters of a grammar (in the spirit of FTE and Marionette), so two servers speak two
  dialects of it and a rotation is a new seed, not a new protocol.
- **Inside mimicry.** Its outer layer is a protocol an allow list keeps (TLS to a real site or QUIC first), never bytes that
  look like nothing.
- **Open.** The grammar, the generator and the client are public; each server's seed and keys are secret. Its advantage is
  the panel: generation, rotation, signals and delivery to the apps across a fleet, which a protocol alone does not have.
- **Proven like the others**: the same classifier and probe tests, a published design note, and an outside review before it
  is offered by default.

## Plan

1. **Dialects for what exists**: the dialect model, seeds in the vault, the families that describe today's per-profile
   draws (AmneziaWG, Hysteria2, REALITY), the record. **Rotate now** with overlap for REALITY (native) and Hysteria2.
2. **Overlap for AmneziaWG** (a second listener) and the static-client flow (the new key on the person's page, the notices).
3. **Scheduled rotation** with canaries, through the rollout machinery.
4. **Signals**: rotation proposed, then (owner's choice) applied, when two Reachability sources agree that a profile is cut.
5. **New families**: realistic size and timing distributions inside REALITY and XHTTP padding; programmable shaping in the
   spirit of Marionette for the transports that allow it; classifier tests for each.

Open questions: how often the apps really refresh (measure from the subscription fetches before relying on the overlap
length); whether the node agent can hold two AmneziaWG listeners per inbound without a second interface; what the allow-list
regimes keep, per country, to choose the families to imitate.
