#!/usr/bin/env bash
# The own probes of the country check, end to end: the real panel binary, the real probe script (internal/panel/ownprobe/
# mistgate-probe.sh, installed with the command the panel prints), real curl, busybox ash when it is installed (dash if not),
# real node agents with real Xray, and a "TSPU" router made of nft, all in network namespaces.
#
#   MSYS_NO_PATHCONV=1 wsl -d Ubuntu -u root -- bash scripts/e2e/probe.sh [--keep] [--term-test] [--stop-after STAGE] [--hold]
#
# Needs: go, curl, jq, python3, openssl, nft, ip, mount, mountpoint, unshare, setsid, ss, nsenter, sed. Runs as root, but
# ONLY inside a private network and mount namespace (`unshare -n -m`): the host's interfaces, routes, nft tables and Docker
# are neither visible nor touched. Everything is a documentation address (RFC 5737: 192.0.2.0/24, 198.51.100.0/24,
# 203.0.113.0/24) or private (10.78.0.0/16); a guard drops what goes beyond. Nothing talks to a real host.
#
# TOPOLOGY (a veth pair on every link; link N is a /30, the left end is .1):
#
#   panel -- r1 -- r2 -- tspu -- r3 -- de1 / nl1 / de2 / de3      home is behind r1
#
#   home    10.78.13.2 (network A, AS64496), later 10.78.113.2 (network B, AS64497): the ASN table the panel gets (a signed
#           bundle with a throwaway release key) maps 10.78.13.0/24 and 10.78.113.0/24 to them
#   r3      198.51.100.10 the control (TLS, never blocked), 198.51.100.66 the blocked reference (the TSPU drops SYNs to it),
#           198.51.100.100:8443 the REALITY target of the nodes (a page of 100 KiB, so that a "fetch" has something to read)
#   nodes   de1 203.0.113.11  SYNs from the home are dropped            -> tcp_timeout
#           nl1 203.0.113.12  RST after the ClientHello                  -> tls_reset
#           de2 203.0.113.13  the reply stops after 16 KB                -> freeze
#           de3 203.0.113.14  open                                       -> reach
#
# The probe is installed with the command the panel creates (opkg is a stand-in; the download directory is the lab's), in the
# home namespace, under MGP_ROOT=<lab>/home (the test root of the script: every path of the install is under it; a real install
# never sets it). Its date is shifted by a file, to run cycles the 30 minute rule would otherwise space out; its sleep is real.
# The worker runs as nobody when the lab can (MGP_DROP=1 lets the script drop privileges under a test root).
#
# SCENARIOS: install by the issued command; first results; the points of de1, nl1, de2, de3 and their reasons; a reference that
# opens (no_filter); a cycle with a root that is read-only; the network changing (network_changed) and confirmed; the revocation
# (the site of the decoy answers, the probe goes silent, its points go); the script's stop and uninstall. --term-test starts the
# whole lab, holds it, sends SIGTERM and checks that no process, directory or namespace is left.
# shellcheck disable=SC2016,SC2034,SC2317,SC2015
# (the scenario checks are shell text run by eval, where the variables are read; the cleanup runs from a trap)
set -Eeuo pipefail
SELF=$(readlink -f "$0")
ROOT=$(cd "$(dirname "$SELF")/../.." && pwd)
ARGS=("$@")
KEEP="" TERM_TEST="" STOP_AFTER="" HOLD=""
while [ $# -gt 0 ]; do
  case $1 in
    --keep) KEEP=1 ;;
    --term-test) TERM_TEST=1 ;;
    --stop-after) STOP_AFTER=${2:?--stop-after needs a stage}; shift ;;
    --hold) HOLD=1 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

if [ "$(uname -s)" != Linux ] || [ "$(id -u)" != 0 ]; then
  if ! command -v wsl.exe >/dev/null 2>&1; then
    echo "scripts/e2e/probe.sh needs root on Linux (or WSL on Windows: wsl.exe was not found)" >&2
    exit 2
  fi
  distro=${WSL_DISTRO:-Ubuntu}
  here=$(pwd -W 2>/dev/null || pwd)
  repo=$(MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -u root wslpath -a "$here" | tr -d '\r')
  MSYS_NO_PATHCONV=1 exec wsl.exe -d "$distro" -u root -- bash "$repo/scripts/e2e/probe.sh" "${ARGS[@]}"
fi
for tool in go curl jq python3 openssl nft ip mount mountpoint unshare setsid ss nsenter sed; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done

