#!/usr/bin/env bash
# The shared TCP 443 of one node (Mistgate/mistgate#51) on real processes: a real panel, real node agents, a real Xray client, the
# official TrustTunnel client, a Telegram-proxy client, and a probe that compares what a stranger sees on a node that shares 443 by
# SNI with what it sees on a node that runs the same service alone. Plan: plans/tcp443-sni-plan.md, "Стенд sni443.sh" (step S6).
#
#   MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu -u root --exec bash scripts/e2e/sni443.sh [options]      (from the repo root)
#   bash scripts/e2e-wsl.sh --sni443 [options]                                                     (the same)
#
# Options:
#   --only a,b,c    run only these scenarios (names in SCENARIOS below; the stand itself always comes up)
#   --quick         smaller transfers (10 MB instead of 100 MB) and a flood of 1200 instead of 3000, for work on the script
#   --keep          keep the work directory (logs, configs)
#
# Ending the run (an exit, INT, TERM, HUP) stops every process of the run, marked by MG_SNI_MARK in its environment, and removes the
# work directory; the namespaces go with the private mount namespace.
#
# Isolation: like scripts/e2e/bridge.sh. The script builds first (the Go module cache is the only thing it reads from the internet), then
# `unshare -n -m` gives it a private network and mount namespace; the panel, the nodes and every client are network namespaces inside
# it (a private /run/netns) on one bridge. Nothing outside is visible or touched: no host interface, route, nft table, service or
# container; only the PIDs this script started are stopped. Addresses: documentation ranges only; no packet leaves. The names
# the nodes answer to are example.com subdomains and are never resolved: the clients dial the address and say the name in the SNI.
#
# TOPOLOGY (one L2 segment 203.0.113.0/24 on the bridge "sbr"; every namespace has "eth0" on it):
#
#   pan 203.0.113.2     the panel (agents dial it with the secret SNI name, as in production)
#   a   203.0.113.10    the node with the shared 443: TrustTunnel, MTProto (decoy), VLESS TLS WebSocket, two REALITY servers
#                       with another site (XHTTP, TCP + Vision); Hysteria2 on UDP 443 whose site gives its TCP port up
#   b   203.0.113.11    the reference: one of those services alone on 443, one after the other; also the node of the transitions
#   e   203.0.113.30    the entry of the relay (DNAT) and of the bridge: its own shared 443 (MTProto and TrustTunnel)
#   cli 203.0.113.50    the clients (and the flood); cli2 203.0.113.51 a second client address (the per-address limits of the engines)
#   net 203.0.113.100   the "internet": 198.51.100.100 (REALITY target, far server), 149.154.167.51 (the fake Telegram DC 2)
#
# What a REALITY server with the node's own site would add (the one the plan lists first) needs a Let's Encrypt certificate, and
# there is no CA here: it is a SKIP row, and the default route of the port is a service that shows the node's own site with a
# self-signed certificate (TrustTunnel, MTProto, VLESS TLS).
set -Eeuo pipefail
SELF=$(readlink -f "$0")
ROOT=${MG_SNI_ROOT:-$(cd "$(dirname "$SELF")/../.." && pwd)}
ARGS=("$@")

ONLY="" QUICK="" KEEP=""
while [ $# -gt 0 ]; do
  case $1 in
    --only) ONLY=${2:?--only needs a list}; shift ;;
    --quick) QUICK=1 ;;
    --keep) KEEP=1 ;;
    -h|--help) sed -n '2,/^set -E/p' "$SELF" | sed 's/^# \{0,1\}//' | head -n -1; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

if [ "$(uname -s)" != Linux ] || [ "$(id -u)" != 0 ]; then
  if ! command -v wsl.exe >/dev/null 2>&1; then
    echo "scripts/e2e/sni443.sh needs root on Linux (or WSL on Windows: wsl.exe was not found)" >&2
    exit 2
  fi
  distro=${WSL_DISTRO:-Ubuntu}
  here=$(pwd -W 2>/dev/null || pwd)
  repo=$(MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -u root wslpath -a "$here" | tr -d '\r')
  MSYS_NO_PATHCONV=1 exec wsl.exe -d "$distro" -u root -- bash "$repo/scripts/e2e/sni443.sh" "${ARGS[@]}"
fi

# What is needed and what is only nice: without tcpdump the capture checks are SKIP (they say so), the rest runs.
for tool in go curl jq python3 openssl ip mount mountpoint unshare setsid ss sed awk sha256sum; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done
HAVE_TCPDUMP="" HAVE_NFT=""
command -v tcpdump >/dev/null && HAVE_TCPDUMP=1
command -v nft >/dev/null && HAVE_NFT=1

# ---------------------------------------------------------------- outer: build, then re-run in a private namespace

if [ "${MG_SNI_INNER:-}" != 1 ]; then
  WORK=$(mktemp -d /tmp/mg-sni443.XXXXXX)
  chmod 711 "$WORK" # the official TrustTunnel client runs as another user and reads its files from $WORK/pub only
  mkdir -p "$WORK/bin" "$WORK/pub"
  chmod 755 "$WORK/pub"
  # Every process of the run carries MG_SNI_MARK in its environment. The outer script stays as the parent: whatever ends the run
  # first lets the inner script clean up, then sweeps by the mark, so no process and no work directory outlive the run.
  MARK=mgsn$$.$RANDOM
  CHILD=""
  sweep() { # <signal>
    local d pid
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      [ "$pid" != "$$" ] || continue
      [ -r "$d/environ" ] || continue
      if { tr '\0' '\n' <"$d/environ" | grep -qx "MG_SNI_MARK=$MARK"; } 2>/dev/null; then kill "-$1" "$pid" 2>/dev/null || true; fi
    done
  }
  outer_end() {
    local rc=$? i
    trap - EXIT INT TERM HUP
    if [ -n "$CHILD" ] && kill -0 "$CHILD" 2>/dev/null; then
      kill -TERM "$CHILD" 2>/dev/null || true
      for ((i = 0; i < 100; i++)); do kill -0 "$CHILD" 2>/dev/null || break; sleep 0.1; done
    fi
    sweep TERM
    sleep 0.5
    sweep KILL
    if [ -z "$KEEP" ]; then case "$WORK" in /tmp/mg-sni443.*) rm -rf -- "$WORK";; esac; else echo "work directory kept: $WORK"; fi
    exit "$rc"
  }
  trap outer_end EXIT
  trap 'exit 143' INT TERM HUP
  export GOTOOLCHAIN=${GOTOOLCHAIN:-auto} CGO_ENABLED=0 GOFLAGS="${GOFLAGS:--buildvcs=false -p=2}"
  cd "$ROOT"
  echo '[sni443] building the panel, the agent and the helpers'
  go build -o "$WORK/bin/mistgate" ./cmd/mistgate
  go build -o "$WORK/bin/mistgate-node" ./cmd/mistgate-node
  go build -o "$WORK/bin/tlsdest" ./scripts/e2e/tlsdest
  go build -o "$WORK/bin/bridgetool" ./scripts/e2e/bridgetool
  go build -o "$WORK/bin/sni443tool" ./scripts/e2e/sni443tool
  go build -o "$WORK/bin/ttprobe" ./scripts/e2e/ttprobe
  go build -o "$WORK/bin/h3get" ./scripts/e2e/h3get
  # Xray, the client: the version the agent embeds, built once and cached (the cache of scripts/e2e/m3.sh and bridge.sh).
  CACHE=${MG3_TEST_CACHE:-$HOME/.cache/mistgate-tests}
  XVER=$(sed -n 's|^[[:space:]]*github.com/xtls/xray-core \(v[^ ]*\).*|\1|p' go.mod | head -n 1)
  [ -n "$XVER" ] || { echo "xray-core is not pinned in go.mod" >&2; exit 2; }
  if [ ! -x "$CACHE/xray-$XVER" ]; then
    mkdir -p "$CACHE"
    tmp=$(mktemp -d "$CACHE/xray-build.XXXXXX")
    (cd "$WORK" && GOBIN=$tmp GOFLAGS=-mod=mod go install "github.com/xtls/xray-core/main@$XVER") >"$WORK/build-xray.log" 2>&1 \
      && mv "$tmp/main" "$CACHE/xray-$XVER" || { tail -n 5 "$WORK/build-xray.log"; rm -rf "$tmp"; echo "building xray-core failed" >&2; exit 2; }
    rm -rf "$tmp"
  fi
  cp "$CACHE/xray-$XVER" "$WORK/bin/xray"
  # The official TrustTunnel client (fetched and verified by scripts/e2e/tt-fetch.sh; the stand never downloads it): without it
  # the TrustTunnel traffic rows are SKIP.
  TT_CACHE=${TT_CACHE:-$CACHE/trusttunnel}
  d=$(find "$TT_CACHE/client-v1.1.12" -maxdepth 2 -name trusttunnel_client -type f 2>/dev/null | head -n 1 || true)
  if [ -n "$d" ] && [ -f "$TT_CACHE/client-v1.1.12/VERIFIED" ]; then
    cp "$d" "$WORK/pub/trusttunnel_client"
    chmod 755 "$WORK/pub/trusttunnel_client"
  fi
  env MG_SNI_INNER=1 MG_SNI_WORK="$WORK" MG_SNI_KEEP="$KEEP" MG_SNI_MARK="$MARK" unshare -n -m -- bash "$SELF" "${ARGS[@]}" &
  CHILD=$!
  rc=0
  while kill -0 "$CHILD" 2>/dev/null; do wait "$CHILD" && rc=0 || rc=$?; done
  exit "$rc"
fi

# ---------------------------------------------------------------- inner: everything below runs in the private namespace

WORK=${MG_SNI_WORK:?missing work directory}
KEEP=${MG_SNI_KEEP:-}
BIN=$WORK/bin
T0=$(date +%s)
PFX=sn$$
PIDS=() RESULTS=() FAILED=0 COOKIE="" TOTP_SECRET="" OWNER_STEP=-1 OWNER_STEPUP_UNTIL=0 STEP=start SCN=stand
PANEL_IP=203.0.113.2 A_IP=203.0.113.10 B_IP=203.0.113.11 E_IP=203.0.113.30 CLI_IP=203.0.113.50 CLI2_IP=203.0.113.51 NET_IP=203.0.113.100
FAR=198.51.100.100 DC2=149.154.167.51
ADMIN=http://127.0.0.1:8081 PUBLIC=https://$PANEL_IP:8443
declare -A NODE_ID=() NODE_PID=() NODE_NS=()
ulimit -n 65536 2>/dev/null || true
if [ -n "$QUICK" ]; then BIG=10000000 BIGLABEL="10 MB" FLOOD=1200; else BIG=100000000 BIGLABEL="100 MB" FLOOD=3000; fi

