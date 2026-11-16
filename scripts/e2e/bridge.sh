#!/usr/bin/env bash
# The reverse relay ("bridge") on real processes: a real panel, real node agents, real Xray clients, and a filter that cuts the
# direct way the way a direction-dependent TSPU does. Plan: plans/reverse-relay-plan.md §14 (step R6) and the owner's matrix
# "check it for different protocols" (every cell is passed, or says why it was not checked).
#
#   MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu -u root --exec bash scripts/e2e/bridge.sh [options]      (from the repo root)
#   bash scripts/e2e-wsl.sh --bridge [options]                                                     (the same)
#
# Options:
#   --only a,b,c    run only these scenarios (names in SCENARIOS below; the stand itself always comes up)
#   --quick         short sessions (30 s, 1 min, 2 min) and 20 MB instead of 200 MB, for work on the script
#   --long-return   also the two returns to the direct way by their REAL rules: the relay's (3 samples 30 min apart, a 30 minute stay,
#                   an 8 MB flow: about 100 minutes) and the exit's control session (two probes 10 minutes apart, 16-24 minutes);
#                   off by default, there is no time seam in the panel or the agent
#   --keep          keep the work directory (logs, configs)
#
# A full run takes about 80 minutes, 200 more with --long-return. Run it from a copy of the script if you edit the original while it
# runs (bash reads a script as it goes). Ending the run (an exit, INT, TERM, HUP) stops every process of the run, marked by
# MG_BRIDGE_MARK in its environment, and removes the work directory; the namespaces go with the private mount namespace.
#
# Isolation: like scripts/e2e/censor.sh. The script builds first, then `unshare -n -m` gives it a private network and mount
# namespace; the panel, the five nodes, the client, the filter and the "internet" are network namespaces inside it (a private
# /run/netns). Nothing outside is visible or touched: no host interface, route, nft table, service or container; the namespace
# names carry the run's prefix; only the PIDs this script started are stopped. Addresses: RFC 1918 links and documentation
# ranges only (203.0.113.0/24 the nodes, 198.51.100.0/24 the "internet"); no packet leaves.
#
# TOPOLOGY (a /30 veth link per line; "flt" is the filter, a router that sees both ways between the entries and the exits):
#
#   cli -- ent  -- flt -- ext  -- net      ent, ent2  entries ("in the country"): 203.0.113.30, 203.0.113.31
#   cli -- ent2 -- flt -- ext2 -- net      ext, ext2  exits ("abroad"): 203.0.113.10, 203.0.113.11
#   pan -- ent, ent2, ext, ext2            pan: the panel (10.88.100.1), reachable from every node directly
#   flt -- net                             net: the "internet": 198.51.100.100 (a far server, a REALITY target)
#
# The client reaches an entry and nothing else. The filter has three faults for TCP started by an entry towards an exit and
# one for UDP (flt_mode): "syn" drops the SYN, "freeze" lets 16 KB of a flow through and drops the rest, "udp" drops UDP;
# whatever an exit starts towards an entry passes in every mode. That is the asymmetry the bridge uses.
set -Eeuo pipefail
SELF=$(readlink -f "$0")
ROOT=${MG_BRIDGE_ROOT:-$(cd "$(dirname "$SELF")/../.." && pwd)}
ARGS=("$@")

ONLY="" QUICK="" LONG_RETURN="" KEEP=""
while [ $# -gt 0 ]; do
  case $1 in
    --only) ONLY=${2:?--only needs a list}; shift ;;
    --quick) QUICK=1 ;;
    --long-return) LONG_RETURN=1 ;;
    --keep) KEEP=1 ;;
    -h|--help) sed -n '2,/^set -E/p' "$SELF" | sed 's/^# \{0,1\}//' | head -n -1; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

if [ "$(uname -s)" != Linux ] || [ "$(id -u)" != 0 ]; then
  if ! command -v wsl.exe >/dev/null 2>&1; then
    echo "scripts/e2e/bridge.sh needs root on Linux (or WSL on Windows: wsl.exe was not found)" >&2
    exit 2
  fi
  distro=${WSL_DISTRO:-Ubuntu}
  here=$(pwd -W 2>/dev/null || pwd)
  repo=$(MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -u root wslpath -a "$here" | tr -d '\r')
  MSYS_NO_PATHCONV=1 exec wsl.exe -d "$distro" -u root -- bash "$repo/scripts/e2e/bridge.sh" "${ARGS[@]}"
fi

for tool in go curl jq python3 openssl nft ip mount mountpoint unshare setsid ss sed awk sha256sum; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done

# ---------------------------------------------------------------- outer: build, then re-run in a private namespace

if [ "${MG_BRIDGE_INNER:-}" != 1 ]; then
  WORK=$(mktemp -d /tmp/mg-bridge.XXXXXX)
  chmod 700 "$WORK"
  mkdir -p "$WORK/bin"
  # Every process of the run (the inner script, the panel, the agents, Xray, the helpers) carries MG_BRIDGE_MARK in its environment.
  # The outer script does not exec into the namespace: it stays as the parent, and whatever ends the run (an exit, INT, TERM, HUP)
  # first lets the inner script clean up, then sweeps by the mark, so no process and no work directory outlive the run. Only the
  # PIDs of this run are ever signalled.
  MARK=mgbr$$.$RANDOM
  CHILD=""
  sweep() { # <signal>
    local d pid
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      [ "$pid" != "$$" ] || continue
      [ -r "$d/environ" ] || continue
      if { tr '\0' '\n' <"$d/environ" | grep -qx "MG_BRIDGE_MARK=$MARK"; } 2>/dev/null; then kill "-$1" "$pid" 2>/dev/null || true; fi
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
    if [ -z "$KEEP" ]; then case "$WORK" in /tmp/mg-bridge.*) rm -rf -- "$WORK";; esac; else echo "work directory kept: $WORK"; fi
    exit "$rc"
  }
  trap outer_end EXIT
  trap 'exit 143' INT TERM HUP
  export GOTOOLCHAIN=${GOTOOLCHAIN:-auto} CGO_ENABLED=0 GOFLAGS="${GOFLAGS:--buildvcs=false -p=2}"
  cd "$ROOT"
  echo '[bridge] building the panel, the agent and the helpers'
  go build -o "$WORK/bin/mistgate" ./cmd/mistgate
  go build -o "$WORK/bin/mistgate-node" ./cmd/mistgate-node
  go build -o "$WORK/bin/tlsdest" ./scripts/e2e/tlsdest
  go build -o "$WORK/bin/bridgetool" ./scripts/e2e/bridgetool
  # Xray, the client: the version the agent embeds, built once and cached (the cache of scripts/e2e/m3.sh).
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
  chmod 755 "$WORK"
  env MG_BRIDGE_INNER=1 MG_BRIDGE_WORK="$WORK" MG_BRIDGE_KEEP="$KEEP" MG_BRIDGE_MARK="$MARK" unshare -n -m -- bash "$SELF" "${ARGS[@]}" &
  CHILD=$!
  rc=0
  while kill -0 "$CHILD" 2>/dev/null; do wait "$CHILD" && rc=0 || rc=$?; done
  exit "$rc"
fi

# ---------------------------------------------------------------- inner: everything below runs in the private namespace

WORK=${MG_BRIDGE_WORK:?missing work directory}
KEEP=${MG_BRIDGE_KEEP:-}
BIN=$WORK/bin
T0=$(date +%s)
PFX=brg$$
PIDS=() RESULTS=() FAILED=0 COOKIE="" TOTP_SECRET="" OWNER_STEP=-1 OWNER_STEPUP_UNTIL=0 STEP=start
PANEL_IP=10.88.100.1
ENT_IP=203.0.113.30 ENT2_IP=203.0.113.31 EXT_IP=203.0.113.10 EXT2_IP=203.0.113.11 FAR=198.51.100.100 PROBE=198.51.100.101
MTNAME=stub.example.com # the name of the MTProto server's own site
ADMIN=http://127.0.0.1:8081 PUBLIC=https://$PANEL_IP:8443
declare -A NODE_ID=() NODE_PID=() NODE_NS=() NODE_ADDR=()

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

table() {
  local row n=0
  printf '\n%-5s %-22s %-14s %s\n' result scenario cell detail
  printf '%-5s %-22s %-14s %s\n' ----- ---------------------- -------------- ------
  for row in "${RESULTS[@]}"; do
    IFS='|' read -r r s c d <<<"$row"
    printf '%-5s %-22s %-14s %s\n' "$r" "$s" "$c" "$d"
    n=$((n + 1))
  done
  printf '\n%d rows: %d PASS, %d FAIL, %d SKIP (not checked, the reason is in the row)\n' "$n" \
    "$(printf '%s\n' "${RESULTS[@]}" | grep -c '^PASS|' || true)" "$(printf '%s\n' "${RESULTS[@]}" | grep -c '^FAIL|' || true)" \
    "$(printf '%s\n' "${RESULTS[@]}" | grep -c '^SKIP|' || true)"
}

cleanup() {
  local rc=$? i
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
  if [ -n "$KEEP" ]; then say "work directory kept: $WORK"; else case "$WORK" in /tmp/mg-bridge.*) rm -rf -- "$WORK" ;; esac; fi
  table
  say "finished in $(($(date +%s) - T0)) s: $([ "$rc" = 0 ] && [ "$FAILED" = 0 ] && echo ALL-PASS || echo FAILED)"
  [ "$rc" != 0 ] || [ "$FAILED" = 0 ] || rc=1
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP
trap 'say "unexpected error at line $LINENO (step: $STEP)"' ERR

# ---------------------------------------------------------------- namespaces, links, routes, the filter

mount --make-rprivate /run 2>/dev/null || true
mkdir -p /run/netns
mountpoint -q /run/netns || mount -t tmpfs -o mode=755 tmpfs /run/netns
mount --make-rprivate /run/netns
mkdir -p /etc/netns && mount -t tmpfs tmpfs /etc/netns # the hosts files `ip netns exec` puts over /etc/hosts: private, gone with the run
ns() { printf '%s-%s' "$PFX" "$1"; }
nsx() { local n=$1; shift; ip netns exec "$(ns "$n")" "$@"; }
for n in pan ent ent2 ext ext2 cli net flt; do
  ip netns add "$(ns "$n")"
  ip -n "$(ns "$n")" link set lo up
done
nsx flt sysctl -qw net.ipv4.ip_forward=1 net.netfilter.nf_conntrack_acct=1 2>/dev/null || nsx flt sysctl -qw net.ipv4.ip_forward=1
nsx net sysctl -qw net.ipv4.ip_forward=1

SEQ=0
link() { # <a> <b> <10.88.N>: a gets .1 and b gets .2
  local a=$1 b=$2 net=$3
  SEQ=$((SEQ + 1))
  ip link add "bga$SEQ" type veth peer name "bgb$SEQ"
  ip link set "bga$SEQ" netns "$(ns "$a")"
  ip link set "bgb$SEQ" netns "$(ns "$b")"
  ip -n "$(ns "$a")" addr add "$net.1/30" dev "bga$SEQ"
  ip -n "$(ns "$b")" addr add "$net.2/30" dev "bgb$SEQ"
  ip -n "$(ns "$a")" link set "bga$SEQ" up
  ip -n "$(ns "$b")" link set "bgb$SEQ" up
}
link ent flt 10.88.1   # bga1 (ent) bgb1 (flt)
link flt ext 10.88.2   # bga2 (flt) bgb2 (ext)
link flt ext2 10.88.3  # bga3 (flt) bgb3 (ext2)
link ent2 flt 10.88.4  # bga4 (ent2) bgb4 (flt)
link pan ent 10.88.5
link pan ext 10.88.6
link pan ext2 10.88.7
link pan ent2 10.88.8
link cli ent 10.88.9
link cli ent2 10.88.10
link ext net 10.88.11
link ext2 net 10.88.12
link flt net 10.88.13  # bga7.. see below

# The filter's interfaces by name (the order of the link calls above): entry side bgb1 and bgb4, exit side bga2 and bga3, net bga13.
ENTRY_IFS='{ "bgb1", "bgb4" }'

ipa() { local n=$1; shift; ip -n "$(ns "$n")" "$@"; }
ipa ent addr add "$ENT_IP/32" dev lo
ipa ent2 addr add "$ENT2_IP/32" dev lo
ipa ext addr add "$EXT_IP/32" dev lo
ipa ext2 addr add "$EXT2_IP/32" dev lo
ipa pan addr add "$PANEL_IP/32" dev lo
ipa net addr add "$FAR/32" dev lo
ipa net addr add "$PROBE/32" dev lo

# Every node sources its connections from its own public address, as a host with one public address does.
route() { local n=$1; shift; ipa "$n" route add "$@"; }
for who in ent:$ENT_IP:10.88.1.2:10.88.5.1:10.88.9.1 ent2:$ENT2_IP:10.88.4.2:10.88.8.1:10.88.10.1; do
  IFS=: read -r n me flt_gw pan_gw cli_gw <<<"$who"
  route "$n" default via "$flt_gw" src "$me"
  route "$n" "$PANEL_IP/32" via "$pan_gw" src "$me"
done
for who in ext:$EXT_IP:10.88.2.1:10.88.6.1:10.88.11.2 ext2:$EXT2_IP:10.88.3.1:10.88.7.1:10.88.12.2; do
  IFS=: read -r n me flt_gw pan_gw net_gw <<<"$who"
  route "$n" "$ENT_IP/32" via "$flt_gw" src "$me"
  route "$n" "$ENT2_IP/32" via "$flt_gw" src "$me"
  route "$n" "$PANEL_IP/32" via "$pan_gw" src "$me"
  route "$n" 198.51.100.0/24 via "$net_gw" src "$me"
  route "$n" 10.88.1.0/30 via "$flt_gw" # a DNAT relay masquerades to the address of the entry's link
  route "$n" 10.88.4.0/30 via "$flt_gw"