# ================================================================ outside: build and prepare, then enter the namespaces
if [ "${MG_PROBE_INNER:-}" != 1 ]; then
  if [ -n "$TERM_TEST" ]; then
    # The cleanup after SIGTERM: the whole lab runs and is held, SIGTERM goes to it, and nothing may be left of it.
    mark=$(mktemp /tmp/mg-probe-term.XXXXXX)
    MG_PROBE_WORKFILE=$mark bash "$SELF" --hold --stop-after install &
    pid=$!
    for ((i = 0; i < 1500; i++)); do
      [ -s "$mark" ] && [ -e "$(cat "$mark")/held" ] && break
      kill -0 "$pid" 2>/dev/null || { echo "FAIL  the lab ended before it was held" >&2; rm -f "$mark"; exit 1; }
      sleep 1
    done
    work=$(cat "$mark")
    [ -e "$work/held" ] || { kill -KILL "$pid" 2>/dev/null; rm -f "$mark"; echo "FAIL  the lab did not start in 25 minutes" >&2; exit 1; }
    marked=0
    for p in /proc/[0-9]*; do { tr '\0' '\n' <"$p/environ" | grep -qx "MG_PROBE_WORK=$work"; } 2>/dev/null && marked=$((marked + 1)); done
    echo "[term-test] the lab is up ($marked processes of it); sending SIGTERM"
    kill -TERM "$pid"
    for ((i = 0; i < 90; i++)); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
    kill -0 "$pid" 2>/dev/null && { echo "FAIL  the lab did not end within 90 s of SIGTERM"; kill -KILL "$pid"; rm -f "$mark"; exit 1; }
    wait "$pid" 2>/dev/null || true
    sleep 1
    left=0
    for p in /proc/[0-9]*; do
      if { tr '\0' '\n' <"$p/environ" | grep -qx "MG_PROBE_WORK=$work"; } 2>/dev/null; then echo "FAIL  a process is left: ${p#/proc/} $(tr '\0' ' ' <"$p/cmdline" 2>/dev/null | cut -c1-80)"; left=1; fi
    done
    [ ! -e "$work" ] || { echo "FAIL  the work directory is left: $work"; left=1; }
    compgen -G '/run/netns/mgprobe-*' >/dev/null && { echo "FAIL  a namespace is left"; left=1; }
    rm -f "$mark"
    [ "$left" = 0 ] && { echo "PASS  after SIGTERM no process, directory or namespace is left"; exit 0; }
    exit 1
  fi
  WORK=$(mktemp -d /tmp/mg-probe.XXXXXX)
  chmod 755 "$WORK"
  [ -z "${MG_PROBE_WORKFILE:-}" ] || printf '%s\n' "$WORK" >"$MG_PROBE_WORKFILE"
  mkdir -p "$WORK/bin" "$WORK/node"
  trap 'rc=$?; case "$WORK" in /tmp/mg-probe.*) rm -rf -- "$WORK";; esac; exit "$rc"' EXIT
  export GOTOOLCHAIN=${GOTOOLCHAIN:-auto} CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOPROXY=off
  cd "$ROOT"
  echo "[probe] building (module downloads are off)"
  go build -trimpath -o "$WORK/bin/probeseed" ./scripts/e2e/probeseed
  go build -trimpath -o "$WORK/bin/signbundle" ./scripts/e2e/signbundle
  go build -trimpath -o "$WORK/bin/tlsdest" ./scripts/e2e/tlsdest
  KEY_PUB=$("$WORK/bin/probeseed" -keygen "$WORK/release.key")
  BI=github.com/mistgate/mistgate/internal/buildinfo
  go build -trimpath -tags censorsim -ldflags "-X $BI.Version=0.0.0-probe-lab -X $BI.Built=1780000000 -X $BI.ReleaseKey=$KEY_PUB" -o "$WORK/bin/mistgate" ./cmd/mistgate
  go build -trimpath -o "$WORK/bin/mistgate-node" ./cmd/mistgate-node
  # the ASN table of the lab and the signed bundle that carries it into the panel
  mkdir -p "$WORK/asn"
  "$WORK/bin/probeseed" -out "$WORK/asn/mistgate-asndb-data-any" -net 10.78.13.0/24=64496=RU=ExampleNet -net 10.78.113.0/24=64497=RU=OtherNet
  "$WORK/bin/signbundle" --key "$WORK/release.key" --version 0.0.0-probe-lab --built "$(( $(date +%s) - 3600 ))" --out "$WORK/dist" "$WORK/asn/mistgate-asndb-data-any" >/dev/null
  chmod 755 "$WORK/bin" "$WORK/dist"
  exec env MG_PROBE_INNER=1 MG_PROBE_WORK="$WORK" unshare -n -m -- bash "$SELF" "${ARGS[@]}"
fi

# ================================================================ inside the private namespaces
WORK=${MG_PROBE_WORK:?missing work directory}
export MG_PROBE_WORK
BIN=$WORK/bin
DATA=$WORK/data
T0=$(date +%s)
PIDS=() CREATED_NS=() RESULTS=() FAILED=0 COOKIE="" TOTP_SECRET="" OWNER_STEP=-1 OWNER_STEPUP_UNTIL=0 STAGE=start
DE1=203.0.113.11 NL1=203.0.113.12 DE2=203.0.113.13 DE3=203.0.113.14
HOME_A=10.78.13.2 HOME_B=10.78.113.2
CONTROL=198.51.100.10 BLOCKED=198.51.100.66 DEST=198.51.100.100
ADMIN=http://127.0.0.1:8081 PUBLIC=https://10.78.10.1:8443
HOME_ROOT=$WORK/home STATE=$WORK/home/tmp/mistgate-probe PROBE_BIN=$WORK/home/opt/sbin/mistgate-probe
declare -A NODE_ID=() NODE_PID=()