say() { printf '[%4ds] %s\n' $(($(date +%s) - T0)) "$*"; }
die() { say "FAIL  $*  (step: $STEP)"; RESULTS+=("FAIL|setup|stand|$*"); exit 1; }
alive() { local state; state=$(ps -o stat= -p "${1:-0}" 2>/dev/null) && [ -n "$state" ] && [ "${state:0:1}" != Z ]; }
stop_pid() {
  local pid=$1 i
  alive "$pid" || { wait "$pid" 2>/dev/null || true; return 0; }
  kill -TERM "$pid" 2>/dev/null || true
  for ((i = 0; i < 50; i++)); do alive "$pid" || break; sleep 0.1; done
  alive "$pid" && kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

# rec <PASS|FAIL|SKIP|NOTE> <scenario> <cell> <detail>: one row of the final table
rec() {
  RESULTS+=("$1|$2|$3|$4")
  say "$1  $2 / $3: $4"
  [ "$1" != FAIL ] || FAILED=1
}
ok() { rec PASS "$SCN" "$1" "$2"; }
bad() { rec FAIL "$SCN" "$1" "$2"; }
skip() { rec SKIP "$SCN" "$1" "$2"; }
note() { rec NOTE "$SCN" "$1" "$2"; }

table() {
  local row n=0
  printf '\n%-5s %-12s %-16s %s\n' result scenario cell detail
  printf '%-5s %-12s %-16s %s\n' ----- ------------ ---------------- ------
  for row in "${RESULTS[@]}"; do
    IFS='|' read -r r s c d <<<"$row"
    printf '%-5s %-12s %-16s %s\n' "$r" "$s" "$c" "$d"
    n=$((n + 1))
  done
  printf '\n%d rows: %d PASS, %d FAIL, %d SKIP (not checked, the reason is in the row)\n' "$n" \
    "$(printf '%s\n' "${RESULTS[@]}" | grep -c '^PASS|' || true)" "$(printf '%s\n' "${RESULTS[@]}" | grep -c '^FAIL|' || true)" \
    "$(printf '%s\n' "${RESULTS[@]}" | grep -c '^SKIP|' || true)"
}

cleanup() {
  local rc=$? i f
  trap - EXIT INT TERM HUP ERR
  set +e
  for ((i = ${#PIDS[@]} - 1; i >= 0; i--)); do stop_pid "${PIDS[$i]}"; done
  if [ "$rc" != 0 ] && [ -z "$KEEP" ]; then
    for f in "$WORK"/panel.log "$WORK"/node-*.log; do
      [ -s "$f" ] || continue
      echo "--- tail $(basename "$f")"
      tail -n 12 "$f" | cut -c1-300
    done
  fi
  if [ -n "$KEEP" ]; then say "work directory kept: $WORK"; else case "$WORK" in /tmp/mg-sni443.*) rm -rf -- "$WORK" ;; esac; fi
  table
  say "finished in $(($(date +%s) - T0)) s: $([ "$rc" = 0 ] && [ "$FAILED" = 0 ] && echo ALL-PASS || echo FAILED)"
  [ "$rc" != 0 ] || [ "$FAILED" = 0 ] || rc=1
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP
trap 'say "unexpected error at line $LINENO (step: $STEP)"' ERR

# ---------------------------------------------------------------- namespaces, one bridge, addresses

mount --make-rprivate /run 2>/dev/null || true
mkdir -p /run/netns
mountpoint -q /run/netns || mount -t tmpfs -o mode=755 tmpfs /run/netns
mount --make-rprivate /run/netns
mkdir -p /etc/netns && mount -t tmpfs tmpfs /etc/netns # the hosts files `ip netns exec` puts over /etc/hosts: private, gone with the run
ns() { printf '%s-%s' "$PFX" "$1"; }
nsx() { local n=$1; shift; ip netns exec "$(ns "$n")" "$@"; }
ipa() { local n=$1; shift; ip -n "$(ns "$n")" "$@"; }
ip link add sbr type bridge
ip link set sbr up
attach() { # <name> <addr/len>...: a namespace with eth0 on the bridge
  local n=$1 a
  shift
  ip netns add "$(ns "$n")"
  ipa "$n" link set lo up
  ip link add "v$n" type veth peer name "p$n"
  ip link set "v$n" netns "$(ns "$n")" name eth0
  ip link set "p$n" master sbr up
  ipa "$n" link set eth0 up
  for a in "$@"; do ipa "$n" addr add "$a" dev eth0; done
}
attach pan "$PANEL_IP/24"
attach a "$A_IP/24"
attach b "$B_IP/24"
attach e "$E_IP/24"
attach cli "$CLI_IP/24"
attach cli2 "$CLI2_IP/24"
attach net "$NET_IP/24" "$FAR/32" "$DC2/32"
for n in a b e; do
  ipa "$n" route add "$FAR/32" dev eth0
  ipa "$n" route add "$DC2/32" dev eth0
  ipa "$n" route add default via "$NET_IP"
done

# ---------------------------------------------------------------- processes

spawn_in() { # <ns name> <logfile> <command...>
  local n=$1 log=$2
  shift 2
  ip netns exec "$(ns "$n")" setsid "$@" >>"$log" 2>&1 </dev/null &
  LAST_PID=$!
  PIDS+=("$LAST_PID")
}
wait_for() { # <seconds> <what> <command...>: 0 when it held in time, 1 when not
  local n=$1 what=$2 i
  shift 2
  for ((i = 0; i < n; i++)); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  say "timeout after ${n} s waiting for: $what"
  return 1
}
listening() { [ -n "$(nsx "$1" ss -Hltn "sport = :$2")" ]; } # <ns> <tcp port>
need() { # <seconds> <what> <command...>: wait_for, and die when it did not hold
  local n=$1 what=$2
  shift 2
  wait_for "$n" "$what" "$@" || die "timeout waiting for $what"
}
rss_kb() { awk '/^VmRSS:/ {print $2}' "/proc/$1/status" 2>/dev/null || echo 0; }

# ---------------------------------------------------------------- the far side, the REALITY target, the fake Telegram DC

TARGET_NAMES=www.example.com,www.example.net,www.example.org
start_far() {
  spawn_in net "$WORK/far.log" "$BIN/bridgetool" serve -listen "$FAR:8080"
  spawn_in net "$WORK/tlsdest.log" "$BIN/tlsdest" -listen "$FAR:8443" -names "$TARGET_NAMES" -cert "$WORK/tlsdest.pem"
  need 20 "the REALITY target certificate" test -s "$WORK/tlsdest.pem"
  need 10 "the far server" listening net 8080
  spawn_in net "$WORK/dc.log" "$BIN/sni443tool" dc -listen "$DC2:443"
  need 10 "the fake Telegram DC" listening net 443
}

# ---------------------------------------------------------------- the panel

api() { # <Service/Method> [JSON]
  local out code body=${2:-}
  [ -n "$body" ] || body='{}'
  out=$(nsx pan curl -skS -m 120 -w $'\n%{http_code}' -X POST "$ADMIN/api/mistgate.admin.v1.$1" \
    -H 'Content-Type: application/json' ${COOKIE:+-H "Cookie: $COOKIE"} -d "$body") || return 1
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [ "$code" != 200 ]; then
    echo "api $1 -> HTTP $code: $(jq -r '[.code, .message] | map(select(. != null)) | join(" ")' <<<"$out" 2>/dev/null || echo "$out")" >&2
    return 1
  fi
  printf '%s' "$out"
}
api_err() { api "$@" 2>&1 || true; } # the error text of a call that is expected to be refused

totp_code() {
  python3 - "$TOTP_SECRET" <<'PY'
import base64, hashlib, hmac, struct, sys, time
k = base64.b32decode(sys.argv[1].upper() + "=" * ((8 - len(sys.argv[1]) % 8) % 8))
h = hmac.new(k, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest(); o = h[-1] & 15
print("%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7fffffff) % 1000000))
PY
}
owner_stepup() {
  local now step code result
  now=$(date +%s)
  [ "$now" -lt "$OWNER_STEPUP_UNTIL" ] && return 0
  step=$((now / 30))
  while [ "$step" -le "$OWNER_STEP" ]; do sleep 1; now=$(date +%s); step=$((now / 30)); done
  code=$(totp_code)
  result=$(api AuthService/FinishStepUp "$(jq -nc --arg c "$code" '{totpCode:$c}')")
  OWNER_STEP=$step
  OWNER_STEPUP_UNTIL=$(jq -r '.stepUpUntilUnix // 0' <<<"$result")
}

panel_serve() {
  spawn_in pan "$WORK/panel.log" env SSL_CERT_FILE="$WORK/trust.pem" "$BIN/mistgate" serve --data-dir "$WORK/data" --listen "$PANEL_IP:8443" \
    --admin-listen 127.0.0.1:8081 --tls-cert "$WORK/panel.crt" --tls-key "$WORK/panel.key" --agent-addr "$PANEL_IP:8443"
}
start_panel() {
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=sni443-panel \
    -addext "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:$PANEL_IP" -keyout "$WORK/panel.key" -out "$WORK/panel.crt" 2>/dev/null
  local out token begin ceremony login=sni-admin password
  out=$(nsx pan "$BIN/mistgate" setup --data-dir "$WORK/data" --public-url "$PUBLIC" --admin-listen 127.0.0.1:8081)
  token=$(sed -n 's|^Setup link: .*/setup#\(.*\)$|\1|p' <<<"$out")
  [ -n "$token" ] || die "panel setup returned no setup token"
  panel_serve
  need 45 "the panel listener" nsx pan curl -skS -m 3 -o /dev/null "$PUBLIC/"
  password=$(openssl rand -hex 16)
  begin=$(api AuthService/BeginSetup "$(jq -nc --arg t "$token" --arg l "$login" --arg p "$password" \
    '{setupToken:$t,displayName:"Shared 443 stand",method:"SETUP_METHOD_PASSWORD",login:$l,password:$p}')") || die "BeginSetup failed"
  ceremony=$(jq -r .ceremonyId <<<"$begin")
  TOTP_SECRET=$(jq -r .totpSecret <<<"$begin")
  nsx pan curl -skS -m 30 -D "$WORK/setup.hdr" -o /dev/null -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/FinishSetup" \
    -H 'Content-Type: application/json' -d "$(jq -nc --arg t "$token" --arg c "$ceremony" --arg code "$(totp_code)" \
    '{setupToken:$t,ceremonyId:$c,totpCode:$code}')"
  grep -q '^HTTP/[0-9.]* 200' "$WORK/setup.hdr" || die "first-admin setup failed"
  COOKIE=$(sed -n 's/^[Ss]et-[Cc]ookie: \(__Host-sid=[^;]*\).*/\1/p' "$WORK/setup.hdr" | tr -d '\r')
  [ -n "$COOKIE" ] || die "no session cookie"
  OWNER_STEP=$(($(date +%s) / 30))
  OWNER_STEPUP_UNTIL=$(api AuthService/Me | jq -r '.stepUpUntilUnix // 0')
  say "the panel is up in $(ns pan)"
}

# ---------------------------------------------------------------- the nodes

node_cmd() { # <name>: the agent, in its namespace, with its own sysctl.d and journald drop-in on private tmpfs
  local name=$1
  NODE_PID[$name]=""
  spawn_in "${NODE_NS[$name]}" "$WORK/node-$name.log" env SSL_CERT_FILE="$WORK/tlsdest.pem" ${MG_SNI_NODE_LOG:+MISTGATE_LOG_LEVEL=$MG_SNI_NODE_LOG} bash -c \
    'mkdir -p /etc/systemd/journald.conf.d; mount -t tmpfs tmpfs /etc/sysctl.d; mount -t tmpfs tmpfs /etc/systemd/journald.conf.d; exec "$@"' _ \
    "$BIN/mistgate-node" run --state-dir "$WORK/state/$name"
  NODE_PID[$name]=$LAST_PID
}
make_node() { # <name> <address> <country> <namespace short name>
  local name=$1 address=$2 country=$3 n=$4 out line
  NODE_NS[$name]=$n
  out=$(api NodeService/CreateEnrollment "$(jq -nc --arg n "sn-$name" --arg a "$address" --arg c "$country" '{name:$n,address:$a,countryCode:$c,sharing:{mode:"NODE_SHARE_MODE_ALL"}}')") \
    || die "CreateEnrollment failed for $name"
  NODE_ID[$name]=$(jq -r .node.id <<<"$out")
  line=$(jq -r .installCommand <<<"$out" | grep -m1 ' enroll ')
  flag() { sed -n "s/.* --$1 \([^ ]*\).*/\1/p" <<<"$line"; }
  mkdir -p "$WORK/state/$name"
  SSL_CERT_FILE="$WORK/tlsdest.pem" nsx "$n" "$BIN/mistgate-node" enroll --panel "$(flag panel)" --sni "$(flag sni)" \
    --ca-sha256 "$(flag ca-sha256)" --token "$(flag token)" --state-dir "$WORK/state/$name" >"$WORK/enroll-$name.log" 2>&1 \
    || die "enrollment of $name failed: $(tail -n 3 "$WORK/enroll-$name.log")"
  node_cmd "$name"
}
node_online() { api NodeService/ListNodes '{}' | jq -e --arg n "$1" '.nodes[] | select(.name == "sn-" + $n and .status == "NODE_STATUS_ONLINE")' >/dev/null; }
kill_node() { # <name>: only the PID this script started
  local pid=${NODE_PID[$1]}
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
restart_node() { node_cmd "$1"; }
node_get() { api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[$1]}" '{nodeId:$n}')"; }

# ---------------------------------------------------------------- profiles and inbounds

settings_of() { api ProfileService/ListProtocols '{}' | jq -c --arg id "$1" '.protocols[] | select(.id == $id) | .defaultSettingsJson | fromjson'; }
make_profile() { # <protocol> <name> <settings json> -> id
  api ProfileService/CreateProfile "$(jq -nc --arg p "$1" --arg n "$2" --arg s "$3" '{protocol:$p,name:$n,settingsJson:$s}')" | jq -r .profile.id
}
# make_inbound <profile id> <node name> [server name] [port]: the inbound on that node, on 443 with that name unless said otherwise
make_inbound() {
  local out
  out=$(api ProfileService/CreateInbound "$(jq -nc --arg p "$1" --arg n "${NODE_ID[$2]}" --arg s "${3:-}" --argjson port "${4:-443}" \
    '{profileId:$p,nodeId:$n,portOverride:$port,tlsServerNameOverride:$s}')") || return 1
  jq -r .inbound.id <<<"$out"
}
delete_inbound() { api ProfileService/DeleteInbound "$(jq -nc --arg i "$1" '{inboundId:$i}')" >/dev/null; }
inbound_state() { # <node name> <inbound id>
  node_get "$1" | jq -r --arg id "$2" '.inbounds[] | select(.id == $id) | .state'
}
inbound_active() { [ "$(inbound_state "$1" "$2")" = INBOUND_STATE_ACTIVE ]; } # <node name> <inbound id>

# ---------------------------------------------------------------- the stand comes up

STEP=stand
say "bringing up the far side"
start_far
cat "$WORK/tlsdest.pem" >"$WORK/trust.pem"
say "bringing up the panel"
start_panel
make_node a "$A_IP" DE a
make_node b "$B_IP" NL b
make_node e "$E_IP" RU e
nodes_up() { local n; for n in a b e; do node_online "$n" || return 1; done; }
need 120 "three online node agents" nodes_up
say "agents online: a b e ($(($(date +%s) - T0)) s)"

# ---------------------------------------------------------------- the services of the shared 443

mutate() { owner_stepup && api "$@"; }
NAME_TT=tt.d.example.com NAME_MT=mt.d.example.com NAME_WS=ws.d.example.com NAME_WSO=origin.d.example.com NAME_XH=www.example.com NAME_VI=www.example.net
SVCS=(tt mt ws xh vi)
declare -A PROF=() NAMES=([tt]="$NAME_TT" [mt]="$NAME_MT" [ws]="$NAME_WS $NAME_WSO" [xh]="$NAME_XH" [vi]="$NAME_VI")
vless_reality() { settings_of vless | jq -c --argjson p "$1" --arg t "$2" --arg sn "$3" --arg target "$FAR:8443" \
  '.port=$p|.transport=$t|.reality.target_kind="site"|.reality.target=$target|.reality.server_names=[$sn]'; }
make_profiles() {
  PROF[tt]=$(make_profile trusttunnel sn-tt "$(jq -nc --arg s "$NAME_TT" '{port:443,h3:false,sni:$s,tls_mode:"self_signed",egress:"direct"}')")
  PROF[mt]=$(make_profile mtproto sn-mt "$(settings_of mtproto | jq -c --arg d "$NAME_MT" '.port=443|.fake_tls={kind:"own",domain:$d}')")
  PROF[ws]=$(make_profile vless sn-ws "$(settings_of vless | jq -c --arg o "$NAME_WSO" --arg c "$NAME_WS" \
    '.port=443|.transport="ws"|.security="tls"|.tls_mode="self_signed"|.sni=$o|.cdn={host:$c,port:443}')")
  PROF[xh]=$(make_profile vless sn-xhttp "$(vless_reality 443 xhttp "$NAME_XH")")
  PROF[vi]=$(make_profile vless sn-vision "$(vless_reality 443 tcp "$NAME_VI")")
  PROF[h2]=$(make_profile hysteria2 sn-hy2 "$(settings_of hysteria2 | jq -c '.tls_mode="self_signed"|.port=443|.hop={from:0,to:0}|.obfs={type:"none"}')")
  local k
  for k in "${SVCS[@]}" h2; do [ -n "${PROF[$k]}" ] && [ "${PROF[$k]}" != null ] || die "the profile of $k was not created"; done
}
# svc_inbound <service> <node name>: that service on that node's 443 under its name; prints the inbound id
svc_inbound() {
  local id name=""
  case $1 in tt) name=$NAME_TT ;; mt) name=$NAME_MT ;; ws) name=$NAME_WSO ;; esac
  id=$(make_inbound "${PROF[$1]}" "$2" "$name") || return 1
  [ -n "$id" ] && [ "$id" != null ] || return 1
  printf '%s' "$id"
}
declare -A INB_A=() # service -> inbound id on a
inbounds_all_active() { # <node name>: every inbound of the node is ACTIVE (and there is one at least)
  node_get "$1" | jq -e '(.inbounds | length) > 0 and all(.inbounds[]; .state == "INBOUND_STATE_ACTIVE")' >/dev/null
}
inbound_error() { node_get "$1" | jq -c '.inbounds[] | select(.state != "INBOUND_STATE_ACTIVE") | {protocol, state, lastError}'; }

new_user() { # <name> <group id>: sets U_ID, U_URL
  local out
  out=$(api UserService/CreateUser "$(jq -nc --arg n "$1" --arg g "$2" '{name:$n,groupId:$g,apps:{happ:true,amnezia:true,telegram:true,trustTunnel:true}}')") || die "CreateUser failed"
  U_ID=$(jq -r .user.id <<<"$out") U_URL=$(jq -r .subscriptionUrl <<<"$out")
}

# ---------------------------------------------------------------- the clients

declare -A CLIENT_PID=()
bt() { nsx cli "$BIN/bridgetool" "$@"; }
via() { local p=$1 c=$2; shift 2; bt "$c" -socks "127.0.0.1:$p" -target "$FAR:8080" "$@"; }
PORT_NEXT=11000
alloc_port() { PORT_NEXT=$((PORT_NEXT + 1)); printf -v "$1" %s "$PORT_NEXT"; } # <variable>: the next SOCKS port of a client
seed() { echo $((100 + RANDOM * 32 + RANDOM % 32)); }

write_vless2xray() {
  cat >"$WORK/vless2xray.py" <<'PY'
import json, sys
from urllib.parse import urlsplit, parse_qs, unquote
# vless2xray.py <vless:// line> <socks port> <out file> <address to dial> <pin sha256 of the node's certificate, TLS lines only>
u = urlsplit(sys.argv[1])
q = {k: v[0] for k, v in parse_qs(u.query).items()}
user = {"id": unquote(u.username), "encryption": "none"}
if q.get("flow"):
    user["flow"] = q["flow"]
if q["security"] == "reality":
    stream = {"network": q["type"], "security": "reality", "realitySettings": {
        "serverName": q["sni"], "fingerprint": q["fp"], "publicKey": q["pbk"], "shortId": q["sid"], "spiderX": q.get("spx", "")}}
else:  # TLS behind a "CDN" that is the node: dial the node, say the CDN's name, trust the node's self-signed certificate by its pin
    stream = {"network": q["type"], "security": "tls", "tlsSettings": {
        "serverName": q["sni"], "fingerprint": q["fp"], "pinnedPeerCertSha256": sys.argv[5]}}
if q["type"] == "xhttp":
    x = {"path": q["path"], "mode": q.get("mode", "auto")}
    if q.get("host"):
        x["host"] = q["host"]
    stream["xhttpSettings"] = x
elif q["type"] == "ws":
    stream["wsSettings"] = {"path": q["path"], "host": q["host"]}
conf = {"log": {"loglevel": "warning"},
        "inbounds": [{"listen": "127.0.0.1", "port": int(sys.argv[2]), "protocol": "socks", "settings": {"auth": "noauth", "udp": False}}],
        "outbounds": [{"protocol": "vless", "settings": {"vnext": [{"address": sys.argv[4], "port": u.port, "users": [user]}]}, "streamSettings": stream}]}
json.dump(conf, open(sys.argv[3], "w"))
PY
}
sub_lines() { nsx pan curl -skS -m 30 -A 'v2rayNG/1.10.20' "$U_URL" | base64 -d 2>/dev/null; }
# vless_line <service> <address>: that service's line in the subscription (REALITY: by its name; WS: the CDN's)
vless_line() {
  local want
  case $1 in vi) want="sni=$NAME_VI" ;; xh) want="sni=$NAME_XH" ;; ws) want="sni=$NAME_WS" ;; esac
  sub_lines | grep '^vless://' | grep -- "$want" | grep -E "@($2|$NAME_WS):${LINE_PORT:-443}\?" | head -n 1
}
# start_xray <service> <node address> <variable for the socks port>: an Xray client of that service towards that node
start_xray() {
  local svc=$1 addr=$2 line pin="" port
  local i
  for ((i = 0; i < 40; i++)); do # the subscription follows the node a little behind its inbound
    line=$(vless_line "$svc" "$addr")
    [ -z "$line" ] || break
    sleep 1
  done
  [ -n "$line" ] || { say "no subscription line for $svc on $addr:${LINE_PORT:-443}"; return 1; }
  [ "$svc" != ws ] || pin=$(nsx cli "$BIN/sni443tool" pin -addr "$addr:443" -sni "$NAME_WS") || return 1
  alloc_port port
  python3 "$WORK/vless2xray.py" "$line" "$port" "$WORK/xray-$svc-$port.json" "$addr" "$pin" || return 1
  chmod 600 "$WORK/xray-$svc-$port.json"
  spawn_in cli "$WORK/xray-$svc-$port.log" "$BIN/xray" run -c "$WORK/xray-$svc-$port.json"
  CLIENT_PID[$port]=$LAST_PID
  wait_for 25 "xray $svc to listen" listening cli "$port" || { tail -n 5 "$WORK/xray-$svc-$port.log" >&2; return 1; }
  printf -v "$3" %s "$port"
}

