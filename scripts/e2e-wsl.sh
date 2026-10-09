#!/usr/bin/env bash
# The end-to-end test, for real: a Hysteria2 client built from source goes through a node and its traffic shows up
# in the panel. Runs as root inside WSL, and removes everything it started or created when it ends.
#
#   MSYS_NO_PATHCONV=1 wsl -d Ubuntu -u root -- bash scripts/e2e-wsl.sh [--keep]   (from the repo root)
#
# Needs: go (GOTOOLCHAIN=auto fetches the pinned one), curl, jq, python3, openssl, nft, ip, internet access.
# --keep (or KEEP=1 where the environment reaches the script; wsl.exe does not pass it) keeps the work directory
# with the logs and configs. Secrets (admin password, session cookie, enrollment token, subscription token,
# Hysteria2 credentials) are never printed, and every log is grepped for them at the end.
#
# Panel: `mistgate setup` + `serve` with a self-signed certificate on the public listener; the node agent dials
# that listener with the secret SNI name (the production path; no --agent-listen).
#
# Self-update (step 2d): both binaries are built with a throwaway release key (made by `mistgate release
# keygen` into the work directory, never the owner's) and distinct build times. The node starts as v1; the script signs
# bundles with scripts/e2e/signbundle (release sign without its rebuild check: these builds are not reproducible from a
# tag on purpose) into <data-dir>/dist and rolls them out through the admin API: v2 (must commit),
# a manual RollbackNode and a second rollout, v3a (stamped older than its manifest: the agent rolls itself back at
# once), v3b (cannot reach the panel: the agent rolls itself back when its commit window ends; built from a temporary
# Go overlay of this tree with a dead dialer and a 20 s window, the tree itself has no test switch) and v5 (signed by
# another key: the agent refuses it). With --old-node the node is a build without update/1 and the step checks that
# it is shown as UNSUPPORTED and never sent an update.
#
# The tunnel steps (AmneziaWG, WARP, Mihomo; they are in scripts/e2e/m3.sh) are opt in: --m3, or --awg,
# --warp, --mihomo one by one, run after step 9 and before the retire; --m3-only runs them without the base and
# self-update steps between. Clients and a fake internet sit in anonymous namespaces inside the run's namespace (`unshare -n`, a veth
# each, interfaces mg3*): real amneziawg-go clients (3.1 and 2.0), a real mihomo, a kernel WireGuard peer that plays
# WARP. With --old-node the tunnel steps are replaced by a compatibility step: an awg inbound is refused as
# agent_too_old, device and WARP calls are refused, the old node stays untouched. The retire step also checks that no
# AWG/WARP link, table, rule or route is left.
#
# --edge runs the Cloudflare edition instead (scripts/e2e/edge.sh): the panel is the Go js/wasm build inside a local
# `wrangler dev` (workerd, miniflare D1 and Durable Objects, nothing of a Cloudflare account is touched), the node is a
# link-only agent that enrols and connects over the Worker's public WebSocket link, and the steps are the edge subset of
# this scenario (no self-update, no tunnel steps, no panel restarts) plus the edge-specific ones: fan-out, the cron
# path, an object reset. It needs node 22 and pnpm, and builds into the work directory only.
#
# ISOLATION. A node agent owns things that exist once per host: TCP 443 (masquerade), the nft table
# mistgate_node, /etc/sysctl.d/90-mistgate.conf and the journald drop-in. So the whole run happens in a private
# network namespace (an ipvlan slave of the default interface gives it internet access without forwarding, which
# Docker's FORWARD policy would drop), and /etc/sysctl.d and /etc/systemd/journald.conf.d are private tmpfs
# mounts inside it. Another panel or node running in this WSL at the same time is neither disturbed nor able to
# disturb the run, and nothing the agent installs can outlive it.

set -Eeuo pipefail

# --old-node <abs path>: run the whole scenario with that mistgate-node binary (built from an older commit) in
# place of the one built from this tree. The panel is always the new one; the health steps then expect an agent
# without the doctor (compatibility check). The path must be reachable from WSL (/mnt/...).
OLD_NODE=""
# The tunnel steps (AmneziaWG, WARP, Mihomo) are opt-in: without these flags the run is the base and self-update
# scenario and takes what it always took.
# --m3 is all three; --awg also runs the self-service calls of the user's page; --mihomo needs the AWG profiles of --awg
# and turns it on. --m3-only skips every base step after the node is up (development of the tunnel steps: ~5 minutes).
M3_AWG="" M3_WARP="" M3_MIHOMO="" M3_ONLY="" EDGE=""
ARGS=("$@")
while [ $# -gt 0 ]; do
  case $1 in
    --keep) KEEP=1 ;;
    --m3) M3_AWG=1 M3_WARP=1 M3_MIHOMO=1 ;;
    --awg) M3_AWG=1 ;;
    --warp) M3_WARP=1 ;;
    --mihomo) M3_MIHOMO=1 M3_AWG=1 ;;
    --m3-only) M3_ONLY=1 ;;
    --edge) EDGE=1 ;;
    --old-node) OLD_NODE=${2:?--old-node needs a path}; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done
set -- "${ARGS[@]}"
[ -z "$OLD_NODE" ] || [ -x "$OLD_NODE" ] || [ -f "$OLD_NODE" ] || { echo "--old-node: no such file: $OLD_NODE" >&2; exit 2; }
[ -z "$M3_ONLY" ] || [ -n "$M3_AWG$M3_WARP$M3_MIHOMO$OLD_NODE" ] || { M3_AWG=1 M3_WARP=1 M3_MIHOMO=1; }
M3_LIVE="$M3_AWG$M3_WARP$M3_MIHOMO"
# An old node cannot run what the tunnel steps add: the live steps would only fail, the compatibility step is what it needs.
[ -z "$OLD_NODE" ] || M3_LIVE=""
[ -z "$EDGE" ] || [ -z "$M3_LIVE$M3_ONLY$OLD_NODE" ] || { echo "--edge cannot be combined with the tunnel steps or --old-node" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "run as root (wsl -d Ubuntu -u root)" >&2; exit 2; }
for tool in go curl jq python3 openssl nft ip mount; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done
if [ -n "$EDGE" ]; then
  for tool in node pnpm gzip; do
    command -v "$tool" >/dev/null || { echo "missing tool for --edge: $tool" >&2; exit 2; }
  done
  [ "$(node -p 'process.versions.node.split(".")[0]')" -ge 22 ] || { echo "--edge needs node 22 or newer" >&2; exit 2; }
fi
if [ -n "$M3_LIVE" ]; then
  for tool in nsenter unshare ss ping base64; do
    command -v "$tool" >/dev/null || { echo "missing tool for the tunnel steps: $tool" >&2; exit 2; }
  done
  python3 -c 'import yaml' 2>/dev/null || { echo "the tunnel steps need python3-yaml" >&2; exit 2; }
  [ -z "$M3_WARP" ] || command -v wg >/dev/null || { echo "--warp needs wg (wireguard-tools) and the wireguard kernel module" >&2; exit 2; }
fi

# ---------------------------------------------------------------- outer: create the namespace, re-run inside it