say() { printf '[%3ds] %s\n' "$(( $(date +%s) - T0 ))" "$*"; }
die() {
  say "FAIL $*"; RESULTS+=("FAIL  $*")
  if [ -n "$COOKIE" ] && [ "$STAGE" != start ]; then # what the panel thinks, for whoever reads the log
    { echo "---- own probes:"; own Get; echo; echo "---- points: $(node_point de1) / $(node_point nl1) / $(node_point de2) / $(node_point de3)"; echo "---- probe log:"; tail -n 8 "$WORK/home-logger.log"; echo "---- probe state:"; ls "$STATE"; cat "$STATE/resp" 2>/dev/null; echo "---- de1 checks:"; api HealthService/GetChecks "$(jq -nc --arg id "${NODE_ID[de1]:-}" '{nodeId:$id}')" | head -c 1500; echo; echo "---- panel log:"; tail -n 12 "$WORK/panel.log"; } >&2 2>/dev/null || true
  fi
  exit 1
}
pass() { say "PASS $*"; RESULTS+=("PASS  $*"); }
check() { # <name> <shell expression>: a scenario line of the table; the expression runs in a subshell with the lab's functions
  local name=$1
  if (set +e; eval "$2") >"$WORK/check.out" 2>&1; then pass "$name"
  else RESULTS+=("FAIL  $name"); say "FAIL $name"; sed 's/^/        /' "$WORK/check.out" | head -n 8; FAILED=1; fi
}
alive() { local state; state=$(ps -o stat= -p "${1:-0}" 2>/dev/null) && [ -n "$state" ] && [ "${state:0:1}" != Z ]; }
stop_pid() {
  local pid=$1 i
  alive "$pid" || { wait "$pid" 2>/dev/null || true; return 0; }
  kill -TERM "$pid" 2>/dev/null || true
  for ((i = 0; i < 30; i++)); do alive "$pid" || break; sleep 0.1; done
  alive "$pid" && kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
cleanup() {
  local rc=$? i ns p
  trap - EXIT INT TERM
  set +e
  # the probe stops by its own stop (the pid files); the agents and the rest by their pids
  if [ -x "$PROBE_BIN" ] && [ -d "$HOME_ROOT" ]; then
    ip netns exec mgprobe-home env MGP_ROOT="$HOME_ROOT" PATH="$HOME_ROOT/bin:$PATH" "${STAND_SH[@]}" "$PROBE_BIN" stop >/dev/null 2>&1
  fi
  for ((i = ${#PIDS[@]} - 1; i >= 0; i--)); do stop_pid "${PIDS[$i]}"; done
  # whatever else of this run is still alive (children of the agents, the probe's processes): marked by the run's environment
  for p in /proc/[0-9]*; do
    [ "${p#/proc/}" = "$$" ] && continue
    { tr '\0' '\n' <"$p/environ" | grep -qx "MG_PROBE_WORK=$WORK" && kill -KILL "${p#/proc/}"; } 2>/dev/null
  done
  for ((i = ${#CREATED_NS[@]} - 1; i >= 0; i--)); do ip netns del "${CREATED_NS[$i]}" 2>/dev/null; done
  if [ -n "$KEEP" ]; then say "kept $WORK"; else case "$WORK" in /tmp/mg-probe.*) rm -rf -- "$WORK" ;; esac; fi
  printf '\n'
  printf '%s\n' "${RESULTS[@]}"
  if [ "$rc" = 0 ] && [ "$FAILED" = 0 ]; then say "ALL PASS"; else say "FAILED (exit $rc, stage $STAGE)"; [ "$rc" != 0 ] || rc=1; fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' INT TERM

NS_NAMES=(mgprobe-panel mgprobe-home mgprobe-r1 mgprobe-r2 mgprobe-tspu mgprobe-r3 mgprobe-de1 mgprobe-nl1 mgprobe-de2 mgprobe-de3)
for ns in "${NS_NAMES[@]}"; do
  if [ -e "/run/netns/$ns" ]; then die "namespace $ns already exists; it was not touched"; fi
done
# namespace bind mounts stay inside this mount namespace
mount --make-rprivate /run
mkdir -p /run/netns
if ! mountpoint -q /run/netns; then mount -t tmpfs -o mode=755 tmpfs /run/netns; fi
mount --make-rprivate /run/netns
for ns in "${NS_NAMES[@]}"; do
  ip netns add "$ns"
  CREATED_NS+=("$ns")
  ip -n "$ns" link set lo up
done
for r in mgprobe-r1 mgprobe-r2 mgprobe-tspu mgprobe-r3; do ip netns exec "$r" sysctl -qw net.ipv4.ip_forward=1; done

SEQ=0
link() { # <namespace-a> <namespace-b> <left /30 network>: a gets .1, b gets .2
  local a=$1 b=$2 net=$3 left right
  SEQ=$((SEQ + 1))
  left="pv${SEQ}a" right="pv${SEQ}b"
  ip link add "$left" type veth peer name "$right"
  ip link set "$left" netns "$a"
  ip link set "$right" netns "$b"
  ip -n "$a" addr add "$net.1/30" dev "$left"
  ip -n "$b" addr add "$net.2/30" dev "$right"
  ip -n "$a" link set "$left" up
  ip -n "$b" link set "$right" up
}
link mgprobe-panel mgprobe-r1 10.78.10
link mgprobe-r1 mgprobe-home 10.78.13
link mgprobe-r1 mgprobe-r2 10.78.2
link mgprobe-r2 mgprobe-tspu 10.78.3
link mgprobe-tspu mgprobe-r3 10.78.4
link mgprobe-r3 mgprobe-de1 10.78.21
link mgprobe-r3 mgprobe-nl1 10.78.22
link mgprobe-r3 mgprobe-de2 10.78.23
link mgprobe-r3 mgprobe-de3 10.78.24
ip -n mgprobe-panel route add default via 10.78.10.2
ip -n mgprobe-home route add default via 10.78.13.1
ip -n mgprobe-home addr add "$HOME_B/32" dev lo
ip -n mgprobe-r1 route add "$HOME_B/32" via 10.78.13.2
ip -n mgprobe-r1 route add default via 10.78.2.2
LEFT=(10.78.10.0/30 10.78.13.0/30 "$HOME_B/32")
for net in "${LEFT[@]}"; do ip -n mgprobe-r2 route add "$net" via 10.78.2.1; done
ip -n mgprobe-r2 route add default via 10.78.3.2
for net in "${LEFT[@]}" 10.78.2.0/30; do ip -n mgprobe-tspu route add "$net" via 10.78.3.1; done
ip -n mgprobe-tspu route add default via 10.78.4.2
for net in "${LEFT[@]}" 10.78.2.0/30 10.78.3.0/30; do ip -n mgprobe-r3 route add "$net" via 10.78.4.1; done
declare -A NODE_ADDR=([de1]=$DE1 [nl1]=$NL1 [de2]=$DE2 [de3]=$DE3) NODE_LINK=([de1]=21 [nl1]=22 [de2]=23 [de3]=24)
for n in de1 nl1 de2 de3; do
  ip -n "mgprobe-$n" route add default via "10.78.${NODE_LINK[$n]}.1"
  ip -n "mgprobe-$n" addr add "${NODE_ADDR[$n]}/32" dev lo
  ip -n mgprobe-r3 route add "${NODE_ADDR[$n]}/32" via "10.78.${NODE_LINK[$n]}.2"
done
for a in $CONTROL $BLOCKED $DEST; do ip -n mgprobe-r3 addr add "$a/32" dev lo; done

# The guard: whatever the lab sends beyond the private and documentation ranges is dropped at the "TSPU". Then the filter.
ip netns exec mgprobe-tspu nft -f - <<NFT
table inet probe_guard {
  chain pre {
    type filter hook prerouting priority -400; policy accept;
    ip daddr != { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24 } drop
  }
}
table inet probe_tspu {
  chain forward {
    type filter hook forward priority -10; policy accept;
    ip saddr { $HOME_A, $HOME_B } ip daddr $DE1 tcp flags & (syn | ack) == syn drop
    ip saddr { $HOME_A, $HOME_B } ip daddr $NL1 tcp dport 8443 meta length > 100 tcp flags & psh == psh reject with tcp reset
    ip saddr $DE2 ip daddr { $HOME_A, $HOME_B } tcp sport 8443 ct reply bytes > 16000 drop
  }
}
NFT
nft_tspu() { ip netns exec mgprobe-tspu nft "$@"; }
reference_blocked() {
  nft_tspu -f - <<NFT
table inet probe_ref {
  chain forward {
    type filter hook forward priority -10; policy accept;
    ip saddr { $HOME_A, $HOME_B } ip daddr $BLOCKED tcp flags & (syn | ack) == syn drop
  }
}
NFT
}
reference_open() { nft_tspu delete table inet probe_ref; }
reference_blocked

spawn_in() { # <namespace> <logfile> <command...>; only this returned PID is stopped at teardown
  local ns=$1 log=$2
  shift 2
  ip netns exec "$ns" setsid "$@" >>"$log" 2>&1 </dev/null &
  LAST_PID=$!
  PIDS+=("$LAST_PID")
}
inpanel() { ip netns exec mgprobe-panel "$@"; }
wait_for() { local n=$1 what=$2 i; shift 2; for ((i = 0; i < n; i++)); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done; die "timeout waiting for $what"; }

# ---------------------------------------------------------------- the far side: the control, the blocked reference, the REALITY target
cat >"$WORK/hold.py" <<'PY'
import socket, sys, threading
def hold(host, port):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind((host, port)); s.listen(32)
    while True:
        c, _ = s.accept(); c.close()
threading.Thread(target=hold, args=(sys.argv[1], int(sys.argv[2])), daemon=False).start()
PY
spawn_in mgprobe-r3 "$WORK/blocked.log" python3 "$WORK/hold.py" "$BLOCKED" 443
spawn_in mgprobe-r3 "$WORK/control.log" "$BIN/tlsdest" -listen "$CONTROL:443" -names www.example.com -cert "$WORK/control.pem"
spawn_in mgprobe-r3 "$WORK/dest.log" "$BIN/tlsdest" -listen "$DEST:8443" -names www.example.com -cert "$WORK/tlsdest.pem" -page-kib 100
wait_for 20 "the REALITY target certificate" test -s "$WORK/tlsdest.pem"

api() { # <Service/Method> [JSON]: the admin API; a name with a dot is a full service name
  local out code detail body path=$1
  body=${2:-}
  [ -n "$body" ] || body='{}'
  case $path in *.*) ;; *) path=mistgate.admin.v1.$path ;; esac
  out=$(inpanel curl -skS -m 180 -w $'\n%{http_code}' -X POST "$ADMIN/api/$path" \
    -H 'Content-Type: application/json' ${COOKIE:+-H "Cookie: $COOKIE"} -d "$body") || return 1
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [ "$code" != 200 ]; then
    detail=$(jq -r '.message // .code // empty' <<<"$out" 2>/dev/null || true)
    echo "panel API $1 returned HTTP $code${detail:+: $detail}" >&2
    return 1
  fi
  printf '%s' "$out"
}
own() { api "mistgate.ownprobe.v1.OwnProbeService/$1" "${2:-}"; }

# ---------------------------------------------------------------- the panel
STAGE=panel
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=probe-panel \
  -addext subjectAltName=DNS:localhost,IP:127.0.0.1,IP:10.78.10.1 -keyout "$WORK/panel.key" -out "$WORK/panel.crt" 2>/dev/null
SETUP_OUT=$(inpanel "$BIN/mistgate" setup --data-dir "$DATA" --public-url "$PUBLIC" --admin-listen 127.0.0.1:8081)
SETUP_TOKEN=$(sed -n 's|^Setup link: .*/setup#\(.*\)$|\1|p' <<<"$SETUP_OUT")
[ -n "$SETUP_TOKEN" ] || die "panel setup returned no setup token"
cp -r "$WORK/dist" "$DATA/dist"
sleep 3 # the table file is read once it has settled
spawn_in mgprobe-panel "$WORK/panel.log" "$BIN/mistgate" serve --data-dir "$DATA" --listen 10.78.10.1:8443 \
  --admin-listen 127.0.0.1:8081 --tls-cert "$WORK/panel.crt" --tls-key "$WORK/panel.key" --agent-addr 10.78.10.1:8443
wait_for 45 "the panel listener" inpanel curl -skS -m 3 -o /dev/null "$PUBLIC/"
LOGIN=probe-admin PASSWORD=$(openssl rand -hex 16)
BEGIN=$(api AuthService/BeginSetup "$(jq -nc --arg t "$SETUP_TOKEN" --arg l "$LOGIN" --arg p "$PASSWORD" \
  '{setupToken:$t,displayName:"Probe lab",method:"SETUP_METHOD_PASSWORD",login:$l,password:$p}')")
CEREMONY=$(jq -r .ceremonyId <<<"$BEGIN") TOTP_SECRET=$(jq -r .totpSecret <<<"$BEGIN")
totp_code() {
  python3 - "$TOTP_SECRET" <<'PY'
import base64,hashlib,hmac,struct,sys,time
k=base64.b32decode(sys.argv[1].upper()+"="*((8-len(sys.argv[1])%8)%8))
h=hmac.new(k,struct.pack(">Q",int(time.time())//30),hashlib.sha1).digest(); o=h[-1]&15
print("%06d" % ((struct.unpack(">I",h[o:o+4])[0]&0x7fffffff)%1000000))
PY
}
inpanel curl -skS -m 30 -D "$WORK/setup.hdr" -o /dev/null -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/FinishSetup" \
  -H 'Content-Type: application/json' -d "$(jq -nc --arg t "$SETUP_TOKEN" --arg c "$CEREMONY" --arg code "$(totp_code)" \
  '{setupToken:$t,ceremonyId:$c,totpCode:$code}')"
grep -q '^HTTP/[0-9.]* 200' "$WORK/setup.hdr" || die "first-admin setup failed"
COOKIE=$(sed -n 's/^[Ss]et-[Cc]ookie: \(__Host-sid=[^;]*\).*/\1/p' "$WORK/setup.hdr" | tr -d '\r')
[ -n "$COOKIE" ] || die "first-admin setup did not set a session cookie"
ME=$(api AuthService/Me)
jq -e '.admin.id' <<<"$ME" >/dev/null || die "the owner session does not work"
OWNER_STEP=$(( $(date +%s) / 30 ))
OWNER_STEPUP_UNTIL=$(jq -r '.stepUpUntilUnix // 0' <<<"$ME")
say "the panel is up (built from this tree, with the lab's references and its ASN table)"
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

# ---------------------------------------------------------------- the nodes: real agents, one REALITY inbound each
STAGE=nodes
make_node() { # <node-name> <address> <country> <namespace>
  local name=$1 address=$2 country=$3 ns=$4 out line
  out=$(api NodeService/CreateEnrollment "$(jq -nc --arg n "$name" --arg a "$address" --arg c "$country" '{name:$n,address:$a,countryCode:$c}')")
  NODE_ID[$name]=$(jq -r .node.id <<<"$out")
  line=$(jq -r .installCommand <<<"$out" | grep -m1 ' enroll ')
  flag() { sed -n "s/.* --$1 \([^ ]*\).*/\1/p" <<<"$line"; }
  mkdir -p "$WORK/node/$name"
  NODE_SSL_CERT_FILE="$WORK/tlsdest.pem" ip netns exec "$ns" "$BIN/mistgate-node" enroll --panel "$(flag panel)" --sni "$(flag sni)" \
    --ca-sha256 "$(flag ca-sha256)" --token "$(flag token)" --state-dir "$WORK/node/$name" >"$WORK/enroll-$name.log" 2>&1 \
    || die "node enrollment failed for $name"
  spawn_in "$ns" "$WORK/node-$name.log" env NODE_SSL_CERT_FILE="$WORK/tlsdest.pem" bash -c \
    'mkdir -p /etc/systemd/journald.conf.d; mount -t tmpfs tmpfs /etc/sysctl.d; mount -t tmpfs tmpfs /etc/systemd/journald.conf.d; exec "$@"' _ \
    "$BIN/mistgate-node" run --state-dir "$WORK/node/$name"
  NODE_PID[$name]=$LAST_PID
}
for n in de1 nl1 de2 de3; do make_node "$n" "${NODE_ADDR[$n]}" DE "mgprobe-$n"; done
nodes_online() { api NodeService/ListNodes '{}' | jq -e '[.nodes[] | select(.status == "NODE_STATUS_ONLINE")] | length == 4' >/dev/null; }
wait_for 120 "four online node agents" nodes_online
say "real agents online: de1, nl1, de2, de3"
VLESS_SETTINGS=$(api ProfileService/ListProtocols '{}' | jq -c --arg target "$DEST:8443" '.protocols[] | select(.id == "vless") | .defaultSettingsJson | fromjson
  | .port = 8443 | .transport = "tcp" | .security = "reality" | .reality.target_kind = "site"
  | .reality.target = $target | .reality.server_names = ["www.example.com"]')
VLESS_PROFILE=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$VLESS_SETTINGS" '{protocol:"vless",name:"probe-vless",settingsJson:$s}')" | jq -r .profile.id)
for n in de1 nl1 de2 de3; do
  api ProfileService/CreateInbound "$(jq -nc --arg p "$VLESS_PROFILE" --arg n "${NODE_ID[$n]}" '{profileId:$p,nodeId:$n}')" >/dev/null
done
inbounds_active() {
  local n
  for n in de1 nl1 de2 de3; do
    api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[$n]}" '{nodeId:$n}')" \
      | jq -e 'all(.inbounds[]; .state == "INBOUND_STATE_ACTIVE") and (.inbounds | length) >= 1' >/dev/null || return 1
  done
}
wait_for 120 "the VLESS inbounds ACTIVE" inbounds_active
owner_stepup
api CountryReachabilityService/UpdateCountryReachability '{"country":"RU"}' >/dev/null
say "four REALITY inbounds are active; the country is RU"
sleep 7 # the list of targets of the health module is reused for 5 s: the probe must find the nodes and their inbounds in it
[ "$STOP_AFTER" != nodes ] || exit 0

# ---------------------------------------------------------------- the home: a stand-in router with the probe's tools
STAGE=home
if id nobody >/dev/null 2>&1; then DROP=1; else DROP=""; fi
if command -v busybox >/dev/null && busybox ash -c 'exit 0' 2>/dev/null; then STAND_SH=("$(command -v busybox)" ash) SH_NAME="busybox ash"
else STAND_SH=("$(command -v dash || command -v sh)") SH_NAME=$(basename "${STAND_SH[0]}"); fi
say "the probe script runs under: $SH_NAME (worker as nobody: ${DROP:-no})"
mkdir -p "$HOME_ROOT/bin" "$HOME_ROOT/opt/sbin" "$HOME_ROOT/opt/etc/init.d" "$HOME_ROOT/tmp" "$WORK/dl"
chmod -R 755 "$HOME_ROOT"
ln -s "${STAND_SH[0]}" "$HOME_ROOT/bin/sh"
cat >"$HOME_ROOT/bin/opkg" <<'EOT'
#!/bin/sh
echo "opkg $*" >>"$MG_PROBE_WORK/opkg.log"
EOT
cat >"$HOME_ROOT/bin/date" <<'EOT'
#!/bin/sh
case ${1:-} in
  +%s) o=0; [ ! -f "$MG_CLOCK_OFFSET" ] || read -r o <"$MG_CLOCK_OFFSET"; echo $(($(/bin/date +%s) + o)) ;;
  *) exec /bin/date "$@" ;;
esac
EOT
cat >"$HOME_ROOT/bin/logger" <<'EOT'
#!/bin/sh
echo "$*" >>"$MG_PROBE_WORK/home-logger.log"
EOT
chmod 755 "$HOME_ROOT/bin/opkg" "$HOME_ROOT/bin/date" "$HOME_ROOT/bin/logger"
: >"$WORK/home-logger.log"; chmod 666 "$WORK/home-logger.log" # written by the worker, which may be nobody
echo 0 >"$WORK/clock.offset"
inhome() { ip netns exec mgprobe-home env MGP_ROOT="$HOME_ROOT" ${DROP:+MGP_DROP=1} MG_CLOCK_OFFSET="$WORK/clock.offset" CURL_CA_BUNDLE="$WORK/panel.crt" \
  MG_PROBE_WORK="$WORK" PATH="$HOME_ROOT/bin:$PATH" "$@"; }
probe_run() { inhome "${STAND_SH[@]}" "$PROBE_BIN" "$@"; }
advance_clock() { local c; read -r c <"$WORK/clock.offset"; echo $((c + 2400)) >"$WORK/clock.offset"; } # 40 minutes on (kept in a file: cycles run in subshells)
probe_cycle() { # a probe clock 40 minutes on, one cycle: the 30 minute rule of the script is real, the date is shifted
  advance_clock
  probe_run run
}


# ---------------------------------------------------------------- the probe: created in the panel, installed by the issued command
STAGE=install
api UpdateService/RescanBundle '{}' >/dev/null 2>&1 || true # the signed bundle with the ASN table
owner_stepup
own Update '{"settings":{"enabled":true,"intervalMin":30,"fallbackUrl":""}}' >/dev/null
owner_stepup
CREATED=$(own Create '{"name":"alice","city":"Exampleville","kind":"home"}')
PROBE_ID=$(jq -r .probe.id <<<"$CREATED")
TOKEN=$(jq -r .token <<<"$CREATED")
COMMAND=$(jq -r .command <<<"$CREATED")
SCRIPT_SHA=$(jq -r .scriptSha256 <<<"$CREATED")
CONF=$HOME_ROOT/opt/etc/mistgate-probe/conf
probe_json() { own Get | jq -c --arg id "$PROBE_ID" '.probes[] | select(.id == $id)'; }
probe_state() { probe_json | jq -r .state; }
check "the panel's command carries the sha256 of the script it serves" \
  '[ "$SCRIPT_SHA" = "$(sha256sum "$ROOT/internal/panel/ownprobe/mistgate-probe.sh" | cut -d" " -f1)" ]'
case $COMMAND in "opkg update && opkg install curl ca-bundle coreutils-sha256sum && cd /tmp && curl -fsSo mgp.sh "*) ;; *) die "the install command has another shape" ;; esac
INSTALL_OUT=$(inhome "${STAND_SH[@]}" -c "${COMMAND/cd \/tmp/cd $WORK/dl}" 2>&1) || { printf '%s\n' "$INSTALL_OUT" >&2; die "the install command failed"; }
printf '%s\n' "$INSTALL_OUT"
check "install by the issued command prints OK with the id the panel gave" 'case $INSTALL_OUT in *"OK: probe $PROBE_ID registered and running"*) true ;; *) false ;; esac'
check "install printed neither the token nor the secret" '
  secret=$(sed -n "s/^secret=//p" "$CONF"); [ -n "$secret" ]; case $INSTALL_OUT in *"$TOKEN"*|*"$secret"*) false ;; *) true ;; esac'