done
# the panel reaches everything directly (the exit's control path is not the point of this stand)
route pan "$ENT_IP/32" via 10.88.5.2 src "$PANEL_IP"
route pan "$ENT2_IP/32" via 10.88.8.2 src "$PANEL_IP"
route pan "$EXT_IP/32" via 10.88.6.2 src "$PANEL_IP"
route pan "$EXT2_IP/32" via 10.88.7.2 src "$PANEL_IP"
# the client reaches the entries only
route cli "$ENT_IP/32" via 10.88.9.2 src 10.88.9.1
route cli "$ENT2_IP/32" via 10.88.10.2 src 10.88.10.1
# the filter and the internet
route flt "$ENT_IP/32" via 10.88.1.1
route flt "$ENT2_IP/32" via 10.88.4.1
route flt "$EXT_IP/32" via 10.88.2.2
route flt "$EXT2_IP/32" via 10.88.3.2
route flt 198.51.100.0/24 via 10.88.13.2
route net "$EXT_IP/32" via 10.88.11.1
route net "$EXT2_IP/32" via 10.88.12.1
route net "$ENT_IP/32" via 10.88.13.1
route net "$ENT2_IP/32" via 10.88.13.1

# flt_mode "<open|syn|freeze|udp ...>" [exit address ...]: the faults of the filter for what an entry starts towards these exits (every
# exit when none is named); one or several as one word ("syn udp"). What an exit starts towards an entry passes in every mode. A call replaces the faults of the exit.
declare -A FLT_MODE=()
flt_apply() {
  local mode addrs rules=""
  for addr in "$EXT_IP" "$EXT2_IP"; do
    for mode in ${FLT_MODE[$addr]:-open}; do
    case $mode in
      syn) rules+="    iifname $ENTRY_IFS ip daddr $addr tcp flags & (syn | ack) == syn counter name dropped drop"$'\n' ;;
      freeze) # the flows an entry started: whatever it sends to the exit, and what the exit sends back, after 16 KB of both
        rules+="    iifname $ENTRY_IFS ip daddr $addr ct direction original ct bytes > 16384 counter name dropped drop"$'\n'
        rules+="    oifname $ENTRY_IFS ip saddr $addr ct direction reply ct bytes > 16384 counter name dropped drop"$'\n' ;;
      udp) rules+="    iifname $ENTRY_IFS ip daddr $addr meta l4proto udp counter name dropped drop"$'\n' ;;
    esac
    done
  done
  nsx flt nft -f - <<EOF