# the Mistgate app's device of a person: the sync gives the TrustTunnel path of a node
enrol_device() { # <user url>: sets DEV_KEY, DEV_BASE
  local out
  DEV_KEY=$(python3 -c 'import base64, os; print(base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("="))')
  out=$(nsx pan curl -sk -m 20 -X POST "$1/client/enrol" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg h stand-hwid-1 --arg k "$DEV_KEY" '{hwid:$h, platform:"linux", model:"sni443-stand", os_version:"", device_key:$k}')") || return 1
  DEV_BASE=$(jq -r .base <<<"$out")
  [ -n "$DEV_BASE" ] && [ "$DEV_BASE" != null ]
}
tt_path_of() { # <node address>: the TrustTunnel path of the sync for that node, as JSON
  nsx pan curl -sk -m 20 -H "Authorization: Bearer $DEV_KEY" -H 'X-Mistgate-Caps: v=1; trusttunnel=1' "$DEV_BASE/sync" \
    | jq -c --arg h "$1" '[.servers[].paths[] | select(.protocol == "trusttunnel" and .via == "direct" and .settings.server == $h)][0] // empty'
}
tt_relay_path() { # the TrustTunnel path of the sync that goes through a relay
  nsx pan curl -sk -m 20 -H "Authorization: Bearer $DEV_KEY" -H 'X-Mistgate-Caps: v=1; trusttunnel=1' "$DEV_BASE/sync"     | jq -c '[.servers[].paths[] | select(.protocol == "trusttunnel" and .via == "relay")][0] // empty'
}
# start_tt <node address> <variable for the socks port> [path json]: the official client in the clients' namespace
start_tt() {
  local addr=$1 port pj=${3:-} cfg
  [ -f "$WORK/pub/trusttunnel_client" ] || return 2
  [ -n "$pj" ] || pj=$(tt_path_of "$addr")
  [ -n "$pj" ] || { say "the sync has no TrustTunnel path"; return 1; }
  alloc_port port
  cfg=$WORK/pub/tt-$port.toml
  "$BIN/ttprobe" toml -host "$addr" -skip-verify -socks "127.0.0.1:$port" -protocol http2 <<<"$pj" >"$cfg"
  chmod 644 "$cfg"
  spawn_in cli "$WORK/tt-client-$port.log" setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs --bounding-set=-all \
    prlimit --as=1073741824 "$WORK/pub/trusttunnel_client" -c "$cfg" -l info
  CLIENT_PID[$port]=$LAST_PID
  wait_for 25 "the TrustTunnel client to listen" listening cli "$port" || { tail -n 5 "$WORK/tt-client-$port.log" >&2; return 1; }
  printf -v "$2" %s "$port"
}
# mt_relay_link: "host port secret" of the person's tg://proxy link of a relay twin
mt_relay_link() {
  api UserService/GetTelegramLinks "$(jq -nc --arg u "$U_ID" '{userId:$u}')" | jq -r '.links[] | select(.relay == true) | .url' | head -n 1     | python3 -c 'import sys, urllib.parse as u
q = u.parse_qs(u.urlsplit(sys.stdin.read().strip()).query)
print(q["server"][0], q["port"][0], q["secret"][0])'
}
# mt_link <node name>: "host port secret" of the person's tg://proxy link for that node (the secret is not printed by anything)
mt_link() {
  api UserService/GetTelegramLinks "$(jq -nc --arg u "$U_ID" '{userId:$u}')" \
    | jq -r --arg n "${NODE_ID[$1]}" '.links[] | select(.nodeId == $n and (.ipv6 // false) == false and (.relay // false) == false) | .url' | head -n 1 \
    | python3 -c 'import sys, urllib.parse as u
l = sys.stdin.read().strip()
q = u.parse_qs(u.urlsplit(l).query)
print(q["server"][0], q["port"][0], q["secret"][0])'
}