check "the configuration is private (0600, or 0640 for the group of nobody)" 'case $(stat -c %a "$CONF") in 600|640) true ;; *) false ;; esac'
check "the panel shows the probe active, in the network of its first request, with the version of the script" '
  p=$(probe_json); [ "$(jq -r .state <<<"$p")" = active ] && [ "$(jq -r .operator <<<"$p")" = AS64496 ] && [ "$(jq -r .version <<<"$p")" = "${SCRIPT_SHA:0:12}" ]'
check "the worker runs as nobody (drop of privileges)" '[ -z "$DROP" ] || [ "$(ps -o user= -p "$(cat "$STATE/worker.pid")" | tr -d " ")" = nobody ]'
check "a supervisor and a worker run with the command lines of the script" '
  tr "\0" " " <"/proc/$(cat "$STATE/supervisor.pid")/cmdline" | grep -q "mistgate-probe supervise" &&
  tr "\0" " " <"/proc/$(cat "$STATE/worker.pid")/cmdline" | grep -q "mistgate-probe worker"'
probe_idle() { [ -f "$STATE/synced" ] && [ ! -d "$STATE/lock" ] && [ -f "$STATE/last_exec" ]; }
wait_for 120 "the first cycle of the probe (its first answer carries a job: the nodes are known)" probe_idle
[ "$STOP_AFTER" != install ] || { [ -z "$HOLD" ] || { touch "$WORK/held"; say "held: the lab runs until SIGTERM"; while :; do sleep 1; done; }; exit 0; }