outer_main() {
  local dev gw cidr addr nic rc=0 n
  # stale namespaces of killed runs (no process in them any more)
  for n in $(ip netns list | awk '/^mge2e-/ {print $1}'); do
    [ -z "$(ip netns pids "$n")" ] && { ip netns del "$n" 2>/dev/null || true; rm -rf "/etc/netns/$n"; }
  done
  dev=$(ip -4 route show default | awk '{for (i = 1; i < NF; i++) if ($i == "dev") {print $(i + 1); exit}}')
  gw=$(ip -4 route show default | awk '{for (i = 1; i < NF; i++) if ($i == "via") {print $(i + 1); exit}}')
  cidr=$(ip -4 -o addr show dev "${dev:-none}" 2>/dev/null | awk '{print $4; exit}')
  [ -n "$dev" ] && [ -n "$gw" ] && [ -n "$cidr" ] || { echo "no default route: the test namespace would have no internet" >&2; return 2; }
  # an address nobody else in the subnet uses (the WSL NAT subnet holds the VM and the gateway, nothing more)
  addr=$(python3 - "$cidr" "$gw" <<'PY'
import ipaddress, random, sys
n = ipaddress.ip_interface(sys.argv[1])
skip = {n.ip, ipaddress.ip_address(sys.argv[2])}
hosts = [h for h in n.network.hosts() if h not in skip]
print(random.choice(hosts[len(hosts) // 2:]))
PY
  )
  NS=mge2e-$$
  nic=mge2e$$
  trap 'ip netns del "$NS" 2>/dev/null; rm -rf "/etc/netns/$NS"' EXIT
  ip netns add "$NS"
  ip link add link "$dev" name "$nic" type ipvlan mode l2
  ip link set "$nic" netns "$NS"
  ip -n "$NS" addr add "$addr/${cidr#*/}" dev "$nic"
  ip -n "$NS" link set "$nic" up
  ip -n "$NS" link set lo up
  ip -n "$NS" route add default via "$gw"
  mkdir -p "/etc/netns/$NS"
  printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' >"/etc/netns/$NS/resolv.conf"
  # --edge: the Worker answers on de1.example.com, a name that exists only inside this namespace
  [ -z "$EDGE" ] || printf '127.0.0.1 localhost\n127.0.0.1 de1.example.com\n' >"/etc/netns/$NS/hosts"
  echo "network namespace $NS: $nic $addr via $dev, gateway $gw"
  MG_E2E_INNER=1 ip netns exec "$NS" bash "${BASH_SOURCE[0]}" "$@" || rc=$?
  return "$rc"
}

if [ "${MG_E2E_INNER:-}" != 1 ]; then
  outer_main "$@"
  exit $?
fi

# ---------------------------------------------------------------- inner: everything below runs in the namespace

# `ip netns exec` gave us a private mount namespace: hide the real sysctl.d and journald drop-ins behind tmpfs so
# the agent's files land in memory and vanish with us.
mkdir -p /etc/systemd/journald.conf.d
mount -t tmpfs tmpfs /etc/sysctl.d
mount -t tmpfs tmpfs /etc/systemd/journald.conf.d

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto} CGO_ENABLED=0 GOFLAGS=-buildvcs=false
# shellcheck source=e2e/m3.sh
. "$REPO/scripts/e2e/m3.sh" # the tunnel steps (functions only; the calls are below)

WORK=$(mktemp -d /tmp/mg-e2e.XXXXXX)
BIN=$WORK/bin DATA=$WORK/data NODE_STATE=$WORK/node-state
mkdir -p "$BIN" "$NODE_STATE"
T0=$(date +%s)
NODE_NAME=wsl1
NFT_TABLE=mistgate_node
SYSCTL_FILE=/etc/sysctl.d/90-mistgate.conf
JOURNALD_FILE=/etc/systemd/journald.conf.d/90-mistgate.conf

RESULTS=() SECRETS=() PIDS=()
STEP=start PANEL_PID="" NODE_PID="" COOKIE="" LAST_PID=""
DL_BYTES=0 USED_BYTES=0

# ---------------------------------------------------------------- output helpers

say() { printf '[%3ds] %s\n' $(($(date +%s) - T0)) "$*"; }
step() { STEP=$*; printf '\n=== %s\n' "$*"; }
pass() { say "PASS  $*"; RESULTS+=("PASS  $*"); }
note() { say "note  $*"; RESULTS+=("note  $*"); }
die() { say "FAIL  $*"; RESULTS+=("FAIL  $*"); exit 1; }

# redact replaces every known secret by [REDACTED] (stdin -> stdout).
redact() {
  SECRETS_NL=$(printf '%s\n' "${SECRETS[@]:-}") python3 -c '
import os, sys
s = [x for x in os.environ["SECRETS_NL"].split("\n") if len(x) >= 6]
t = sys.stdin.read()
for x in s:
    t = t.replace(x, "[REDACTED]")
sys.stdout.write(t)'
}

tail_logs() {
  local f
  for f in panel wrangler node client; do
    for l in "$WORK"/$f*.log; do
      [ -s "$l" ] || continue
      echo "--- tail $(basename "$l")"
      tail -n 25 "$l" | redact
    done
  done
}

on_err() { say "FAIL  unexpected error at line $1 (step: $STEP)"; RESULTS+=("FAIL  unexpected error in: $STEP (line $1)"); }
trap 'on_err $LINENO' ERR

# ---------------------------------------------------------------- process helpers

spawn() { "$@" & LAST_PID=$!; PIDS+=("$LAST_PID"); }

# alive: running, not a zombie waiting for our wait (kill -0 succeeds on those).
alive() {
  local st
  [ -n "${1:-}" ] && st=$(ps -o stat= -p "$1" 2>/dev/null) && [ -n "$st" ] && [ "${st:0:1}" != Z ]
}
not_alive() { ! alive "$1"; }

# stop_pid: TERM, wait up to 10 s, then KILL. Only ever a pid this script started.
stop_pid() {
  local pid=$1 i
  alive "$pid" || { wait "$pid" 2>/dev/null || true; return 0; }
  kill -TERM "$pid" 2>/dev/null || true
  for ((i = 0; i < 100; i++)); do alive "$pid" || break; sleep 0.1; done
  alive "$pid" && kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

cleanup() {
  local rc=$? p
  trap - ERR
  set +e
  if [ "$rc" != 0 ]; then
    echo
    tail_logs
  fi
  [ -e "$WORK/sampling" ] && rm -f "$WORK/sampling"
  for ((p = ${#PIDS[@]} - 1; p >= 0; p--)); do stop_pid "${PIDS[$p]}"; done
  ! declare -F edge_reap >/dev/null || edge_reap # workerd and the wrangler processes that outlived their parent
  # Nothing to undo on the host: the nft table lives in the namespace and the agent's files on private tmpfs
  # mounts, both gone when the last process here exits.
  if [ -n "${KEEP:-}" ]; then say "work directory kept: $WORK"; else rm -rf "$WORK"; fi
  echo
  echo "================ summary"
  printf '%s\n' "${RESULTS[@]}"
  if [ "$rc" = 0 ]; then echo "E2E PASSED in $(($(date +%s) - T0)) s"; else echo "E2E FAILED (exit $rc)"; fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

wait_for() { # <seconds> <what> <command...>: poll once a second until the command succeeds
  local n=$1 what=$2 i
  shift 2
  for ((i = 0; i < n; i++)); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  say "timeout after ${n} s waiting for: $what"
  return 1
}

free_port() { # tcp|udp: a port nothing listens on and this run has not handed out yet (the kernel repeats itself)
  local p
  while :; do
    p=$(python3 - "$1" <<'PY'
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM if sys.argv[1] == "udp" else socket.SOCK_STREAM)
s.bind(("0.0.0.0", 0))
print(s.getsockname()[1])
PY
    )
    grep -qx "$p" "$WORK/ports" 2>/dev/null && continue
    echo "$p" >>"$WORK/ports"
    echo "$p"
    return 0
  done
}

vmrss_mb() { awk '/^VmRSS:/ {printf "%.1f", $2 / 1024}' "/proc/$1/status"; }

# ---------------------------------------------------------------- admin API (Connect, JSON)

api() { # <Service/Method> [json]  -> response body on stdout; non-200 is an error
  local out code
  out=$(curl -sS -m 40 ${CURL_CA:+--cacert "$CURL_CA" --compressed} -w $'\n%{http_code}' -X POST "$ADMIN/api/mistgate.admin.v1.$1" \
    -H 'Content-Type: application/json' ${COOKIE:+-H "Cookie: $COOKIE"} -d "${2:-{\}}") || return 1
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [ "$code" != 200 ]; then
    echo "api $1 -> HTTP $code: $out" | redact >&2
    return 1
  fi
  printf '%s' "$out"
}

node_status() { api NodeService/ListNodes '{"includeRetired":true}' | jq -r --arg n "$NODE_NAME" '.nodes[] | select(.name == $n) | .status'; }
node_online() { [ "$(node_status)" = NODE_STATUS_ONLINE ]; }
inbounds_active() { # <count>: exactly that many inbounds, all ACTIVE
  api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -e --argjson n "$1" '(.inbounds | length) == $n and all(.inbounds[]; .state == "INBOUND_STATE_ACTIVE")'
}
user_json() { api UserService/GetUser "{\"userId\":\"$USER_ID\"}"; }
used_bytes() { user_json | jq -r '(.user.usedBytes // 0) | tonumber'; }
used_ge() { [ "$(used_bytes)" -ge "$1" ]; }
panel_up() { curl -sf -m 3 -o /dev/null "$ADMIN/api/mistgate.admin.v1.AuthService/GetLoginInfo" -H 'Content-Type: application/json' -d '{}'; }

start_node() { spawn "$NODE_BIN" run --state-dir "$NODE_STATE" >>"$WORK/node.log" 2>&1; }

start_panel() {
  # The health test hook: a silence of 3 s already counts as an outage (production: 10 min), so the stopped
  # agent of step 2c opens NODE_DOWN within a couple of evaluations.
  MISTGATE_HEALTH_BLIP_WINDOW=3s spawn "${PANEL_BIN:-$BIN/mistgate}" serve --data-dir "$DATA" --listen "127.0.0.1:$PUB" \
    --tls-cert "$WORK/pub.crt" --tls-key "$WORK/pub.key" --agent-addr "127.0.0.1:$PUB" >>"$WORK/panel.log" 2>&1
  PANEL_PID=$LAST_PID
  wait_for 30 "the panel to answer" panel_up || die "panel did not start"
}

# ---------------------------------------------------------------- clients

hy2_yaml() { # <socks port>: client config from $URI on stdout
  URI=$URI python3 - "$1" <<'PY'
import json, os, sys
from urllib.parse import urlsplit, parse_qs, unquote
u = urlsplit(os.environ["URI"])
q = {k: v[0] for k, v in parse_qs(u.query).items()}
auth = unquote(u.username or "") + ((":" + unquote(u.password)) if u.password else "")
j = json.dumps
out = [f"server: {j(u.netloc.rsplit('@', 1)[-1])}", f"auth: {j(auth)}"]
tls = []
if q.get("sni"): tls.append(f"  sni: {j(q['sni'])}")
if q.get("insecure") in ("1", "true"): tls.append("  insecure: true")
if q.get("pinSHA256"): tls.append(f"  pinSHA256: {j(q['pinSHA256'])}")
if tls: out += ["tls:"] + tls
if q.get("obfs"):
    out += ["obfs:", f"  type: {q['obfs']}", f"  {q['obfs']}:", f"    password: {j(q.get('obfs-password', ''))}"]
out += ["socks5:", f"  listen: 127.0.0.1:{sys.argv[1]}"]
print("\n".join(out))
PY
}

# start_client <name> <socks port>: sets LAST_PID; 0 once the client reports a connection to the node.
start_client() {
  local name=$1 port=$2 i
  hy2_yaml "$port" >"$WORK/client-$name.yaml"
  spawn "$BIN/hysteria" client -c "$WORK/client-$name.yaml" >"$WORK/client-$name.log" 2>&1
  for ((i = 0; i < 150; i++)); do
    grep -q "connected to server" "$WORK/client-$name.log" && return 0
    alive "$LAST_PID" || return 1
    sleep 0.1
  done
  return 1
}

socks_curl() { # <socks port> <curl args...>
  local port=$1
  shift
  curl -sS --socks5-hostname "127.0.0.1:$port" "$@"
}

# Test payloads come from public speed-test servers through the tunnel. They rate-limit (HTTP 429) an address that
# fetches a lot, so there are several; each download fails over to the next. All accept a Range header.
SOURCES=(hetzner cloudflare ovh cachefly)
SRC="" # the one chosen by pick_source, for downloads that must not be retried
dl_args() { # <source> <bytes>: the curl arguments that fetch exactly that many bytes
  case $1 in
    hetzner) echo "-r 0-$(($2 - 1)) https://fsn1-speed.hetzner.com/100MB.bin" ;;
    cloudflare) echo "https://speed.cloudflare.com/__down?bytes=$2" ;;
    ovh) echo "-r 0-$(($2 - 1)) https://proof.ovh.net/files/100Mb.dat" ;;
    cachefly) echo "-r 0-$(($2 - 1)) http://cachefly.cachefly.net/100mb.test" ;;
  esac
}
pick_source() { # <socks port>: sets SRC to the first server that answers with a real 2 MB body
  local s out
  for s in "${SOURCES[@]}"; do
    # shellcheck disable=SC2046 # dl_args prints several words on purpose
    out=$(socks_curl "$1" -m 30 -o /dev/null -w '%{http_code} %{size_download}' $(dl_args "$s" 2000000) 2>/dev/null) || continue
    case $out in "200 2000000" | "206 2000000") SRC=$s; return 0 ;; esac
  done
  return 1
}
# dl_one <socks port> <bytes> <first source index> <curl args...>: prints "<code> <bytes>" of the first source that
# delivers the full body, trying each source twice.
dl_one() {
  local port=$1 bytes=$2 first=$3 n=${#SOURCES[@]} i round out
  shift 3
  for ((round = 0; round < 2; round++)); do
    for ((i = 0; i < n; i++)); do
      # shellcheck disable=SC2046
      out=$(socks_curl "$port" -m 120 -w '%{http_code} %{size_download}\n' "$@" $(dl_args "${SOURCES[(first + i) % n]}" "$bytes") 2>/dev/null) || continue
      case $out in "200 $bytes" | "206 $bytes") echo "$out"; return 0 ;; esac
    done
    sleep 3
  done
  return 1
}
ok_code() { case $1 in 200 | 206) return 0 ;; esac; return 1; }

# burst <socks port>: four parallel 20 MB downloads through a connected client while the RSS of the panel and the
# node is sampled. Sets BURST_BYTES, BURST_SECS, BURST_MBPS, PEAK_PANEL, PEAK_NODE (MB).
burst() {
  local port=$1 i t0 t1 code size sampler
  local -a dls=()
  : >"$WORK/peak.panel"
  : >"$WORK/peak.node"
  touch "$WORK/sampling"
  ( while [ -e "$WORK/sampling" ]; do
      { vmrss_mb "$PANEL_PID"; echo; } >>"$WORK/peak.panel"
      { vmrss_mb "$NODE_PID"; echo; } >>"$WORK/peak.node"
      sleep 0.2
    done ) &
  sampler=$!
  PIDS+=("$sampler")
  t0=$(date +%s.%N)
  for i in 1 2 3 4; do
    dl_one "$port" 20000000 "$((i - 1))" -o /dev/null >"$WORK/dl$i.res" 2>"$WORK/dl$i.err" &
    dls+=("$!")
    PIDS+=("$!")
  done
  wait "${dls[@]}" || true
  t1=$(date +%s.%N)
  rm -f "$WORK/sampling"
  wait "$sampler" 2>/dev/null || true
  BURST_BYTES=0
  for i in 1 2 3 4; do
    read -r code size <"$WORK/dl$i.res" || { cat "$WORK/dl$i.err"; die "download $i produced no result"; }
    ok_code "$code" || die "download $i: HTTP $code"
    BURST_BYTES=$((BURST_BYTES + size))
  done
  BURST_SECS=$(python3 -c "print(round($t1 - $t0, 1))")
  BURST_MBPS=$(python3 -c "print(round($BURST_BYTES * 8 / 1e6 / ($t1 - $t0)))")
  PEAK_PANEL=$(sort -n "$WORK/peak.panel" | tail -1)
  PEAK_NODE=$(sort -n "$WORK/peak.node" | tail -1)
}