table inet bridgelab
delete table inet bridgelab
table inet bridgelab {
  counter dropped {}
  chain forward {
    type filter hook forward priority 0; policy accept;
$rules  }
}
EOF
}
flt_mode() {
  local mode=$1 addr
  shift
  [ $# -gt 0 ] || set -- "$EXT_IP" "$EXT2_IP"
  for addr in "$@"; do FLT_MODE[$addr]=$mode; done
  flt_apply
  say "filter: $mode for $*"
}
flt_dropped() { nsx flt nft list counter inet bridgelab dropped | awk '/packets/ {print $2}'; }
flt_apply

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

# ---------------------------------------------------------------- the far side and the REALITY target

start_far() {
  spawn_in net "$WORK/far.log" "$BIN/bridgetool" serve -listen "$FAR:8080"
  spawn_in net "$WORK/tlsdest.log" "$BIN/tlsdest" -listen "$FAR:8443" -names www.example.com,www.example.net,www.example.org -cert "$WORK/tlsdest.pem"
  need 20 "the REALITY target certificate" test -s "$WORK/tlsdest.pem"
  need 10 "the far server" listening net 8080
  # What the panel's own client-eye round asks through a relay (generate_204, the trace, a 64 KiB download) lives on real names of the
  # internet: here the exits resolve those names to $PROBE (a hosts file of their namespaces) and the panel trusts that site's certificate.
  spawn_in net "$WORK/probesite.log" "$BIN/bridgetool" probesite -listen "$PROBE:443" -cert "$WORK/probesite.pem"
  need 20 "the probe site" listening net 443
  cat "$WORK/tlsdest.pem" "$WORK/probesite.pem" >"$WORK/trust.pem"
  local n
  for n in ext ext2; do
    mkdir -p "/etc/netns/$(ns "$n")"
    printf '127.0.0.1 localhost\n%s www.gstatic.com www.cloudflare.com speed.cloudflare.com\n' "$PROBE" >"/etc/netns/$(ns "$n")/hosts"
  done
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
api_err() { # like api, but prints the error text and always returns 0: for a call that is expected to be refused
  api "$@" 2>&1 || true
}

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

panel_serve() { # the panel's process (again, after a stop, with the same data directory)
  spawn_in pan "$WORK/panel.log" env SSL_CERT_FILE="$WORK/trust.pem" "$BIN/mistgate" serve --data-dir "$WORK/data" --listen "$PANEL_IP:8443" \
    --admin-listen 127.0.0.1:8081 --tls-cert "$WORK/panel.crt" --tls-key "$WORK/panel.key" --agent-addr "$PANEL_IP:8443"
  PANEL_PID=$LAST_PID
}
start_panel() {
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=bridge-panel \
    -addext "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:$PANEL_IP" -keyout "$WORK/panel.key" -out "$WORK/panel.crt" 2>/dev/null
  local out token begin ceremony login=bridge-admin password
  out=$(nsx pan "$BIN/mistgate" setup --data-dir "$WORK/data" --public-url "$PUBLIC" --admin-listen 127.0.0.1:8081)
  token=$(sed -n 's|^Setup link: .*/setup#\(.*\)$|\1|p' <<<"$out")
  [ -n "$token" ] || die "panel setup returned no setup token"
  panel_serve
  need 45 "the panel listener" nsx pan curl -skS -m 3 -o /dev/null "$PUBLIC/"
  password=$(openssl rand -hex 16)
  begin=$(api AuthService/BeginSetup "$(jq -nc --arg t "$token" --arg l "$login" --arg p "$password" \
    '{setupToken:$t,displayName:"Bridge lab",method:"SETUP_METHOD_PASSWORD",login:$l,password:$p}')") || die "BeginSetup failed"
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
  spawn_in "${NODE_NS[$name]}" "$WORK/node-$name.log" env SSL_CERT_FILE="$WORK/tlsdest.pem" bash -c \
    'mkdir -p /etc/systemd/journald.conf.d; mount -t tmpfs tmpfs /etc/sysctl.d; mount -t tmpfs tmpfs /etc/systemd/journald.conf.d; exec "$@"' _ \
    "$BIN/mistgate-node" run --state-dir "$WORK/state/$name"
  NODE_PID[$name]=$LAST_PID
}
make_node() { # <name> <address> <country> <namespace short name>
  local name=$1 address=$2 country=$3 n=$4 out line
  NODE_NS[$name]=$n NODE_ADDR[$name]=$address
  out=$(api NodeService/CreateEnrollment "$(jq -nc --arg n "$name" --arg a "$address" --arg c "$country" '{name:$n,address:$a,countryCode:$c}')") \
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
node_online() { api NodeService/ListNodes '{}' | jq -e --arg n "$1" '.nodes[] | select(.name == $n and .status == "NODE_STATUS_ONLINE")' >/dev/null; }
kill_node() { # <name>: only the PID this script started
  local pid=${NODE_PID[$1]}
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
restart_node() { node_cmd "$1"; }

# ---------------------------------------------------------------- profiles, users, relays

settings_of() { api ProfileService/ListProtocols '{}' | jq -c --arg id "$1" '.protocols[] | select(.id == $id) | .defaultSettingsJson | fromjson'; }
vless_settings() { # <port> <transport> <server name> [jq filter]
  settings_of vless | jq -c --argjson p "$1" --arg t "$2" --arg sn "$3" --arg target "$FAR:8443" \
    ".port = \$p | .transport = \$t | .reality.target_kind = \"site\" | .reality.target = \$target | .reality.server_names = [\$sn] | ${4:-.}"
}
make_profile() { # <protocol> <name> <settings json> -> id
  api ProfileService/CreateProfile "$(jq -nc --arg p "$1" --arg n "$2" --arg s "$3" '{protocol:$p,name:$n,settingsJson:$s}')" | jq -r .profile.id
}
make_inbound() { # <profile id> <node name> -> inbound id
  api ProfileService/CreateInbound "$(jq -nc --arg p "$1" --arg n "${NODE_ID[$2]}" '{profileId:$p,nodeId:$n}')" | jq -r .inbound.id
}
inbound_active() { # <node name> <inbound id>
  api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[$1]}" '{nodeId:$n}')" \
    | jq -e --arg id "$2" 'any(.inbounds[]; .id == $id and .state == "INBOUND_STATE_ACTIVE")' >/dev/null
}

relay_json() { api RelayService/ListRelays "{}" 2>/dev/null | jq -c --arg id "$1" '.relays[] | select(.id == $id)' || true; }
relay_get() { relay_json "$1" | jq -r "$2" 2>/dev/null || true; }
relay_mode() { relay_get "$1" '.activeMode'; }
relay_reason() { relay_get "$1" '.modeReason // ""'; }
relay_summary() { relay_json "$1" | jq -c '{mode, active: .activeMode, reason: .modeReason, enabled, state, port, streak: .directStreak,
  bridge: (.bridge | {connected, conns, rttMs, clients, pool, capacity, error}), monitor: (.monitor | {ok, stage, code, failStreak}),
  check: (.check | {ok, stage, leg: .leg.status, legCode: .leg.code, reach: .reverseReach.status, reachCode: .reverseReach.code})}'; }
relay_is() { [ "$(relay_mode "$1")" = "$2" ]; }
relay_bridge_up() { [ "$(relay_get "$1" '.bridge.connected // false')" = true ]; }

# add_relay <inbound id> <entry node> [mode] -> relay id; the relay is made, checked and switched on the way the owner does it
add_relay() {
  local inb=$1 entry=$2 mode=${3:-auto} out id
  out=$(api RelayService/CreateRelay "$(jq -nc --arg i "$inb" --arg v "${NODE_ID[$entry]}" --arg m "$mode" '{inboundId:$i,viaNodeId:$v,mode:$m}')") || return 1
  id=$(jq -r .relay.id <<<"$out")
  echo "$id"
}
check_relay() { api RelayService/CheckRelay "$(jq -nc --arg i "$1" '{id:$i}')"; }
enable_relay() { api RelayService/UpdateRelay "$(jq -nc --arg i "$1" '{id:$i,enabled:true}')"; }

# ---------------------------------------------------------------- the client: one Xray per relay, a SOCKS port, the relay's URI

declare -A SOCKS_LABEL=() XRAY_PID=()
start_xray() { # <label> <config> <socks port>
  SOCKS_LABEL[$3]=$1
  spawn_in cli "$WORK/xray-$1.log" "$BIN/xray" run -c "$2"
  XRAY_PID[$3]=$LAST_PID
  wait_for 25 "xray $1 to listen" listening cli "$3" || { tail -n 5 "$WORK/xray-$1.log" >&2; return 1; }
}
write_vless2xray() {
  cat >"$WORK/vless2xray.py" <<'PY'
import json, sys
from urllib.parse import urlsplit, parse_qs, unquote
u = urlsplit(sys.argv[1])
q = {k: v[0] for k, v in parse_qs(u.query).items()}
assert u.scheme == "vless" and q["encryption"] == "none" and q["security"] == "reality", q
user = {"id": unquote(u.username), "encryption": "none"}
if q.get("flow"):
    user["flow"] = q["flow"]
stream = {"network": q["type"], "security": "reality", "realitySettings": {
    "serverName": q["sni"], "fingerprint": q["fp"], "publicKey": q["pbk"], "shortId": q["sid"], "spiderX": q.get("spx", "")}}
label = "vision" if q["type"] == "tcp" else "xhttp"
if q["type"] == "xhttp":
    x = {"path": q["path"], "mode": q.get("mode", "auto")}
    if q.get("host"):
        x["host"] = q["host"]
    if q.get("extra"):
        x["extra"] = json.loads(q["extra"])
    stream["xhttpSettings"] = x
conf = {"log": {"loglevel": "warning"},
        "inbounds": [{"listen": "127.0.0.1", "port": int(sys.argv[2]), "protocol": "socks", "settings": {"auth": "noauth", "udp": False}}],
        "outbounds": [{"protocol": "vless", "settings": {"vnext": [{"address": u.hostname, "port": u.port, "users": [user]}]}, "streamSettings": stream}]}
json.dump(conf, open(sys.argv[3], "w"))
print(label)
PY
}
sub_lines() { nsx pan curl -skS -m 30 -A 'v2rayNG/1.10.20' "$USER_URL" | base64 -d 2>/dev/null; }
# client_for <relay id> <socks port> [label]: the subscription's line for this relay (the entry's address and the relay's port)
client_for() {
  local rid=$1 port=$2 label=${3:-$1} host rport line conf
  host=${NODE_ADDR[$(relay_get "$rid" '.viaNodeName')]}
  rport=$(relay_get "$rid" '.port')
  line=""
  for _ in $(seq 1 30); do
    line=$(sub_lines | grep '^vless://' | grep -E "@$host:$rport\?" | head -n 1 || true)
    [ -n "$line" ] && break
    sleep 1
  done
  [ -n "$line" ] || { say "no line for $host:$rport in the subscription: $(sub_lines | sed 's|vless://[^@]*@|vless://X@|' | cut -c1-90 | tr '\n' ' ')"; return 1; }
  conf=$WORK/xray-$label.json
  python3 "$WORK/vless2xray.py" "$line" "$port" "$conf" >/dev/null || return 1
  chmod 600 "$conf"
  start_xray "$label" "$conf" "$port"
}
SOCKS_OF=()
bt() { nsx cli "$BIN/bridgetool" "$@"; } # a client command; the socks port and target are the caller's
via() { local p=$1 c=$2; shift 2; bt "$c" -socks "127.0.0.1:$p" -target "$FAR:8080" "$@"; }

# ---------------------------------------------------------------- the stand comes up

STEP=stand
say "bringing up the far side"
start_far
say "bringing up the panel"
start_panel
write_vless2xray
make_node ent "$ENT_IP" RU ent
make_node ent2 "$ENT2_IP" RU ent2
make_node ext "$EXT_IP" DE ext
make_node ext2 "$EXT2_IP" NL ext2
nodes_up() { local n; for n in ent ent2 ext ext2; do node_online "$n" || return 1; done; }
need 120 "four online node agents" nodes_up
say "agents online: ent ent2 ext ext2"

# The exits' servers: REALITY RAW + Vision, REALITY XHTTP (two exits, so two exits can stand behind one entry); the entry ent holds
# a Hysteria2 server whose self-signed site answers on TCP 443, which is where its bridge lives (the bridge point "on the site of
# an inbound"); ent2 holds a REALITY server with an outside target on 443, so its bridge needs a port of its own.
P_VISION=$(make_profile vless bridge-vision "$(vless_settings 8443 tcp www.example.net)")
P_XHTTP=$(make_profile vless bridge-xhttp "$(vless_settings 8444 xhttp www.example.com)")
P_XHTTP2=$(make_profile vless bridge-xhttp2 "$(vless_settings 8443 xhttp www.example.org)")
P_VISION2=$(make_profile vless bridge-vision2 "$(vless_settings 8447 tcp www.example.com)")
P_ENT443=$(make_profile vless bridge-ent443 "$(vless_settings 443 tcp www.example.com)")
H2=$(settings_of hysteria2 | jq -c '.tls_mode = "self_signed" | .port = 4433 | .hop = {from: 0, to: 0} | .obfs.password = "$generate"')
P_H2ENT=$(make_profile hysteria2 bridge-hy2-entry "$H2")
for p in "$P_VISION" "$P_XHTTP" "$P_XHTTP2" "$P_VISION2" "$P_ENT443" "$P_H2ENT"; do
  [ -n "$p" ] && [ "$p" != null ] || die "a profile was not created"
done
I_VISION=$(make_inbound "$P_VISION" ext)
I_XHTTP=$(make_inbound "$P_XHTTP" ext)
I_XHTTP2=$(make_inbound "$P_XHTTP2" ext2)
I_VISION2=$(make_inbound "$P_VISION2" ext2)
I_ENT443=$(make_inbound "$P_ENT443" ent2)
I_H2ENT=$(make_inbound "$P_H2ENT" ent)
servers_active() {
  inbound_active ext "$I_VISION" && inbound_active ext "$I_XHTTP" && inbound_active ext2 "$I_XHTTP2" && inbound_active ext2 "$I_VISION2" && inbound_active ent2 "$I_ENT443" \
    && inbound_active ent "$I_H2ENT"
}
need 120 "the servers ACTIVE" servers_active
GROUP=$(api GroupService/CreateGroup "$(jq -nc --argjson p "[\"$P_VISION\",\"$P_XHTTP\",\"$P_XHTTP2\",\"$P_VISION2\"]" '{name:"bridge-group",profileIds:$p}')" | jq -r .group.id)
USER=$(api UserService/CreateUser "$(jq -nc --arg g "$GROUP" '{name:"alice",groupId:$g}')")
USER_ID=$(jq -r .user.id <<<"$USER") USER_URL=$(jq -r .subscriptionUrl <<<"$USER")
say "stand is up: 4 nodes, 6 servers, a user; $(($(date +%s) - T0)) s"


# ================================================================ scenarios
#
# Every scenario is a function scn_<name>; run_scn runs it, records PASS/FAIL/SKIP rows (ok, bad, rec SKIP) and goes on (a stand
# failure is die, a scenario's own failure is a FAIL row). A scenario that needs relays another one makes asks for them with
# the setup_* functions, so --only works.

if [ -n "$QUICK" ]; then
  D30=30 D2M=60 D10M=120 BIG=20000000 BIGLABEL="20 MB"
else
  D30=30 D2M=120 D10M=600 BIG=200000000 BIGLABEL="200 MB"
fi
IDLE_SECS=150 # the bridge closes a session that has been quiet for 2 minutes

PORT_NEXT=11000
alloc_port() { PORT_NEXT=$((PORT_NEXT + 1)); printf -v "$1" %s "$PORT_NEXT"; } # <variable>: the next SOCKS port of a client (a $(...) would lose the counter)
seed() { echo $((100 + RANDOM * 32 + RANDOM % 32)); }

retry() { # <tries> <command...>: the panel's port checks take the check lock of a node for a few seconds
  local n=$1 i out
  shift
  for ((i = 1; i <= n; i++)); do
    if out=$("$@" 2>&1); then printf '%s' "$out"; return 0; fi
    sleep 8
  done
  printf '%s' "$out"
  return 1
}

wait_mode() { # <relay id> <mode> <seconds>: 0 when the relay stands in the mode in time; ELAPSED is how long it took
  local t0 i
  t0=$(date +%s)
  for ((i = 0; i < $3; i += 5)); do
    if relay_is "$1" "$2"; then ELAPSED=$(($(date +%s) - t0)); return 0; fi
    sleep 5
  done
  ELAPSED=$(($(date +%s) - t0))
  return 1
}
relay_path_ok() { [ "$(relay_get "$1" '.check.path.status // ""')" = ok ]; }
# wait_settled <relay id>: the entry has swapped the forward for the bridge's listener and a third node got through it (the proof the
# controller runs after a switch); people who connect while the DNAT rule is going and the listener is not yet there are reset
wait_settled() { wait_for 150 "the path stage of $1" relay_path_ok "$1"; }
wait_bridge() { # <relay id> <seconds>: the exit holds a connection to the entry
  wait_for "$2" "the bridge of $1" relay_bridge_up "$1"
}
sub_line_of() { # <relay id>: the host:port of the relay's line in the subscription (what the person's app has)
  local host rport
  host=${NODE_ADDR[$(relay_get "$1" '.viaNodeName')]}
  rport=$(relay_get "$1" '.port')
  sub_lines | grep '^vless://' | grep -oE "@$host:$rport\?" | head -n 1
}

# via_ok <cell> <socks port> <what> <bridgetool command and its arguments>: one transfer; the tool's own line is the detail
via_ok() {
  local cell=$1 p=$2 what=$3 out
  shift 3
  if out=$(via "$p" "$@" 2>&1); then ok "$cell" "$what: $out"; return 0; fi
  bad "$cell" "$what: $out"
  diag "$p"
  return 1
}
# diag <socks port>: what the client of that port and the agents logged lately (a failed transfer is worth its evidence)
diag() {
  local f
  say "  diag: client log of port $1 ($(relay_summary "${DIAG_RELAY:-none}" 2>/dev/null | cut -c1-200)):"
  tail -n 6 "$WORK/xray-${SOCKS_LABEL[$1]:-none}.log" 2>/dev/null | cut -c1-240 | sed 's/^/    xray: /'
  for f in "$WORK"/node-ent.log "$WORK"/node-ext.log; do
    grep -E 'level=(WARN|ERROR)|bridge' "$f" 2>/dev/null | tail -n 4 | cut -c1-300 | sed "s|^|    $(basename "$f" .log): |"
  done
}
# renew_client <variable of the socks port> <relay id> <label>: the person's app starts again (a new Xray on a new SOCKS port, the old
# one stopped). A client that pools connections (XHTTP) holds some from the DNAT time: the flow of such a connection lived in the
# entry's NAT, which went with the DNAT rule, so the first packets on it are answered with a reset by the entry's own TCP stack. That
# is the way of any switch to the bridge (the lab counts it, see scn_auto_reverse); the connections the app opens after it are the
# bridge's.
renew_client() {
  local var=$1 rid=$2 label=$3 old=${!1} port
  stop_pid "${XRAY_PID[$old]:-0}"
  alloc_port port
  client_for "$rid" "$port" "$label" || return 1
  printf -v "$var" %s "$port"
}
via_try() { via "$@" >/dev/null 2>&1; } # <socks port> <command ...>: silent, only the status
who_is() { # <cell> <socks port> <expected exit address> <what>
  local out
  out=$(via "$2" who 2>&1) || { bad "$1" "$4: $out"; return 1; }
  if [ "$out" = "ok who $3" ]; then ok "$1" "$4: the far server saw $3"; else bad "$1" "$4: the far server saw '$out', want $3"; return 1; fi
}

# setup_main: two relays of the exit ext through the entry ent (a REALITY RAW+Vision server and an XHTTP one), switched on while
# the direct way is open, each with its own Xray client; R_VISION, R_XHTTP, S_VISION, S_XHTTP are what the scenarios use
setup_main() {
  [ -z "${R_VISION:-}" ] || return 0
  flt_mode open "$EXT_IP"
  local r
  R_VISION=$(add_relay "$I_VISION" ent auto) || die "CreateRelay vision: $(api_err RelayService/CreateRelay "$(jq -nc --arg i "$I_VISION" --arg v "${NODE_ID[ent]}" '{inboundId:$i,viaNodeId:$v}')")"
  R_XHTTP=$(add_relay "$I_XHTTP" ent auto) || die "CreateRelay xhttp"
  for r in "$R_VISION" "$R_XHTTP"; do
    retry 4 check_relay "$r" >/dev/null || die "CheckRelay $r"
    retry 4 enable_relay "$r" >/dev/null || die "UpdateRelay(enabled) $r"
  done
  alloc_port S_VISION
  alloc_port S_XHTTP
  client_for "$R_VISION" "$S_VISION" vision || die "no Xray client for the vision relay"
  client_for "$R_XHTTP" "$S_XHTTP" xhttp || die "no Xray client for the xhttp relay"
}
# setup_reverse: setup_main, then the direct way cut for ext and both relays standing in the reverse way
setup_reverse() {
  setup_main
  relay_is "$R_VISION" reverse && relay_is "$R_XHTTP" reverse && return 0
  flt_mode syn "$EXT_IP"
  wait_mode "$R_VISION" reverse 900 && wait_mode "$R_XHTTP" reverse 900 || die "the relays did not move to the bridge"
  wait_bridge "$R_VISION" 60 && wait_bridge "$R_XHTTP" 60 || die "the bridge is not up"
  wait_settled "$R_VISION" && wait_settled "$R_XHTTP" || die "the path through the bridge was not proven"
}

# ---------------------------------------------------------------- 1. the direct way dies, the relay moves to the bridge by itself

scn_auto_reverse() {
  setup_main
  local c rid p
  declare -A RID=([vision]=$R_VISION [xhttp]=$R_XHTTP) PRT=([vision]=$S_VISION [xhttp]=$S_XHTTP)
  for c in vision xhttp; do
    rid=${RID[$c]} p=${PRT[$c]}
    if relay_is "$rid" dnat; then ok "$c" "the relay is on and stands in DNAT while the direct way is open (mode auto)"
    else bad "$c" "the relay stands in $(relay_mode "$rid") before anything was cut"; fi
    via_ok "$c" "$p" "DNAT, 3 MB down" get -n 3000000 -seed "$(seed)"
    via_ok "$c" "$p" "DNAT, 1 MB up" put -n 1000000 -seed "$(seed)"
    who_is "$c" "$p" "$EXT_IP" "DNAT"
  done
  LINE_V0=$(sub_line_of "$R_VISION") LINE_X0=$(sub_line_of "$R_XHTTP")
  [ -n "$LINE_V0" ] && [ -n "$LINE_X0" ] || bad subscription "no line of the relay in the subscription"

  flt_mode syn "$EXT_IP" # a SYN of an entry towards the exit is dropped; whatever the exit starts passes
  local t0 tv tx
  t0=$(date +%s)
  wait_mode "$R_VISION" reverse 900 && tv=$ELAPSED || tv=-1
  wait_mode "$R_XHTTP" reverse 900 && tx=$ELAPSED || tx=-1
  for c in vision xhttp; do
    rid=${RID[$c]} p=${PRT[$c]}
    if relay_is "$rid" reverse && [ "$(relay_reason "$rid")" = leg_failed ]; then
      ok "$c" "auto moved the relay to reverse by itself, reason leg_failed, $(($(date +%s) - t0)) s after the SYNs began to be dropped (two failed client-eye rounds, one diagnosis)"
    else bad "$c" "the relay stands in $(relay_mode "$rid") ($(relay_reason "$rid")) $(($(date +%s) - t0)) s after the direct way died: $(relay_summary "$rid")"; fi
  done
  wait_bridge "$R_VISION" 60 && wait_bridge "$R_XHTTP" 60 || bad bridge "the exit holds no connection to the entry"
  local gap0
  gap0=$(date +%s)
  wait_settled "$R_VISION" && wait_settled "$R_XHTTP" && ok bridge "the controller's proof (the entry's listener, a third node through it) passed $(($(date +%s) - gap0)) s after the switch"     || bad bridge "no passing path stage after the switch to reverse: $(relay_json "$R_VISION" | jq -c '.check.path')"
  spawn_in ent "$WORK/rst.txt" tcpdump -l -n -i any 'tcp[tcpflags] & tcp-rst != 0' # the resets the entry sends or sees from here on
  for c in vision xhttp; do
    rid=${RID[$c]} p=${PRT[$c]}
    sleep 1
    if [ "$c" = xhttp ]; then
      # the pooled connections of the DNAT time: how many requests meet a reset, and does the entry say so
      local stale=0 k
      for k in 1 2 3; do via_try "$p" get -n 100000 -seed "$k" || stale=$((stale + 1)); done
      rec NOTE "$SCN" "$c" "the client's pool still held connections of the DNAT time: $stale of 3 requests on them were reset ($(grep -c 'Flags \[R' "$WORK/rst.txt" || true) resets sent by the entry's TCP stack since the switch); the app that starts again, or opens new connections, is served by the bridge"
      renew_client S_XHTTP "$rid" xhttp-after || { bad "$c" "no Xray client after the switch"; continue; }
      p=$S_XHTTP
    fi
    via_ok "$c" "$p" "reverse, 3 MB down" get -n 3000000 -seed "$(seed)"
    via_ok "$c" "$p" "reverse, 1 MB up" put -n 1000000 -seed "$(seed)"
    who_is "$c" "$p" "$EXT_IP" "reverse"
    if [ "$(relay_get "$rid" '.bridge.conns // 0')" -ge 1 ]; then ok "$c" "the bridge reports $(relay_get "$rid" '.bridge.conns') TLS connection(s), error '$(relay_get "$rid" '.bridge.error // ""')'"
    else bad "$c" "the relay reports no bridge connection"; fi
  done
  if [ "$(sub_line_of "$R_VISION")" = "$LINE_V0" ] && [ "$(sub_line_of "$R_XHTTP")" = "$LINE_X0" ]; then
    ok subscription "the subscription's lines of both relays are the same as before the switch (the app changes nothing)"
  else bad subscription "the relay's line in the subscription changed with the mode: vision '$LINE_V0' -> '$(sub_line_of "$R_VISION")', xhttp '$LINE_X0' -> '$(sub_line_of "$R_XHTTP")'; relay path stage: $(relay_json "$R_VISION" | jq -c '.check.path')"; fi
  relay_down_open() { [ "$(api HealthService/ListAlerts '{}' | jq '[.active[]? | select(.kind | test("RELAY_DOWN"))] | length')" != 0 ]; }
  if relay_down_open; then
    rec NOTE "$SCN" alerts "the alert relay_down was open while the direct way was dead ($(api HealthService/ListAlerts '{}' | jq -c '[.active[]? | select(.kind | test("RELAY_DOWN")) | .whyKey] | unique' | cut -c1-120))"
    local i
    for ((i = 0; i < 40 && 1; i++)); do relay_down_open || break; sleep 6; done
  fi
  if relay_down_open; then bad alerts "relay_down is still open 4 minutes after the bridge carries the relays"
  else ok alerts "no relay_down alert while the bridge carries the relays (it closed by itself after the switch)"; fi
}

# ---------------------------------------------------------------- 2. a bridge on a port of its own, found by the gate of the switch-on

scn_gate_port() {
  flt_mode syn "$EXT2_IP"
  R_GATE=$(add_relay "$I_XHTTP2" ent2 auto) || { bad xhttp "CreateRelay via ent2 failed"; return; }
  local out port reason
  out=$(retry 4 check_relay "$R_GATE") || { bad xhttp "CheckRelay: $out"; return; }
  if jq -e '.relay.check.leg.status == "fail" and .relay.check.reverseReach.status == "ok"' <<<"$out" >/dev/null; then
    ok xhttp "CheckRelay with the direct way dead: leg fail ($(jq -r .relay.check.leg.code <<<"$out")), reverse_reach ok (port $(jq -r '.relay.check.reverseReach.port' <<<"$out"))"
  else bad xhttp "CheckRelay: $(jq -c '.relay.check | {leg, reverseReach}' <<<"$out" | cut -c1-400)"; fi
  out=$(retry 3 enable_relay "$R_GATE") || { bad xhttp "UpdateRelay(enabled): $out"; return; }
  if [ "$(relay_mode "$R_GATE")" = reverse ] && [ "$(relay_reason "$R_GATE")" = leg_failed ]; then
    ok xhttp "the gate of the switch-on put the relay straight in reverse (leg_failed): direct dead, reverse_reach passed"
  else bad xhttp "the relay stands in $(relay_mode "$R_GATE") ($(relay_reason "$R_GATE")) after the switch-on"; fi
  port=$(api NodeService/GetNode "{\"nodeId\":\"${NODE_ID[ent2]}\"}" | jq -r '.node.bridgePort // 0')
  reason=$(api NodeService/GetNode "{\"nodeId\":\"${NODE_ID[ent2]}\"}" | jq -r '.node.bridgePortReason // ""')
  if [ "$port" != 0 ] && [ "$port" != 443 ] && [ "$reason" = reality_external_target ]; then
    ok xhttp "bridge point of a port of its own: ent2 holds 443 for a REALITY server with an outside target, the bridge is on port $port ($reason)"
  else bad xhttp "ent2's bridge point: port $port, reason '$reason'"; fi
  wait_bridge "$R_GATE" 60 || bad xhttp "the exit holds no connection to ent2's bridge port"
  wait_settled "$R_GATE" || bad xhttp "no passing path stage through ent2's bridge"
  alloc_port S_GATE
  client_for "$R_GATE" "$S_GATE" gate || { bad xhttp "no Xray client for the relay"; return; }
  via_ok xhttp "$S_GATE" "separate bridge port, 3 MB down" get -n 3000000 -seed "$(seed)"
  via_ok xhttp "$S_GATE" "separate bridge port, 1 MB up" put -n 1000000 -seed "$(seed)"
  who_is xhttp "$S_GATE" "$EXT2_IP" "separate bridge port"
}

# ---------------------------------------------------------------- 3. the flow freezes after 16 KB: packets live, the flow does not

scn_freeze() {
  # the second exit (ext2) holds a Vision server and an XHTTP one, both relayed by ent: the filter freezes every flow after 16 KB
  # (the packets of the leg live, so the leg check passes) and the panel's own client round must say "freeze", for the transport that
  # opens a flow of its own per connection (Vision) and for the one that carries a round's requests in a single flow (XHTTP)
  flt_mode open "$EXT2_IP"
  local c rid p
  R_FREEZE=$(add_relay "$I_XHTTP2" ent auto) || { bad xhttp "CreateRelay via ent failed"; return; }
  R_FREEZEV=$(add_relay "$I_VISION2" ent auto) || { bad vision "CreateRelay of the Vision server of ext2 failed"; return; }
  for rid in "$R_FREEZE" "$R_FREEZEV"; do
    retry 4 check_relay "$rid" >/dev/null || { bad freeze "CheckRelay failed for $rid"; return; }
    retry 4 enable_relay "$rid" >/dev/null || { bad freeze "UpdateRelay(enabled) failed for $rid"; return; }
  done
  alloc_port S_FREEZE
  alloc_port S_FREEZEV
  client_for "$R_FREEZE" "$S_FREEZE" freeze || { bad xhttp "no Xray client"; return; }
  client_for "$R_FREEZEV" "$S_FREEZEV" freezev || { bad vision "no Xray client"; return; }
  declare -A RID=([xhttp]=$R_FREEZE [vision]=$R_FREEZEV) PRT=([xhttp]=$S_FREEZE [vision]=$S_FREEZEV)
  for c in xhttp vision; do
    relay_is "${RID[$c]}" dnat && ok "$c" "second exit (ext2) behind the same entry: DNAT while the direct way is open" || bad "$c" "not in dnat"
    via_ok "$c" "${PRT[$c]}" "DNAT, 3 MB down" get -n 3000000 -seed "$(seed)"
  done
  flt_mode freeze "$EXT2_IP"
  sleep 2
  for c in xhttp vision; do
    if out=$(via "${PRT[$c]}" get -n 3000000 -seed "$(seed)" 2>&1); then bad "$c" "the filter did not freeze a 3 MB flow: $out"
    else ok "$c" "the filter freezes a 3 MB flow after 16 KB ($out)"; fi
  done
  local seen_xhttp="" seen_vision="" i
  for ((i = 0; i < 90; i++)); do
    [ "$(relay_get "$R_FREEZE" '.monitor.stage // ""')" != freeze ] || seen_xhttp=1
    [ "$(relay_get "$R_FREEZEV" '.monitor.stage // ""')" != freeze ] || seen_vision=1
    relay_is "$R_FREEZE" reverse && relay_is "$R_FREEZEV" reverse && break
    sleep 10
  done
  for c in xhttp vision; do
    rid=${RID[$c]}
    seen=$seen_xhttp
    [ "$c" = xhttp ] || seen=$seen_vision
    [ -n "$seen" ] && ok "$c" "the client-eye round named the stage 'freeze' (packets pass, the flow stops)" \
      || rec NOTE "$SCN" "$c" "the stage 'freeze' was not seen in the monitor: $(relay_json "$rid" | jq -c '.monitor | {ok, stage, code, failStreak}')"
    if relay_is "$rid" reverse; then
      ok "$c" "the controller moved the relay to reverse (reason $(relay_reason "$rid"))"
      wait_bridge "$rid" 60 || bad "$c" "no bridge"
      wait_settled "$rid" || bad "$c" "no passing path stage through the bridge"
      if [ "$c" = xhttp ]; then # the pooled connections of the DNAT time were frozen and are reset by the switch: the app starts again
        renew_client S_FREEZE "$rid" freeze-after || { bad "$c" "no Xray client after the switch"; continue; }
        PRT[xhttp]=$S_FREEZE
      fi
      via_ok "$c" "${PRT[$c]}" "reverse after the freeze, 3 MB down" get -n 3000000 -seed "$(seed)"
      via_ok "$c" "${PRT[$c]}" "reverse after the freeze, 1 MB up" put -n 1000000 -seed "$(seed)"
    else bad "$c" "the relay stays in $(relay_mode "$rid") ($(relay_reason "$rid")) 15 minutes after the flow began to freeze: $(relay_summary "$rid")"; fi
  done
}

# ---------------------------------------------------------------- 4. two exits behind one entry, different protocols at once

scn_two_exits() {
  setup_reverse
  [ -n "${R_FREEZE:-}" ] && relay_is "$R_FREEZE" reverse && relay_is "$R_FREEZEV" reverse || { rec SKIP "$SCN" vision+xhttp "the relays of the second exit did not reach reverse (see freeze)"; return; }
  local a b c3
  (via "$S_VISION" par -n 10 -bytes 1000000 -rounds 4 -seed 7000 >"$WORK/two-a.out" 2>&1; echo $? >>"$WORK/two-a.out") &
  a=$!
  (via "$S_FREEZE" par -n 10 -bytes 1000000 -rounds 4 -seed 8000 >"$WORK/two-b.out" 2>&1; echo $? >>"$WORK/two-b.out") &
  b=$!
  (via "$S_FREEZEV" par -n 10 -bytes 1000000 -rounds 4 -seed 8500 >"$WORK/two-c.out" 2>&1; echo $? >>"$WORK/two-c.out") &
  c3=$!
  who_is vision "$S_FREEZEV" "$EXT2_IP" "exit ext2 (RAW+Vision) while ext and ext2 are used"
  who_is vision "$S_VISION" "$EXT_IP" "exit ext (RAW+Vision) while ext2 is used"
  who_is xhttp "$S_FREEZE" "$EXT2_IP" "exit ext2 (XHTTP) while ext is used"
  wait "$a" "$b" "$c3" || true
  if [ "$(tail -n 1 "$WORK/two-a.out")" = 0 ] && [ "$(tail -n 1 "$WORK/two-b.out")" = 0 ] && [ "$(tail -n 1 "$WORK/two-c.out")" = 0 ]; then
    ok two_exits "two exits and three relays through one entry at the same time: $(head -n 1 "$WORK/two-a.out") | $(head -n 1 "$WORK/two-b.out") | $(head -n 1 "$WORK/two-c.out")"
  else bad two_exits "$(head -n 1 "$WORK/two-a.out") | $(head -n 1 "$WORK/two-b.out") | $(head -n 1 "$WORK/two-c.out")"; fi
  local ids
  ids=$(jq -c '.bridgeServe.exitNodeIds | length' "$WORK/state/ent/state.json" 2>/dev/null || echo 0)
  [ "$ids" = 2 ] && ok two_exits "the entry serves the bridge for two exits (ent's persisted state lists both)" || bad two_exits "the entry's bridge serves $ids exit(s)"
}

# ---------------------------------------------------------------- 5. long sessions, big files, 50 parallel, accounting, occupancy, pool

used_bytes() { api UserService/GetUser "{\"userId\":\"$USER_ID\"}" | jq -r '(.user.usedBytes // 0) | tonumber'; }
mode_events() { api FleetService/ListEvents '{"limit":200}' | jq '[.events[]? | select(.code == "relay_mode_changed")] | length'; }

# sampler <name> <relay id>...: once in 2 s the busiest the bridge of each relay was, until $WORK/sample.stop exists
sampler() {
  local out=$1 r
  shift
  rm -f "$WORK/sample.stop"
  : >"$out"
  while [ ! -e "$WORK/sample.stop" ]; do
    for r in "$@"; do
      echo "$r $(relay_get "$r" '[.bridge.clients // 0, .bridge.pool // 0, .bridge.conns // 0, (.bps // 0 | tonumber)] | @tsv')" >>"$out"
    done
    sleep 2
  done
}
run_doctor() { api HealthService/RunDoctor "$(jq -nc --arg n "${NODE_ID[$1]}" '{nodeId:$n}')" >/dev/null || say "RunDoctor of $1 failed"; } # <node name>: a report with the relay counters now
max_of() { awk -v r="$2" -v c="$3" '$1 == r && $c + 0 > m {m = $c + 0} END {print m + 0}' "$1"; } # <file> <relay> <column>

scn_load() {
  setup_reverse
  local c p d every pid
  SESS=() SESSNAMES=()
  declare -A PRT=([vision]=$S_VISION [xhttp]=$S_XHTTP)
  for c in vision xhttp; do
    p=${PRT[$c]}
    for d in $D30 $D2M $D10M; do
      every=5s
      [ "$d" != "$D2M" ] || every=10s
      [ "$d" != "$D10M" ] || every=25s # a quiet session: a line every 25 s, nothing travels downwards in between
      (via "$p" echo -secs "$d" -every "$every" >"$WORK/sess-$c-$d.out" 2>&1; echo "exit $?" >>"$WORK/sess-$c-$d.out") &
      SESS+=($!) SESSNAMES+=("$c-$d")
    done
  done
  (via "$S_VISION" idle -secs "$IDLE_SECS" >"$WORK/idle.out" 2>&1) &
  IDLE_PID=$!
  say "sessions of ${D30}s, ${D2M}s and ${D10M}s started on both relays; transfers run beside them"
  sleep 3
  local seen=0 i
  for ((i = 0; i < 15; i++)); do
    seen=$(($(relay_get "$R_VISION" '.bridge.clients // 0') + $(relay_get "$R_XHTTP" '.bridge.clients // 0')))
    [ "$seen" -ge 6 ] && break
    sleep 2
  done
  if [ "$seen" -ge 6 ]; then ok occupancy "the entry counts the people it carries now: $seen with six sessions open (clients_now, not the cumulative streams)"
  else bad occupancy "with six sessions open the relays report $seen people now"; fi

  # --- accounting: a quiet moment first, so the baseline holds every earlier byte
  sleep 25
  local u0 u1 sent
  u0=$(used_bytes)
  (sampler "$WORK/sample.tsv" "$R_VISION" "$R_XHTTP") &
  SAMPLER=$!

  # --- 50 sessions held open at once: the entry must count 50 people (and 6 more) at the same time
  (via "$S_VISION" hold -n 50 -secs 25 -every 4s >"$WORK/hold.out" 2>&1; echo "exit $?" >>"$WORK/hold.out") &
  HOLD_PID=$!

  # --- big files, sha256 both ways. The relay's byte counter rides on the entry's doctor report (every ten minutes otherwise), and the
  # rate is the difference of two reports: one is asked for now, one after the transfers.
  run_doctor ent
  sent=0
  for c in vision xhttp; do
    p=${PRT[$c]}
    via_ok "$c" "$p" "$BIGLABEL down" get -n "$BIG" -seed "$(seed)" && sent=$((sent + BIG))
    via_ok "$c" "$p" "$BIGLABEL up" put -n "$BIG" -seed "$(seed)" && sent=$((sent + BIG))
  done

  wait "$HOLD_PID" || true
  if head -n 1 "$WORK/hold.out" | grep -q '^ok hold' && tail -n 1 "$WORK/hold.out" | grep -q '^exit 0'; then ok vision "50 sessions open at once, each with data both ways: $(head -n 1 "$WORK/hold.out")"
  else bad vision "50 parallel sessions: $(head -n 1 "$WORK/hold.out")"; fi
  # --- 50 parallel connections on each relay (a get or a put of PARB bytes, PARR times each)
  local parb=2000000 parr=4
  [ -z "$QUICK" ] || parb=500000
  for c in vision xhttp; do
    p=${PRT[$c]}
    if out=$(via "$p" par -n 50 -bytes "$parb" -rounds "$parr" -seed "$((9000 + RANDOM % 1000))" 2>&1); then
      ok "$c" "50 parallel connections, every transfer sha256-checked: $out"
      sent=$((sent + 50 * parr * parb))
    else bad "$c" "50 parallel connections: $out"; sent=$((sent + 50 * parr * parb / 2)); fi
  done

  # --- accounting
  local delta ok_acc
  sleep 25
  u1=$(used_bytes)
  delta=$((u1 - u0))
  ok_acc=$(awk -v d="$delta" -v s="$sent" 'BEGIN {r = (s > 0) ? d / s : 0; printf "%.4f", r; exit !(r >= 0.99 && r <= 1.01)}') && acc=ok || acc=bad
  if [ "$acc" = ok ]; then ok accounting "the person's traffic is counted once: panel counted $delta bytes for $sent transferred, ratio $ok_acc (limit 0.99..1.01; counted at the exit only)"
  else bad accounting "the panel counted $delta bytes for $sent transferred, ratio $ok_acc (want 0.99..1.01; a double count would be about 2)"; fi

  # --- occupancy and the pool at the load
  local mc pool
  mc=$(max_of "$WORK/sample.tsv" "$R_VISION" 2)
  if [ "$mc" -ge 50 ]; then ok occupancy "with 50 sessions held open the relay reported up to $mc people at once (clients_now of the entry)"
  else bad occupancy "with 50 sessions held open the relay reported at most $mc people now"; fi
  run_doctor ent
  local bps=0
  for ((i = 0; i < 30; i++)); do
    pool=$(relay_get "$R_VISION" '.bridge.pool // 0')
    bps=$(relay_get "$R_VISION" '(.bps // 0 | tonumber)')
    [ "$pool" != 4 ] || break
    sleep 3
  done
  if [ "$pool" = 4 ]; then ok pool "the pool of the busy relay went from 2 to 4 TLS connections (the counter said $bps bit/s between two reports, the limit is 10 Mbit/s)"
  else bad pool "the pool of the busy relay stayed at $pool (the counter said $bps bit/s)"; fi
  local conns=0
  touch "$WORK/sample.stop"
  wait "$SAMPLER" 2>/dev/null || true
  for ((i = 0; i < 20; i++)); do
    conns=$(relay_get "$R_VISION" '.bridge.conns // 0')
    [ "$conns" -ge 4 ] && break
    sleep 3
  done
  if [ "$conns" -ge 4 ]; then ok pool "the exit really holds $conns TLS connections for the busy relay (the pool of 4 was asked for)"
  else bad pool "the pool of the busy relay is 4 but the exit holds $conns TLS connections a minute later"; fi

  # --- the sessions
  local k
  for k in "${!SESS[@]}"; do
    pid=${SESS[$k]}
    wait "$pid" || true
    local f="$WORK/sess-${SESSNAMES[$k]}.out" cell=${SESSNAMES[$k]%%-*} secs=${SESSNAMES[$k]#*-}
    if head -n 1 "$f" | grep -q '^ok echo' && tail -n 1 "$f" | grep -q '^exit 0'; then ok "$cell" "a session of ${secs}s with data in both directions stayed up: $(head -n 1 "$f")"
    else bad "$cell" "the session of ${secs}s broke: $(head -n 1 "$f")"; fi
  done
  wait "$IDLE_PID" || true
  if grep -q '^closed' "$WORK/idle.out"; then
    rec NOTE "$SCN" idle "a session quiet for 2 minutes is closed by the bridge (as the plan says: $(cat "$WORK/idle.out")); apps with keep-alive reconnect, DNAT would hold it until the conntrack timeout"
  else rec NOTE "$SCN" idle "a quiet session of ${IDLE_SECS}s: $(cat "$WORK/idle.out")"; fi
}

# ---------------------------------------------------------------- 6. the exit's agent dies and comes back; one bridge connection is reset

scn_bridge_kill() {
  setup_reverse
  local before_events before_changed t_kill t_up i out loop_pid recovered=""
  before_events=$(mode_events)
  before_changed=$(relay_get "$R_VISION" '.modeChangedUnix')
  rm -f "$WORK/kill.log" "$WORK/kill.stop"
  (
    while [ ! -e "$WORK/kill.stop" ]; do
      for c in "$S_VISION:v" "$S_XHTTP:x"; do
        if via_try "${c%%:*}" get -n 200000 -seed 31; then echo "$(date +%s) ${c##*:} ok"; else echo "$(date +%s) ${c##*:} fail"; fi
      done
      sleep 1
    done
  ) >>"$WORK/kill.log" &
  loop_pid=$!
  sleep 8
  t_kill=$(date +%s)
  say "killing the exit's agent (PID ${NODE_PID[ext]}); the bridge, the inbounds and every session on them go"
  kill_node ext
  : >"$WORK/modes.log"
  for ((i = 0; i < 6; i++)); do echo "$(relay_mode "$R_VISION") $(relay_reason "$R_VISION") $(relay_mode "$R_XHTTP")" >>"$WORK/modes.log"; sleep 4; done
  restart_node ext
  t_up=$(date +%s)
  say "the exit's agent is started again"
  for ((i = 0; i < 90; i++)); do
    if relay_bridge_up "$R_VISION" && relay_bridge_up "$R_XHTTP" && via_try "$S_VISION" get -n 200000 -seed 32 && via_try "$S_XHTTP" get -n 200000 -seed 33; then
      recovered=$(($(date +%s) - t_up)); break
    fi
    echo "$(relay_mode "$R_VISION") $(relay_reason "$R_VISION") $(relay_mode "$R_XHTTP")" >>"$WORK/modes.log"
    sleep 3
  done
  sleep 10
  touch "$WORK/kill.stop"
  wait "$loop_pid" 2>/dev/null || true
  local failed okn
  failed=$(awk -v k="$t_kill" '$1 >= k && $3 == "fail"' "$WORK/kill.log" | wc -l)
  okn=$(awk -v k="$t_up" '$1 >= k && $3 == "ok"' "$WORK/kill.log" | wc -l)
  if [ -n "$recovered" ]; then ok bridge_kill "after the exit's agent was killed and started again the bridge was up and both clients worked again in $recovered s ($failed failed requests during the outage, $okn good ones after the start)"
  else bad bridge_kill "the clients did not recover within 4.5 minutes after the exit's agent was started again: $(relay_summary "$R_VISION" | cut -c1-300)"; fi
  if ! grep -qv '^reverse leg_failed reverse$' "$WORK/modes.log"; then ok bridge_kill "the controller did not flap: both relays stood in reverse (leg_failed) at every look during the outage and the recovery"
  else bad bridge_kill "the way changed during the outage: $(sort -u "$WORK/modes.log" | tr '\n' ';')"; fi
  if [ "$(mode_events)" = "$before_events" ] && [ "$(relay_get "$R_VISION" '.modeChangedUnix')" = "$before_changed" ]; then
    ok bridge_kill "no relay_mode_changed event and the same time of the last change: the outage was not taken for a mode problem"
  else bad bridge_kill "mode events $before_events -> $(mode_events), changed_unix $before_changed -> $(relay_get "$R_VISION" '.modeChangedUnix')"; fi
  who_is bridge_kill "$S_VISION" "$EXT_IP" "after the restart"
}

scn_bridge_reset() {
  setup_reverse
  local sport conns0 conns1 failed=0 i before_changed
  before_changed=$(relay_get "$R_VISION" '.modeChangedUnix')
  for ((i = 0; i < 20; i++)); do
    [ "$(relay_get "$R_VISION" '.bridge.conns // 0')" -ge 2 ] && break
    sleep 2
  done
  conns0=$(relay_get "$R_VISION" '.bridge.conns // 0')
  sport=$(nsx ext ss -Htn "dst $ENT_IP:443" | awk '{n = split($4, a, ":"); print a[n]}' | head -n 1)
  [ -n "$sport" ] || { rec SKIP "$SCN" vision "no bridge connection of the exit to the entry's 443 was found to reset"; return; }
  nsx flt nft insert rule inet bridgelab forward ip saddr "$EXT_IP" ip daddr "$ENT_IP" tcp sport "$sport" reject with tcp reset
  say "one TLS connection of the bridge (exit port $sport) is reset"
  for ((i = 0; i < 10; i++)); do via_try "$S_VISION" get -n 100000 -seed 41 || failed=$((failed + 1)); via_try "$S_XHTTP" get -n 100000 -seed 42 || failed=$((failed + 1)); sleep 1; done
  flt_apply
  for ((i = 0; i < 60; i++)); do
    conns1=$(relay_get "$R_VISION" '.bridge.conns // 0')
    [ "$conns1" -ge "$conns0" ] && break
    sleep 2
  done
  if [ "$failed" -le 4 ]; then ok reconnect "with one of the bridge's TLS connections reset, 20 new transfers on two relays: $failed failed (the people on the other connection are untouched)"
  else bad reconnect "$failed of 20 transfers failed while one bridge connection was down"; fi
  if [ "$conns1" -ge "$conns0" ]; then ok reconnect "the exit dialed the lost connection again: $conns0 -> $conns1 TLS connections"
  else bad reconnect "the bridge has $conns1 connections, had $conns0"; fi
  if [ "$(relay_get "$R_VISION" '.modeChangedUnix')" = "$before_changed" ]; then ok reconnect "the way did not change"; else bad reconnect "the way changed on a reset of one connection"; fi
  via_ok reconnect "$S_VISION" "after the reconnect" get -n 2000000 -seed "$(seed)"
}

# ---------------------------------------------------------------- 7. the direct way is back: the panel does not hurry

scn_hold_direct() {
  setup_reverse
  flt_mode open "$EXT_IP" # (ext2 keeps its freezing filter: its relay is the check of the direct proof, see --long-return)
  say "the filter is gone; three minutes of looking at the controller"
  sleep 180
  local c rid
  for c in vision:$R_VISION xhttp:$R_XHTTP; do
    rid=${c##*:}
    if relay_is "$rid" reverse && [ "$(relay_get "$rid" '.directStreak // 0')" -le 1 ]; then
      ok "${c%%:*}" "the direct way is alive again, the relay stays in reverse: hold of 30 minutes and 3 samples 30 minutes apart (streak $(relay_get "$rid" '.directStreak // 0') of 3)"
    else bad "${c%%:*}" "the relay left reverse or counts too fast: $(relay_summary "$rid" | cut -c1-300)"; fi
  done
  via_ok vision "$S_VISION" "still carried by the bridge" get -n 2000000 -seed "$(seed)"
  rec NOTE "$SCN" return "the return to DNAT needs 3 direct samples at least 30 minutes apart, a 30 minute stay and an 8 MB flow of the direct way: about 95 minutes of the panel's own clock. The panel has no time seam (the constants of store/relay_controller.go); --long-return waits it out"
}

scn_return_direct() {
  [ -n "$LONG_RETURN" ] || { rec SKIP "$SCN" return "not run (--long-return): the rules take about 95 minutes of real time and the panel has no time seam"; return; }
  setup_reverse
  flt_mode open "$EXT_IP"
  [ -z "${R_FREEZE:-}" ] || flt_mode freeze "$EXT2_IP"
  local i t0 last="" s
  t0=$(date +%s)
  for ((i = 0; i < 170; i++)); do
    relay_is "$R_VISION" dnat && break
    s=$(relay_get "$R_VISION" '.directStreak // 0')
    [ "$s" = "$last" ] || { say "return: streak $s of 3 after $((($(date +%s) - t0) / 60)) min"; last=$s; }
    sleep 60
  done
  if relay_is "$R_VISION" dnat && [ "$(relay_reason "$R_VISION")" = direct_back ]; then
    ok vision "back to DNAT by the rules (3 samples 30 minutes apart, the hold, the 8 MB flow of the direct way), reason direct_back, $((($(date +%s) - t0) / 60)) min after the filter went; the owner clicked nothing"
    via_ok vision "$S_VISION" "DNAT again" get -n 3000000 -seed "$(seed)"
    who_is vision "$S_VISION" "$EXT_IP" "DNAT again"
  else bad vision "after $((($(date +%s) - t0) / 60)) min the relay stands in $(relay_mode "$R_VISION") ($(relay_reason "$R_VISION")): $(relay_summary "$R_VISION" | cut -c1-300)"; fi
  if relay_is "$R_XHTTP" dnat; then ok xhttp "the second relay of the same exit went back as well"; else bad xhttp "the second relay of the same exit stays in $(relay_mode "$R_XHTTP")"; fi
  if [ -n "${R_FREEZE:-}" ]; then
    # the packets of the direct way pass on ext2 (three good samples) but its flows freeze at 16 KB: the panel's own 8 MB proof, to a
    # random port of the agent, must be cut by the filter too, and the relay must stay on the bridge
    for ((i = 0; i < 30; i++)); do
      [ "$(relay_json "$R_FREEZE" | jq -r '.check.directSpeed.status // ""')" != "" ] && break
      sleep 60
    done
    if [ "$(relay_json "$R_FREEZE" | jq -r '.check.directSpeed.status // ""')" = fail ] && relay_is "$R_FREEZE" reverse; then
      ok xhttp "the exit behind the freezing filter: the panel's 8 MB proof of the direct way failed ($(relay_json "$R_FREEZE" | jq -c '.check.directSpeed | {status, code, bytes}')), the relay stays on the bridge although the leg's packets are fine"
    else bad xhttp "the freezing exit: proof $(relay_json "$R_FREEZE" | jq -c '.check.directSpeed'), way $(relay_mode "$R_FREEZE")"; fi
  fi
}

# ---------------------------------------------------------------- 8. UDP servers stay on DNAT and refuse with a reason

scn_udp_refusal() {
  setup_reverse # the TCP leg of ext stays dead (syn) while the UDP one is tried
  local h2 out id rid cand
  h2=$(settings_of hysteria2 | jq -c '.tls_mode = "self_signed" | .port = 4434 | .hop = {from: 0, to: 0} | .obfs.password = "$generate"')
  P_H2X=$(make_profile hysteria2 bridge-hy2-exit "$h2")
  I_H2X=$(make_inbound "$P_H2X" ext)
  wait_for 60 "the Hysteria2 server of the exit" inbound_active ext "$I_H2X" || { bad hy2 "the Hysteria2 server of the exit did not start"; return; }
  out=$(api_err RelayService/CreateRelay "$(jq -nc --arg i "$I_H2X" --arg v "${NODE_ID[ent]}" '{inboundId:$i,viaNodeId:$v,mode:"reverse"}')")
  if grep -q 'reverse_unavailable' <<<"$out" && grep -q 'udp' <<<"$out"; then ok hy2 "an explicit reverse relay of a Hysteria2 server is refused: $(tr -d '\n' <<<"$out" | cut -c1-160)"
  else bad hy2 "CreateRelay(mode reverse) for UDP: $(tr -d '\n' <<<"$out" | cut -c1-200)"; fi
  cand=$(api RelayService/ListRelayCandidates "$(jq -nc --arg v "${NODE_ID[ent]}" '{viaNodeId:$v}')" | jq -c --arg i "$I_H2X" '.candidates[] | select(.inboundId == $i) | {reverseBlocked, hint: .exitReachesEntry.verdict}')
  if jq -e '.reverseBlocked == "udp"' <<<"$cand" >/dev/null 2>&1; then ok hy2 "the candidate list says reverse_blocked = udp and shows no 'bridge possible' hint ($cand)"
  else bad hy2 "the candidate of the UDP server: $cand"; fi
  rid=$(add_relay "$I_H2X" ent auto) || { bad hy2 "CreateRelay(auto) for the Hysteria2 server failed"; return; }
  if [ "$(relay_get "$rid" '.reverseBlocked')" = udp ]; then ok hy2 "the relay shows reverse_blocked = udp"; else bad hy2 "reverse_blocked of the UDP relay: '$(relay_get "$rid" '.reverseBlocked')'"; fi
  out=$(retry 3 check_relay "$rid") || true
  if jq -e '.relay.check.leg.status == "ok"' <<<"$out" >/dev/null 2>&1; then ok hy2 "with UDP open the relay's leg passes and the relay is a DNAT relay"; else bad hy2 "leg with UDP open: $(jq -c '.relay.check.leg' <<<"$out" 2>/dev/null | cut -c1-200)"; fi
  retry 3 enable_relay "$rid" >/dev/null && ok hy2 "switched on in DNAT ($(relay_mode "$rid"), reason '$(relay_reason "$rid")')" || bad hy2 "switch-on of the UDP relay failed"
  flt_mode "syn udp" "$EXT_IP"
  out=$(retry 2 check_relay "$rid") || true
  if jq -e '.relay.check.leg.status == "fail"' <<<"$out" >/dev/null 2>&1; then
    ok hy2 "UDP towards the exit is dropped: the check fails at the leg ($(jq -r '.relay.check.leg.code' <<<"$out"), verdict $(jq -r '.relay.check.leg.verdict' <<<"$out"))"
  else bad hy2 "check with UDP dropped: $(jq -c '.relay.check | {leg, reverseReach}' <<<"$out" 2>/dev/null | cut -c1-300)"; fi
  # the same server through the other entry, while the leg is dead: the switch-on is refused at the leg, whatever reverse_reach found
  local rid2 on
  rid2=$(add_relay "$I_H2X" ent2 auto) || { bad hy2 "CreateRelay via ent2 failed"; rid2=""; }
  if [ -n "$rid2" ]; then
    retry 2 check_relay "$rid2" >/dev/null || true
    on=$(api_err RelayService/UpdateRelay "$(jq -nc --arg i "$rid2" '{id:$i,enabled:true}')")
    if grep -q 'relay_check_failed' <<<"$on" && grep -q 'stage=leg' <<<"$on"; then ok hy2 "switching on a UDP relay with a dead leg is refused at the leg: $(tr -d '\n' <<<"$on" | cut -c1-160)"
    else bad hy2 "switch-on of a UDP relay with a dead leg: $(tr -d '\n' <<<"$on" | cut -c1-220)"; fi
  fi
  on=$(api_err RelayService/UpdateRelay "$(jq -nc --arg i "$rid" '{id:$i,mode:"reverse"}')")
  if grep -q 'reverse_unavailable' <<<"$on"; then ok hy2 "UpdateRelay(mode reverse) on the UDP relay: $(tr -d '\n' <<<"$on" | cut -c1-140)"; else bad hy2 "UpdateRelay(mode reverse): $(tr -d '\n' <<<"$on" | cut -c1-200)"; fi
  local seen="" i
  for ((i = 0; i < 100; i++)); do
    if api HealthService/ListAlerts '{}' | jq -e '[.active[]? | select((.kind | test("RELAY_DOWN")) and .nodeName == "ent")] | length > 0' >/dev/null; then seen=1; break; fi
    sleep 5
  done
  if [ -n "$seen" ]; then
    ok hy2 "the panel says so: a relay_down alert for the UDP relay ($(api HealthService/ListAlerts '{}' | jq -c '[.active[]? | select(.kind | test("RELAY_DOWN")) | {why: .whyKey}][0]'))"
  else rec NOTE "$SCN" hy2 "no relay_down alert within 8 minutes for the UDP relay with a dead leg: $(relay_summary "$rid" | cut -c1-300)"; fi
  if [ "$(relay_mode "$rid")" = dnat ] && [ "$(relay_reason "$rid")" = udp ]; then ok hy2 "the controller keeps the UDP relay in DNAT, reason 'udp' (no bridge for UDP in this version)"
  else bad hy2 "the UDP relay stands in $(relay_mode "$rid") reason '$(relay_reason "$rid")'"; fi
  flt_mode syn "$EXT_IP"
  rec SKIP "$SCN" hy2-warp "not checked: the WARP egress needs a registration with Cloudflare (no internet in the stand); the relay code does not look at the egress, so the row of Hysteria2 is the same code path"
  rec SKIP "$SCN" awg31 "not checked here: AmneziaWG relays are the AWG plan's (own keys, nft counters); a UDP server takes the same refusals as Hysteria2 above (reverse_blocked = udp, reverse_unavailable)"
  rec SKIP "$SCN" awg20 "not checked here, same as awg31"
}

# ---------------------------------------------------------------- 9. a server behind a CDN is not relayed at all

scn_cdn_refusal() {
  local s p i out
  s=$(settings_of vless | jq -c '.port = 8445 | .transport = "xhttp" | .security = "tls" | .tls_mode = "self_signed" | .sni = "origin.edge.test" | .cdn = {host: "cdn.edge.test", port: 8445}')
  p=$(make_profile vless bridge-vless-cdn "$s")
  i=$(make_inbound "$p" ext)
  wait_for 60 "the CDN server of the exit" inbound_active ext "$i" || { bad vless-cdn "the server behind a CDN did not start"; return; }
  out=$(api_err RelayService/CreateRelay "$(jq -nc --arg i "$i" --arg v "${NODE_ID[ent]}" '{inboundId:$i,viaNodeId:$v}')")
  if grep -q 'relay_unsupported' <<<"$out" && grep -q 'cdn' <<<"$out"; then ok vless-cdn "a relay of a server behind a CDN is refused with its reason: $(tr -d '\n' <<<"$out" | cut -c1-140)"
  else bad vless-cdn "CreateRelay for a CDN server: $(tr -d '\n' <<<"$out" | cut -c1-200)"; fi
  if api RelayService/ListRelayCandidates "$(jq -nc --arg v "${NODE_ID[ent]}" '{viaNodeId:$v}')" | jq -e --arg i "$i" '.candidates[] | select(.inboundId == $i and .unsupported == "cdn")' >/dev/null; then
    ok vless-cdn "the candidate list marks it unsupported: cdn"
  else bad vless-cdn "the candidate list does not mark the CDN server"; fi
  rec SKIP "$SCN" reality-self "not checked: 'this node's site' needs a domain and a Let's Encrypt certificate (the agent has no ACME test seam and the stand has no internet); the bridge point on an inbound's TLS site is checked with Hysteria2's self-signed site (443 of ent), the unix-socket site of REALITY self by the unit tests of R2 only"
  rec SKIP "$SCN" vless-tls "not checked: VLESS TLS without a CDN needs an ACME certificate (self-signed is allowed only behind a CDN, and a CDN is refused by relays); the entry's side is the same as for the site of Hysteria2 (443 of ent), the exit's side as for REALITY"
}

# ---------------------------------------------------------------- 10. a stranger on the bridge's port sees an ordinary site

http_sig() { # <curl args...>: status, the headers that show a different program, and the body's sha256 of one request
  local h b
  h=$(mktemp -p "$WORK") b=$(mktemp -p "$WORK")
  nsx cli curl -sk -m 15 -o "$b" -D "$h" "$@" >/dev/null 2>&1 || true
  printf '%s %s %s\n' "$(head -n 1 "$h" | tr -d '\r')" "$(grep -i -E '^(content-type|content-length|server):' "$h" | tr -d '\r' | tr 'A-Z' 'a-z' | sort | tr '\n' ' ')" "$(sha256sum <"$b" | cut -c1-16)"
  rm -f "$h" "$b"
}
scn_active_probe() {
  setup_reverse
  local path rand a b c d e
  path=$(jq -r '.bridgeServe.path // ""' "$WORK/state/ent/state.json")
  [ -n "$path" ] || { bad probe "the entry's state holds no bridge path"; return; }
  rand=$(tr -dc 'A-Za-z0-9_-' </dev/urandom | head -c 43)
  a=$(http_sig "https://$ENT_IP/")
  b=$(http_sig "https://$ENT_IP/.well-known/mistgate/bridge/$rand")
  c=$(http_sig "https://$ENT_IP$path")
  d=$(http_sig -X POST "https://$ENT_IP/.well-known/mistgate/bridge/$rand" -H 'X-Mistgate-Bridge-Version: 2' -H 'X-Mistgate-Bridge-Stream: down' -H 'X-Mistgate-Bridge-Pair-ID: 00000000000000000000000000000000' -H 'X-Mistgate-Bridge-Certificate: AAAA' -H 'X-Mistgate-Bridge-Signature: AAAA')
  e=$(http_sig -X POST "https://$ENT_IP$path" -H 'X-Mistgate-Bridge-Version: 2' -H 'X-Mistgate-Bridge-Stream: down' -H 'X-Mistgate-Bridge-Pair-ID: 00000000000000000000000000000000' -H 'X-Mistgate-Bridge-Certificate: AAAA' -H 'X-Mistgate-Bridge-Signature: AAAA')
  say "site /: $a"
  if [ "$c" = "$b" ]; then ok probe "curl without a signature on the secret bridge path gets the same answer as on any other path of the site: $c"
  else bad probe "the bridge path answers differently from a random path: '$c' vs '$b'"; fi
  if [ "$e" = "$d" ]; then ok probe "a POST with forged bridge headers (bad certificate and signature) gets what the same POST gets on a random path: $e"
  else bad probe "forged bridge headers on the bridge path: '$e' vs '$d'"; fi
  if grep -q ' 200 \| 404 \| 403 ' <<<"$a $b" || grep -q 'HTTP/' <<<"$a"; then ok probe "the entry's 443 shows an ordinary site (a TLS site of Hysteria2 with the decoy page)"; else bad probe "no answer from the entry's 443: $a"; fi
  # ent2: the bridge site of the agent's own, on a port of its own
  local p2 path2
  p2=$(api NodeService/GetNode "{\"nodeId\":\"${NODE_ID[ent2]}\"}" | jq -r '.node.bridgePort // 0')
  path2=$(jq -r '.bridgeServe.path // ""' "$WORK/state/ent2/state.json")
  if [ "$p2" != 0 ] && [ -n "$path2" ]; then
    a=$(http_sig "https://$ENT2_IP:$p2/.well-known/mistgate/bridge/$rand")
    b=$(http_sig "https://$ENT2_IP:$p2$path2")
    c=$(http_sig -X POST "https://$ENT2_IP:$p2$path2" -H 'X-Mistgate-Bridge-Version: 2' -H 'X-Mistgate-Bridge-Stream: up' -H 'X-Mistgate-Bridge-Pair-ID: 00000000000000000000000000000000' -H 'X-Mistgate-Bridge-Certificate: AAAA' -H 'X-Mistgate-Bridge-Signature: AAAA')
    d=$(http_sig -X POST "https://$ENT2_IP:$p2/.well-known/mistgate/bridge/$rand" -H 'X-Mistgate-Bridge-Version: 2' -H 'X-Mistgate-Bridge-Stream: up' -H 'X-Mistgate-Bridge-Pair-ID: 00000000000000000000000000000000' -H 'X-Mistgate-Bridge-Certificate: AAAA' -H 'X-Mistgate-Bridge-Signature: AAAA')
    if [ "$a" = "$b" ] && [ "$c" = "$d" ]; then ok probe "the agent's own bridge site (port $p2 of ent2): the secret path and forged headers look like any other request: $a"
    else bad probe "ent2's site: GET '$b' vs '$a'; POST '$c' vs '$d'"; fi
  fi
  scn_pin_mismatch
}

# an impostor on the entry's address: the exit dials 443 of the entry and gets another certificate; it must stop at the handshake
scn_pin_mismatch() {
  local imp=$FAR:9443 i
  spawn_in net "$WORK/impostor.log" "$BIN/bridgetool" impostor -listen "$imp" -log "$WORK/impostor.seen"
  need 10 "the impostor" listening net 9443
  nsx flt nft -f - <<EOF
table ip bridgeimp
delete table ip bridgeimp
table ip bridgeimp {
  chain pre { type nat hook prerouting priority -100; ip saddr $EXT_IP ip daddr $ENT_IP tcp dport 443 dnat to $imp; }
  chain post { type nat hook postrouting priority 100; ip daddr $FAR tcp dport 9443 masquerade; }
}
EOF
  say "the exit's calls to $ENT_IP:443 now land on an impostor with another certificate; the exit's agent is restarted so that it dials again"
  kill_node ext
  : >"$WORK/impostor.seen"
  restart_node ext
  local err="" conns
  for ((i = 0; i < 60; i++)); do
    err=$(relay_get "$R_VISION" '.bridge.error // ""')
    conns=$(relay_get "$R_VISION" '.bridge.conns // 0')
    [ "$err" = pin_mismatch ] && [ "$conns" = 0 ] && break # the entry's report of the connections the exit had before is up to 90 s old
    sleep 3
  done
  if [ "$err" = pin_mismatch ] && [ "$conns" = 0 ]; then ok probe "a foreign certificate on the entry's address is refused by the exit: bridge error pin_mismatch, 0 connections"
  else bad probe "the exit with a foreign certificate in front: error '$err', $conns connection(s)"; fi
  if [ -s "$WORK/impostor.seen" ] && ! grep -q '^request' "$WORK/impostor.seen"; then
    ok probe "the impostor saw $(grep -c '^handshake' "$WORK/impostor.seen") handshake(s) and no request: no path, no certificate, no signature of the exit reached it"
  elif [ ! -s "$WORK/impostor.seen" ]; then bad probe "the impostor saw nothing (the redirect did not catch the exit's dial)"
  else bad probe "the impostor received a request: $(grep '^request' "$WORK/impostor.seen" | head -n 2)"; fi
  nsx flt nft delete table ip bridgeimp
  for ((i = 0; i < 40; i++)); do relay_bridge_up "$R_VISION" && break; sleep 3; done
  relay_bridge_up "$R_VISION" && ok probe "the redirect removed, the exit dials the real entry again and the bridge is up" || bad probe "the bridge did not come back after the redirect was removed"
  via_ok probe "$S_VISION" "through the bridge again" get -n 2000000 -seed "$(seed)"
}

# ---------------------------------------------------------------- 10b. MTProto (Fake TLS): what the panel can prove about it without Telegram

# The part of the matrix the stand can reach for a Telegram proxy is the part before the bytes: the checks of the relay, the refusals
# and their reasons. The bytes themselves are not reachable here, and the reason is worth knowing (see the SKIP row): a relay is switched
# on only after a proof that the path carries what the server is (a TLS target, checked from a third node), and the lab has neither the
# certificate the node's own site wants (ACME) nor a site the node may forward to that the proof can name.
scn_mtproto() {
  setup_reverse # the direct way to ext is cut (syn)
  local s rid out i
  # (1) the node's own site as the fallback (the usual MTProto server): the leg has a TLS target, so the dead direct way is seen
  s=$(settings_of mtproto | jq -c '.port = 8446')
  P_MT=$(make_profile mtproto bridge-mtproto "$s")
  I_MT=$(api ProfileService/CreateInbound "$(jq -nc --arg p "$P_MT" --arg n "${NODE_ID[ext]}" --arg s "$MTNAME" '{profileId:$p,nodeId:$n,tlsServerNameOverride:$s}')" | jq -r .inbound.id)
  wait_for 60 "the MTProto server of the exit" inbound_active ext "$I_MT" || { bad mtproto "the MTProto server did not start: $(grep -i mtproto "$WORK/node-ext.log" | tail -n 2 | cut -c1-250)"; return; }
  rid=$(add_relay "$I_MT" ent auto) || { bad mtproto "CreateRelay for the MTProto server failed"; return; }
  out=$(retry 4 check_relay "$rid") || true
  if jq -e '.relay.check.leg.status == "fail" and .relay.check.reverseReach.status == "ok"' <<<"$out" >/dev/null 2>&1; then
    ok mtproto "own-site MTProto server, the direct way dead: leg fail ($(jq -r .relay.check.leg.code <<<"$out")), reverse_reach ok (the exit reaches the entry's bridge site)"
  else bad mtproto "CheckRelay of the own-site MTProto relay: $(jq -c '.relay.check | {leg, reverseReach, path}' <<<"$out" 2>/dev/null | cut -c1-400)"; fi
  out=$(api_err RelayService/UpdateRelay "$(jq -nc --arg i "$rid" '{id:$i,enabled:true}')")
  if grep -q 'relay_check_failed' <<<"$out" && grep -q 'stage=path' <<<"$out"; then
    rec SKIP "$SCN" mtproto-bytes "not checked: a client of the Telegram proxy through the entry and the bridge to the exit's listener. The switch-on of the MTProto relay is refused at the path proof ($(tr -d '\n' <<<"$out" | cut -c1-110)): the proof wants the node's site to show a certificate it can verify (a public ACME one, or a pin the node reports), and the lab's node can only make a self-signed one for the name; there is no ACME in the stand and no seam in the agent to give it one. The bridge itself is protocol-blind (the exit only dials a TCP port): what it carries for VLESS in this run is what it would carry here"
  else bad mtproto "switch-on of the own-site MTProto relay: $(tr -d '\n' <<<"$out" | cut -c1-220)"; fi
  # (2) a server that shows another site to strangers ("site" fallback) has no target the panel can verify, so it cannot use the bridge: the refusal is clean
  s=$(settings_of mtproto | jq -c --arg d "$MTNAME" '.port = 8447 | .fake_tls = {kind: "site", domain: $d}')
  local p2 i2 r2
  p2=$(make_profile mtproto bridge-mtproto-site "$s")
  i2=$(make_inbound "$p2" ext)
  wait_for 60 "the MTProto server (site) of the exit" inbound_active ext "$i2" || { bad mtproto "the MTProto server with a site did not start"; return; }
  r2=$(add_relay "$i2" ent reverse) || { bad mtproto "CreateRelay(reverse) of the MTProto server with a site failed"; return; }
  retry 3 check_relay "$r2" >/dev/null || true
  out=$(api_err RelayService/UpdateRelay "$(jq -nc --arg i "$r2" '{id:$i,enabled:true}')")
  if grep -q 'relay_check_failed' <<<"$out" && grep -q 'code=unverifiable' <<<"$out"; then
    ok mtproto "a MTProto server that shows a site to strangers cannot be put on the bridge, and the refusal says why: $(tr -d '\n' <<<"$out" | cut -c1-160)"
  else bad mtproto "switch-on of the site-fallback MTProto relay in reverse: $(tr -d '\n' <<<"$out" | cut -c1-220)"; fi
  rec NOTE "$SCN" mtproto "a MTProto relay in mode auto whose server shows a site has leg and path 'unverifiable' (no TLS target): nothing the controller reads can tell it that the direct way is dead, and the bridge cannot be switched on for it (above); such a relay stays DNAT, whatever the filter does"
  for i in $r2 $rid; do api RelayService/DeleteRelay "$(jq -nc --arg i "$i" '{id:$i}')" >/dev/null 2>&1 || true; done
}

# ---------------------------------------------------------------- 12. the exit's control session through the entry (step R4, plan section 6)

# What the exit sends towards the panel is cut, and the exit's agent must reach the panel through the bridge of an entry node (the
# entry dials the panel's own address from its own installation and carries the session, which the panel authenticates through it).
# ctl_block <node> <drop|reject|freeze>: drop = the packets vanish; reject = a refusal at once; freeze = a flow towards the panel
# lives for 6000 bytes and then stops (the filter that lets a handshake through and freezes the flow).
ctl_block() {
  local n=$1 mode=$2 out in
  case $mode in
    drop) out="ip daddr $PANEL_IP drop"; in="" ;;
    reject) out="ip daddr $PANEL_IP reject with tcp reset"; in="" ;;
    freeze) out="ip daddr $PANEL_IP ct bytes > 6000 drop"; in="ip saddr $PANEL_IP ct bytes > 6000 drop" ;;
  esac
  nsx "$n" nft -f - <<EOF
table ip ctlblock
delete table ip ctlblock
table ip ctlblock {
  counter panel_syn {}
  chain out {
    type filter hook output priority 0; policy accept;
    ip daddr $PANEL_IP tcp flags & (syn | ack) == syn counter name panel_syn
    $out
  }
  chain in {
    type filter hook input priority 0; policy accept;
    $in
  }
}
EOF
  if [ "$mode" = freeze ]; then # the byte counts of a flow exist only once the namespace has conntrack (the rules above load it) and accounting is on
    nsx "$n" sysctl -qw net.netfilter.nf_conntrack_acct=1 || say "ctl: conntrack accounting could not be switched on in $n"
    say "ctl: conntrack accounting in $n: $(nsx "$n" sysctl -n net.netfilter.nf_conntrack_acct 2>&1)"
  fi
  say "ctl: what $n sends towards the panel is $mode"
}
ctl_unblock() { nsx "$1" nft delete table ip ctlblock >/dev/null 2>&1 || true; say "ctl: the way of $1 to the panel is open"; }
ctl_syns() { nsx "$1" nft list counter ip ctlblock panel_syn 2>/dev/null | awk '/packets/ {print $2}'; } # SYN packets towards the panel since the block (retransmits included)
node_diag() { api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[$1]}" '{nodeId:$n}')" | jq -c --argjson now "$(date +%s)" '.node | {status, reason, lastSeenAgo: ($now - ((.lastSeenUnix // 0) | tonumber)), via: .linkViaNodeId, online}' 2>&1 | cut -c1-300; }
node_via() { api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[$1]}" '{nodeId:$n}')" | jq -r '.node.linkViaNodeId // ""'; }
# a node is connected when its agent's statistics reached the panel in the last 45 s (the node's status can say DOWN or NO_TRAFFIC for
# reasons of its own: an inbound waiting for a certificate that needs the internet, no traffic yet)
node_connected() { api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[$1]}" '{nodeId:$n}')" | jq -e --argjson now "$(date +%s)" '$now - (.node.lastSeenUnix | tonumber) < 45' >/dev/null; }
# on the bridge of an entry: the agent takes any entry whose bridge is up (ext2 has relays through both entries)
node_on_bridge() { local v; node_connected "$1" && v=$(node_via "$1") && { [ "$v" = "${NODE_ID[ent]}" ] || [ "$v" = "${NODE_ID[ent2]}" ]; }; }
node_direct() { node_connected "$1" && [ -z "$(node_via "$1")" ]; }
log_count() { grep -c "$2" "$WORK/node-$1.log" 2>/dev/null || true; } # <node> <code>: how many times the agent logged it
timed() { # <seconds> <what> <command...>: wait_for, and TOOK is how long it took
  local t=$(date +%s) rc=0
  wait_for "$@" || rc=$?
  TOOK=$(($(date +%s) - t))
  return $rc
}