# ---------------------------------------------------------------- the results
STAGE=results
node_point() { # <node> -> "state why" of the probes' point, or "-"
  api HealthService/GetChecks "$(jq -nc --arg id "${NODE_ID[$1]}" '{nodeId:$id}')" \
    | jq -r '[.countryChecks[]?.vantages[]? | select(.source == "probes")] | if length == 0 then "-" else (.[0].state + " " + (.[0].why // "")) end' | sed 's/ $//'
}
points_are() { # four "state why" strings: de1 nl1 de2 de3
  [ "$(node_point de1)" = "$1" ] && [ "$(node_point nl1)" = "$2" ] && [ "$(node_point de2)" = "$3" ] && [ "$(node_point de3)" = "$4" ]
}
check_now() { own CheckNow "$(jq -nc --arg id "$PROBE_ID" '{id:$id}')" >/dev/null; }
check_now
OUT=$(probe_cycle) || die "a cycle failed: $OUT"
say "cycle: $OUT"
wait_for 30 "the points of the four nodes" points_are "dark tcp_timeout" "dark tls_reset" "dark freeze" "reach"
GOT="de1=$(node_point de1) nl1=$(node_point nl1) de2=$(node_point de2) de3=$(node_point de3)"
say "points: $GOT"
check "de1 (SYNs dropped), nl1 (RST after the ClientHello), de2 (freeze after 16 KB), de3 (open): tcp_timeout, tls_reset, freeze, reach" \
  '[ "$GOT" = "de1=dark tcp_timeout nl1=dark tls_reset de2=dark freeze de3=reach" ]'
