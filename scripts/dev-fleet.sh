#!/usr/bin/env bash
# A throwaway fleet for coding agents: one panel and three node agents (de1, nl1, fi1), each in its own network namespace,
# joined by a bridge, with optional netem between them. A coding agent (or the panel's own assistant) can then run path_probe,
# node_trace, path_speed, node_dns_check and checks_run_now against real agents. Runs as root on Linux (WSL or a throwaway VM);
# started from Windows (Git Bash, PowerShell) it runs itself in WSL as root.
#
#   scripts/dev-fleet.sh [--netem "delay 25ms loss 0.5%"] [--profile readonly|operator|admin] [--keep-bin DIR]
#   scripts/dev-fleet.sh --status
#   scripts/dev-fleet.sh --down
#   scripts/dev-fleet.sh --mcp-e2e  # run the full MCP read-tool sweep, print its table, then tear down
#
# It prints the admin URL and an MCP token (made by scripts/e2e/mcp-token.sh, the owner's session plus a step-up, the way the
# e2e tests do it); the admin profile is the default because path_speed needs it. The panel's admin listens on loopback inside
# its namespace, so a client reaches it with `ip netns exec mgdf-panel <command>`; the line it prints for `claude mcp add` says so.
#
# Namespaces (all named mgdf-*): mgdf-hub holds the bridge br0 (10.77.0.0/24), mgdf-panel is 10.77.0.1, mgdf-de1, mgdf-nl1 and
# mgdf-fi1 are 10.77.0.11, .12 and .13. Each node has a Hysteria2 inbound on UDP 4433 with its TCP masquerade on 443, so the
# probes have ports to talk to. Nothing is added to the host's interfaces, routes or nft tables: the agents' tables, sysctl and
# journald drop-ins live in the namespaces and on private tmpfs mounts (the rules of the header of scripts/e2e-wsl.sh). The
# nodes have no internet, so no self-update and no public speed test; node-to-node tests work.
#
# State is in /tmp/mg-devfleet (MG_DEVFLEET_DIR): binaries, logs, the owner's cookie and authenticator secret in owner.env (0600),
# pids. --down stops everything and removes the namespaces and the directory. Never use this panel for anything real.
set -euo pipefail
cd "$(dirname "$0")/.."