# build_hysteria: the Hysteria2 client, built from source, never downloaded. `go install ...app/v2@version` is refused
# (the app module's go.mod has replace directives pointing at ../core and ../extras), so build it from a scratch
# module that requires the app and pins core and extras to the same published tag.
HYV=v2.12.3 HYM=github.com/apernet/hysteria
build_hysteria() {
  mkdir "$WORK/hy"
  (
    cd "$WORK/hy"
    go mod init hyclient >/dev/null 2>&1
    go mod edit -go="$(sed -n 's/^toolchain go//p' "$REPO/go.mod")" -require="$HYM/app/v2@$HYV" -replace="$HYM/core/v2=$HYM/core/v2@$HYV" -replace="$HYM/extras/v2=$HYM/extras/v2@$HYV"
    GOFLAGS="-mod=mod -buildvcs=false" go build -trimpath -o "$BIN/hysteria" "$HYM/app/v2"
  )
  "$BIN/hysteria" version 2>&1 | head -3 | sed 's/^/    /'
}

# ---------------------------------------------------------------- the Cloudflare edition (--edge)
if [ -n "$EDGE" ]; then
  # shellcheck source=e2e/edge.sh
  . "$REPO/scripts/e2e/edge.sh"
  edge_run
  exit 0
fi

# ================================================================ 0. prerequisites
step "0. build (linux binaries, hysteria client from source)"
cd "$REPO"
[ -d web/dist ] || die "web/dist is missing: build the SPA first (cd web && pnpm build)"
# Release keys for the self-update steps: two throwaway keys of this run (A trusted by the builds under test, B
# "someone else's"), and build times that order the releases. See step 2d.
BI=github.com/mistgate/mistgate/internal/buildinfo
ARCH=$(go env GOARCH)
B1=1780000000 B2=1781000000 B3A=1782000000 B3A_MANIFEST=1782500000 B3B=1783000000 B5=1784000000
go build -trimpath -o "$BIN/mistgate-tool" ./cmd/mistgate # keygen only
go build -trimpath -o "$BIN/signbundle" ./scripts/e2e/signbundle
keygen() { "$BIN/mistgate-tool" release keygen --out "$1" | sed -n 's/^public key: //p'; }
KEY_A=$(keygen "$WORK/release-a.key") KEY_B=$(keygen "$WORK/release-b.key")
[ -n "$KEY_A" ] && [ -n "$KEY_B" ] || die "release keygen printed no public key"
SECRETS+=("$(cat "$WORK/release-a.key")" "$(cat "$WORK/release-b.key")")
ldf() { echo "-X $BI.Version=$1 -X $BI.Built=$2 -X $BI.ReleaseKey=$3"; } # <version> <built> <public key>
# node_build <release dir> <version> <built> <public key> [go build args]: $WORK/rel/<dir>/mistgate-node-linux-<arch>
node_build() {
  local d=$WORK/rel/$1 v=$2 b=$3 k=$4
  shift 4
  mkdir -p "$d"
  go build -trimpath -ldflags "$(ldf "$v" "$b" "$k")" "$@" -o "$d/mistgate-node-linux-$ARCH" ./cmd/mistgate-node
}
# the panel under test trusts key A, a second panel binary (step 2d, the foreign signature) trusts key B
go build -trimpath -ldflags "$(ldf 0.1.0-e2e-v2 "$B2" "$KEY_A")" -o "$BIN/mistgate" ./cmd/mistgate
node_build v1 0.1.0-e2e-v1 "$B1" "$KEY_A"
if [ -z "$M3_ONLY" ]; then # (the update builds are for step 2d, which --m3-only skips)
go build -trimpath -ldflags "$(ldf 0.1.0-e2e-v2 "$B2" "$KEY_B")" -o "$BIN/mistgate-b" ./cmd/mistgate
node_build v2 0.1.0-e2e-v2 "$B2" "$KEY_A"
node_build v3a 0.1.0-e2e-v3a "$B3A" "$KEY_A"    # its manifest will claim a later build time than the binary has
node_build v5 0.1.0-e2e-v5 "$B5" "$KEY_A"       # signed by key B in step 2d
# v3b cannot reach the panel and gives up after 20 s instead of 5 minutes. The tree has no switch for that: the build
# runs on an overlay (go build -overlay) that replaces two files with patched copies, and is checked to have changed them.
OVL=$WORK/overlay
mkdir -p "$OVL"
sed 's/\(CommitWindow[[:space:]]*=[[:space:]]*\)5 \* time.Minute/\120 * time.Second/' "$REPO/internal/node/update/update.go" >"$OVL/update.go"
sed 's/DialContext:[[:space:]]*(&net.Dialer{Timeout: dial, KeepAlive: 30 \* time.Second}).DialContext,/DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed },/' \
  "$REPO/internal/node/agent/identity.go" >"$OVL/identity.go"
! cmp -s "$OVL/update.go" "$REPO/internal/node/update/update.go" || die "e2e overlay: the commit window patch did not apply (update.go changed?)"
! cmp -s "$OVL/identity.go" "$REPO/internal/node/agent/identity.go" || die "e2e overlay: the dialer patch did not apply (identity.go changed?)"
jq -nc --arg a "$REPO/internal/node/update/update.go" --arg b "$OVL/update.go" --arg c "$REPO/internal/node/agent/identity.go" --arg d "$OVL/identity.go" \
  '{Replace: {($a): $b, ($c): $d}}' >"$OVL/overlay.json"
node_build v3b 0.1.0-e2e-v3b "$B3B" "$KEY_A" -overlay "$OVL/overlay.json"
fi
cp "$WORK/rel/v1/mistgate-node-linux-$ARCH" "$BIN/mistgate-node"
go build -trimpath -o "$BIN/h3get" ./scripts/e2e/h3get
[ -z "$M3_LIVE" ] || m3_build
NODE_BIN=$BIN/mistgate-node
cp "$NODE_BIN" "$WORK/node-v1.bin"
if [ -n "$OLD_NODE" ]; then
  cp "$OLD_NODE" "$BIN/mistgate-node-old" && chmod +x "$BIN/mistgate-node-old"
  NODE_BIN=$BIN/mistgate-node-old
  say "COMPATIBILITY RUN: the node agent is the older build $("$NODE_BIN" version 2>&1 | head -1)"
fi
build_hysteria
pass "built mistgate, mistgate-node, h3get and hysteria v2.12.3 ($(du -h "$BIN/mistgate" | cut -f1) panel, $(du -h "$BIN/mistgate-node" | cut -f1) node)"

PUB=$(free_port tcp) ADM=$(free_port tcp) HY2=$(free_port udp) HY2_PLAIN=$(free_port udp)
SOCKS=$(free_port tcp)
HOP_FROM=$((30000 + RANDOM % 20000))
HOP_TO=$((HOP_FROM + 9))
ADMIN=http://localhost:$ADM
TCP_MASQ=443
if ss -Hltn "sport = :$TCP_MASQ" | grep -q .; then TCP_MASQ=""; fi

# ================================================================ 1. panel
step "1. setup, serve, first admin (password + TOTP)"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=localhost \
  -addext subjectAltName=DNS:localhost,IP:127.0.0.1 -keyout "$WORK/pub.key" -out "$WORK/pub.crt" 2>/dev/null
SETUP_OUT=$("$BIN/mistgate" setup --data-dir "$DATA" --public-url "https://localhost:$PUB" --admin-listen "127.0.0.1:$ADM")
SETUP_TOKEN=$(sed -n 's|^Setup link: .*/setup#\(.*\)$|\1|p' <<<"$SETUP_OUT")
[ -n "$SETUP_TOKEN" ] || die "no setup link in the output of setup"
SECRETS+=("$SETUP_TOKEN")
grep -v '^Setup link:' <<<"$SETUP_OUT" | sed 's/^/    /'
start_panel
ADMIN_LOGIN=e2e-admin ADMIN_PASSWORD=$(openssl rand -hex 16)
SECRETS+=("$ADMIN_PASSWORD")

BEGIN=$(api AuthService/BeginSetup "$(jq -nc --arg t "$SETUP_TOKEN" --arg l "$ADMIN_LOGIN" --arg p "$ADMIN_PASSWORD" \
  '{setupToken:$t, displayName:"E2E admin", method:"SETUP_METHOD_PASSWORD", login:$l, password:$p}')")