# ================================================================ scenarios
#
# Every scenario is a function scn_<name>; run_scn runs it, records PASS/FAIL/SKIP rows (ok, bad, skip) and goes on (a stand failure
# is die, a scenario's own failure is a FAIL row). A scenario that needs the services of node a asks setup_a for them, so --only works.

TOOL() { nsx cli "$BIN/sni443tool" "$@"; }
retry() { # <tries> <command...>
  local n=$1 i out
  shift
  for ((i = 1; i <= n; i++)); do
    if out=$("$@" 2>&1); then printf '%s' "$out"; return 0; fi
    sleep 8 # the panel's port checks take the check lock of a node for a few seconds
  done
  printf '%s' "$out"
  return 1
}

# setup_a: every service of the shared 443 on node a, a person with every kind of key, and a client of each kind
A_READY=""
setup_a() {
  [ -z "$A_READY" ] || return 0
  write_vless2xray
  make_profiles
  local k
  for k in "${SVCS[@]}" h2; do INB_A[$k]=$(svc_inbound "$k" a) || die "creating the $k inbound on a failed: $(api_err ProfileService/CreateInbound "$(jq -nc --arg p "${PROF[$k]}" --arg n "${NODE_ID[a]}" '{profileId:$p,nodeId:$n,portOverride:443}')")"; done
  need 90 "the inbounds of a ACTIVE" inbounds_all_active a
  GROUP=$(api GroupService/CreateGroup "$(jq -nc --argjson p "$(printf '%s\n' "${PROF[@]}" | jq -R . | jq -sc .)" '{name:"sn-group",profileIds:$p}')" | jq -r .group.id)
  new_user alice "$GROUP"
  mutate ClientApiService/UpdateClientApi '{"enabled":true}' >/dev/null || die "the client API did not switch on"
  api UserService/IssueTelegramKey "$(jq -nc --arg u "$U_ID" '{userId:$u}')" >/dev/null || die "no Telegram key"
  lines_ready() { [ "$(sub_lines | grep -c '^vless://')" -ge 3 ]; }
  need 60 "the lines of a in the subscription" lines_ready
  enrol_device "$U_URL" || die "the device did not enrol"
  # the default route of the port: the lowest id of the services that show the node's own site
  DEFAULT_SVC=""
  local best=""
  for k in tt mt ws; do
    if [ -z "$best" ] || [[ "${INB_A[$k]}" < "$best" ]]; then best=${INB_A[$k]} DEFAULT_SVC=$k; fi
  done
  say "node a: $(printf '%s ' "${SVCS[@]}") share 443; the default route is $DEFAULT_SVC"
  A_READY=1
}
# a_clients: the clients towards a: sets P_<svc> for xh, vi, ws, tt
a_clients() {
  [ -z "${P_XH_A:-}" ] || return 0
  start_xray xh "$A_IP" P_XH_A || return 1
  start_xray vi "$A_IP" P_VI_A || return 1
  start_xray ws "$A_IP" P_WS_A || return 1
  if [ -f "$WORK/pub/trusttunnel_client" ]; then start_tt "$A_IP" P_TT_A || return 1; else P_TT_A=""; fi
}