check "the point names operator, city and probe, and holds no address" '
  v=$(api HealthService/GetChecks "$(jq -nc --arg id "${NODE_ID[de3]}" "{nodeId:\$id}")" | jq -r "[.countryChecks[]?.vantages[]? | select(.source == \"probes\")][0].network")
  echo "network: $v"; [ "$v" = "AS64496 · Exampleville · probe alice" ]'
check "the panel counts the probe's checks of the day" '[ "$(probe_json | jq -r ".checksDay // 0")" -gt 0 ]'

# a reference that opens: nothing filters on the way, so "reach" proves nothing (no_filter); the dark points stay
reference_open
check_now
OUT=$(probe_cycle) || die "a cycle failed: $OUT"
say "cycle with the reference open: $OUT"
wait_for 30 "de3 to be uncertain (no_filter)" points_are "dark tcp_timeout" "dark tls_reset" "dark freeze" "uncertain no_filter"
check "the reference opens: de3 drops to uncertain no_filter, the dark points stay" '[ "$(node_point de3) / $(node_point de1)" = "uncertain no_filter / dark tcp_timeout" ]'
reference_blocked

# a cycle on a root that is read-only: the probe writes only its state directory (a tmpfs here)
check_now
advance_clock
RO_OUT=$(inhome unshare -m bash -c 'mount --make-rprivate / && mount -o remount,ro,bind / && mount -t tmpfs -o mode=755 tmpfs "$MGP_ROOT/tmp" && exec "$@"' _ \
  "${STAND_SH[@]}" "$PROBE_BIN" run 2>&1) || true