STATE=${MG_DEVFLEET_DIR:-/tmp/mg-devfleet}
# --down removes the state directory: never let it point at a home directory or the root
case "$STATE" in
  /|/home|/home/*|/root|/root/*) echo "refusing to use or remove $STATE" >&2; exit 2 ;;
esac
NODES=(de1 nl1 fi1)
PUB=8443 ADM=8081
NETEM="" PROFILE=admin KEEP_BIN="" ACTION=up TRACE="" MCP_E2E=""
args=("$@")
while [ $# -gt 0 ]; do
  case $1 in
    --netem) NETEM=${2:?--netem needs tc netem arguments, e.g. "delay 25ms loss 0.5%"}; shift ;;
    --profile) PROFILE=${2:?--profile needs readonly, operator or admin}; shift ;;
    --keep-bin) KEEP_BIN=${2:?--keep-bin needs a directory with mistgate and mistgate-node}; shift ;;
    --trace) TRACE=1 ;;
    --mcp-e2e) MCP_E2E=1 ;;
    --down) ACTION=down ;;
    --status) ACTION=status ;;
    -h|--help) sed -n '2,27p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

# Not Linux, or not root: run in WSL as root, from the same checkout.
if [ "$(uname -s)" != Linux ] || [ "$(id -u)" != 0 ]; then
  if ! command -v wsl.exe >/dev/null 2>&1; then
    echo "scripts/dev-fleet.sh needs root on Linux (or WSL on Windows: wsl.exe was not found)" >&2
    exit 2
  fi
  distro=${WSL_DISTRO:-Ubuntu}
  here=$(pwd -W 2>/dev/null || pwd)
  repo=$(MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -u root wslpath -a "$here" | tr -d '\r')
  MSYS_NO_PATHCONV=1 exec wsl.exe -d "$distro" -u root -- bash "$repo/scripts/dev-fleet.sh" "${args[@]}"
fi

[ -z "$TRACE" ] || set -x
[ -z "$MCP_E2E" ] || [ "$PROFILE" = admin ] || { echo "--mcp-e2e needs the admin profile" >&2; exit 2; }
say() { printf '[dev-fleet] %s\n' "$*"; }
die() { printf '[dev-fleet] FAIL %s\n' "$*" >&2; exit 1; }

# only ever our own: a namespace named mgdf-*, a pid we wrote down
proc_start() {
  local line rest i
  IFS= read -r line <"/proc/$1/stat" || return 1
  rest=${line##*) }
  set -- $rest
  for ((i = 0; i < 19; i++)); do shift; done
  printf '%s' "${1:-}"
}

down() {
  local pid start ns
  if [ -r "$STATE/pids" ]; then
    while read -r pid start; do
      [ -n "$pid" ] && [ -n "$start" ] && [ "$(proc_start "$pid" 2>/dev/null || true)" = "$start" ] \
        && kill -TERM "$pid" 2>/dev/null || true
    done <"$STATE/pids"
    sleep 1
    while read -r pid start; do
      [ -n "$pid" ] && [ -n "$start" ] && [ "$(proc_start "$pid" 2>/dev/null || true)" = "$start" ] \
        && kill -KILL "$pid" 2>/dev/null || true
    done <"$STATE/pids"
  fi
  if [ -r "$STATE/namespaces" ]; then
    while read -r ns; do [ -n "$ns" ] && ip netns del "$ns" 2>/dev/null || true; done <"$STATE/namespaces"
  fi
  rm -rf "$STATE"
}

if [ "$ACTION" = down ]; then
  down
  say "the fleet is down: namespaces mgdf-* and $STATE are gone"
  exit 0
fi
if [ "$ACTION" = status ]; then
  ip netns list 2>/dev/null | awk '/^mgdf-/ {print "namespace " $1}'
  [ -r "$STATE/owner.env" ] && sed -n 's/^MG_ADMIN_URL=\(.*\)$/admin \1 (inside namespace mgdf-panel)/p' "$STATE/owner.env"
  exit 0
fi

for tool in go ip curl jq python3 openssl mount; do
  command -v "$tool" >/dev/null || die "missing tool: $tool"
done
[ -z "$NETEM" ] || command -v tc >/dev/null || die "--netem needs tc (iproute2)"
[ ! -e "$STATE" ] || die "$STATE exists: a fleet is already up (scripts/dev-fleet.sh --down first)"
ip netns list 2>/dev/null | grep -q '^mgdf-' && die "namespaces mgdf-* exist already (scripts/dev-fleet.sh --down first)"

export GOTOOLCHAIN=${GOTOOLCHAIN:-auto} CGO_ENABLED=0 GOFLAGS=-buildvcs=false
mkdir -p "$STATE/bin" "$STATE/node"
chmod 700 "$STATE"
: >"$STATE/pids"
trap 'rc=$?; if [ $rc != 0 ]; then echo; tail -n 20 "$STATE"/*.log 2>/dev/null || true; down; fi; exit $rc' EXIT
trap 'exit 130' INT TERM

# ---------------------------------------------------------------- binaries
if [ -n "$KEEP_BIN" ]; then
  cp "$KEEP_BIN/mistgate" "$KEEP_BIN/mistgate-node" "$STATE/bin/"
else
  say "building mistgate and mistgate-node"
  go build -o "$STATE/bin/mistgate" ./cmd/mistgate
  go build -o "$STATE/bin/mistgate-node" ./cmd/mistgate-node
fi
if [ -n "$MCP_E2E" ]; then
  say "building the MCP sweep and token helpers"
  go build -o "$STATE/bin/mcpe2e" ./scripts/e2e/mcpe2e
  go build -o "$STATE/bin/mcptoken" ./scripts/e2e/mcptoken
fi
PANEL_BIN=$STATE/bin/mistgate NODE_BIN=$STATE/bin/mistgate-node

# ---------------------------------------------------------------- namespaces: one bridge, five members
add_namespace() {
  printf '%s\n' "$1" >>"$STATE/namespaces"
  ip netns add "$1"
}
: >"$STATE/namespaces"
add_namespace mgdf-hub
ip -n mgdf-hub link set lo up
ip -n mgdf-hub link add br0 type bridge
ip -n mgdf-hub link set br0 up
member() { # <name> <last octet>: namespace mgdf-<name> with eth0 on the bridge
  local name=$1 ns=mgdf-$1
  add_namespace "$ns"
  ip link add "v-$name" netns mgdf-hub type veth peer name eth0 netns "$ns"
  ip -n mgdf-hub link set "v-$name" master br0 up
  ip -n "$ns" link set lo up
  ip -n "$ns" addr add "10.77.0.$2/24" dev eth0
  ip -n "$ns" link set eth0 up
}
member panel 1
octet=11
for n in "${NODES[@]}"; do
  member "$n" "$octet"
  [ -z "$NETEM" ] || ip netns exec "mgdf-$n" tc qdisc add dev eth0 root netem $NETEM
  octet=$((octet + 1))
done
say "namespaces up: mgdf-panel 10.77.0.1, mgdf-de1 .11, mgdf-nl1 .12, mgdf-fi1 .13${NETEM:+, netem $NETEM on the nodes}"

# ---------------------------------------------------------------- the panel
inpanel() { ip netns exec mgdf-panel "$@"; }
spawn_in() { # <namespace> <logfile> <command...>: a daemon in a namespace, its pid written down
  local ns=$1 log=$2
  shift 2
  ip netns exec "$ns" setsid "$@" >>"$log" 2>&1 </dev/null &
  local pid=$!
  printf '%s %s\n' "$pid" "$(proc_start "$pid")" >>"$STATE/pids"
}
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=localhost \
  -addext subjectAltName=DNS:localhost,IP:10.77.0.1 -keyout "$STATE/pub.key" -out "$STATE/pub.crt" 2>/dev/null
SETUP_OUT=$(inpanel "$PANEL_BIN" setup --data-dir "$STATE/data" --public-url "https://10.77.0.1:$PUB" --admin-listen "127.0.0.1:$ADM")
SETUP_TOKEN=$(sed -n 's|^Setup link: .*/setup#\(.*\)$|\1|p' <<<"$SETUP_OUT")
[ -n "$SETUP_TOKEN" ] || die "no setup link in the output of setup"
spawn_in mgdf-panel "$STATE/panel.log" "$PANEL_BIN" serve --data-dir "$STATE/data" --listen "10.77.0.1:$PUB" \
  --tls-cert "$STATE/pub.crt" --tls-key "$STATE/pub.key" --agent-addr "10.77.0.1:$PUB"