# ss_sample <ns> <port>: what the kernel of that namespace holds on that TCP port, as "peer-address count" lines, and the loopback sockets
ss_peers() { nsx "$1" ss -Htn state established "( sport = :$2 )" | awk '{print $4}' | sed 's/:[0-9]*$//' | sort | uniq -c | awk '{print $2 "x" $1}' | tr '\n' ' '; }
ss_loopback() { nsx "$1" ss -Htn state established | awk '$3 ~ /^127\./ || $4 ~ /^127\./' | wc -l; }

# ---------------------------------------------------------------- 1 and 2. every service works through the shared 443 with its own name, and sees the client

scn_traffic() {
  setup_a
  a_clients || { bad clients "a client of node a did not start"; return; }
  local svc p out pids=() peers="" maxlo=0 lo any
  declare -A PORT_OF=([xh]=$P_XH_A [vi]=$P_VI_A [ws]=$P_WS_A [tt]=${P_TT_A:-})
  # 1. the real traffic of every service at once: BIG down and BIG up on the Xray and TrustTunnel clients, an echo of BIG on MTProto
  for svc in xh vi ws tt; do
    [ -n "${PORT_OF[$svc]}" ] || { skip "$svc-bytes" "no official TrustTunnel client in the cache (scripts/e2e/tt-fetch.sh)"; continue; }
    ( out=$(via "${PORT_OF[$svc]}" get -n "$BIG" -seed "$(seed)" 2>&1) && out+=" / $(via "${PORT_OF[$svc]}" put -n "$BIG" -seed "$(seed)" 2>&1)" || true
      echo "$out" >"$WORK/traffic-$svc.out"; case $out in *FAIL*) exit 1;; esac ) &
    pids+=($!)
    PIDS+=($!)
  done
  local mtline mh mp ms
  mtline=$(mt_link a) || { bad mt-bytes "no tg:// link for node a"; return; }
  read -r mh mp ms <<<"$mtline"
  ( out=$(TOOL mt -addr "$mh:$mp" -domain "$NAME_MT" -secret "$ms" -n "$BIG" 2>&1) || true; echo "$out" >"$WORK/traffic-mt.out"; case $out in ok*) ;; *) exit 1;; esac ) &
  pids+=($!)
  PIDS+=($!)
  # 2. while they run: who holds the sockets of the port, and is there a hop through loopback
  local i
  for ((i = 0; i < 40; i++)); do
    any=""
    for p in "${pids[@]}"; do alive "$p" && any=1; done
    [ -n "$any" ] || break
    peers=$(ss_peers a 443)
    lo=$(ss_loopback a)
    [ "$lo" -le "$maxlo" ] || maxlo=$lo
    sleep 0.25
  done
  for p in "${pids[@]}"; do wait "$p" 2>/dev/null || true; done
  for svc in xh vi ws tt mt; do
    [ -f "$WORK/traffic-$svc.out" ] || continue
    if grep -q '^FAIL\|^ok' "$WORK/traffic-$svc.out" && ! grep -q FAIL "$WORK/traffic-$svc.out"; then ok "$svc-bytes" "$BIGLABEL through 443 under its own name, all five at once: $(tr '\n' ' ' <"$WORK/traffic-$svc.out" | cut -c1-200)"
    else bad "$svc-bytes" "$(tr '\n' ' ' <"$WORK/traffic-$svc.out" | cut -c1-300)"; fi
  done
  # 2. the address
  if [ "$maxlo" = 0 ]; then ok client-addr "during the transfers there was no TCP socket on loopback in node a (no hop through 127.0.0.1); the kernel's peers on :443 were: ${peers:-none seen}"
  else bad client-addr "$maxlo established TCP sockets on loopback in node a during the transfers: the front or an engine goes through 127.0.0.1"; fi
  out=$(nsx cli "$BIN/h3get" "https://$A_IP:443/" 2>&1 | head -n 1) || true
  case $out in HTTP/3*) ok hy2-udp "Hysteria2 on UDP 443 beside the shared TCP 443 answers a stranger over HTTP/3: $out" ;; *) bad hy2-udp "UDP 443 of the Hysteria2 inbound: ${out:-no answer}" ;; esac
  for svc in xh vi ws tt; do
    [ -n "${PORT_OF[$svc]}" ] || continue
    out=$(via "${PORT_OF[$svc]}" who 2>&1) || true
    [ "$out" = "ok who $A_IP" ] && ok "$svc-exit" "the far server saw the node ($A_IP) as the exit of $svc" || bad "$svc-exit" "$out"
  done
}

# ---------------------------------------------------------------- 3. the signature of a shared node is the one of a node with the service alone

# fp_to <file> <addr> <sni> <what>: the probes from the clients' namespace
fp_to() { TOOL fp -addr "$2:443" ${3:+-sni "$3"} -what "$4" >"$1" 2>&1 || true; }
NAMED_PROBES=tls,h2,http,byte1,records3
UNNAMED_PROBES=tls,h2,http,plain,junk,byte1,records3
UNKNOWN_NAME=nothing.example.org

# svc_alone <service>: node b runs that service alone on 443 (nothing else on it)
svc_alone() {
  local id
  id=$(svc_inbound "$1" b) || return 1
  B_INB=$id
  wait_for 90 "$1 ACTIVE alone on b" inbound_active b "$id"
}
svc_gone() { # remove the inbound of b
  [ -z "${B_INB:-}" ] || delete_inbound "$B_INB"
  B_INB=""
  b_empty() { node_get b | jq -e '(.inbounds | length) == 0' >/dev/null; }
  wait_for 60 "node b without inbounds" b_empty
}

# diffwhat <cell> <expected file> <actual file> <what>
diffwhat() {
  if diff -u "$2" "$3" >"$WORK/diff.out" 2>&1; then ok "$1" "$4: identical ($(wc -l <"$2") observations)"
  else bad "$1" "$4 differ between the shared node and the reference: $(grep '^[-+][^-+]' "$WORK/diff.out" | head -n 4 | cut -c1-230 | tr '\n' ' ')"; fi
}