say "read-only root: $RO_OUT"
check "a cycle on a read-only root works" 'case $RO_OUT in "run: http 200, executed 1"*) true ;; *) false ;; esac'

# ---------------------------------------------------------------- the network changes
STAGE=network
ip -n mgprobe-home route replace default via 10.78.13.1 src "$HOME_B"
check_now
OUT=$(probe_cycle) || true
say "from network B: $OUT"
check "another operator: the probe is network_changed and its points are gone" '[ "$(probe_state)" = network_changed ] && [ "$(node_point de1)" = - ] && [ "$(node_point de3)" = - ]'
check "the panel pauses a probe that changed network: nothing was measured" 'case $OUT in *"executed 0"*) true ;; *) false ;; esac'
owner_stepup
own ConfirmNetwork "$(jq -nc --arg id "$PROBE_ID" '{id:$id}')" >/dev/null
check_now
OUT=$(probe_cycle) || true
wait_for 30 "the points of the confirmed network" points_are "dark tcp_timeout" "dark tls_reset" "dark freeze" "reach"
check "after the owner confirms the network the points come back, from operator AS64497" '[ "$(probe_state)" = active ] && [ "$(probe_json | jq -r .operator)" = AS64497 ]'
ip -n mgprobe-home route replace default via 10.78.13.1