ADMIN=http://127.0.0.1:$ADM COOKIE=""
api() { # <Service/Method> [json]: the admin API from inside the panel's namespace
  local out code
  out=$(inpanel curl -sS -m 40 -w $'\n%{http_code}' -X POST "$ADMIN/api/mistgate.admin.v1.$1" -H 'Content-Type: application/json' \
    ${COOKIE:+-H "Cookie: $COOKIE"} -d "${2:-{\}}") || return 1
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  [ "$code" = 200 ] || { echo "api $1 -> HTTP $code" >&2; return 1; }
  printf '%s' "$out"
}
wait_for() { local n=$1 what=$2 i; shift 2; for ((i = 0; i < n; i++)); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done; die "timeout waiting for $what"; }
wait_for 40 "the panel to answer" inpanel curl -sS -m 3 -o /dev/null "$ADMIN/"

LOGIN=dev-admin PASSWORD=$(openssl rand -hex 16)
BEGIN=$(api AuthService/BeginSetup "$(jq -nc --arg t "$SETUP_TOKEN" --arg l "$LOGIN" --arg p "$PASSWORD" \
  '{setupToken:$t, displayName:"Dev admin", method:"SETUP_METHOD_PASSWORD", login:$l, password:$p}')")
CEREMONY=$(jq -r .ceremonyId <<<"$BEGIN") TOTP_SECRET=$(jq -r .totpSecret <<<"$BEGIN")
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
inpanel curl -sS -m 40 -D "$STATE/setup.hdr" -o /dev/null -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/FinishSetup" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$SETUP_TOKEN" --arg c "$CEREMONY" --arg code "$(totp_code)" '{setupToken:$t, ceremonyId:$c, totpCode:$code}')"
grep -q '^HTTP/[0-9.]* 200' "$STATE/setup.hdr" || die "FinishSetup failed: $(head -1 "$STATE/setup.hdr")"
COOKIE=$(sed -n 's/^[Ss]et-[Cc]ookie: \(__Host-sid=[^;]*\).*/\1/p' "$STATE/setup.hdr" | tr -d '\r')
[ -n "$COOKIE" ] || die "FinishSetup set no session cookie"
api AuthService/Me | jq -e '.admin.id' >/dev/null || die "the session cookie does not work"
say "panel up, first admin created"

# ---------------------------------------------------------------- the nodes
SETTINGS=$(api ProfileService/ListProtocols '{}' | jq -c \
  '.protocols[] | select(.id == "hysteria2") | .defaultSettingsJson | fromjson
   | .tls_mode = "self_signed" | .port = 4433 | .hop = {from: 0, to: 0} | .obfs.password = "$generate"')
PROFILE_ID=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$SETTINGS" '{protocol:"hysteria2", name:"dev-hy2", settingsJson:$s}')" | jq -r .profile.id)
octet=11
declare -A NODE_ID
for n in "${NODES[@]}"; do
  ENROLL=$(api NodeService/CreateEnrollment "{\"name\":\"$n\",\"address\":\"10.77.0.$octet\"}")
  NODE_ID[$n]=$(jq -r .node.id <<<"$ENROLL")
  # A declared lab capacity prevents the first-enrollment path from asking the node for a public speed test.
  api NodeService/UpdateNode "$(jq -nc --arg id "${NODE_ID[$n]}" '{nodeId:$id,bandwidthMbps:100}')" >/dev/null
  LINE=$(jq -r .installCommand <<<"$ENROLL" | grep -m1 ' enroll ')
  flag() { sed -n "s/.* --$1 \([^ ]*\).*/\1/p" <<<"$LINE"; }
  mkdir -p "$STATE/node/$n"
  ip netns exec "mgdf-$n" "$NODE_BIN" enroll --panel "$(flag panel)" --sni "$(flag sni)" --ca-sha256 "$(flag ca-sha256)" --token "$(flag token)" \
    --state-dir "$STATE/node/$n" >"$STATE/node-$n-enroll.log" 2>&1 || die "enroll of $n failed (see $STATE/node-$n-enroll.log)"
  # a private mount namespace (ip netns exec gives one): the agent's sysctl and journald files land in memory and vanish with it
  spawn_in "mgdf-$n" "$STATE/node-$n.log" bash -c 'mkdir -p /etc/systemd/journald.conf.d; mount -t tmpfs tmpfs /etc/sysctl.d; mount -t tmpfs tmpfs /etc/systemd/journald.conf.d; exec "$@"' _ \
    "$NODE_BIN" run --state-dir "$STATE/node/$n"
  octet=$((octet + 1))