scn_ctl() {
  setup_reverse
  local before took n polls badpolls out
  # --- (c) the direct way freezes after the handshake: sessions die young, and after three of them the agent goes to the bridge
  before=$(log_count ext agent_panel_via_bridge)
  ctl_block ext freeze
  kill_node ext # new flows: the bytes of a flow are counted from its first packet
  restart_node ext
  if timed 480 "the exit's session through the bridge, after a freeze of the direct way" node_on_bridge ext; then
    ok ctl_freeze "the direct way froze after the handshake; the agent went to the bridge of the entry by itself and is online through it ($TOOK s; the node's link_via is the entry)"
  else bad ctl_freeze "after $TOOK s the exit is not on the bridge: connected=$(node_connected ext && echo yes || echo no) via='$(node_via ext)'"; fi
  [ "$(log_count ext agent_panel_via_bridge)" -gt "$before" ] && ok ctl_freeze "the agent logged agent_panel_via_bridge" || bad ctl_freeze "no agent_panel_via_bridge in the agent's log"

  # --- (a)+(i) the panel is not reachable directly at all, and the agent restarts: it must come back through the bridge
  ctl_block ext drop
  kill_node ext
  restart_node ext
  if timed 240 "the restarted exit through the bridge" node_on_bridge ext; then
    if [ "$TOOK" -le 120 ]; then ok ctl_restart "the exit's agent restarted with the panel cut off directly and was back through the entry's bridge in $TOOK s (plan: about 60-70 s: three direct tries, then the bridge)"
    else bad ctl_restart "back through the bridge only after $TOOK s (plan: about 60-70 s)"; fi
  else bad ctl_restart "the restarted exit did not come back through the bridge in $TOOK s"; fi
  via_ok ctl_restart "$S_VISION" "the relay of the exit works while the exit's control session runs through the entry" get -n 3000000 -seed "$(seed)"
  out=$(nsx ext ss -Htn "dst $PANEL_IP" | wc -l)
  [ "$out" = 0 ] && ok ctl_restart "no TCP connection of the exit to the panel exists: the control session is on the bridge only" || bad ctl_restart "the exit holds $out TCP connection(s) to the panel directly"

  # --- (g) a second exit behind the same entry, also cut off from the panel
  flt_mode syn "$EXT2_IP"
  if [ -z "${R_FREEZE:-}" ]; then
    R_FREEZE=$(add_relay "$I_XHTTP2" ent auto) || { bad ctl_two "CreateRelay of the second exit failed"; return; }
    retry 4 check_relay "$R_FREEZE" >/dev/null || true
    retry 4 enable_relay "$R_FREEZE" >/dev/null || true
  fi
  relay_is "$R_FREEZE" reverse || wait_mode "$R_FREEZE" reverse 900 || { bad ctl_two "the relay of the second exit did not reach reverse"; return; }
  wait_bridge "$R_FREEZE" 90 || bad ctl_two "the second exit holds no bridge connection"
  ctl_block ext2 drop
  kill_node ext2
  restart_node ext2
  if timed 240 "the second exit through the bridge" node_on_bridge ext2 && node_on_bridge ext; then
    ok ctl_two "two exits behind one entry, both cut off from the panel: both online through the entry's bridge at once ($TOOK s for the second), each with its own control session"
  else bad ctl_two "after $TOOK s: ext on the bridge $(node_on_bridge ext && echo yes || echo no), ext2 $(node_on_bridge ext2 && echo yes || echo no); ext: $(node_diag ext); ext2: $(node_diag ext2); the entry is $(node_diag ent)"; fi

  # --- (h) the people and the control session at the same time: 50 people held open and 100 MB (a minute of the busiest hour)
  local via_before
  via_before=$(($(log_count ext agent_panel_via_bridge) + $(log_count ext2 agent_panel_via_bridge)))
  (via "$S_VISION" hold -n 50 -secs 25 -every 4s >"$WORK/ctl-hold.out" 2>&1; echo "exit $?" >>"$WORK/ctl-hold.out") &
  local hold_pid=$! sent=0
  polls=0 badpolls=0
  rm -f "$WORK/ctl-poll.stop"
  (
    while [ ! -e "$WORK/ctl-poll.stop" ]; do
      if node_on_bridge ext && node_on_bridge ext2; then echo ok; else echo bad; fi
      sleep 3
    done
  ) >"$WORK/ctl-poll.out" 2>&1 &
  local poll_pid=$!
  via_ok ctl_people "$S_VISION" "100 MB down while 50 people are held open and the control session is on the bridge" get -n 100000000 -seed "$(seed)"
  via_ok ctl_people "$S_VISION" "100 MB up" put -n 100000000 -seed "$(seed)"
  wait "$hold_pid" || true
  touch "$WORK/ctl-poll.stop"
  wait "$poll_pid" 2>/dev/null || true
  polls=$(wc -l <"$WORK/ctl-poll.out") badpolls=$(grep -c bad "$WORK/ctl-poll.out" || true)
  if head -n 1 "$WORK/ctl-hold.out" | grep -q '^ok hold' && [ "$badpolls" = 0 ]; then
    ok ctl_people "50 people held open and 200 MB moved, and both exits stayed online through the bridge at all $polls looks (the people do not push the control session out)"
  else bad ctl_people "hold: $(head -n 1 "$WORK/ctl-hold.out"); looks with an exit off the bridge: $badpolls of $polls"; fi
  [ "$(($(log_count ext agent_panel_via_bridge) + $(log_count ext2 agent_panel_via_bridge)))" = "$via_before" ] \
    && ok ctl_people "no new switch to the bridge in the agents' logs during the load" || bad ctl_people "an agent went to the bridge again during the load"
  rec NOTE "$SCN" ctl_people "the eight connections of people to one exit (the whole budget of the people's class) are not filled here: the stand holds 2 to 4 TLS connections of people per exit; the separate budgets are the unit tests of the bridge package (TestControlLinkAndPeopleKeepSeparateConnectionBudgets)"

  # --- (d) the bridge falls under the control session: the exit has the direct way to the panel closed, so it is off until the bridge is back, and does not storm
  nsx ext nft -f - <<EOF
table ip ctlbridge
delete table ip ctlbridge
table ip ctlbridge {
  chain out { type filter hook output priority 0; policy accept; ip daddr $ENT_IP tcp dport 443 drop; }
}
EOF
  local syn0 syn1 t_cut
  syn0=$(ctl_syns ext) t_cut=$(date +%s)
  say "ctl: the bridge of ext (its connections to the entry) is cut; the direct way to the panel is cut as well"
  sleep 120
  syn1=$(ctl_syns ext)
  if [ "$(node_connected ext && echo yes || echo no)" = no ]; then ok ctl_bridge_down "the control session fell with the bridge: the exit is off in the panel's eyes (as expected: both ways are closed)"
  else rec NOTE "$SCN" ctl_bridge_down "the exit still shows online 2 minutes after both ways were cut"; fi
  if [ $((syn1 - syn0)) -le 60 ]; then ok ctl_bridge_down "no storm towards the panel: $((syn1 - syn0)) SYN packets in 2 minutes with both ways closed (retransmits included; a loop of one attempt a second would give over 120)"
  else bad ctl_bridge_down "$((syn1 - syn0)) SYN packets towards the panel in 2 minutes with both ways closed"; fi
  nsx ext nft delete table ip ctlbridge
  if timed 300 "the exit back through the bridge" node_on_bridge ext; then ok ctl_bridge_down "the bridge is back: the exit is online through it again ($TOOK s after the bridge was opened)"
  else bad ctl_bridge_down "the exit did not come back through the restored bridge in $TOOK s"; fi

  # --- (e)+(f) the panel is down for a minute, and then up again, with both exits on the bridge
  local t_down t_up lost0 lost1
  lost0=$(log_count ext panel_connection_lost)
  say "ctl: the panel is stopped for 60 s"
  stop_pid "$PANEL_PID"
  t_down=$(date +%s)
  sleep 60
  lost1=$(log_count ext panel_connection_lost)
  panel_serve
  need 45 "the panel listener" nsx pan curl -skS -m 3 -o /dev/null "$PUBLIC/"
  t_up=$(date +%s)
  local t_ext t_ext2
  timed 180 "ext back through the bridge after the panel returned" node_on_bridge ext && t_ext=$(($(date +%s) - t_up)) || t_ext=-1
  timed 180 "ext2 back through the bridge after the panel returned" node_on_bridge ext2 && t_ext2=$(($(date +%s) - t_up)) || t_ext2=-1
  if [ "$t_ext" -ge 0 ] && [ "$t_ext2" -ge 0 ] && [ "$t_ext" -le 90 ] && [ "$t_ext2" -le 90 ]; then
    ok ctl_panel "after a stop of the panel for $((t_up - t_down)) s both exits are online through the entry's bridge again: ext $t_ext s, ext2 $t_ext2 s after the panel returned (their pause had grown towards the 60 s ceiling; a deploy does not cost the minute of a direct retry)"
  else bad ctl_panel "after the panel returned: ext back after $t_ext s, ext2 after $t_ext2 s (-1: not back in 180 s; want within 90 s)"; fi
  if [ $((lost1 - lost0)) -le 20 ]; then ok ctl_panel "the exit did not hammer a panel that was down for both ways: $((lost1 - lost0)) lost-connection lines of its agent in 60 s (the pause grows from 1 s towards 60 s)"
  else bad ctl_panel "$((lost1 - lost0)) lost-connection lines of the exit's agent in 60 s"; fi
  node_connected ent && node_connected ent2 && ok ctl_panel "the entries (direct) are online as well" || bad ctl_panel "an entry is not online after the panel's return"

  # --- (l) a long control session on the bridge
  local soak=600 i soakbad=0 via_now
  [ -z "$QUICK" ] || soak=120
  via_now=$(log_count ext agent_panel_via_bridge)
  say "ctl: the control sessions stay on the bridge for $soak s"
  for ((i = 0; i < soak; i += 15)); do
    node_on_bridge ext && node_on_bridge ext2 || soakbad=$((soakbad + 1))
    sleep 15
  done
  if [ "$soakbad" = 0 ] && [ "$(log_count ext agent_panel_via_bridge)" = "$via_now" ]; then
    ok ctl_soak "both control sessions lived on the bridge for $soak s with a look every 15 s and no reconnect in the agents' logs (the stream's idle limit of 2 minutes is kept by the session's pings every 20 s; the byte bucket of 256 KiB/s is not hit by statistics)"
  else bad ctl_soak "$soakbad looks of $((soak / 15)) found an exit off the bridge"; fi
  ctl_unblock ext
  ctl_unblock ext2
  rec SKIP "$SCN" ctl_transport "not checked: an mTLS exit with an entry that has only the link (and the reverse): the stand's agents are all enrolled one way; the entry dials one address of the panel (plan, R4 note 9) and the clean refusal is the unit test of the dial (TestPanelControlLinkIsReplacedAndItsErrorsStayOutOfTheBridgeState and the bridge tests of target_not_allowed)"
  rec SKIP "$SCN" ctl_update "not checked: an update of the exit with the source 'panel' unreachable: the stand has no release source and no internet; the update download is separate from the control session and stays direct by design (R4 note 8)"
  rec SKIP "$SCN" ctl_stats "not checked: a control session with 1000 people online in its statistics: the stand cannot make so many; the bucket (64 MiB, 256 KiB/s) is a unit test of the bridge package"
  [ -n "$LONG_RETURN" ] || rec SKIP "$SCN" ctl_return "not run (--long-return): the return of the exit to the direct way takes two probes 10 minutes apart (16-24 minutes) and has no time seam; a session on the bridge for an hour is the same flag"
}