CEREMONY=$(jq -r .ceremonyId <<<"$BEGIN") TOTP_SECRET=$(jq -r .totpSecret <<<"$BEGIN")
SECRETS+=("$TOTP_SECRET")
totp_code() { # RFC 6238: HMAC-SHA1, 6 digits, 30 s
  python3 - "$TOTP_SECRET" <<'PY'
import base64, hashlib, hmac, struct, sys, time
b32 = sys.argv[1].upper()
key = base64.b32decode(b32 + "=" * (-len(b32) % 8))
h = hmac.new(key, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
o = h[-1] & 15
print("%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 10**6))
PY
}
curl -sS -m 40 -D "$WORK/setup.hdr" -o /dev/null -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/FinishSetup" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$SETUP_TOKEN" --arg c "$CEREMONY" --arg code "$(totp_code)" '{setupToken:$t, ceremonyId:$c, totpCode:$code}')"
grep -q '^HTTP/[0-9.]* 200' "$WORK/setup.hdr" || die "FinishSetup failed: $(head -1 "$WORK/setup.hdr")"
COOKIE=$(sed -n 's/^[Ss]et-[Cc]ookie: \(__Host-sid=[^;]*\).*/\1/p' "$WORK/setup.hdr" | tr -d '\r')
[ -n "$COOKIE" ] || die "FinishSetup set no session cookie"
SECRETS+=("${COOKIE#*=}")
TOTP_LAST=$(($(date +%s) / 30)) STEPUP_UNTIL=$(($(date +%s) + 280)) # the sign-in just proved a factor: step-up is open for 5 minutes
api AuthService/Me | jq -e '.admin.id' >/dev/null || die "the session cookie does not work"
pass "panel up (public TLS :$PUB, admin :$ADM), first admin created, session works"

# Step-up: the owner's mutations ask for a fresh factor once the sign-in's 5 minutes are over. A password admin
# proves it with an authenticator code that is newer than the last one used (so this may wait for the next 30 s step).
stepup() {
  local out now step
  now=$(date +%s)
  [ "$STEPUP_UNTIL" -gt $((now + 90)) ] && return 0
  while step=$(($(date +%s) / 30)); [ "$step" -le "$TOTP_LAST" ]; do sleep 1; done
  out=$(api AuthService/FinishStepUp "$(jq -nc --arg c "$(totp_code)" '{totpCode:$c}')") || return 1
  TOTP_LAST=$step
  STEPUP_UNTIL=$(jq -r '.stepUpUntilUnix | tonumber' <<<"$out")
}
mutate() { stepup && api "$@"; }

# ================================================================ 2. node, profile, inbound
step "2. profile, enrollment, node online, inbound running"
ENROLL=$(api NodeService/CreateEnrollment "{\"name\":\"$NODE_NAME\",\"address\":\"127.0.0.1\"}")
NODE_ID=$(jq -r .node.id <<<"$ENROLL")
ENROLL_LINE=$(jq -r .installCommand <<<"$ENROLL" | grep -m1 ' enroll ')
flag() { sed -n "s/.* --$1 \([^ ]*\).*/\1/p" <<<"$ENROLL_LINE"; }
E_PANEL=$(flag panel) E_SNI=$(flag sni) E_PIN=$(flag ca-sha256) E_TOKEN=$(flag token)
[ -n "$E_PANEL" ] && [ -n "$E_SNI" ] && [ -n "$E_PIN" ] && [ -n "$E_TOKEN" ] || die "cannot parse the install command"
SECRETS+=("$E_TOKEN" "$E_SNI")
say "install command: enroll --panel $E_PANEL --sni [secret] --ca-sha256 ${E_PIN:0:8}... --token [secret]"

"$NODE_BIN" enroll --panel "$E_PANEL" --sni "$E_SNI" --ca-sha256 "$E_PIN" --token "$E_TOKEN" \
  --state-dir "$NODE_STATE" >"$WORK/node-enroll.log" 2>&1 || { cat "$WORK/node-enroll.log" | redact; die "enroll failed"; }
start_node
NODE_PID=$LAST_PID
wait_for 60 "node ONLINE" node_online || die "node never became ONLINE (status: $(node_status))"
PORT_CHECK=$(mutate NodeService/CheckPorts "$(jq -nc --arg n "$NODE_ID" --argjson ports "[$HY2,$HY2_PLAIN,443]" '{nodeId:$n,ports:$ports}')")
jq -e '(.errorCode == "same_host") and (.ports | length == 3) and ([.ports[] | .got == .sent] | all)' <<<"$PORT_CHECK" >/dev/null \
  || die "local UDP port check did not return same_host with matching counts: $(jq -c '{errorCode,ports}' <<<"$PORT_CHECK")"
pass "CheckPorts reports same_host with matching sent and received counts on every port"

SETTINGS=$(api ProfileService/ListProtocols '{}' | jq -c --argjson p "$HY2" --argjson f "$HOP_FROM" --argjson t "$HOP_TO" \
  '.protocols[] | select(.id == "hysteria2") | .defaultSettingsJson | fromjson
   | .tls_mode = "self_signed" | .port = $p | .hop = {from: $f, to: $t} | .obfs.password = "$generate"')
PROFILE_ID=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$SETTINGS" '{protocol:"hysteria2", name:"e2e-hy2", settingsJson:$s}')" | jq -r .profile.id)
api ProfileService/CreateInbound "{\"profileId\":\"$PROFILE_ID\",\"nodeId\":\"$NODE_ID\"}" >/dev/null
wait_for 60 "inbound ACTIVE" inbounds_active 1 || die "inbound did not become ACTIVE: $(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[0] | {state, lastError}')"
INBOUND=$(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[0] | {state, port, pin: (.certPinSha256 | .[0:8])}')
pass "node ONLINE, inbound running on UDP $HY2 ($INBOUND), hop $HOP_FROM-$HOP_TO"
nft list table inet "$NFT_TABLE" >"$WORK/nft.out" 2>/dev/null
grep -q "dport $HOP_FROM-$HOP_TO redirect to :$HY2" "$WORK/nft.out" \
  || die "the agent did not install the port-hop redirect in nft table $NFT_TABLE"
pass "agent installed the nft hop rule $HOP_FROM-$HOP_TO -> $HY2"

# Salamander makes the QUIC port unreadable to anything that is not a Hysteria2 client, so a plain HTTP/3 probe
# can only be answered (by the decoy) on an inbound without obfuscation: deploy a second one for step 7. It is in
# no group, so no user and no subscription ever sees it.
PLAIN=$(jq -c --argjson p "$HY2_PLAIN" '.tls_mode = "self_signed" | .port = $p | .hop = {from: 0, to: 0} | .obfs = {type: "none"}' <<<"$SETTINGS")
PLAIN_ID=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$PLAIN" '{protocol:"hysteria2", name:"e2e-plain", settingsJson:$s}')" | jq -r .profile.id)
api ProfileService/CreateInbound "{\"profileId\":\"$PLAIN_ID\",\"nodeId\":\"$NODE_ID\"}" >/dev/null
wait_for 60 "both inbounds ACTIVE" inbounds_active 2 || die "second (plain) inbound did not become ACTIVE: $(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[] | {port, state, lastError}')"
pass "second inbound (no obfuscation, UDP $HY2_PLAIN) ACTIVE for the HTTP/3 masquerade check"

if [ -z "$M3_ONLY" ]; then # ---- base and self-update steps 2b..9, skipped by --m3-only
# ================================================================ 2b. health
step "2b. health: the node's doctor report and a synthetic round reach the panel"
doctor_json() { api HealthService/GetDoctor "{\"nodeId\":\"$NODE_ID\"}"; }
checks_json() { api HealthService/GetChecks "{\"nodeId\":\"$NODE_ID\"}"; }
doctor_ready() { doctor_json | jq -e '.nodes[0] | .agentSupported and .hasReport and (.items | length) >= 10'; }
# both inbounds were probed through a real tunnel: every deployed cell has a finished result (ok, degraded or failed)
rounds_done() {
  checks_json | jq -e '[.rows[0].cells[] | select(.deployed) | .last.status // "" | select(. == "CHECK_STATUS_OK" or . == "CHECK_STATUS_DEGRADED" or . == "CHECK_STATUS_FAILED")] | length == 2'
}
# An --old-node build may or may not have the doctor: give it the time a current agent needs for its first report.
HAS_DOCTOR=1
if [ -n "$OLD_NODE" ]; then
  wait_for 90 "the old node's doctor report" doctor_ready || HAS_DOCTOR=0
fi
if [ "$HAS_DOCTOR" = 1 ]; then
  wait_for 90 "the node's doctor report (the agent sends one about 30 s after it connects)" doctor_ready \
    || die "no doctor report: $(doctor_json | jq -c '.nodes[0] | {agentSupported, hasReport, items: (.items | length)}')"
  DOC=$(doctor_json | jq -c '.nodes[0] | {checks: (.items | length), ok: ([.items[] | select(.status == "DOCTOR_STATUS_OK")] | length),
    warn: ([.items[] | select(.status == "DOCTOR_STATUS_WARN")] | length), fail: ([.items[] | select(.status == "DOCTOR_STATUS_FAIL")] | length),
    skip: ([.items[] | select(.status == "DOCTOR_STATUS_SKIP")] | length), stale}')
  pass "the doctor report of the node is stored and served: $DOC"
  # "Run again" reaches the agent and comes back with a fresh report
  RUN_AGE=$(api HealthService/RunDoctor "{\"nodeId\":\"$NODE_ID\"}" | jq -r '.nodes[0] | select(.hasReport) | .ageS // 0')
  [ -n "$RUN_AGE" ] && [ "$RUN_AGE" -le 10 ] || die "RunDoctor did not return a fresh report (age '$RUN_AGE')"
  pass "RunDoctor: the agent answered with a fresh report (age ${RUN_AGE} s)"
else
  # An agent without the doctor: connected and working, but no report, and the doctor RPCs say so plainly.
  # (the wait above is already past the moment a current agent would have sent its first report)
  doctor_json | jq -e '.nodes[0] | (.agentSupported | not) and (.hasReport | not) and ((.items // []) | length == 0)' >/dev/null \
    || die "old agent: expected agentSupported=false and no report, got $(doctor_json | jq -c '.nodes[0] | {agentSupported, hasReport, items: (.items | length)}')"
  OUT=$(api HealthService/RunDoctor "{\"nodeId\":\"$NODE_ID\"}" 2>&1) && die "old agent: RunDoctor succeeded: $OUT"
  grep -q 'agent too old' <<<"$OUT" || die "old agent: RunDoctor failed with something other than 'agent too old': $OUT"
  OUT=$(api HealthService/ApplyFix "{\"nodeId\":\"$NODE_ID\",\"fixId\":\"apply_baseline\",\"dryRun\":true}" 2>&1) && die "old agent: ApplyFix succeeded: $OUT"
  grep -q 'agent too old' <<<"$OUT" || die "old agent: ApplyFix failed with something other than 'agent too old': $OUT"
  node_online || die "old agent: the node is not ONLINE"
  pass "old agent: ONLINE, doctor absent (agentSupported=false, no report); RunDoctor and ApplyFix answer FAILED_PRECONDITION 'agent too old'"
fi
wait_for 120 "a synthetic round for both inbounds" rounds_done \
  || die "no synthetic round: $(checks_json | jq -c '[.rows[0].cells[] | select(.deployed) | .last]')"
ROUND=$(checks_json | jq -c '[.rows[0].cells[] | select(.deployed) | {status: .last.status, error: (.last.errorCode // ""), ms: (.last.latencyMs // 0), exit: (.last.exitCountry // "")}]')
pass "the panel probed both inbounds as a client (system credential, real tunnel): $ROUND"
# at least one check PASSES end to end (client -> node -> internet); a failed round would be a real defect
checks_json | jq -e '[.rows[0].cells[] | select(.deployed) | .last | select(.status == "CHECK_STATUS_OK" and (.latencyMs // 0) > 0)] | length >= 1' >/dev/null \
  || die "no synthetic check passed through the node: $ROUND"
pass "synthetic check OK through the real node (status OK, latency measured)"
api HealthService/ListAlerts '{}' | jq -e '(.active // []) | type == "array"' >/dev/null || die "ListAlerts did not answer"
api FleetService/Overview '{"range":"OVERVIEW_RANGE_24H"}' | jq -e '.nodesTotal | tonumber >= 1' >/dev/null || die "Overview did not answer"

# ================================================================ 2c. an alert follows the agent
step "2c. health: stop the node agent -> NODE_DOWN opens; start it again -> the alert resolves"
node_offline() { [ "$(node_status)" != NODE_STATUS_ONLINE ]; }
node_down_active() { api HealthService/ListAlerts "{\"nodeId\":\"$NODE_ID\"}" | jq -e '[(.active // [])[] | select(.kind == "ALERT_KIND_NODE_DOWN")] | length == 1'; }
node_down_history() { api HealthService/ListAlerts "{\"nodeId\":\"$NODE_ID\"}" | jq -e '[(.history // [])[] | select(.kind == "ALERT_KIND_NODE_DOWN" and .resolvedAtUnix != null and (.resolvedAtUnix | tonumber) > 0)] | length >= 1'; }
api HealthService/ListAlerts "{\"nodeId\":\"$NODE_ID\"}" | jq -e '[(.active // [])[] | select(.kind == "ALERT_KIND_NODE_DOWN")] | length == 0' >/dev/null \
  || die "a NODE_DOWN alert is open while the node is ONLINE"
stop_pid "$NODE_PID"
wait_for 30 "the panel to see the node go" node_offline \
  || die "the node is still ONLINE 30 s after its agent stopped"
wait_for 60 "NODE_DOWN to open" node_down_active \
  || die "no NODE_DOWN alert 60 s after the agent stopped: $(api HealthService/ListAlerts "{\"nodeId\":\"$NODE_ID\"}" | jq -c '[(.active // [])[] | {kind, severity}]')"
ALERT=$(api HealthService/ListAlerts "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.active[] | select(.kind == "ALERT_KIND_NODE_DOWN") | {severity, titleKey, whyKey, nodeName}')
api FleetService/Overview '{"range":"OVERVIEW_RANGE_24H"}' | jq -e '(.alertsActive // 0) >= 1 and (.alertsCritical // 0) >= 1' >/dev/null \
  || die "Overview does not count the critical alert: $(api FleetService/Overview '{"range":"OVERVIEW_RANGE_24H"}' | jq -c '{alertsActive, alertsCritical}')"
pass "agent stopped: NODE_DOWN opened and counted in the Overview badge: $ALERT"
start_node
NODE_PID=$LAST_PID
wait_for 60 "node ONLINE again" node_online || die "the node did not come back after its agent restarted (status: $(node_status))"
wait_for 60 "NODE_DOWN to resolve" node_down_history \
  || die "NODE_DOWN did not resolve after the agent returned: $(api HealthService/ListAlerts "{\"nodeId\":\"$NODE_ID\"}" | jq -c '{active: [(.active // [])[] | .kind], history: [(.history // [])[] | {kind, resolution}]}')"
api HealthService/ListAlerts "{\"nodeId\":\"$NODE_ID\"}" | jq -e '[(.active // [])[] | select(.kind == "ALERT_KIND_NODE_DOWN")] | length == 0' >/dev/null \
  || die "NODE_DOWN is still active after the agent returned"
RES=$(api HealthService/ListAlerts "{\"nodeId\":\"$NODE_ID\"}" | jq -r '[(.history // [])[] | select(.kind == "ALERT_KIND_NODE_DOWN")][0].resolution')
wait_for 60 "both inbounds ACTIVE again" inbounds_active 2 || die "the inbounds did not come back after the agent restart: $(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[] | {port, state, lastError}')"
pass "agent restarted: node ONLINE, NODE_DOWN resolved ('$RES'), both inbounds ACTIVE again"

# ================================================================ 2d. self-update
step "2d. updates: signed bundle, rollout, manual rollback, broken builds, a foreign signature"
upd() { api UpdateService/GetUpdates; }
upd_node() { upd | jq -c --arg n "$NODE_NAME" '.nodes[] | select(.name == $n)'; }
node_built() { upd_node | jq -r '(.built // "0") | tonumber'; }
node_ustate() { upd_node | jq -r '.state // ""'; }
node_built_is() { [ "$(node_built)" = "$1" ]; }
ro_json() { upd | jq -c '.rollout // {}'; }
ro_status() { ro_json | jq -r '.status // ""'; }
ro_id() { ro_json | jq -r '.id // ""'; }
ro_step() { ro_json | jq -c --arg n "$NODE_NAME" '[(.steps // [])[] | select(.nodeName == $n)][0] // {}'; }
ro_settled() { [ "$(ro_status)" != ROLLOUT_STATUS_RUNNING ]; }
ro_brief() { ro_json | jq -c '{status, pauseKey, pauseParams, steps: [(.steps // [])[] | {nodeName, state, errorKey, params}]}'; }
bundle_status() { upd | jq -r '.bundle.status // ""'; }
update_alert_active() { api HealthService/ListAlerts '{}' | jq -e '[(.active // [])[] | select(.kind == "ALERT_KIND_UPDATE_FAILED")] | length == 1'; }
no_update_alert() { api HealthService/ListAlerts '{}' | jq -e '[(.active // [])[] | select(.kind == "ALERT_KIND_UPDATE_FAILED")] | length == 0'; }
node_events_since() { # <event id>: event codes of this node newer than that id, one per line
  api FleetService/ListEvents "{\"nodeId\":\"$NODE_ID\",\"limit\":200}" | jq -r --argjson m "$1" '(.events // [])[] | select((.id | tonumber) > $m) | .code'
}
# publish_bundle <dir> <key file> <version> <built> <binary>: sign into $WORK/bundles/<dir>
publish_bundle() { "$BIN/signbundle" --key "$2" --version "$3" --built "$4" --out "$WORK/bundles/$1" "$5" >>"$WORK/release.log"; }
# install_bundle <dir>: make it the panel's <data-dir>/dist and have the panel read it now
install_bundle() {
  rm -rf "$DATA/dist.new" "$DATA/dist"
  cp -r "$WORK/bundles/$1" "$DATA/dist.new" && mv "$DATA/dist.new" "$DATA/dist"
  mutate UpdateService/RescanBundle >/dev/null
}
# start_rollout [json]: on success the rollout id. Like the admin, it names the bundle it reviewed (the version and build
# GetUpdates shows): the panel refuses a rollout of a bundle that changed since. A panel that just started checks GitHub
# for a node bundle and refuses rollouts meanwhile ("... is being installed"): wait that out.
start_rollout() {
  local pin out i
  for i in $(seq 60); do
    pin=$(upd | jq -c '{expectedVersion: .bundle.version, expectedBuilt: .bundle.built}')
    if out=$(mutate UpdateService/StartRollout "$(jq -c --argjson pin "$pin" '. + $pin' <<<"${1:-{\}}")" 2>&1); then
      jq -r '.rollout.id' <<<"$out"
      return
    fi
    grep -q 'is being installed' <<<"$out" || break
    sleep 2
  done
  echo "$out" >&2
  return 1
}

EVENT_MARK=$(api FleetService/ListEvents "{\"nodeId\":\"$NODE_ID\",\"limit\":1}" | jq -r '(.events[0].id // "0") | tonumber')
UPD0=$(upd)
jq -e '.panel.hasReleaseKey == true and ((.panel.releaseKeyFingerprint // "") | length) == 16' <<<"$UPD0" >/dev/null \
  || die "GetUpdates: the panel build does not report its release key: $(jq -c .panel <<<"$UPD0")"
[ "$(bundle_status)" = BUNDLE_STATUS_MISSING ] || die "GetUpdates: bundle status before any bundle: $(bundle_status)"
OUT=$(mutate UpdateService/StartRollout 2>&1) && die "StartRollout without a bundle succeeded: $OUT"
grep -qi 'failed_precondition' <<<"$OUT" || die "StartRollout without a bundle: expected failed_precondition, got: $OUT"
pass "no bundle: GetUpdates says MISSING with the release key fingerprint, StartRollout is refused (failed_precondition)"

publish_bundle v2 "$WORK/release-a.key" 0.1.0-e2e-v2 "$B2" "$WORK/rel/v2/mistgate-node-linux-$ARCH"
install_bundle v2
[ "$(bundle_status)" = BUNDLE_STATUS_TRUSTED ] || die "a bundle signed with the panel's release key is not TRUSTED: $(upd | jq -c .bundle)"
upd | jq -e '.bundle.version == "0.1.0-e2e-v2" and (.bundle.built | tonumber) == '"$B2"' and (.bundle.files | length) == 1' >/dev/null \
  || die "GetUpdates: unexpected bundle: $(upd | jq -c .bundle)"
pass "bundle v2 signed by the release key: TRUSTED, version and built as signed, one file"

if [ -n "$OLD_NODE" ]; then
  # A build without update/1 (and without a release key) is shown as UNSUPPORTED and never sent an update.
  [ "$(node_ustate)" = NODE_UPDATE_STATE_UNSUPPORTED ] || die "old agent: expected UNSUPPORTED, got: $(upd_node)"
  upd_node | jq -e '(.supportsUpdate // false) == false' >/dev/null || die "old agent: supportsUpdate is true"
  OUT=$(mutate UpdateService/StartRollout 2>&1) && die "old agent: StartRollout with nothing to update succeeded: $OUT"
  grep -qi 'failed_precondition' <<<"$OUT" || die "old agent: StartRollout expected failed_precondition, got: $OUT"
  if OUT=$(mutate UpdateService/StartRollout "$(upd | jq -c --arg n "$NODE_ID" '{nodeIds: [$n], expectedVersion: .bundle.version, expectedBuilt: .bundle.built}')" 2>&1); then
    RID=$(jq -r .rollout.id <<<"$OUT")
    wait_for 60 "the rollout over the unsupported node to settle" ro_settled
    ro_step | jq -e '.state == "STEP_STATE_SKIPPED" and .errorKey == "updates.step.err.unsupported"' >/dev/null \
      || die "old agent: the step of an unsupported node is not SKIPPED/unsupported: $(ro_brief)"
  else
    grep -qi 'failed_precondition' <<<"$OUT" || die "old agent: StartRollout of the unsupported node: $OUT"
  fi
  node_online || die "old agent: the node is not ONLINE after the rollout attempt"
  node_built_is 0 || die "old agent: the node reports a build time: $(upd_node)"
  alive "$NODE_PID" || die "old agent: the node process is gone"
  grep -q 'update' "$WORK/node.log" && say "note: the old node's log mentions update: $(grep -c update "$WORK/node.log") line(s)"
  pass "old agent: UNSUPPORTED, StartRollout refuses or skips it, the node stays ONLINE and untouched; the rest of the scenario runs on it"
else
  upd_node | jq -e '.supportsUpdate == true and .state == "NODE_UPDATE_STATE_OUTDATED"' >/dev/null \
    || die "node v1: expected supportsUpdate and OUTDATED, got: $(upd_node)"
  upd_node | jq -e '(.crashGuard // false) == false' >/dev/null || die "node v1 reports crashGuard without a generation 2 unit: $(upd_node)"
  node_built_is "$B1" || die "node v1 reports built $(node_built), want $B1"
  pass "node v1 (built $B1): supports update/1, OUTDATED, no crash guard (generation 1 unit: only the self-rollback protects it)"

  # ---- v2: canary rollout of the only node; the same process re-executes the new binary and commits
  RID=$(start_rollout)
  [ -n "$RID" ] && [ "$RID" != null ] || die "StartRollout returned no rollout id"
  say "rollout $RID started"
  wait_for 300 "the rollout to settle" ro_settled || die "rollout did not settle in 5 minutes: $(ro_brief)"
  [ "$(ro_status)" = ROLLOUT_STATUS_DONE ] || die "rollout v2 ended as $(ro_status): $(ro_brief)"
  ro_step | jq -e '.state == "STEP_STATE_PASSED" and (.stage // 0) == 0' >/dev/null || die "rollout v2: the step is not PASSED on stage 0: $(ro_step)"
  node_built_is "$B2" || die "after the rollout the node reports built $(node_built), want $B2"
  [ "$(node_ustate)" = NODE_UPDATE_STATE_UP_TO_DATE ] || die "after the rollout the node is $(node_ustate)"
  alive "$NODE_PID" || die "the node process ($NODE_PID) is gone: the update must re-execute in place"
  [ "$(readlink "/proc/$NODE_PID/exe")" = "$NODE_BIN" ] || die "pid $NODE_PID runs $(readlink "/proc/$NODE_PID/exe"), not the swapped $NODE_BIN"
  cmp -s "$NODE_BIN" "$WORK/rel/v2/mistgate-node-linux-$ARCH" || die "the installed binary is not the one from the bundle"
  cmp -s "$NODE_BIN.prev" "$WORK/node-v1.bin" || die "$NODE_BIN.prev is not the v1 binary"
  [ ! -e "$NODE_STATE/update.pending" ] || die "update.pending is still there after the commit: $(cat "$NODE_STATE/update.pending")"
  [ ! -e "$NODE_BIN.new" ] || die "$NODE_BIN.new is left behind"
  "$NODE_BIN" version | sed -n 2p | grep -qx "built $B2" || die "the binary on disk is not built $B2: $("$NODE_BIN" version | tr '\n' ' ')"
  node_online || die "node not ONLINE after the update"
  wait_for 60 "both inbounds ACTIVE after the update" inbounds_active 2 || die "inbounds after the update: $(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[] | {port, state, lastError}')"
  CODES=$(node_events_since "$EVENT_MARK" | sort | uniq -c | tr '\n' ' ')
  node_events_since "$EVENT_MARK" | grep -qx update_committed || die "no update_committed event for the node (events: $CODES)"
  ! node_events_since "$EVENT_MARK" | grep -Eqx 'node_blip|node_down' || die "the update made the node look down (events: $CODES)"
  node_down_active >/dev/null 2>&1 && die "a NODE_DOWN alert is open after the update"
  if upd_node | jq -e '.lastUpdate.outcome == "ok"' >/dev/null; then
    LASTUP="last_update ok is recorded"
  else
    LASTUP="last_update is not recorded until the node's next Hello (the UI relies on the rollout step)"
    note "v2: GetUpdates has no lastUpdate for the node right after the commit"
  fi
  pass "rollout v2: canary PASSED, the same pid $NODE_PID runs built $B2, .prev = v1, update.pending gone, update_committed and no node_blip/node_down (events: $CODES); $LASTUP"

  # ---- manual rollback (RollbackAgent) and a second rollout of the same bundle
  mutate UpdateService/RollbackNode "{\"nodeId\":\"$NODE_ID\"}" >/dev/null || die "RollbackNode was refused"
  wait_for 90 "the node to be back on v1" node_built_is "$B1" || die "RollbackNode: the node still reports built $(node_built)"
  wait_for 60 "node ONLINE after the rollback" node_online || die "node not ONLINE after the manual rollback"
  alive "$NODE_PID" || die "the node process is gone after the manual rollback"
  cmp -s "$NODE_BIN" "$WORK/node-v1.bin" || die "after RollbackNode the binary is not v1"
  [ ! -e "$NODE_BIN.prev" ] || die "the rollback left a .prev (it was consumed)"
  STATE=$(node_ustate)
  case $STATE in NODE_UPDATE_STATE_ROLLED_BACK | NODE_UPDATE_STATE_OUTDATED) ;; *) die "after RollbackNode the state is $STATE" ;; esac
  wait_for 60 "both inbounds ACTIVE after the rollback" inbounds_active 2 || die "inbounds after the rollback not ACTIVE"
  pass "RollbackNode: same pid, v1 restored from .prev, node ONLINE ($STATE)"
  RID=$(start_rollout "{\"nodeIds\":[\"$NODE_ID\"]}")
  wait_for 300 "the second rollout to settle" ro_settled || die "second rollout did not settle: $(ro_brief)"
  [ "$(ro_status)" = ROLLOUT_STATUS_DONE ] || die "second rollout ended as $(ro_status): $(ro_brief)"
  node_built_is "$B2" || die "after the second rollout the node reports built $(node_built)"
  wait_for 60 "both inbounds ACTIVE after the second update" inbounds_active 2 || die "inbounds after the second update not ACTIVE"
  # The gate counts an update_committed event from up to a minute before the step was sent (clockSlack), and the first
  # update committed 25 s earlier, so the rollout can be DONE a second or two before the agent commits; the next update
  # would then be refused as busy. Let the agent commit first.
  wait_for 60 "the agent to commit the second update" test ! -e "$NODE_STATE/update.pending" || die "the second update never committed"
  pass "a rolled-back node takes the bundle again: rollout DONE, built $B2"

  # ---- v3a: a manifest that claims a later build time than the binary has; the agent notices at once and goes back
  publish_bundle v3a "$WORK/release-a.key" 0.1.0-e2e-v3a "$B3A_MANIFEST" "$WORK/rel/v3a/mistgate-node-linux-$ARCH"
  install_bundle v3a
  upd_node | jq -e '.state == "NODE_UPDATE_STATE_OUTDATED"' >/dev/null || die "v3a: node should be OUTDATED: $(upd_node)"
  RID=$(start_rollout "{\"nodeIds\":[\"$NODE_ID\"]}")
  wait_for 240 "the rollout v3a to settle" ro_settled || die "rollout v3a did not settle: $(ro_brief)"
  [ "$(ro_status)" = ROLLOUT_STATUS_PAUSED ] || die "rollout v3a: expected PAUSED, got: $(ro_brief)"
  ro_json | jq -e '.pauseKey == "updates.pause.gate_failed"' >/dev/null || die "rollout v3a: pause reason: $(ro_brief)"
  ro_step | jq -e '.state == "STEP_STATE_ROLLED_BACK" and .errorKey == "updates.step.err.rolled_back_by_agent" and .params.reason == "built_mismatch"' >/dev/null \
    || die "rollout v3a: step: $(ro_step)"
  wait_for 60 "the node to be back on built $B2" node_built_is "$B2" || die "v3a: the node reports built $(node_built), want $B2"
  node_online || die "v3a: node not ONLINE"
  alive "$NODE_PID" || die "v3a: the node process is gone"
  cmp -s "$NODE_BIN" "$WORK/rel/v2/mistgate-node-linux-$ARCH" || die "v3a: the installed binary is not v2 again"
  wait_for 120 "an UPDATE_FAILED alert" update_alert_active || die "v3a: no UPDATE_FAILED alert: $(api HealthService/ListAlerts '{}' | jq -c '[(.active // [])[] | .kind]')"
  ALERT=$(api HealthService/ListAlerts '{}' | jq -c '.active[] | select(.kind == "ALERT_KIND_UPDATE_FAILED") | {severity, titleKey, whyKey, nodeName}')
  upd_node | jq -e '.lastUpdate.outcome == "rolled_back" and .lastUpdate.reason == "built_mismatch"' >/dev/null || die "v3a: lastUpdate: $(upd_node)"
  mutate UpdateService/CancelRollout "{\"rolloutId\":\"$(ro_id)\"}" >/dev/null || die "CancelRollout was refused"
  wait_for 120 "the UPDATE_FAILED alert to close" no_update_alert || die "v3a: the alert is still open after CancelRollout"
  pass "v3a (built_mismatch): agent rolled itself back, step ROLLED_BACK, rollout PAUSED (gate_failed), UPDATE_FAILED alert opened ($ALERT) and closed by CancelRollout; node on v2 again, same pid"

  # ---- v3b: a build that cannot reach the panel; it rolls itself back when its (20 s) commit window ends
  publish_bundle v3b "$WORK/release-a.key" 0.1.0-e2e-v3b "$B3B" "$WORK/rel/v3b/mistgate-node-linux-$ARCH"
  install_bundle v3b
  T_B=$(date +%s)
  RID=$(start_rollout "{\"nodeIds\":[\"$NODE_ID\"]}")
  wait_for 300 "the rollout v3b to settle" ro_settled || die "rollout v3b did not settle: $(ro_brief)"
  [ "$(ro_status)" = ROLLOUT_STATUS_PAUSED ] || die "rollout v3b: expected PAUSED, got: $(ro_brief)"
  ro_json | jq -e '(.pauseKey | startswith("updates.pause."))' >/dev/null || die "rollout v3b: no pause reason: $(ro_brief)"
  ro_step | jq -e '.state == "STEP_STATE_ROLLED_BACK" and .errorKey == "updates.step.err.rolled_back_by_agent" and .params.reason == "not_committed"' >/dev/null \
    || die "rollout v3b: step: $(ro_step)"
  wait_for 120 "the node to be back on built $B2" node_built_is "$B2" || die "v3b: the node reports built $(node_built), want $B2"
  wait_for 60 "node ONLINE after v3b" node_online || die "v3b: node not ONLINE"
  alive "$NODE_PID" || die "v3b: the node process is gone"
  cmp -s "$NODE_BIN" "$WORK/rel/v2/mistgate-node-linux-$ARCH" || die "v3b: the installed binary is not v2 again"
  [ ! -e "$NODE_STATE/update.pending" ] || die "v3b: update.pending is left behind"
  upd_node | jq -e '.lastUpdate.outcome == "rolled_back" and .lastUpdate.reason == "not_committed"' >/dev/null || die "v3b: lastUpdate: $(upd_node)"
  wait_for 120 "an UPDATE_FAILED alert" update_alert_active || die "v3b: no UPDATE_FAILED alert"
  grep -q 'not_committed' "$WORK/node.log" || die "v3b: the node log does not say why it rolled back"
  mutate UpdateService/ResumeRollout "{\"rolloutId\":\"$(ro_id)\"}" >/dev/null || die "ResumeRollout was refused"
  wait_for 120 "the UPDATE_FAILED alert to close" no_update_alert || die "v3b: the alert is still open after ResumeRollout"
  wait_for 60 "the resumed rollout to settle" ro_settled || true
  pass "v3b (cannot connect): agent rolled itself back after $(($(date +%s) - T_B)) s (not_committed), rollout PAUSED then $(ro_status) after Resume, alert opened and closed; node on v2, same pid"
  wait_for 60 "both inbounds ACTIVE again" inbounds_active 2 || die "inbounds after v3b not ACTIVE"

  # ---- v5: signed by another key. A panel that trusts key B (a hacked panel, or a stolen signing key) starts the
  # rollout; the node, which trusts key A only, refuses the manifest.
  mutate UpdateService/CancelRollout "{\"rolloutId\":\"$(ro_id)\"}" >/dev/null 2>&1 || true
  stop_pid "$PANEL_PID"
  # the data directory still holds key A: the key-B binary trusts nothing until its key is confirmed explicitly
  "$BIN/mistgate-b" release trust-key --data-dir "$DATA" >>"$WORK/release.log" || die "release trust-key (key B) failed"
  PANEL_BIN=$BIN/mistgate-b start_panel
  wait_for 90 "node ONLINE at the key-B panel" node_online || die "node did not connect to the second panel binary"
  publish_bundle v5 "$WORK/release-b.key" 0.1.0-e2e-v5 "$B5" "$WORK/rel/v5/mistgate-node-linux-$ARCH"
  install_bundle v5
  [ "$(bundle_status)" = BUNDLE_STATUS_TRUSTED ] || die "the key-B panel does not trust a bundle signed with key B: $(upd | jq -c .bundle)"
  RID=$(start_rollout "{\"nodeIds\":[\"$NODE_ID\"]}")
  wait_for 120 "the rollout v5 to settle" ro_settled || die "rollout v5 did not settle: $(ro_brief)"
  [ "$(ro_status)" = ROLLOUT_STATUS_PAUSED ] || die "rollout v5: expected PAUSED, got: $(ro_brief)"
  ro_json | jq -e '.pauseKey == "updates.pause.step_failed" and .pauseParams.reason == "bad_signature"' >/dev/null || die "rollout v5: pause: $(ro_brief)"
  ro_step | jq -e '.state == "STEP_STATE_FAILED" and .errorKey == "updates.step.err.bad_signature"' >/dev/null || die "rollout v5: step: $(ro_step)"
  node_built_is "$B2" || die "v5: the node reports built $(node_built) after refusing a foreign signature"
  alive "$NODE_PID" || die "v5: the node process is gone"
  cmp -s "$NODE_BIN" "$WORK/rel/v2/mistgate-node-linux-$ARCH" || die "v5: the installed binary changed"
  [ ! -e "$NODE_BIN.new" ] && [ ! -e "$NODE_STATE/update.pending" ] || die "v5: the refused update left files behind"
  node_online || die "v5: node not ONLINE"
  mutate UpdateService/CancelRollout "{\"rolloutId\":\"$(ro_id)\"}" >/dev/null || die "CancelRollout of the v5 rollout was refused"
  pass "v5 (signed by another key): the key-B panel started it, the node refused with bad_signature, step FAILED, rollout PAUSED (step_failed); binary, pid and state untouched"

  # ---- back to the panel under test and the v2 bundle for the rest of the run
  stop_pid "$PANEL_PID"
  "$BIN/mistgate" release trust-key --data-dir "$DATA" >>"$WORK/release.log" || die "release trust-key (key A) failed"
  start_panel
  wait_for 90 "node ONLINE at the panel under test" node_online || die "node did not come back to the panel under test"
  install_bundle v2
  [ "$(bundle_status)" = BUNDLE_STATUS_TRUSTED ] || die "v2 bundle is not TRUSTED after the restore"
  [ "$(node_ustate)" = NODE_UPDATE_STATE_UP_TO_DATE ] || die "after the whole scenario the node is $(node_ustate), want UP_TO_DATE"
  wait_for 60 "both inbounds ACTIVE" inbounds_active 2 || die "inbounds not ACTIVE after the update scenario"
  pass "panel under test restored with the v2 bundle: node UP_TO_DATE on built $B2, inbounds ACTIVE (the traffic steps below run on the updated node)"