done
node_online() { api NodeService/ListNodes '{}' | jq -e '[.nodes[] | select(.status == "NODE_STATUS_ONLINE")] | length == 3'; }
wait_for 90 "three nodes ONLINE" node_online
for n in "${NODES[@]}"; do api ProfileService/CreateInbound "{\"profileId\":\"$PROFILE_ID\",\"nodeId\":\"${NODE_ID[$n]}\"}" >/dev/null; done
active() { local x; api NodeService/ListNodes '{}' | jq -e '.nodes | length == 3' >/dev/null && for x in "${NODES[@]}"; do
  api NodeService/GetNode "{\"nodeId\":\"${NODE_ID[$x]}\"}" | jq -e '(.inbounds | length) == 1 and all(.inbounds[]; .state == "INBOUND_STATE_ACTIVE")' >/dev/null || return 1; done; }
wait_for 90 "the Hysteria2 inbounds ACTIVE" active
say "three nodes online (de1, nl1, fi1), a Hysteria2 inbound on each (UDP 4433, TCP 443)"

# ---------------------------------------------------------------- the owner's session for the token script, and the token
umask 077
( printf 'MG_ADMIN_URL=%s\nMG_OWNER_COOKIE=%s\nMG_TOTP_SECRET=%s\nMG_NETNS=%s\n' "$ADMIN" "${COOKIE#__Host-sid=}" "$TOTP_SECRET" mgdf-panel >"$STATE/owner.env" )
echo
say "admin URL: $ADMIN/   (loopback inside the namespace mgdf-panel: reach it with  ip netns exec mgdf-panel <command>)"
if [ -z "$MCP_E2E" ]; then
  say "the owner: login $LOGIN, authenticator secret and session cookie in $STATE/owner.env (0600); a throwaway panel, never a real one"
fi
if [ -n "$MCP_E2E" ]; then
  umask 077
  set -a
  # shellcheck disable=SC1090
  . "$STATE/owner.env"
  set +a
  inpanel "$STATE/bin/mcptoken" -profile admin -name dev-fleet-e2e >"$STATE/mcp-token.out" 2>"$STATE/mcp-token.log" || die "could not mint the throwaway admin token (see $STATE/mcp-token.log)"
  unset MG_OWNER_COOKIE MG_TOTP_SECRET MG_NETNS
  inpanel "$STATE/bin/mcpe2e" -url "$ADMIN" -token-file "$STATE/mcp-token.out" -secret-file "$STATE/owner.env" \
    -addresses "10.77.0.1,10.77.0.11,10.77.0.12,10.77.0.13" -sweep
  down
  trap - EXIT
  say "the MCP sweep passed and namespaces mgdf-* and $STATE are gone"
  exit 0
fi
if [ -x scripts/e2e/mcp-token.sh ] || [ -f scripts/e2e/mcp-token.sh ]; then
  say "MCP token ($PROFILE profile), printed once:"
  # the token script runs `go run ./scripts/e2e/mcptoken` inside the panel's namespace; wait for the next 30 s authenticator step
  bash scripts/e2e/mcp-token.sh "$STATE/owner.env" --profile "$PROFILE" || say "the token could not be made: run  bash scripts/e2e/mcp-token.sh $STATE/owner.env --profile $PROFILE"
else
  say "scripts/e2e/mcp-token.sh is not in this checkout: make a token in the admin panel (Integrations)"
fi
echo
say "try: path_probe from de1 to nl1 (udp, tcp, tls), node_trace, node_dns_check, checks_run_now, and path_speed (plan, the owner approves in the panel, apply)"
say "tear down: scripts/dev-fleet.sh --down"
trap - EXIT
