---
title: Requirements
description: What the panel server, the node servers and the build machine need, and what the agent changes on a node.
---

Mistgate needs one Linux server for the panel, one or more Linux servers for the nodes and a domain name. The binaries come from a GitHub release, or you build them from source on a machine of your own.

## The panel server

| What | Requirement |
|:--|:--|
| System | Linux on amd64 or arm64. systemd to keep `mistgate serve` running (an example unit is in [Install the panel](install-panel.md)). |
| Access | root to install. The running panel only needs to bind TCP 443 and 80 and to write its data directory. |
| Domain | A host name whose A (and, if you have IPv6, AAAA) record points at the server, for example `panel.example.com`. |
| Inbound ports | TCP 443: the decoy site, subscriptions, user pages, the admin (in the prefix and host modes) and the node agents. TCP 80 when the panel gets its certificate from Let's Encrypt. Nothing else, unless you choose a separate admin or agent listener. |
| Outbound | Let's Encrypt over HTTPS when you use it. The ports of your servers on the nodes: the panel checks every server as a client (Hysteria2 over UDP, AmneziaWG over UDP). SSH (TCP 22 or the port you give) to a server you install as a node from the admin. GitHub over HTTPS (`api.github.com`, `github.com` and its download host) for releases: the panel looks for a new signed node bundle every 10 minutes and for a panel update every 10 minutes. Cloudflare over HTTPS when you register WARP accounts, switch on the Turnstile captcha or back up to R2. |
| Storage | The data directory, `/var/lib/mistgate` by default: one SQLite database, the master key, the node bundle and a few small files. |
| Other software | None. The panel is one static binary. Only its self-update calls systemd (`systemctl`, `systemd-run`). |

> **Note:** keep the panel on a server of its own. A node on the same host would compete with the panel for TCP 443 (Hysteria2 runs an HTTPS site there by default) and TCP 80.

## Node servers

| What | Requirement |
|:--|:--|
| System | Linux with systemd: Ubuntu 22.04 or newer, or Debian 12 or newer. amd64 or arm64. |
| Access | For the SSH installer: root over SSH, or an SSH user with non-interactive `sudo -n`, with a password login; the SSH port must be reachable from the panel's server. The installer runs system changes as root, needs at least 256 MB of memory on the server and a signed node bundle in the panel, and opens the SSH port, TCP 80, TCP 443 and UDP 443 in a UFW or firewalld that is already active. Manual `mistgate-node install` also runs as root; the agent runs as root with a narrowed set of capabilities. |
| Address | A public IPv4 address. Clients connect to the node's address: a domain that points at the server, or the IP itself. |
| nftables | The `nft` command (installed by default on Debian 12 and Ubuntu 22.04+). The agent drives its firewall rules through it. Port hopping needs `redirect` in the `inet` family, which kernels from 5.2 have. |
| TUN device | `/dev/net/tun` for the userspace AmneziaWG backend and for WARP. On a container VPS (OpenVZ, LXC) enable TUN in the hoster's control panel. |
| Clock | Synchronised with NTP. The agent reports a clock that differs from the panel's by more than 30 seconds, and the doctor's `time_sync` check looks at it too. |
| Inbound ports | The UDP port of each server on the node: Hysteria2 starts at 443, an AmneziaWG profile gets a random port between 10000 and 60000. The port-hopping range of a Hysteria2 profile when you turn it on (from 1024 up, at most 20000 ports). TCP 443 for the HTTPS site Hysteria2 shows to everyone who is not a client, and for Let's Encrypt. TCP 80 for Let's Encrypt when nothing else holds it. |
| Outbound | TCP to the panel (usually 443). Let's Encrypt, DNS, and Cloudflare's WARP endpoints when the node uses WARP. HTTPS to `www.speedtest.net` and the nearest Ookla speed servers (port 8080), or, as fallbacks, `speed.cloudflare.com`, `proof.ovh.net` and `cachefly.net`, only while the owner measures the node's network capacity, and once for a new node: see [Nodes](../guide/nodes.md). |
| No management port | The agent dials the panel. Nothing on the node has to accept connections from the panel. |

Good to know:

- **IPv6 is optional.** A node without IPv6 serves over IPv4. A node that has a global IPv6 address but cannot connect out over IPv6 is worse than one without: apps that get AAAA answers stall. The doctor's `ipv6` check warns about it.
- **Containers.** On OpenVZ or LXC the agent cannot set its sysctl values (it logs that and carries on), and the AmneziaWG kernel module cannot be installed. The userspace backend works when TUN is enabled.
- **AmneziaWG kernel module.** Optional. By default AmneziaWG runs in userspace and needs no module. On request, the agent (or `mistgate-node awg prepare-kernel`) builds the module on Ubuntu from the Amnezia PPA and on Debian from source. That needs apt, systemd, no container and no Secure Boot.
- **A clean host works best.** The doctor reports what can get in the way: other VPN panels and their containers, Xray, old WireGuard interfaces, nftables NAT rules that are not Mistgate's, and Docker's FORWARD drop policy (it breaks AmneziaWG forwarding).

## Release binaries

Every [GitHub release](https://github.com/Mistgate/mistgate/releases/latest) carries static Linux binaries of both programs for amd64 and arm64 (`mistgate-linux-amd64`, `mistgate-linux-arm64`, `mistgate-node-linux-amd64`, `mistgate-node-linux-arm64`), with `SHA256SUMS`, and the signed manifests the panel and the nodes update from. The binaries carry the project's release key, so a panel installed from them fetches signed node bundles from GitHub and, on a systemd installation, can update itself (see [Updates](../operations/updates.md)). No build machine is needed then.

## Building from source

| Tool | Version |
|:--|:--|
| Go | 1.27. `go.mod` names the toolchain (`go1.27.1`); with `GOTOOLCHAIN=auto` (the default) an older Go downloads it. |
| Node.js | 22 (what CI builds with) or newer: the admin SPA is built and embedded into the panel binary. |
| pnpm | 10: `web/package.json` pins `pnpm@10.33.2`, which Corepack uses when it is on. |
| make and a POSIX shell | On Windows, Git Bash or WSL. |
| git | `make build` stamps the version (`git describe`) and the commit time (`git log`) into the binaries. Without git the version is `0.0.0-dev` and the build time 0. |
| Internet access | For Go modules and npm packages. |

`make build` produces static Linux binaries (`CGO_ENABLED=0`) for both architectures:

```sh
make build
# bin/mistgate-linux-amd64       bin/mistgate-node-linux-amd64
# bin/mistgate-linux-arm64       bin/mistgate-node-linux-arm64
```

> **Warning:** binaries you build update themselves only when they were built with your own release key (`RELEASE_KEY=<public key> make build`), and then only from bundles you sign. A node installed from a build without a key has to be updated by hand once. If you plan to use signed updates, make the key before the first build: see [Releases and signing](../operations/releases.md).

## DNS and certificates

- **Panel.** Point the panel's host name straight at the server, without a CDN or proxy in front of it. Node agents pin the panel's own CA and Let's Encrypt validates on the server itself; neither works through a proxy that terminates TLS.
- **Panel certificate.** Either let the panel get one from Let's Encrypt (`serve --acme-domain`), or give it your own certificate files (`serve --tls-cert` and `--tls-key`). See [Install the panel](install-panel.md).
- **Secret admin host.** If the admin lives on a secret host name, that name needs its own DNS record and must be covered by a certificate. A Let's Encrypt certificate publishes its names in public Certificate Transparency logs; a wildcard certificate of your own keeps the secret name out of them.
- **Agent endpoint.** It needs no DNS record and no public certificate. Agents reach it at the panel's address with a secret TLS name (a random label under the panel's domain), and the panel answers that name with a certificate from its own CA.
- **Nodes.** A Hysteria2 server with a Let's Encrypt certificate needs a domain whose A record points at the node: the node's address, or the profile's Domain (SNI). A node known only by its IP uses the self-signed (pinned) certificate instead. AmneziaWG needs no certificate.

## What the agent changes on a node

The agent changes only what is listed here, and only its own objects: it never edits another program's nftables tables, never redirects a port below 1024, and never lets a port-hopping range cover the sshd ports.