scn_shape() {
  setup_a
  local svc n
  mkdir -p "$WORK/fp"
  # the reference: each service alone on node b
  for svc in "${SVCS[@]}"; do
    svc_alone "$svc" || { bad "$svc-alone" "$svc did not start alone on node b"; svc_gone; continue; }
    wire_start "$svc"
    for n in ${NAMES[$svc]}; do
      fp_to "$WORK/fp/b-$svc-$n" "$B_IP" "$n" "$NAMED_PROBES"
      fp_to "$WORK/fp/a-$svc-$n" "$A_IP" "$n" "$NAMED_PROBES"
      diffwhat "$svc:$n" "$WORK/fp/b-$svc-$n" "$WORK/fp/a-$svc-$n" "TLS, HTTP/2 frames, HTTP answer, hello by 1 byte and in 3 records under the name"
    done
    if [ "$svc" = "$DEFAULT_SVC" ]; then
      for n in "" "$UNKNOWN_NAME"; do
        fp_to "$WORK/fp/b-d-$n" "$B_IP" "$n" "$UNNAMED_PROBES"
        fp_to "$WORK/fp/a-d-$n" "$A_IP" "$n" "$UNNAMED_PROBES"
        diffwhat "default:${n:-no-name}" "$WORK/fp/b-d-$n" "$WORK/fp/a-d-$n" "what a stranger without the name (or with a name no one holds) sees: the default route $DEFAULT_SVC alone vs shared"
      done
    fi
    TOOL fp -addr "$B_IP:443" -what silent >"$WORK/fp/b-silent-$svc" 2>&1 || true
    wire_finish "$svc"
    svc_gone
  done
  TOOL fp -addr "$A_IP:443" -what silent >"$WORK/fp/a-silent" 2>&1 || true
  silent_rows
}
silent_rows() {
  local ta tb svc
  ta=$(sed -n 's/^silent: closed after \([0-9.]*\) s by \([A-Z]*\).*/\1 \2/p' "$WORK/fp/a-silent")
  [ -n "$ta" ] || { bad silent "the silent connection to the shared 443 was not closed in 20 s: $(cat "$WORK/fp/a-silent")"; return; }
  if awk -v t="${ta% *}" 'BEGIN{exit !(t >= 9.5 && t <= 10.5)}' && [ "${ta#* }" = FIN ]; then ok silent "a connection that says nothing is closed after ${ta% *} s by ${ta#* } (10 s +- 0.5, FIN)"
  else bad silent "a silent connection to the shared 443 was closed after ${ta% *} s by ${ta#* } (want 10 +- 0.5 s by FIN)"; fi
  for svc in "${SVCS[@]}"; do
    tb=$(sed -n 's/^silent: closed after \([0-9.]*\) s by \([A-Z]*\).*/\1 \2/p' "$WORK/fp/b-silent-$svc" 2>/dev/null)
    if [ "$svc" != "$DEFAULT_SVC" ]; then
      # a connection that says nothing has no name: on the shared port it is the default route's, so only that service is the reference
      note "silent:$svc" "$svc alone: a silent connection is $([ -n "$tb" ] && echo "closed after ${tb% *} s by ${tb#* }" || echo "not closed within 20 s (REALITY waits for the hello without a deadline)"); the shared port answers it as its default route ($DEFAULT_SVC) does"
      continue
    fi
    [ -n "$tb" ] || { bad "silent:$svc" "$svc alone: no answer to the silent probe"; continue; }
    if awk -v a="${ta% *}" -v b="${tb% *}" 'BEGIN{d=a-b; if (d<0) d=-d; exit !(d <= 0.7)}' && [ "${ta#* }" = "${tb#* }" ]; then ok "silent:$svc" "the default route $svc alone closes a silent connection after ${tb% *} s by ${tb#* }: the shared port does the same (${ta% *} s)"
    else bad "silent:$svc" "$svc alone closes a silent connection after ${tb% *} s by ${tb#* }, the shared port after ${ta% *} s by ${ta#* }"; fi
  done
}

# ---------------------------------------------------------------- 4. a flood of connections that say nothing

# agent_events <node name> <code>: how many events of that code the panel holds for the node
event_count() { api FleetService/ListEvents "$(jq -nc --arg n "${NODE_ID[$1]}" '{nodeId:$n,limit:100}')" | jq --arg c "$2" '[.events[]? | select(.code == $c)] | length'; }
live_row() { # <file> -> "p95 ms" or empty
  sed -n 's/^live ok=\([0-9]*\) err=\([0-9]*\) p50=\([0-9.]*\) p95=\([0-9.]*\) max=\([0-9.]*\) ms$/\1 \2 \3 \4 \5/p' "$1"
}

scn_flood() {
  setup_a
  a_clients || { bad clients "a client of node a did not start"; return; }
  local pid=${NODE_PID[a]} rss0 rssmax rss ev0 f=$FLOOD
  rss0=$(rss_kb "$pid") rssmax=$rss0
  ev0=$(event_count a front_overloaded)
  TOOL live -addr "$A_IP:443" -sni "$NAME_TT" -dur 6s -conc 2 >"$WORK/live-base.out" 2>&1 || true
  ( TOOL flood -addr "$A_IP:443" -n "$f" -hold 30s >"$WORK/flood.out" 2>"$WORK/flood.err" ) &
  local fl=$!
  PIDS+=("$fl")
  sleep 4
  # the flood has been open for a while: live clients now, by name (a site service, a REALITY one) and a real transfer through REALITY
  ( TOOL live -addr "$A_IP:443" -sni "$NAME_TT" -dur 18s -conc 2 >"$WORK/live-tt.out" 2>&1 || true ) &
  local l1=$!
  ( TOOL live -addr "$A_IP:443" -sni "$NAME_XH" -dur 18s -conc 2 >"$WORK/live-xh.out" 2>&1 || true ) &
  local l2=$!
  PIDS+=("$l1" "$l2")
  local xfer
  xfer=$(via "$P_XH_A" get -n $((BIG / 2)) -seed "$(seed)" 2>&1) || true
  local i
  for ((i = 0; i < 30; i++)); do
    rss=$(rss_kb "$pid")
    [ "$rss" -le "$rssmax" ] || rssmax=$rss
    alive "$l1" || alive "$l2" || break
    sleep 0.5
  done
  wait "$l1" "$l2" 2>/dev/null || true
  wait "$fl" 2>/dev/null || true
  rss=$(rss_kb "$pid")
  [ "$rss" -le "$rssmax" ] || rssmax=$rss
  local base tt xh
  base=$(live_row "$WORK/live-base.out") tt=$(live_row "$WORK/live-tt.out") xh=$(live_row "$WORK/live-xh.out")
  say "flood: $(cat "$WORK/flood.out" | tr '\n' ' ')"
  local row
  for row in "tt:$tt" "xh:$xh"; do
    read -r n e p50 p95 mx <<<"${row#*:}"
    if [ -z "${row#*:}" ]; then bad "live-${row%%:*}" "no live client got through during the flood: $(cat "$WORK/live-${row%%:*}.out" | head -n 2)"
    elif awk -v p="$p95" 'BEGIN{exit !(p < 300)}' && [ "$e" = 0 ]; then ok "live-${row%%:*}" "live clients of ${row%%:*} during the flood of $f: $n handshakes, 0 failed, p50 $p50 ms, p95 $p95 ms (< 300), max $mx ms (before the flood: ${base:-none})"
    else bad "live-${row%%:*}" "live clients of ${row%%:*} during the flood of $f: $n ok, $e failed, p95 $p95 ms (want < 300 ms and none failed)"; fi
  done
  case $xfer in ok*) ok live-bytes "a $((BIG / 2000000)) MB transfer through REALITY, started while $f silent connections were open: $xfer" ;; *) bad live-bytes "the transfer during the flood: $xfer" ;; esac
  local grow=$(((rssmax - rss0) / 1024))
  if [ "$grow" -lt 40 ]; then ok memory "the agent's memory grew by $grow MB at most during the flood of $f (limit 40 MB; $((rss0 / 1024)) MB before)"
  else bad memory "the agent's memory grew by $grow MB during the flood of $f (limit 40 MB)"; fi
  if grep -q 'msg=front_overloaded' "$WORK/node-a.log"; then ok evicted-log "the agent warned front_overloaded: $(grep -c 'msg=front_overloaded' "$WORK/node-a.log") line(s), the oldest unfinished handshakes were closed to make room"
  else bad evicted-log "$f silent connections and the agent never logged front_overloaded"; fi
  local n=0
  for ((i = 0; i < 45; i++)); do
    n=$(event_count a front_overloaded)
    [ "$n" -gt "$ev0" ] && break
    sleep 2
  done
  if [ "$n" -gt "$ev0" ]; then ok event "the panel lists the event front_overloaded for the node"
  else bad event "no front_overloaded event in the panel's list 90 s after the flood"; fi
  local sp
  sp=$(node_get a | jq -c '[.sharedPorts[]? | select(.port == 443) | {accepted: (.accepted // 0), evicted: (.evicted // 0), timeouts: (.timeouts // 0), noSni: (.defaultNoSni // 0), unknown: (.defaultUnknownName // 0)}][0] // empty')
  note counters "the panel stores the counters of a shared port when its routes change and then every 5 minutes: the stored snapshot ${sp:-none yet} was written before the flood, the flood itself is in the agent's log and in the event above"
  # people who were connected before still are: the REALITY client of the stand still carries traffic
  out=$(via "$P_XH_A" who 2>&1) || true
  [ "$out" = "ok who $A_IP" ] && ok after "the REALITY client works after the flood" || bad after "after the flood: $out"
}

# ---------------------------------------------------------------- 6. a port becomes shared and stops being shared

scn_transition() {
  setup_a
  local id_mt p mh mp ms out
  svc_alone xh || { bad xh-alone "REALITY did not start alone on node b"; svc_gone; return; }
  # the person's lines for node b come with the next subscription
  sleep 3
  fp_to "$WORK/fp/t-base" "$B_IP" "$NAME_XH" "$NAMED_PROBES"
  start_xray xh "$B_IP" p || { bad xh-alone "no Xray client towards node b"; return; }
  out=$(via "$p" get -n 3000000 -seed "$(seed)" 2>&1) && ok alone-bytes "REALITY alone on b: $out" || bad alone-bytes "REALITY alone on b: $out"
  if grep -q 'shared port opened' "$WORK/node-b.log"; then bad alone-front "the agent opened a shared port with one service on it"; else ok alone-front "one service on 443: no front ('shared port opened' is not in the agent's log); REALITY listens itself"; fi
  node_get b | jq -e '(.sharedPorts // []) | length == 0' >/dev/null && ok alone-panel "the panel shows no shared port for node b" || bad alone-panel "the panel shows a shared port for node b with one service"

  # MTProto joins: the port becomes shared
  id_mt=$(svc_inbound mt b) || { bad join "MTProto could not be added on b"; return; }
  if wait_for 90 "MTProto ACTIVE beside REALITY" inbound_active b "$id_mt" && wait_for 30 "the front" grep -q 'shared port opened' "$WORK/node-b.log"; then
    ok join-front "MTProto added beside REALITY: $(grep 'shared port opened' "$WORK/node-b.log" | tail -n 1 | sed 's/^time=[^ ]* level=[A-Z]* msg=//')"
  else bad join-front "MTProto added beside REALITY: the front did not open (inbound $(inbound_state b "$id_mt"))"; fi
  inbounds_all_active_b() { node_get b | jq -e 'all(.inbounds[]; .state == "INBOUND_STATE_ACTIVE")' >/dev/null; }
  wait_for 60 "both ACTIVE" inbounds_all_active_b || bad join-state "inbounds of b: $(inbound_error b | tr '\n' ' ')"
  stop_pid "${CLIENT_PID[$p]}"
  sleep 12
  start_xray xh "$B_IP" p || { bad join-bytes "no Xray client towards node b"; return; }
  out=$(retry 3 via "$p" get -n 3000000 -seed "$(seed)") && ok join-xh "REALITY works after the port became shared: $out" || bad join-xh "REALITY after the port became shared: $out"
  fp_to "$WORK/fp/t-shared" "$B_IP" "$NAME_XH" "$NAMED_PROBES"
  diffwhat join-fp "$WORK/fp/t-base" "$WORK/fp/t-shared" "what REALITY shows under its name, alone and then beside MTProto"
  read -r mh mp ms < <(mt_link b) || true
  if [ -n "${ms:-}" ]; then
    out=$(retry 3 nsx cli "$BIN/sni443tool" mt -addr "$mh:$mp" -domain "$NAME_MT" -secret "$ms" -n 3000000) && ok join-mt "MTProto works beside REALITY: $out" || bad join-mt "MTProto beside REALITY: $out"
  else bad join-mt "no tg:// link for node b"; fi

  # kill -9 of the agent in the shared state: it comes back on the same port with both services
  local before after
  before=$(grep -c 'shared port opened' "$WORK/node-b.log")
  kill_node b
  restart_node b
  wait_for 90 "node b online again" node_online b || bad kill9 "node b did not come back after kill -9 of its agent"
  wait_for 90 "both ACTIVE again" inbounds_all_active_b || bad kill9-state "inbounds of b after the restart: $(inbound_error b | tr '\n' ' ')"
  after=$(grep -c 'shared port opened' "$WORK/node-b.log")
  if [ "$after" -gt "$before" ] && listening b 443; then ok kill9-front "kill -9 of the agent: it came back, the front is on TCP 443 again ($after openings in the log)"
  else bad kill9-front "after kill -9 of the agent the front did not open again on 443 ($before -> $after)"; fi
  stop_pid "${CLIENT_PID[$p]}"
  start_xray xh "$B_IP" p || { bad kill9-bytes "no Xray client towards node b"; return; }
  out=$(retry 4 via "$p" get -n 3000000 -seed "$(seed)") && ok kill9-xh "REALITY works after kill -9 of the agent: $out" || bad kill9-xh "REALITY after kill -9: $out"
  read -r mh mp ms < <(mt_link b) || true
  out=$(retry 4 nsx cli "$BIN/sni443tool" mt -addr "$mh:$mp" -domain "$NAME_MT" -secret "$ms" -n 3000000) && ok kill9-mt "MTProto works after kill -9 of the agent: $out" || bad kill9-mt "MTProto after kill -9: $out"

  # MTProto leaves: REALITY listens alone again
  local closed_before
  closed_before=$(grep -c 'shared port closed' "$WORK/node-b.log")
  delete_inbound "$id_mt"
  if wait_for 60 "the front closes" bash -c "[ \$(grep -c 'shared port closed' '$WORK/node-b.log') -gt $closed_before ]"; then ok leave-front "MTProto removed: $(grep 'shared port closed' "$WORK/node-b.log" | tail -n 1 | sed 's/^time=[^ ]* level=[A-Z]* msg=//')"
  else bad leave-front "MTProto removed: the front did not close"; fi
  stop_pid "${CLIENT_PID[$p]}"
  sleep 8
  start_xray xh "$B_IP" p || { bad leave-bytes "no Xray client towards node b"; return; }
  out=$(retry 4 via "$p" get -n 3000000 -seed "$(seed)") && ok leave-xh "REALITY listens alone again and works: $out" || bad leave-xh "REALITY after MTProto left: $out"
  fp_to "$WORK/fp/t-after" "$B_IP" "$NAME_XH" "$NAMED_PROBES"
  diffwhat leave-fp "$WORK/fp/t-base" "$WORK/fp/t-after" "what REALITY shows under its name, at the start and after the port was shared and then left alone again"
  svc_gone
}