# the way back: the direct way is open again, and the exit leaves the bridge by its own rules (two direct probes in a row, 10 minutes
# apart with a jitter of 20 percent) without any click
scn_ctl_return() {
  [ -n "$LONG_RETURN" ] || { rec SKIP "$SCN" ctl_return "not run (--long-return): about 25 minutes of the agent's own clock, no time seam"; return; }
  setup_reverse
  ctl_block ext drop
  kill_node ext
  restart_node ext
  timed 240 "ext on the bridge" node_on_bridge ext || { bad ctl_return "the exit did not reach the bridge, cannot start the return test"; return; }
  local back0 i t0
  back0=$(log_count ext agent_panel_direct_back)
  ctl_unblock ext
  t0=$(date +%s)
  for ((i = 0; i < 40 * 60; i += 30)); do
    [ "$(log_count ext agent_panel_direct_back)" -gt "$back0" ] && break
    sleep 30
  done
  if [ "$(log_count ext agent_panel_direct_back)" -gt "$back0" ]; then
    timed 120 "direct" node_direct ext || true
    ok ctl_return "the way is open again: the agent left the bridge by itself $((($(date +%s) - t0) / 60)) min later (agent_panel_direct_back; plan: 16-24 min) and is online directly (link_via empty: $(node_direct ext && echo yes || echo no))"
  else bad ctl_return "40 minutes after the direct way was opened the exit is still on the bridge"; fi
  # a bridge session of an hour: the first control session is not the one that tests this, the agent restarted; here the exit has stayed
  via_ok ctl_return "$S_VISION" "the relay still works" get -n 3000000 -seed "$(seed)"
}