| What | Where | When |
|:--|:--|:--|
| Agent binary | `/usr/local/bin/mistgate-node` (`install --bin` changes the path). Self-update puts `<binary>.new` and `<binary>.prev` next to it. | `mistgate-node install`, self-update |
| Service | `/etc/systemd/system/mistgate-node.service`, enabled and started. | `mistgate-node install` |
| State directory | `/var/lib/mistgate-node` (mode 0700, files 0600): the node's key and certificate, the panel CA, the panel address, the last applied state, the certificates of its servers, update markers. | `enroll`, then the agent |
| sysctl | `/etc/sysctl.d/90-mistgate.conf`: `net.core.default_qdisc = fq`, `net.ipv4.tcp_congestion_control = bbr`, and `net.core.rmem_max` / `net.core.wmem_max` at least 16 MiB, also applied to the running kernel; the larger UDP socket buffers help QUIC avoid packet drops under load. | every agent start |
| journald | `/etc/systemd/journald.conf.d/90-mistgate.conf`: `SystemMaxUse=200M`, `RuntimeMaxUse=200M`. journald is restarted when the file changes. | every agent start |
| Firewall: SSH guard and port hopping | nftables table `inet mistgate_node`. | every agent start; when port-hopping ranges change |
| AmneziaWG | Interfaces `mgawg<port>`, nftables table `inet mistgate_awg` (NAT for the clients, MSS clamp, no traffic between clients, nothing on the node reachable through the tunnel except ping to the tunnel address). IPv4 forwarding on; IPv6 forwarding when a tunnel has IPv6, after moving interfaces with `accept_ra=1` to `accept_ra=2` so the host keeps its own IPv6. Forwarding values are set live only. | while the node runs an AmneziaWG server |
| WARP | Interface `mgwarp`, routing table 51820 with rules at preferences 90 and 110, nftables table `inet mistgate_warp`. | while the node has WARP |
| Torrent protection | nftables table `inet mistgate_torrentguard`: it hands the first packets of the flows AmneziaWG clients start to the agent, and drops a connection the agent recognised as BitTorrent in the kernel. Nothing fails if the agent does not answer (the queue is bypassed). Hysteria2 is checked inside its engine, with nothing on the host. | while **Block recognized BitTorrent traffic** is on and an AmneziaWG server runs |
| Host firewall (UFW) | Allow rules for the UDP port of each server and the hop range of a Hysteria2 server, tagged with the comment `mistgate-node-managed-udp-v1-<port>`. The agent never adds a rule over one for the same port that is not its own and never removes a rule without its tag. `ufw` runs as a transient systemd unit outside the agent's sandbox. firewalld is only checked, never changed. | while UFW is active |
| Resolver | `/etc/systemd/resolved.conf.d/90-mistgate.conf` (with systemd-resolved), or a rewritten `/etc/resolv.conf` with the original kept as `/etc/resolv.conf.mistgate.bak`. | only when you apply the doctor's resolver fix |
| Journal size | `journalctl --vacuum-size=200M --vacuum-time=7d`. | only when you apply the doctor's fix |
| AmneziaWG kernel module | apt packages, the Amnezia PPA on Ubuntu or a source build on Debian, `/etc/modules-load.d/amneziawg.conf`. Runs as the transient unit `mistgate-awg-prepare`. | only when you ask for it |

> **Note:** the SSH guard rate-limits **new** connections to the sshd ports per source address (per /64 for IPv6): a burst of 10, then 6 a minute. Open sessions are never touched, nothing is banned, loopback is exempt, and when its table is full the guard stops matching rather than lock you out. Tools that open many SSH connections at once from one address may still feel it. The agent finds the sshd ports with `sshd -T`, the sshd config files and `ssh.socket`, and uses 22 when it finds nothing.

### The service unit

`mistgate-node install` writes the unit itself, sized to the host's memory. Its main settings:

| Setting | Value |
|:--|:--|
| Start | `mistgate-node run --state-dir /var/lib/mistgate-node` |
| Before start | a crash-loop guard: after an update that crashes three times in a row it puts the previous binary back |
| After every stop | `mistgate-node cleanup-net`: removes the AmneziaWG and WARP interfaces, the WARP routes and rules and their nftables tables, so a crashed agent leaves no broken routing behind (the agent puts them back when it starts) |
| Restart | `Restart=on-failure` after 5 s; exit code 78 (not enrolled or retired) is not restarted |
| Memory | `GOMEMLIMIT` about 60% of RAM (at least 64 MiB), `MemoryHigh` about 70%, `MemoryMax` about 85%; `LimitNOFILE=65536` |
| Privileges | runs as root with only `CAP_NET_ADMIN` and `CAP_NET_BIND_SERVICE`, `NoNewPrivileges=yes` |
| Files | `ProtectSystem=strict`: writable are only the state directory, the binary's directory, `/etc/sysctl.d` and `/etc/systemd/journald.conf.d` |
| Devices | only `/dev/net/tun` besides the standard pseudo devices |
| Other | `ProtectHome`, `PrivateTmp`, `SystemCallFilter=@system-service`, `UMask=0077` and the rest of the usual systemd sandboxing |

What retiring a node removes, and how to clean a host by hand, is under "Remove a node" in [Add a node](add-node.md).