# ---------------------------------------------------------------- the revocation
STAGE=revoke
SECRET=$(sed -n 's/^secret=//p' "$CONF")
PREFIX=$(sed -n 's|^url=https://[^/]*/||p' "$CONF")
owner_stepup
own Revoke "$(jq -nc --arg id "$PROBE_ID" '{id:$id}')" >/dev/null
check "revoked: the points of the probe are gone from the nodes at once" '[ "$(node_point de1)" = - ] && [ "$(node_point de2)" = - ] && [ "$(probe_state)" = revoked ]'
SYNC_HTTP=$(inhome curl -sS -m 20 -o "$WORK/revoked.body" -w '%{http_code}' -X POST -H "Authorization: Bearer $SECRET" --data-binary 'mgp 1' "$PUBLIC/$PREFIX/sync") || true
check "a revoked secret gets the answer of the decoy site (404, no protocol in the body)" '[ "$SYNC_HTTP" = 404 ] && ! grep -q "^mgp " "$WORK/revoked.body"'
OUT1=$(probe_cycle) || true
OUT2=$(probe_cycle) || true
OUT3=$(probe_cycle) || true
say "after the revocation: [$OUT1] [$OUT2] [$OUT3]"
check "the probe falls silent: two refusals mute it, the third cycle does not call the panel" '
  case $OUT1 in *"http 404"*) ;; *) false ;; esac; case $OUT2 in *"http 404"*) ;; *) false ;; esac; case $OUT3 in "run: http none"*) true ;; *) false ;; esac'
check "status says so" 'probe_run status | grep -q "silent: the panel denied this probe"'

# ---------------------------------------------------------------- stop and uninstall
STAGE=uninstall
SUP=$(cat "$STATE/supervisor.pid") WRK=$(cat "$STATE/worker.pid")
probe_run stop >/dev/null
sleep 2
check "stop ends the supervisor and the worker" '! kill -0 "$SUP" 2>/dev/null && ! kill -0 "$WRK" 2>/dev/null'
UNINSTALL_OUT=$(probe_run uninstall)
say "$UNINSTALL_OUT"
nothing_left() {
  local p
  for p in opt/sbin/mistgate-probe opt/etc/mistgate-probe opt/etc/init.d/S99mistgate-probe tmp/mistgate-probe; do [ ! -e "$HOME_ROOT/$p" ] || { echo "left: $p"; return 1; }; done
}
check "uninstall leaves nothing: program, configuration, init script and state are gone" nothing_left
log_ok() { [ ! -f "$WORK/home-logger.log" ] || { ! grep -q "$SECRET" "$WORK/home-logger.log" && awk 'length > 80 { exit 1 }' "$WORK/home-logger.log"; }; }
check "the probe logged short lines and no secret" log_ok
exit 0