# ---------------------------------------------------------------- 7. TLS-ALPN-01

scn_alpn() {
  skip alpn01 "not checked: a TLS-ALPN-01 challenge certificate exists only while Let's Encrypt has an order open for the name, and there is no CA in the stand (and no seam in the agent to point it at one); the unit tests of internal/node/front and internal/node/certs hold the route of a challenge hello to the default route"
  skip reality-self "not checked: REALITY with the node's own site needs a Let's Encrypt certificate for the node's name (the panel forces tls mode acme_domain for target self), so the stand serves REALITY with another site (XHTTP and TCP + Vision) and the default route is TrustTunnel, MTProto or VLESS TLS, whichever has the lowest inbound id"
}

# ---------------------------------------------------------------- 5. the relay (DNAT) and the bridge (reverse relay) through the entry e

relay_json() { api RelayService/ListRelays '{}' 2>/dev/null | jq -c --arg id "$1" '.relays[] | select(.id == $id)' || true; }
relay_get() { relay_json "$1" | jq -r "$2" 2>/dev/null || true; }
relay_is() { [ "$(relay_get "$1" '.activeMode')" = "$2" ]; }
relay_summary() { relay_json "$1" | jq -c '{mode, active: .activeMode, reason: .modeReason, enabled, state, port, bridge: (.bridge | {connected, conns, error}),
  check: (.check | {ok, stage, leg: .leg.status, legCode: .leg.code, reach: .reverseReach.status, reachCode: .reverseReach.code, path: .path.status, pathCode: .path.code})}' | cut -c1-420; }
add_relay() { # <inbound id> <mode> -> relay id
  local out
  out=$(api RelayService/CreateRelay "$(jq -nc --arg i "$1" --arg v "${NODE_ID[e]}" --arg m "$2" '{inboundId:$i,viaNodeId:$v,mode:$m}')") || return 1
  jq -r .relay.id <<<"$out"
}
check_relay() { api RelayService/CheckRelay "$(jq -nc --arg i "$1" '{id:$i}')"; }
enable_relay() { api RelayService/UpdateRelay "$(jq -nc --arg i "$1" '{id:$i,enabled:true}')"; }
relay_mode_set() { api RelayService/UpdateRelay "$(jq -nc --arg i "$1" --arg m "$2" '{id:$i,mode:$m}')"; }

declare -A INB_E=() RID=() RPORT=()
RELAY_READY=""
setup_e() { # the entry's own shared 443 (TrustTunnel and MTProto), and a relay of each server of a that the plan lists, made, checked and switched on
  [ -z "$RELAY_READY" ] || return 0
  setup_a
  nsx cli sysctl -qw net.ipv4.conf.all.accept_redirects=0 net.ipv4.conf.default.accept_redirects=0 2>/dev/null || true
  nsx e sysctl -qw net.ipv4.conf.all.send_redirects=0 net.ipv4.conf.default.send_redirects=0 2>/dev/null || true
  local svc id
  # The default route of e (the lowest id of its site services) is what the bridge's exits verify the entry's site by. The stand has
  # no CA, so only a service with a self-signed certificate and a pin can be that; MTProto's decoy has no certificate here. Ids are
  # random: MTProto is made again until TrustTunnel's id is the lower.
  local i
  INB_E[tt]=$(svc_inbound tt e) || die "creating tt on the entry e failed"
  for ((i = 0; i < 12; i++)); do
    INB_E[mt]=$(svc_inbound mt e) || die "creating mt on the entry e failed"
    [[ "${INB_E[tt]}" < "${INB_E[mt]}" ]] && break
    delete_inbound "${INB_E[mt]}"
    sleep 2
  done
  need 90 "the inbounds of e ACTIVE" inbounds_all_active e
  for svc in xh vi tt mt; do
    id=$(add_relay "${INB_A[$svc]}" dnat 2>"$WORK/relay-$svc.err") || { note relay "CreateRelay($svc): $(tr '\n' ' ' <"$WORK/relay-$svc.err" | cut -c1-250)"; continue; }
    RID[$svc]=$id
    if ! out=$(retry 4 check_relay "$id"); then note relay "CheckRelay($svc): $(tr '\n' ' ' <<<"$out" | cut -c1-250)"; fi
    if ! out=$(retry 4 enable_relay "$id"); then note relay "switching the $svc relay on is refused: $(tr '\n' ' ' <<<"$out" | cut -c1-300)"; continue; fi
    RPORT[$svc]=$(relay_get "$id" '.port')
  done
  RELAY_READY=1
}

# relay_bytes <cell> <service> <what>: real traffic of that service through its relay on e
relay_bytes() {
  local cell=$1 svc=$2 what=$3 p out mh mp ms
  if [ -z "${RPORT[$svc]:-}" ]; then
    case $svc in
      mt) skip "$cell" "the relay of MTProto could not be switched on: its leg check from the entry got the verdict 'other'. For MTProto the check asks the decoy site for a publicly trusted certificate of its name (the spec has no TLS mode), and a node with no CA to reach cannot get one ($(grep -c 'acme certificate not obtained' "$WORK/node-a.log") warnings 'acme certificate not obtained yet' in node a's log). The relay of the same server by name is what the plan lists; the relay is protocol-blind, and REALITY and TrustTunnel through it are the rows above" ;;
      *) skip "$cell" "no switched-on relay for $svc ($(relay_summary "${RID[$svc]:-none}" 2>/dev/null))" ;;
    esac
    return 0
  fi
  case $svc in
    xh | vi)
      LINE_PORT=${RPORT[$svc]} start_xray "$svc" "$E_IP" p || { bad "$cell" "no Xray client through the relay port ${RPORT[$svc]}"; return 0; } ;;
    tt)
      [ -f "$WORK/pub/trusttunnel_client" ] || { skip "$cell" "no official TrustTunnel client in the cache (scripts/e2e/tt-fetch.sh)"; return 0; }
      start_tt "$E_IP" p "$(tt_relay_path)" || { bad "$cell" "no TrustTunnel client through the relay"; return 0; } ;;
    mt)
      read -r mh mp ms < <(mt_relay_link) || true
      [ -n "${ms:-}" ] || { bad "$cell" "no tg:// link of the relay twin"; return 0; }
      out=$(retry 3 nsx cli "$BIN/sni443tool" mt -addr "$mh:$mp" -domain "$NAME_MT" -secret "$ms" -n "$BIG") \
        && ok "$cell" "$what: $out" || bad "$cell" "$what: $out"
      return 0 ;;
  esac
  out=$(retry 3 via "$p" get -n "$BIG" -seed "$(seed)") && out+=" / $(retry 3 via "$p" put -n "$BIG" -seed "$(seed)")"
  case $out in *FAIL*) bad "$cell" "$what: $out"; return 0 ;; esac
  case $svc in tt) ;; *) out+=" / $(via "$p" who 2>&1)" ;; esac
  ok "$cell" "$what: $out"
}

scn_relay() {
  setup_e
  local svc
  for svc in xh vi tt mt; do
    [ -n "${RID[$svc]:-}" ] || { bad "dnat-$svc" "no relay was made for $svc"; continue; }
    if relay_is "${RID[$svc]}" dnat; then relay_bytes "dnat-$svc" "$svc" "through the relay of e (DNAT) to the shared 443 of a, $BIGLABEL"
    else skip "dnat-$svc" "the relay of $svc could not be switched on: its leg check from the entry got the verdict 'other'. For MTProto the check asks the decoy site for a publicly trusted certificate of its name (the node's spec has no TLS mode), and a node with no CA to reach cannot get one ($(grep -c 'acme certificate not obtained' "$WORK/node-a.log") warnings 'acme certificate not obtained yet' in node a's log); the panel's own state: $(relay_summary "${RID[$svc]}" | cut -c1-200)"; fi
  done
}

relay_bridge_up() { [ "$(relay_get "$1" '.bridge.connected // false')" = true ]; }
scn_bridge() {
  setup_e
  local svc out ids=()
  if [ -z "$HAVE_NFT" ]; then skip bridge "no nft in this WSL: the direct way e -> a cannot be cut, and a bridge beside a direct way proves nothing"; return; fi
  # the direct way e -> a:443 is cut for new connections: only the bridge (a dials e) can carry anything from here on
  nsx e nft -f - <<EOF
table inet snblock {
  counter cut {}
  chain out {
    type filter hook output priority 0; policy accept;
    ip daddr $A_IP tcp dport 443 tcp flags & (syn | ack) == syn counter name cut drop
  }
}
EOF
  for svc in xh tt; do
    [ -n "${RID[$svc]:-}" ] || { bad "bridge-$svc" "no relay was made for $svc"; continue; }
    if out=$(retry 3 relay_mode_set "${RID[$svc]}" reverse); then ids+=("$svc"); else skip "bridge-$svc" "the relay of $svc cannot be put in reverse: $(tr '\n' ' ' <<<"$out" | cut -c1-300)"; fi
  done
  for svc in "${ids[@]}"; do
    if wait_for 300 "the relay of $svc in reverse" relay_is "${RID[$svc]}" reverse && wait_for 120 "the bridge of $svc" relay_bridge_up "${RID[$svc]}"; then
      ok "bridge-up-$svc" "the relay of $svc stands in the reverse way; node a holds a bridge connection to the entry e on its shared 443 (the direct way e -> a:443 is cut)"
      relay_bytes "bridge-$svc" "$svc" "through the bridge (a dials e, e -> a:443 is dropped), $BIGLABEL"
    else bad "bridge-up-$svc" "no bridge for $svc: $(relay_summary "${RID[$svc]}")"; fi
  done
  note cut "SYNs of e towards a:443 dropped by the cut: $(nsx e nft list counter inet snblock cut 2>/dev/null | awk '/packets/ {print $2}') (what DNAT tried before the bridge took over)"
}