fi

# ================================================================ 3. user and subscription
step "3. user, subscription link, headers"
GROUP_ID=$(api GroupService/CreateGroup "{\"name\":\"e2e\",\"profileIds\":[\"$PROFILE_ID\"]}" | jq -r .group.id)
CREATED=$(api UserService/CreateUser "{\"name\":\"alice\",\"groupId\":\"$GROUP_ID\"}")
USER_ID=$(jq -r .user.id <<<"$CREATED") SUB_URL=$(jq -r .subscriptionUrl <<<"$CREATED")
SUB_TOKEN=${SUB_URL##*/}
SECRETS+=("$SUB_TOKEN")
case $SUB_URL in https://localhost:$PUB/*) ;; *) die "unexpected subscription URL shape" ;; esac

fetch_sub() { # <user agent> <header file> -> body on stdout
  curl -sk -m 15 -A "$1" -D "$2" "$SUB_URL"
}
have_line() {
  local body
  body=$(fetch_sub 'Happ/3.0' "$WORK/sub.hdr" | base64 -d 2>/dev/null) || return 1
  grep -q '^hysteria2://' <<<"$body"
}
# a self-signed inbound is published once the node reported its certificate pin
wait_for 40 "hysteria2:// line in the subscription" have_line || die "subscription has no hysteria2:// line"

BODY_HAPP=$(fetch_sub 'Happ/3.0' "$WORK/sub-happ.hdr")
BODY_MIHOMO=$(fetch_sub 'mihomo/1.19.31' "$WORK/sub-mihomo.hdr")
hdr() { tr -d '\r' <"$1" | sed -n "s/^$2: *//Ip" | head -1; }
for ua in happ mihomo; do
  f=$WORK/sub-$ua.hdr
  grep -q '^HTTP/[0-9.]* 200' "$f" || die "subscription ($ua) is not 200"
  title=$(hdr "$f" profile-title)
  case $title in base64:?*) ;; *) die "subscription ($ua): profile-title is not base64:… ($title)" ;; esac
  base64 -d <<<"${title#base64:}" >/dev/null 2>&1 || die "subscription ($ua): profile-title is not valid base64"
  hdr "$f" subscription-userinfo | grep -Eq '^upload=[0-9]+; download=[0-9]+; total=[0-9]+; expire=[0-9]+$' \
    || die "subscription ($ua): bad subscription-userinfo: $(hdr "$f" subscription-userinfo)"
  hdr "$f" profile-update-interval | grep -Eq '^[0-9]+$' || die "subscription ($ua): bad profile-update-interval"