# ---------------------------------------------------------------- 11. what the stand cannot check

scn_matrix_notes() {
  rec SKIP "$SCN" mtproto-telegram "not checked: a real Telegram client and the bytes of a Telegram session (the exit dials the Telegram data centres, which the stand does not have); what is checked of MTProto is in scn_mtproto (the checks of the relay and the refusals), and why the bytes through the bridge are not reachable is in its mtproto-bytes row"
  rec SKIP "$SCN" udp-over-bridge "not in this version by design (plan §2.3, second queue W1)"
  rec SKIP "$SCN" real-clients "not checked: Mihomo and the Telegram client are not in the stand; Xray (the version the agent embeds) is the real client of every VLESS row"
  rec SKIP "$SCN" old-agents "not checked on processes: the stand runs one agent build; an agent without relay-reverse/1 is covered by the controller table and the gate tests of R3"
  rec SKIP "$SCN" ja4 "not checked: the ClientHello of the exit's bridge dial against the uTLS Chrome reference (JA4) needs a capture tool and a reference fingerprint; to be done with tcpdump and a JA4 script on the live pair or as a separate step"
}

# ---------------------------------------------------------------- the run

# ---------------------------------------------------------------- 5b. a TLS connection of the bridge is rotated by volume while a session lives on it

scn_rotation() {
  setup_reverse
  local big=1395864371 # 1.3 GiB: one stream carries a TLS connection of the bridge past its 1 GiB, which makes the exit open a new one
  [ -z "$QUICK" ] || big=1395864371
  (via "$S_VISION" echo -secs 100 -every 5s >"$WORK/rot-echo.out" 2>&1; echo "exit $?" >>"$WORK/rot-echo.out") &
  local echo_pid=$! sampler_pid
  (sampler "$WORK/rot.tsv" "$R_VISION") &
  sampler_pid=$!
  via_ok vision "$S_VISION" "1.3 GiB down on one stream" get -n "$big" -seed "$(seed)"
  via_ok vision "$S_VISION" "1.3 GiB up on one stream" put -n "$big" -seed "$(seed)"
  touch "$WORK/sample.stop"
  wait "$sampler_pid" 2>/dev/null || true
  wait "$echo_pid" || true
  if head -n 1 "$WORK/rot-echo.out" | grep -q '^ok echo' && tail -n 1 "$WORK/rot-echo.out" | grep -q '^exit 0'; then
    ok vision "a session with a line every 5 s lived through the rotation: $(head -n 1 "$WORK/rot-echo.out")"
  else bad vision "the session broke while a bridge connection was rotated: $(head -n 1 "$WORK/rot-echo.out")"; fi
  local peak pool
  peak=$(max_of "$WORK/rot.tsv" "$R_VISION" 4)
  pool=$(max_of "$WORK/rot.tsv" "$R_VISION" 3)
  if [ "$peak" -gt "$pool" ]; then ok vision "the bridge showed $peak TLS connections for a pool of $pool: the connection that passed 1 GiB was replaced while it served on"
  else rec NOTE "$SCN" vision "no moment with more connections than the pool ($peak of $pool) was sampled; the rotation may have been between two samples"; fi
  [ -z "$(relay_get "$R_VISION" '.bridge.error // ""')" ] && ok vision "no bridge error after the rotation" || bad vision "bridge error '$(relay_get "$R_VISION" '.bridge.error')' after the rotation"
}