# ---------------------------------------------------------------- 3b. the wire: SYN-ACK options and TCP keepalive

declare -A WIRE_PID=() WIRE_BG=()
# wire_start <service>: idle clients of that service's first name on a and on b (alone), and a capture in both namespaces
wire_start() {
  [ -n "$HAVE_TCPDUMP" ] || return 0
  local svc=$1 name=${NAMES[$1]%% *} who ip
  for who in a b; do
    spawn_in "$who" "$WORK/fp/tcpdump-$who.log" tcpdump -nn -tt -U -i eth0 -w "$WORK/fp/$svc-$who.pcap" 'tcp port 443'
    WIRE_PID[$who]=$LAST_PID
  done
  sleep 1
  for who in a b; do
    ip=$A_IP
    [ "$who" = a ] || ip=$B_IP
    ( nsx cli "$BIN/sni443tool" idle -addr "$ip:443" -sni "$name" -n 2 -hold 26s >"$WORK/fp/idle-$svc-$who" 2>&1 || true ) &
    WIRE_BG[$who]=$!
    PIDS+=($!)
  done
}
# wire_summary <pcap> <idle output>: "<SYN-ACK window and options> keepalive=<server's empty ACKs after the 12th second>"
wire_summary() {
  local ports
  ports=$(sed -n 's/^port //p' "$2" | paste -sd'|')
  [ -n "$ports" ] || { echo "no-idle-connection"; return; }
  tcpdump -nn -tt -r "$1" 2>/dev/null | awk -v ports="$ports" '
    BEGIN { n = split(ports, a, "|"); for (i = 1; i <= n; i++) want[a[i]] = 1 }
    { dst = $5; sub(/:$/, "", dst); k = split(dst, d, "."); if (!(d[k] in want)) next }
    /Flags \[S\.\]/ { if (syn == "") { o = $0; sub(/.*options /, "", o); gsub(/TS val [0-9]+ ecr [0-9]+/, "TS", o); sub(/, length.*/, "", o); w = $0; sub(/.*, win /, "", w); sub(/,.*/, "", w); syn = "win " w " " o } next }
    /Flags \[\.\]/ && /length 0/ { if (t0 == "") t0 = $1; if ($1 - t0 > 12) ka++ }
    END { printf "%s keepalive=%d\n", syn, ka + 0 }'
}
wire_finish() { # <service>
  [ -n "$HAVE_TCPDUMP" ] || return 0
  local svc=$1 who sa sb
  wait "${WIRE_BG[a]}" "${WIRE_BG[b]}" 2>/dev/null || true
  for who in a b; do stop_pid "${WIRE_PID[$who]}"; done
  sa=$(wire_summary "$WORK/fp/$svc-a.pcap" "$WORK/fp/idle-$svc-a")
  sb=$(wire_summary "$WORK/fp/$svc-b.pcap" "$WORK/fp/idle-$svc-b")
  case $svc:$sa in
    xh:* | vi:*) [ "${sa##*keepalive=}" = 0 ] || note "wire-note:$svc" "REALITY on this build sends TCP keepalive probes on an idle client connection (${sa##*keepalive=} empty ACKs in the window), alone and shared alike; the plan's table says Xray sets none" ;;
  esac
  if [ "$sa" = "$sb" ] && [ "${sa#no-idle}" = "$sa" ]; then ok "wire:$svc" "SYN-ACK window and options and TCP keepalive of an idle connection are the same on the shared node and on the node with $svc alone: ${sa##* } ($(cut -c1-90 <<<"$sa"))"
  else bad "wire:$svc" "on the shared node: $sa; with $svc alone: $sb"; fi
}

# ---------------------------------------------------------------- 2b. the address the services see: what the engines do with it

# hello_kex <user id>: "<pq> <classic> <unknown>", the sums over the user's devices of the ClientHello counts the nodes keep per key
# (vless-hello-kex/1): an X25519MLKEM768 share before any X25519 share, a hello without it, a hello that could not be read. The node
# finds the hello of a connection by the connection's address: with an address that is not the client's it counts "unknown".
hello_kex() {
  api UserService/GetUserConnection "$(jq -nc --arg u "$1" '{userId:$u,hours:1}')" \
    | jq -r '[.helloKex[]?] | "\(map(.helloPq // 0 | tonumber) | add // 0) \(map(.helloClassic // 0 | tonumber) | add // 0) \(map(.helloUnknown // 0 | tonumber) | add // 0)"'
}
kex_counted() { local pq cl un; read -r pq cl un < <(hello_kex "$U_ID"); [ $((pq + cl + un)) -ge "$1" ]; }

scn_addr() {
  setup_a
  a_clients || { bad clients "a client of node a did not start"; return; }
  local out idle1 idle2 c1 c2 pq cl un base

  # the decoy site of MTProto limits the anonymous connections of ONE source address (8 of them) in the engine, by RemoteAddr
  # of the connection it was handed. 12 idle clients from one address must lose some; 6 from each of two addresses must lose none:
  # that is only so when the engine sees the two real addresses, not one shared (127.0.0.1, or a unix socket's "")
  TOOL idle -addr "$A_IP:443" -sni "$NAME_MT" -n 12 -hold 6s >"$WORK/idle-ctl.out" 2>&1 || true
  c1=$(sed -n 's/^idle opened=[0-9]* closed-by-server=\([0-9]*\)$/\1/p' "$WORK/idle-ctl.out")
  if [ -z "$c1" ]; then bad addr-limiter "the control run of the site limiter gave no result: $(tail -n 1 "$WORK/idle-ctl.out")"
  elif [ "$c1" -ge 3 ]; then
    ok addr-control "12 idle connections from one address to the MTProto decoy site: the engine closed $c1 of them (its limit is 8 anonymous connections per address)"
    sleep 2
    ( nsx cli "$BIN/sni443tool" idle -addr "$A_IP:443" -sni "$NAME_MT" -n 6 -hold 6s >"$WORK/idle-1.out" 2>&1 || true ) &
    idle1=$!
    ( nsx cli2 "$BIN/sni443tool" idle -addr "$A_IP:443" -sni "$NAME_MT" -n 6 -hold 6s >"$WORK/idle-2.out" 2>&1 || true ) &
    idle2=$!
    PIDS+=("$idle1" "$idle2")
    wait "$idle1" "$idle2" 2>/dev/null || true
    c1=$(sed -n 's/^idle opened=\([0-9]*\) closed-by-server=\([0-9]*\)$/\2/p' "$WORK/idle-1.out")
    c2=$(sed -n 's/^idle opened=\([0-9]*\) closed-by-server=\([0-9]*\)$/\2/p' "$WORK/idle-2.out")
    if [ "$c1" = 0 ] && [ "$c2" = 0 ]; then ok addr-limiter "6 idle connections from $CLI_IP and 6 from $CLI2_IP at once: none closed, so the MTProto decoy site counts them by their own addresses (12 together would have lost some)"
    else bad addr-limiter "6 idle connections from each of two addresses: closed ${c1:-?} and ${c2:-?}: the site limiter sees one source for both"; fi
  else bad addr-control "12 idle connections from one address to the MTProto decoy site: the engine closed $c1 (want 3 or more: the limiter did not work, so the test below proves nothing)"; fi

  # REALITY: the ClientHello counters of a person are filled by looking the connection up by its address (node/vless/tcpmask.go)
  base=$(hello_kex "$U_ID")
  out=$(via "$P_XH_A" get -n 1000000 -seed "$(seed)" 2>&1) && out+=" / $(via "$P_VI_A" get -n 1000000 -seed "$(seed)" 2>&1)"
  if wait_for 60 "the hello counters of the node" kex_counted 2; then
    read -r pq cl un < <(hello_kex "$U_ID")
    if [ "$un" = 0 ]; then ok addr-reality "the two REALITY servers on the shared 443 counted the ClientHellos of the person by their connections' addresses: $pq with a post-quantum share, $cl without, 0 unreadable (an address that is not the client's would count them unreadable)"
    else bad addr-reality "$un of the ClientHellos of REALITY connections on the shared 443 could not be matched to a connection by address ($pq pq, $cl classic)"; fi
  else bad addr-reality "no ClientHello counter of the node after the REALITY traffic ($base)"; fi

  skip addr-asn "not checked: the networks counters (and the per-network people counts) are filled through a table of ASNs that arrives as a signed release file; the stand has none, so every address of 203.0.113.0/24 is 'unknown' on any node, shared or not"
  note addr-log "the agent's journal never prints a client address at debug level either: $(grep -c "$CLI_IP" "$WORK/node-a.log" || true) lines of node a's log hold $CLI_IP (the live tail is the owner's, and the engines do not log connections)"
}

# scenarios, then what the run does
[ -z "${MG_SNI_EXP:-}" ] || { # shellcheck disable=SC1090
  . "$MG_SNI_EXP"; exit 0; } # development: run a scratch file on the stand and stop

SCENARIOS=(traffic addr shape flood transition relay bridge alpn)
run_scn() {
  local n=$1 t
  if [ -n "$ONLY" ] && ! grep -qw "$n" <<<"${ONLY//,/ }"; then return 0; fi
  STEP=$n SCN=$n
  t=$(date +%s)
  say "=== scenario $n"
  "scn_$n" || bad "$n" "the scenario function returned an error"
  say "=== $n took $(($(date +%s) - t)) s"
}
for s in "${SCENARIOS[@]}"; do run_scn "$s"; done