done
[ "$BODY_HAPP" = "$BODY_MIHOMO" ] || note "subscription body differs between Happ and mihomo (this scenario expects one format)"
LINES=$(base64 -d <<<"$BODY_HAPP")
[ "$(grep -c '^hysteria2://' <<<"$LINES")" = 1 ] || die "expected exactly one hysteria2:// line, got: $(grep -c . <<<"$LINES") line(s)"
URI=$(grep -m1 '^hysteria2://' <<<"$LINES")
AUTH=$(URI=$URI python3 -c 'import os; from urllib.parse import urlsplit, unquote; print(unquote(urlsplit(os.environ["URI"]).username or ""))')
SECRETS+=("$AUTH")
OBFS_PW=$(URI=$URI python3 -c 'import os; from urllib.parse import urlsplit, parse_qs; print(parse_qs(urlsplit(os.environ["URI"]).query).get("obfs-password", [""])[0])')
[ -n "$OBFS_PW" ] && [ "$OBFS_PW" != "••••" ] && SECRETS+=("$OBFS_PW")
echo "    $(redact <<<"$URI" | sed 's/[?].*//')?<query redacted: $(URI=$URI python3 -c 'import os; from urllib.parse import urlsplit, parse_qs; print(",".join(sorted(parse_qs(urlsplit(os.environ["URI"]).query))))')>"
grep -q 'insecure=1' <<<"$URI" && grep -q 'pinSHA256=' <<<"$URI" || die "self-signed URI must carry insecure=1 and pinSHA256"
grep -q 'obfs=salamander' <<<"$URI" || die "URI lacks obfs=salamander"
pass "subscription: Happ and mihomo fetches 200, profile-title base64, subscription-userinfo and profile-update-interval ok, one hysteria2:// line with pin"