# ---------------------------------------------------------------- 5c. a burst of new people over a channel with a round trip of 100 ms

scn_burst() {
  setup_reverse
  # the bridge's path between the entries and the exits: 50 ms each way
  nsx flt tc qdisc replace dev bga2 root netem delay 50ms
  nsx flt tc qdisc replace dev bgb1 root netem delay 50ms
  sleep 1
  local out ok_n
  out=$(via "$S_VISION" par -n 150 -bytes 20000 -rounds 1 -seed 5000 2>&1) && ok_n=ok || ok_n=bad
  if [ "$ok_n" = ok ]; then ok vision "150 new people at once over a bridge with a round trip of 100 ms, all transfers sha256-checked: $out"
  else bad vision "a burst of 150 new connections over a round trip of 100 ms: $out"; fi
  out=$(via "$S_XHTTP" par -n 150 -bytes 20000 -rounds 1 -seed 6000 2>&1) && ok_n=ok || ok_n=bad
  if [ "$ok_n" = ok ]; then ok xhttp "150 new people at once over a bridge with a round trip of 100 ms: $out"
  else bad xhttp "a burst of 150 new connections over a round trip of 100 ms: $out"; fi
  nsx flt tc qdisc del dev bga2 root || true
  nsx flt tc qdisc del dev bgb1 root || true
}

# scenarios, then what the run does
[ -z "${MG_BRIDGE_EXP:-}" ] || { . "$MG_BRIDGE_EXP"; exit 0; } # development: run a scratch file on the stand and stop
SCENARIOS=(auto_reverse gate_port freeze two_exits load rotation burst bridge_kill bridge_reset mtproto ctl udp_refusal cdn_refusal active_probe matrix_notes hold_direct return_direct ctl_return)
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
FINISHED=1