# ================================================================ 9a. idle RSS
step "9a. idle RSS right after setup (panel and node, before any traffic)"
sleep 12 # let the first stats cycles settle
RSS_PANEL_SETUP=$(vmrss_mb "$PANEL_PID") RSS_NODE_IDLE=$(vmrss_mb "$NODE_PID")
say "RSS: panel ${RSS_PANEL_SETUP} MB (it just hashed the admin password with argon2id, 64 MiB), node ${RSS_NODE_IDLE} MB"

# ================================================================ 4. real client, real download
step "4. real Hysteria2 client, download through the node"
start_client main "$SOCKS" || { tail -n 15 "$WORK/client-main.log" | redact; die "client did not connect"; }
MAIN_CLIENT=$LAST_PID
say "client connected (SOCKS5 127.0.0.1:$SOCKS)"
IP_VIA=$(socks_curl "$SOCKS" -m 20 https://api.ipify.org 2>/dev/null || true)
# The node runs on this machine, so its exit address is the machine's own. Never print it: the log may be shared.
if [ -n "$IP_VIA" ]; then
  IP_DIRECT=$(curl -s -m 15 https://api.ipify.org 2>/dev/null || true)
  if [ "$IP_VIA" = "$IP_DIRECT" ]; then
    say "egress IP seen through the node is this machine's own (address not printed)"
  else
    say "egress IP seen through the node differs from this machine's own (addresses not printed)"
  fi
fi

burst "$SOCKS"
DL_BYTES=$BURST_BYTES
[ "$DL_BYTES" -ge 20000000 ] || die "downloaded only $DL_BYTES bytes"
pass "downloaded $DL_BYTES bytes (4 x 20 MB in parallel) through the node in ${BURST_SECS} s (~${BURST_MBPS} Mbit/s)"
RSS_PANEL_PEAK1=$PEAK_PANEL RSS_NODE_PEAK=$PEAK_NODE MBPS=$BURST_MBPS

# ================================================================ 5. traffic in the panel
step "5. the panel shows the traffic"
MIN_USED=$((DL_BYTES * 95 / 100))
T_SEEN=$(date +%s)
wait_for 30 "user usedBytes >= 95% of the download" used_ge "$MIN_USED" \
  || die "after 30 s the panel shows $(used_bytes) bytes, the client downloaded $DL_BYTES"
LAG=$(($(date +%s) - T_SEEN))
sleep 11 # one more stats cycle: the number must be stable, not still growing past the download
USED_BYTES=$(used_bytes)
RATIO=$(python3 -c "print(round($USED_BYTES / $DL_BYTES, 4))")
[ "$USED_BYTES" -le $((DL_BYTES * 110 / 100)) ] || die "panel counted $USED_BYTES bytes for a $DL_BYTES byte download (ratio $RATIO)"
say "GetUser.usedBytes = $USED_BYTES, downloaded = $DL_BYTES, ratio $RATIO (visible ${LAG} s after the download ended)"
USER_NOW=$(user_json)
jq -e '.user.online == true' <<<"$USER_NOW" >/dev/null || die "GetUser: user is not online while the client is connected"
OV=$(api FleetService/Overview '{"range":"OVERVIEW_RANGE_24H"}')
SERIES=$(jq '[.traffic[].values[]?.value | tonumber] | add // 0' <<<"$OV")
[ "$SERIES" -ge "$MIN_USED" ] || die "Overview traffic series sums to $SERIES, expected >= $MIN_USED"
jq -e '.usersOnline >= 1' <<<"$OV" >/dev/null || die "Overview: usersOnline is 0"
TODAY=$(api NodeService/ListNodes '{}' | jq -r --arg n "$NODE_NAME" '.nodes[] | select(.name == $n) | (.trafficTodayBytes // 0)')
say "Overview.traffic series sum = $SERIES bytes, usersOnline = $(jq .usersOnline <<<"$OV"), node trafficToday = $TODAY"
pass "panel traffic matches the download: used $USED_BYTES vs downloaded $DL_BYTES (ratio $RATIO), series $SERIES"

# ================================================================ 6. disable / enable
step "6. disable the user: the tunnel dies, a new connection is refused; enable: it works again"
pick_source "$SOCKS" || die "no speed-test server answered through the tunnel"
say "payload source for the long download: $SRC"
socks_curl "$SOCKS" -m 120 --limit-rate 1M -o "$WORK/long.bin" -w '%{http_code} %{size_download}\n' $(dl_args "$SRC" 60000000) >"$WORK/long.res" 2>"$WORK/long.err" &
LONG=$!
PIDS+=("$LONG")
long_started() { [ "$(stat -c %s "$WORK/long.bin" 2>/dev/null || echo 0)" -gt 200000 ]; }
wait_for 15 "the long download to start" long_started || die "the long download did not start: $(cat "$WORK/long.res" "$WORK/long.err" 2>/dev/null)"
T_OFF=$(date +%s.%N)
api UserService/SetUsersEnabled "{\"userIds\":[\"$USER_ID\"],\"enabled\":false}" | jq -e '.users[0].status == "USER_STATUS_DISABLED"' >/dev/null || die "SetUsersEnabled(false) did not disable"
# the node kicks the session: the hysteria client logs the closed stream (curl itself may keep draining socket buffers)
KICK=""
for ((i = 0; i < 300; i++)); do
  if grep -q 'Application error' "$WORK/client-main.log"; then KICK=$(python3 -c "import time; print(round(time.time() - $T_OFF, 1))"); break; fi
  sleep 0.1
done
[ -n "$KICK" ] || die "the node did not kick the session within 30 s of the user being disabled"
wait_for 60 "the running download to end" not_alive "$LONG" || die "the download kept running 60 s after the user was disabled"
LONG_RC=0
wait "$LONG" || LONG_RC=$?
SZ1=$(stat -c %s "$WORK/long.bin")
[ "$LONG_RC" != 0 ] || die "the download finished normally instead of being cut"
[ "$SZ1" -lt 60000000 ] || die "the whole file arrived"
pass "disabled user: session kicked ${KICK} s after SetUsersEnabled(false), the download died (curl exit $LONG_RC, $SZ1 of 60000000 bytes)"

# the same client instance must not get through any more either
if socks_curl "$SOCKS" -m 15 -o /dev/null $(dl_args "$SRC" 1000) 2>/dev/null; then die "the old client still reaches the internet"; fi
stop_pid "$MAIN_CLIENT"
REFUSED_PORT=$(free_port tcp)
if start_client refused "$REFUSED_PORT"; then die "a new client connected although the user is disabled"; fi
REASON=$(grep -Eio 'authentication[^"]*|auth[a-z ]*(fail|error)[^"]*|forbidden|403' "$WORK/client-refused.log" | head -1 || true)
pass "disabled user: a new client is refused (${REASON:-client exited without connecting})"
SUB_OFF=$(fetch_sub 'Happ/3.0' "$WORK/sub-off.hdr" | base64 -d 2>/dev/null || true)
# no real server, only the entry that says why (it points at 0.0.0.0:1 and never connects)
grep '^hysteria2://' <<<"$SUB_OFF" | grep -qv '^hysteria2://off@0\.0\.0\.0:1/' && die "the subscription still lists a server for a disabled user"
grep -q '^hysteria2://off@0\.0\.0\.0:1/' <<<"$SUB_OFF" || die "the disabled user's subscription has no entry that says why"
[ "$(hdr "$WORK/sub-off.hdr" profile-update-interval)" = 1 ] || note "disabled user's profile-update-interval is not the short one"

api UserService/SetUsersEnabled "{\"userIds\":[\"$USER_ID\"],\"enabled\":true}" | jq -e '.users[0].status == "USER_STATUS_ACTIVE"' >/dev/null || die "SetUsersEnabled(true) did not enable"
try_again() {
  local p
  p=$(free_port tcp)
  if start_client again "$p"; then
    AGAIN_CLIENT=$LAST_PID AGAIN_PORT=$p
    dl_one "$p" 1000000 0 -o /dev/null >/dev/null && return 0
  fi
  stop_pid "$LAST_PID"
  return 1
}
T_ON=$(date +%s)
wait_for 40 "a new client to work after enabling" try_again || die "after re-enabling the user no client gets through"
pass "re-enabled user: a fresh client connects and downloads again (after $(($(date +%s) - T_ON)) s)"
stop_pid "$AGAIN_CLIENT"

# ================================================================ 7. masquerade
step "7. masquerade: HTTPS on TCP and HTTP/3 on the QUIC port show the decoy"
if [ -n "$TCP_MASQ" ]; then
  curl -sk -m 10 -D "$WORK/masq-tcp.hdr" -o "$WORK/masq-tcp.body" "https://127.0.0.1:$TCP_MASQ/" || die "HTTPS GET to the node's TCP $TCP_MASQ failed"
  grep -q '^HTTP/[0-9.]* 200' "$WORK/masq-tcp.hdr" || die "TCP masquerade: not 200: $(head -1 "$WORK/masq-tcp.hdr")"
  grep -q 'Coming soon' "$WORK/masq-tcp.body" || die "TCP masquerade: not the decoy page"
  ! grep -qi '^server:' "$WORK/masq-tcp.hdr" || die "TCP masquerade: has a Server header"
  curl -sk -m 10 -o "$WORK/masq-tcp.404" "https://127.0.0.1:$TCP_MASQ/login.php"
  pass "HTTPS GET https://127.0.0.1:$TCP_MASQ/ -> 200 decoy page, no Server header"
else
  note "masquerade TCP check skipped: TCP 443 is taken on this machine"
fi
"$BIN/h3get" "https://127.0.0.1:$HY2_PLAIN/" >"$WORK/masq-h3.out" || die "HTTP/3 GET to the QUIC port failed"
head -1 "$WORK/masq-h3.out" | grep -q ' 200' || die "HTTP/3 masquerade: not 200: $(head -1 "$WORK/masq-h3.out")"
grep -q 'Coming soon' "$WORK/masq-h3.out" || die "HTTP/3 masquerade: not the decoy page"
! grep -qi '^server:' "$WORK/masq-h3.out" || die "HTTP/3 masquerade: has a Server header"
"$BIN/h3get" "https://127.0.0.1:$HY2_PLAIN/login.php" >"$WORK/masq-h3.404" || die "HTTP/3 GET of an unknown path failed"
grep -q 'Not Found' "$WORK/masq-h3.404" || die "HTTP/3 masquerade: unknown path is not the 404 page"
if [ -n "$TCP_MASQ" ]; then
  [ "$(cat "$WORK/masq-tcp.404")" = "$(sed '1,/^$/d' "$WORK/masq-h3.404")" ] || die "the 404 pages of TCP and HTTP/3 differ"
fi
pass "HTTP/3 GET on UDP $HY2_PLAIN -> 200 decoy page, no Server header, unknown path = the same 404"

# ================================================================ 8. panel restart
step "8. restart the panel: the node reconnects, nothing is counted twice"
sleep 12 # the last stats batch of the re-enable test must have arrived, or the comparison below is unfair
USED_BEFORE=$(used_bytes)
# 8a: idle restart (no traffic at all)
stop_pid "$PANEL_PID"
alive "$PANEL_PID" && die "the panel did not stop"
start_panel
wait_for 90 "node ONLINE after the panel restart" node_online || die "node did not come back after the panel restart (status: $(node_status))"
sleep 25 # two stats cycles and a resend window
RSS_PANEL_IDLE=$(vmrss_mb "$PANEL_PID") # a restarted panel has hashed nothing: its real idle footprint
USED_AFTER=$(used_bytes)
[ "$USED_AFTER" = "$USED_BEFORE" ] || die "idle restart changed usedBytes: $USED_BEFORE -> $USED_AFTER"
pass "idle restart: node ONLINE again, usedBytes unchanged ($USED_BEFORE -> $USED_AFTER)"

# 8b: restart in the middle of traffic; every batch that was in flight is resent after the reconnect
start_client restart "$SOCKS" || die "client did not connect for the restart test"
RC=$LAST_PID
pick_source "$SOCKS" || die "no speed-test server answered through the tunnel" # its 2 MB probe is traffic too
sleep 12 # ... so let its stats batch arrive before the baseline is read
USED_PRE=$(used_bytes)
socks_curl "$SOCKS" -m 120 --limit-rate 1500K -o /dev/null -w '%{http_code} %{size_download}\n' $(dl_args "$SRC" 20000000) >"$WORK/dlr.res" 2>"$WORK/dlr.err" &
DLR=$!
PIDS+=("$DLR")
sleep 4
stop_pid "$PANEL_PID"
start_panel
wait "$DLR" || true
read -r code size <"$WORK/dlr.res" || die "the restart-test download produced no result"
ok_code "$code" || die "restart-test download: HTTP $code"
wait_for 90 "node ONLINE after the second restart" node_online || die "node did not come back after the second panel restart"
sleep 30
USED_POST=$(used_bytes)
DELTA=$((USED_POST - USED_PRE))
RATIO2=$(python3 -c "print(round($DELTA / $size, 4))")
[ "$DELTA" -ge $((size * 95 / 100)) ] || die "panel restart lost traffic: downloaded $size, counted $DELTA (ratio $RATIO2)"
[ "$DELTA" -le $((size * 110 / 100)) ] || die "panel restart double counted: downloaded $size, counted $DELTA (ratio $RATIO2)"
pass "restart in the middle of a download: downloaded $size, counted $DELTA (ratio $RATIO2)"
burst "$SOCKS" # load on the restarted (argon2-free) panel, for the RSS report
RSS_PANEL_PEAK2=$PEAK_PANEL
stop_pid "$RC"

# ================================================================ 9b. RSS report
step "9. RSS against the memory budget"
RSS_PANEL_END=$(vmrss_mb "$PANEL_PID") RSS_NODE_END=$(vmrss_mb "$NODE_PID")
printf '    %-44s %9s  %s\n' "" "measured" "budget"
printf '    %-44s %6s MB  %s\n' "panel, idle after a restart" "$RSS_PANEL_IDLE" "<= 80 MB"
printf '    %-44s %6s MB  %s\n' "panel, 12 s after setup (argon2id garbage)" "$RSS_PANEL_SETUP" "(informational, see below)"
printf '    %-44s %6s MB  %s\n' "panel, peak under load (4 x 20 MB), restarted" "$RSS_PANEL_PEAK2" "(5000 users / 50 nodes: <= 200 MB)"
printf '    %-44s %6s MB\n' "panel, peak under load, first process" "$RSS_PANEL_PEAK1"
printf '    %-44s %6s MB\n' "panel, at the end" "$RSS_PANEL_END"
printf '    %-44s %6s MB  %s\n' "node, idle" "$RSS_NODE_IDLE" "<= 100 MB (hy2 + AWG + WARP; here hy2 only)"
printf '    %-44s %6s MB  %s\n' "node, peak under load (4 x 20 MB)" "$RSS_NODE_PEAK" "(~$MBPS Mbit/s, $(nproc) vCPU)"
printf '    %-44s %6s MB\n' "node, at the end" "$RSS_NODE_END"
# The memory budget sets targets, not gates: report, and flag an overrun without failing the run.
OVER=""
python3 -c "import sys; sys.exit(0 if float('$RSS_PANEL_IDLE') <= 80 else 1)" || OVER="$OVER panel-idle"
python3 -c "import sys; sys.exit(0 if float('$RSS_NODE_IDLE') <= 100 else 1)" || OVER="$OVER node-idle"
if [ -z "$OVER" ]; then
  pass "idle RSS within the memory budget: panel ${RSS_PANEL_IDLE} MB <= 80, node ${RSS_NODE_IDLE} MB <= 100 (under load: panel ${RSS_PANEL_PEAK2}, node ${RSS_NODE_PEAK})"
else
  note "RSS over the memory budget:$OVER (panel idle ${RSS_PANEL_IDLE} MB, node idle ${RSS_NODE_IDLE} MB)"
fi
python3 -c "import sys; sys.exit(0 if float('$RSS_PANEL_SETUP') <= 80 else 1)" \
  || note "panel RSS is ${RSS_PANEL_SETUP} MB 12 s after a password setup (> 80): the 64 MiB argon2id buffers stay resident until the Go scavenger returns them; a restarted panel idles at ${RSS_PANEL_IDLE} MB"

fi # ---- end of steps 2b..9

# ================================================================ tunnel steps (AmneziaWG, WARP, Mihomo), sources in scripts/e2e/m3.sh
[ -z "$OLD_NODE" ] || m3_compat
if [ -n "$M3_LIVE" ]; then
  m3_lab
  if [ -n "$M3_AWG" ]; then
    m3_awg
    m3_awg_traffic
    m3_selfservice
    m3_awg_signatures
  fi
  [ -z "$M3_MIHOMO" ] || m3_mihomo
  [ -z "$M3_WARP" ] || m3_warp
fi

# ================================================================ 10. retire
step "10. retire the node: the agent removes its nft table and files"
nft list table inet "$NFT_TABLE" >/dev/null 2>&1 || die "precondition: the nft table is gone before the retire"
[ -f "$SYSCTL_FILE" ] || note "precondition: $SYSCTL_FILE was not there before the retire (agent baseline not applied?)"
RET=$(api NodeService/RetireNode "{\"nodeId\":\"$NODE_ID\",\"confirmName\":\"$NODE_NAME\"}")
jq -e '.agentNotified == true' <<<"$RET" >/dev/null || die "RetireNode: the connected agent was not notified"
wait_for 30 "the agent to exit" not_alive "$NODE_PID" || die "the node agent kept running after the retire"
NODE_RC=0
wait "$NODE_PID" || NODE_RC=$?
[ "$NODE_RC" = 0 ] || die "the agent exited with code $NODE_RC after the retire (want 0)"
! nft list table inet "$NFT_TABLE" >/dev/null 2>&1 || die "nft table $NFT_TABLE is still there after the retire"
[ ! -e "$SYSCTL_FILE" ] || die "$SYSCTL_FILE is still there after the retire"
[ ! -e "$JOURNALD_FILE" ] || die "$JOURNALD_FILE is still there after the retire"
[ "$(node_status)" = NODE_STATUS_RETIRED ] || die "node status after the retire: $(node_status)"
# What the tunnel protocols add to a host: AWG and WARP links, their nft tables, the policy rules and the
# routes of the WARP table. All of it lives in this run's namespace, and none of it may outlive the node.
ip -o link show | awk -F': ' '{print $2}' | grep -E '^(mgawg|mgwarp)' && die "a link of the node (mgawg*/mgwarp) is still there after the retire"
for t in mistgate_awg mistgate_warp; do
  ! nft list tables | grep -q " $t\$" || die "nft table $t is still there after the retire"
done
! ip rule show | grep -qE '^(90|110):' || die "an ip rule of the WARP routing is still there after the retire: $(ip rule show | grep -E '^(90|110):')"
[ -z "$(ip route show table 51820 2>/dev/null)" ] || die "routes are left in table 51820 after the retire"
api NodeService/ListNodes '{}' | jq -e --arg n "$NODE_NAME" '[(.nodes // [])[] | select(.name == $n)] | length == 0' >/dev/null || die "a retired node is still in the default list"
pass "retired: agent notified, exited 0, nft table $NFT_TABLE, sysctl and journald drop-ins removed (and no mgawg*/mgwarp link, mistgate_awg/mistgate_warp table, WARP ip rule or route left), node RETIRED and hidden"

# ================================================================ 11. hygiene
step "11. secrets never reach the logs"
LEAKS=0
for s in "${SECRETS[@]}"; do
  [ ${#s} -ge 8 ] || continue
  if grep -rqF -- "$s" "$WORK"/*.log; then
    LEAKS=$((LEAKS + 1))
    say "secret of ${#s} chars starting '${s:0:2}' found in: $(grep -rlF -- "$s" "$WORK"/*.log | xargs -n1 basename | tr '\n' ' ')"
  fi
done
[ "$LEAKS" = 0 ] || die "$LEAKS secret(s) found in logs"
pass "no secret (admin password, TOTP, cookie, enrollment token, SNI, subscription tokens, hysteria credentials, and with the tunnel steps the AWG client and preshared keys, header protection keys, WARP keys, Mihomo credentials) in any log"
