#!/usr/bin/env bash
# The real block_diagnosis classifier through a simulated Russian TSPU, in a private network namespace.
#
# Classifier map (sources: internal/panel/mcp/tools_access.go gatherAccess/countryFromProto; tools_access_block.go
# blockCtx.diagnose/addCounterClasses/tspuIndex; tools_access_reasons.go; tools_access_report.go buildAccess):
#   ip_blocked: production TspuProbeService.TestTspuProbe from msk1 plus local Globalping measurements from the two fixed
#     vantages. All observed TCP paths to de1 fail; the class scope is node and confidence must be at least medium.
#   port_blocked: the same stored TSPU ports, with one open and one dropped -> measured by TestTspuProbe.
#   udp_blocked: CountryCheck proves TCP open; path_probe udp measures msk1 loss, and node_ports runs its production paired
#     check from msk1/nl1. The stored sender_verdict keeps msk1's loss distinct from the aggregate ok confirmation by nl1.
#   tcp_throttled: three measured Xray VLESS downloads freeze after the nft byte threshold; kl!ck-style observations are sent
#     through POST <subscription>/<token>/probe and stored by the production subscription endpoint (subs/serveProbe).
#   operator_specific: op-a and op-b make real VLESS TCP connections through different source ranges; their measured verdicts
#     are sent through that same production kl!ck endpoint because the classifier consumes operator-grouped client reports.
#   not_blocked_but_broken: a real node inbound fails to bind because a test listener owns its port; GetNode stores the failed
#     inbound and blockCtx.diagnose attributes it to the node.
#   protocol_fingerprinted: needs protocol-specific lost-answer link counters (Hysteria2 hs_unfinished or AWG
#     responses_unmatched) while a second UDP protocol works. This lab run does not generate those authenticated protocol
#     counters, so it is reported as SKIP and remains covered by the table tests.
#   whitelist_mode: the local Globalping/Bsbord-compatible sources make real TCP attempts from panel/home and op-a/op-b
#     namespaces through the TSPU. The Bsbord adapter also tests a known allowed control address, so its production parser
#     stores a measured `whitelist` mobile vantage; the tagged panel stores all vantages through production source services.
#   regional: requires independent geographically located vantages for the same operator in different regions. Synthetic
#     region labels on local namespaces would not be honest regional evidence, so it remains covered by table tests.
#   unknown: is the result when stored evidence is absent or contradictory rather than a distinct block mechanism; it is
#     not a blocked-path scenario and remains covered by the table tests.
#
# Run from Linux/WSL as root; from Windows it re-executes itself in Ubuntu WSL. Builds happen before `unshare` (GOPROXY=off),
# then the panel, agents, clients, routers and nft rules live only in the private namespace. It uses RFC 1918 and documentation
# addresses only; no host interface, route, nft table, service or container is changed.
set -Eeuo pipefail
SELF=$(readlink -f "$0")
ROOT=$(cd "$(dirname "$SELF")/../.." && pwd)
ARGS=("$@")

if [ "$(uname -s)" != Linux ] || [ "$(id -u)" != 0 ]; then
  if ! command -v wsl.exe >/dev/null 2>&1; then
    echo "scripts/e2e/censor.sh needs root on Linux (or WSL on Windows: wsl.exe was not found)" >&2
    exit 2
  fi
  distro=${WSL_DISTRO:-Ubuntu}
  here=$(pwd -W 2>/dev/null || pwd)
  repo=$(MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -u root wslpath -a "$here" | tr -d '\r')
  MSYS_NO_PATHCONV=1 exec wsl.exe -d "$distro" -u root -- bash "$repo/scripts/e2e/censor.sh" "${ARGS[@]}"
fi

for tool in go curl jq python3 openssl nft ip mount mountpoint unshare setsid ss sed; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done

if [ "${MG_CENSOR_INNER:-}" != 1 ]; then
  WORK=$(mktemp -d /tmp/mg-censor.XXXXXX)
  chmod 700 "$WORK"
  mkdir -p "$WORK/bin" "$WORK/node"
  trap 'rc=$?; case "$WORK" in /tmp/mg-censor.*) rm -rf -- "$WORK";; esac; exit "$rc"' EXIT
  export GOTOOLCHAIN=${GOTOOLCHAIN:-auto} CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOPROXY=off
  say_build() { printf '[censor] %s\n' "$*"; }
  say_build "building the tagged panel, agents and MCP helpers (module downloads disabled)"
  go build -tags censorsim -o "$WORK/bin/mistgate" ./cmd/mistgate
  go build -o "$WORK/bin/mistgate-node" ./cmd/mistgate-node
  go build -o "$WORK/bin/mcpe2e" ./scripts/e2e/mcpe2e
  go build -o "$WORK/bin/mcptoken" ./scripts/e2e/mcptoken
  go build -o "$WORK/bin/tlsdest" ./scripts/e2e/tlsdest
  XRAY_VERSION=$(sed -n 's|^[[:space:]]*github.com/xtls/xray-core \(v[^ ]*\).*|\1|p' "$ROOT/go.mod" | head -n 1)
  [ -n "$XRAY_VERSION" ] || { echo "xray-core is not pinned in go.mod" >&2; exit 2; }
  GO_TOOLCHAIN_ROOT=$(go env GOROOT)
  GO_TOOLCHAIN=$GO_TOOLCHAIN_ROOT/bin/go
  [ -x "$GO_TOOLCHAIN" ] || { echo "the selected Go toolchain binary was not found" >&2; exit 2; }
  XRAY_GOMODCACHE=$(go env GOMODCACHE)
  # WSL may have a fuller module cache on a mounted drive; use it only for the extra Xray build when it has its cached modules.
  for candidate in /mnt/*/cache/go-mod; do
    [ -d "$candidate/github.com/xtls/xray-core@$XRAY_VERSION" ] || continue
    for telemetry in "$candidate"/golang.org/x/telemetry@*; do
      if [ -d "$telemetry" ]; then XRAY_GOMODCACHE=$candidate; break 2; fi
    done
  done
  cp "$ROOT/go.mod" "$WORK/censor-xray.mod"
  cp "$ROOT/go.sum" "$WORK/censor-xray.sum"
  python3 - "$ROOT/go.mod" "$WORK/censor-xray.mod" <<'PY'
import json, subprocess, sys

root_mod, build_mod = sys.argv[1:]
mod = json.loads(subprocess.check_output(["go", "mod", "edit", "-json", root_mod], text=True))
replaced = {item["Old"]["Path"] for item in mod.get("Replace", [])}
args = ["go", "mod", "edit", "-modfile=" + build_mod]
for item in mod.get("Require", []):
    if item["Path"] not in replaced:
        args.append("-replace=" + item["Path"] + "=" + item["Path"] + "@" + item["Version"])
subprocess.run(args, check=True)
PY
  GOTOOLCHAIN=local GOROOT="$GO_TOOLCHAIN_ROOT" GOMODCACHE="$XRAY_GOMODCACHE" GOPROXY=off GOFLAGS=-buildvcs=false \
    "$GO_TOOLCHAIN" build -mod=mod -modfile="$WORK/censor-xray.mod" -o "$WORK/bin/main" github.com/xtls/xray-core/main
  chmod 755 "$WORK"
  exec env MG_CENSOR_INNER=1 MG_CENSOR_WORK="$WORK" unshare -n -m -- bash "$SELF" "${ARGS[@]}"
fi

WORK=${MG_CENSOR_WORK:?missing work directory}
BIN=$WORK/bin
T0=$(date +%s)
PIDS=() CREATED_NS=() RESULTS=() FAILED=0 COOKIE="" TOTP_SECRET="" OWNER_STEP=-1 OWNER_STEPUP_UNTIL=0 CURRENT_SCENARIO=""
DE1=203.0.113.10 NL1=203.0.113.20 MSK1=203.0.113.30
ADMIN=http://127.0.0.1:8081 PUBLIC=https://10.77.10.1:8443
MCP_TOKEN=$WORK/mcp-token.out MCP_SECRETS=$WORK/mcp-secrets.txt
declare -A NODE_ID=()
declare -A NODE_PID=()

say() { printf '[%3ds] %s\n' "$(( $(date +%s) - T0 ))" "$*"; }
die() { say "FAIL $*"; exit 1; }
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
  local rc=$? i ns
  trap - EXIT INT TERM
  set +e
  for ((i = ${#PIDS[@]} - 1; i >= 0; i--)); do stop_pid "${PIDS[$i]}"; done
  for ((i = ${#CREATED_NS[@]} - 1; i >= 0; i--)); do ip netns del "${CREATED_NS[$i]}" 2>/dev/null; done
  case "$WORK" in /tmp/mg-censor.*) rm -rf -- "$WORK";; esac
  if [ -n "$CURRENT_SCENARIO" ]; then
    RESULTS+=("$(printf '%-24s %-28s %-28s %-12s FAIL' "$CURRENT_SCENARIO" "$CURRENT_SCENARIO" 'measurement/setup failed' unknown)")
  fi
  printf '\n%-24s %-28s %-28s %-12s %s\n' scenario expected got confidence result
  for ns in "${RESULTS[@]}"; do printf '%s\n' "$ns"; done
  say "finished in $(( $(date +%s) - T0 ))s; $( [ "$rc" = 0 ] && [ "$FAILED" = 0 ] && echo ALL-PASS || echo FAILED )"
  [ "$rc" != 0 ] || [ "$FAILED" = 0 ] || rc=1
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

NS_NAMES=(censor-panel censor-home censor-msk1 censor-op-a censor-op-b censor-r1 censor-r2 censor-tspu censor-r3 censor-de1 censor-nl1)
# Refuse existing names before the private netns mount hides /run/netns.
for ns in "${NS_NAMES[@]}"; do
  if ip netns list | awk '{print $1}' | grep -Fxq "$ns"; then die "namespace $ns already exists; it was not touched"; fi
done
# Namespace bind mounts must stay inside this mount namespace. Use a private tmpfs when /run/netns is not mounted already.
mount --make-rprivate /run
mkdir -p /run/netns
if ! mountpoint -q /run/netns; then mount -t tmpfs -o mode=755 tmpfs /run/netns; fi
mount --make-rprivate /run/netns
for ns in "${NS_NAMES[@]}"; do
  ip netns add "$ns"
  CREATED_NS+=("$ns")
  ip -n "$ns" link set lo up
done
for r in censor-r1 censor-r2 censor-tspu censor-r3; do ip netns exec "$r" sysctl -qw net.ipv4.ip_forward=1; done

SEQ=0
link() { # <namespace-a> <namespace-b> <left-end /30 network>
  local a=$1 b=$2 net=$3 left right
  SEQ=$((SEQ + 1))
  left="cv${SEQ}a" right="cv${SEQ}b"
  ip link add "$left" type veth peer name "$right"
  ip link set "$left" netns "$a"
  ip link set "$right" netns "$b"
  ip -n "$a" addr add "$net.1/30" dev "$left"
  ip -n "$b" addr add "$net.2/30" dev "$right"
  ip -n "$a" link set "$left" up
  ip -n "$b" link set "$right" up
}

# Each operator has a different source subnet; msk1 and panel share the country side of the router chain.
link censor-msk1 censor-r1 10.77.1
link censor-r1 censor-r2 10.77.2
link censor-r2 censor-tspu 10.77.3
link censor-tspu censor-r3 10.77.4
link censor-r3 censor-de1 10.77.5
link censor-r3 censor-nl1 10.77.6
link censor-panel censor-r1 10.77.10
link censor-home censor-r1 10.77.13
link censor-op-a censor-r1 10.77.11
link censor-op-b censor-r1 10.77.12
ip -n censor-msk1 route add default via 10.77.1.2
ip -n censor-panel route add default via 10.77.10.2
ip -n censor-home route add default via 10.77.13.2
ip -n censor-op-a route add default via 10.77.11.2
ip -n censor-op-b route add default via 10.77.12.2
ip -n censor-de1 route add default via 10.77.5.1
ip -n censor-nl1 route add default via 10.77.6.1
ip -n censor-r1 route add default via 10.77.2.2
for net in 10.77.1.0/30 10.77.10.0/30 10.77.11.0/30 10.77.12.0/30 10.77.13.0/30; do ip -n censor-r2 route add "$net" via 10.77.2.1; done
ip -n censor-r2 route add default via 10.77.3.2
for net in 10.77.1.0/30 10.77.2.0/30 10.77.10.0/30 10.77.11.0/30 10.77.12.0/30 10.77.13.0/30; do ip -n censor-tspu route add "$net" via 10.77.3.1; done
ip -n censor-tspu route add default via 10.77.4.2
for net in 10.77.1.0/30 10.77.2.0/30 10.77.3.0/30 10.77.10.0/30 10.77.11.0/30 10.77.12.0/30 10.77.13.0/30; do ip -n censor-r3 route add "$net" via 10.77.4.1; done
for tuple in "$DE1 10.77.5.2" "$NL1 10.77.6.2" "198.51.100.10 198.51.100.10" "198.51.100.66 198.51.100.66" "198.51.100.100 198.51.100.100"; do
  read -r addr via <<<"$tuple"
  if [[ "$addr" == 203.* ]]; then ip -n censor-r3 route add "$addr/32" via "$via"; else ip -n censor-r3 addr add "$addr/32" dev lo; fi
done
ip -n censor-de1 addr add "$DE1/32" dev lo
ip -n censor-nl1 addr add "$NL1/32" dev lo
ip -n censor-msk1 addr add "$MSK1/32" dev lo

# A guard at the simulated block drops every packet aimed beyond the private and documentation ranges.
ip netns exec censor-tspu nft -f - <<'NFT'
table inet censor_guard {
  chain pre {
    type filter hook prerouting priority -400; policy accept;
    ip daddr != { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24 } drop
  }
}
table inet censor_reference {
  chain forward {
    type filter hook forward priority -10; policy accept;
    ip daddr 198.51.100.66 tcp flags & (syn | ack) == syn drop
  }
}
NFT

spawn_in() { # <namespace> <logfile> <command...>; only this returned PID is stopped at teardown
  local ns=$1 log=$2
  shift 2
  ip netns exec "$ns" setsid "$@" >>"$log" 2>&1 </dev/null &
  LAST_PID=$!
  PIDS+=("$LAST_PID")
}
inpanel() { ip netns exec censor-panel "$@"; }
inop() { ip netns exec "censor-$1" "${@:2}"; }
nft_tspu() { ip netns exec censor-tspu nft "$@"; }
new_table() {
  nft_tspu add table inet "$1"
  nft_tspu add chain inet "$1" forward '{ type filter hook forward priority -10; policy accept; }'
}
remove_table() { nft_tspu delete table inet "$1"; }

cat >"$WORK/far_services.py" <<'PY'
import http.server, socket, socketserver, threading, time

class Bulk(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"x" * 1048576
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except OSError:
            pass
    def log_message(self, *args):
        pass

def hold(host, port):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind((host, port)); s.listen(32)
    while True:
        c, _ = s.accept(); c.close()

threading.Thread(target=hold, args=("198.51.100.10", 443), daemon=True).start()
threading.Thread(target=hold, args=("198.51.100.66", 443), daemon=True).start()
server = socketserver.ThreadingTCPServer(("198.51.100.100", 8080), Bulk, bind_and_activate=True)
threading.Thread(target=server.serve_forever, daemon=True).start()
while True: time.sleep(3600)
PY
spawn_in censor-r3 "$WORK/far-services.log" python3 "$WORK/far_services.py"
spawn_in censor-r3 "$WORK/tlsdest.log" "$BIN/tlsdest" -listen 198.51.100.100:8443 -names www.example.com -cert "$WORK/tlsdest.pem"
spawn_in censor-r3 "$WORK/censor-sources.log" python3 "$ROOT/scripts/e2e/censor_sources.py"

api() { # <Service/Method> [JSON]
  local out code detail body
  body=${2:-}
  [ -n "$body" ] || body='{}'
  out=$(inpanel curl -skS -m 180 -w $'\n%{http_code}' -X POST "$ADMIN/api/mistgate.admin.v1.$1" \
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
wait_for() { local n=$1 what=$2 i; shift 2; for ((i = 0; i < n; i++)); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done; die "timeout waiting for $what"; }

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=censor-panel \
  -addext subjectAltName=DNS:localhost,IP:127.0.0.1,IP:10.77.10.1 -keyout "$WORK/panel.key" -out "$WORK/panel.crt" 2>/dev/null
SETUP_OUT=$(inpanel "$BIN/mistgate" setup --data-dir "$WORK/data" --public-url "$PUBLIC" --admin-listen 127.0.0.1:8081)
SETUP_TOKEN=$(sed -n 's|^Setup link: .*/setup#\(.*\)$|\1|p' <<<"$SETUP_OUT")
[ -n "$SETUP_TOKEN" ] || die "panel setup returned no setup token"
spawn_in censor-panel "$WORK/panel.log" "$BIN/mistgate" serve --data-dir "$WORK/data" --listen 10.77.10.1:8443 \
  --admin-listen 127.0.0.1:8081 --tls-cert "$WORK/panel.crt" --tls-key "$WORK/panel.key" --agent-addr 10.77.10.1:8443
wait_for 45 "the panel listener" inpanel curl -skS -m 3 -o /dev/null "$PUBLIC/"

LOGIN=censor-admin PASSWORD=$(openssl rand -hex 16)
BEGIN=$(api AuthService/BeginSetup "$(jq -nc --arg t "$SETUP_TOKEN" --arg l "$LOGIN" --arg p "$PASSWORD" \
  '{setupToken:$t,displayName:"Censor lab",method:"SETUP_METHOD_PASSWORD",login:$l,password:$p}')")
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
say "panel up inside censor-panel"

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

# Wait for the REALITY target certificate, then let the actual node agents trust only that extra lab certificate.
wait_for 20 "the local REALITY target certificate" test -s "$WORK/tlsdest.pem"
make_node de1 "$DE1" DE censor-de1
make_node nl1 "$NL1" NL censor-nl1
make_node msk1 "$MSK1" RU censor-msk1
nodes_online() { api NodeService/ListNodes '{}' | jq -e '[.nodes[] | select(.status == "NODE_STATUS_ONLINE")] | length == 3' >/dev/null; }
probe_node_ready() { api TspuProbeService/GetTspuProbe '{}' | jq -e 'any(.settings.nodes[]; .name == "msk1" and .declares and .online)' >/dev/null; }
wait_for 90 "three online node agents" nodes_online
say "real agents online: de1, nl1, msk1"

H2_SETTINGS=$(api ProfileService/ListProtocols '{}' | jq -c '.protocols[] | select(.id == "hysteria2") | .defaultSettingsJson | fromjson
  | .tls_mode = "self_signed" | .port = 4433 | .hop = {from: 0, to: 0} | .obfs.password = "$generate"')
H2_PROFILE=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$H2_SETTINGS" '{protocol:"hysteria2",name:"censor-hy2",settingsJson:$s}')" | jq -r .profile.id)
VLESS_SETTINGS=$(api ProfileService/ListProtocols '{}' | jq -c --arg target '198.51.100.100:8443' '.protocols[] | select(.id == "vless") | .defaultSettingsJson | fromjson
  | .port = 8443 | .transport = "tcp" | .security = "reality" | .reality.target_kind = "site"
  | .reality.target = $target | .reality.server_names = ["www.example.com"]')
VLESS_PROFILE=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$VLESS_SETTINGS" '{protocol:"vless",name:"censor-vless",settingsJson:$s}')" | jq -r .profile.id)
VLESS_CONTROL_SETTINGS=$(jq -c '.port = 443' <<<"$VLESS_SETTINGS")
VLESS_CONTROL_PROFILE=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$VLESS_CONTROL_SETTINGS" '{protocol:"vless",name:"censor-vless-control",settingsJson:$s}')" | jq -r .profile.id)
for n in de1 nl1; do
  api ProfileService/CreateInbound "$(jq -nc --arg p "$VLESS_PROFILE" --arg n "${NODE_ID[$n]}" '{profileId:$p,nodeId:$n}')" >/dev/null
done
CONTROL_INBOUND=$(api ProfileService/CreateInbound "$(jq -nc --arg p "$VLESS_CONTROL_PROFILE" --arg n "${NODE_ID[de1]}" '{profileId:$p,nodeId:$n}')")
CONTROL_INBOUND_ID=$(jq -r '.inbound.id' <<<"$CONTROL_INBOUND")
api ProfileService/UpdateInbound "$(jq -nc --arg id "$CONTROL_INBOUND_ID" '{inboundId:$id,directInSubscriptions:false}')" >/dev/null
inbounds_active() {
  local n
  for n in de1 nl1; do
    api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[$n]}" '{nodeId:$n}')" \
      | jq -e 'all(.inbounds[]; .state == "INBOUND_STATE_ACTIVE") and (.inbounds | length) >= 1' >/dev/null || return 1
  done
  api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[de1]}" '{nodeId:$n}')" \
    | jq -e --arg id "$CONTROL_INBOUND_ID" 'any(.inbounds[]; .id == $id and .directInSubscriptions != true)' >/dev/null
}
wait_for 90 "VLESS inbounds ACTIVE" inbounds_active
api CountryReachabilityService/UpdateCountryReachability '{"country":"RU"}' >/dev/null
owner_stepup
api ClientProbeService/UpdateClientProbe '{"enabled":true}' >/dev/null
wait_for 30 "msk1 listed as an online TSPU-capable probe node" probe_node_ready
MSK1_PROBE_ID=$(api TspuProbeService/GetTspuProbe '{}' | jq -r '.settings.nodes[] | select(.name == "msk1" and .declares and .online) | .id' | head -n 1)
[ -n "$MSK1_PROBE_ID" ] || die "the TSPU settings do not expose an online msk1 probe node"
NODE_ID[msk1]=$MSK1_PROBE_ID
owner_stepup
api TspuProbeService/UpdateTspuProbe "$(jq -nc --arg id "${NODE_ID[msk1]}" '{enabled:true,probeNodeId:$id,intervalMin:30}')" >/dev/null
owner_stepup
api GlobalpingService/UpdateGlobalping '{"enabled":true,"intervalMin":10,"token":"censor-local","ipv6":false}' >/dev/null

globalping_ready() {
  local checks
  checks=$(api HealthService/GetChecks "$(jq -nc --arg id "${NODE_ID[de1]}" '{nodeId:$id}')") || return 1
  jq -e 'any(.countryChecks[]?; any(.vantages[]?; .source == "globalping"))' <<<"$checks" >/dev/null
}
globalping_state_ready() { # <round-after-unix> <vantage-state>
  local after=$1 state=$2 checks
  checks=$(api HealthService/GetChecks "$(jq -nc --arg id "${NODE_ID[de1]}" '{nodeId:$id}')") || return 1
  jq -e --argjson after "$after" --arg state "$state" \
    'any(.countryChecks[]?; .atUnix > $after and any(.vantages[]?; .source == "globalping" and .state == $state))' \
    <<<"$checks" >/dev/null
}
wait_globalping_state() { # <round-after-unix> <vantage-state>
  wait_for 45 "a fresh Globalping round with $2 vantages" globalping_state_ready "$1" "$2"
}
wait_for 90 "production Globalping source results from fixed lab vantages" globalping_ready
say "the tagged Globalping source stored measured fixed-vantage results through HealthService"

GROUP_ID=$(api GroupService/CreateGroup "$(jq -nc --arg h "$H2_PROFILE" --arg v "$VLESS_PROFILE" \
  '{name:"censor-group",profileIds:[$h,$v]}')" | jq -r .group.id)
USER=$(api UserService/CreateUser "$(jq -nc --arg g "$GROUP_ID" '{name:"alice",groupId:$g}')")
USER_ID=$(jq -r .user.id <<<"$USER") USER_URL=$(jq -r .subscriptionUrl <<<"$USER")
USER_TOKEN=${USER_URL##*/}
umask 077
printf '%s\n' "$USER_TOKEN" >"$MCP_SECRETS"
printf 'GLOBALPING_TOKEN=censor-local\nBSBORD_KEY=bsk_live_censor_lab\n' >>"$MCP_SECRETS"
cat "$WORK/owner.env" 2>/dev/null >>"$MCP_SECRETS" || true
VLESS_URI=$(inpanel curl -skS -m 30 -A 'v2rayNG/1.10.20' "$USER_URL" | base64 -d \
  | grep '^vless://' | grep "$DE1" | head -n 1)
[ -n "$VLESS_URI" ] || die "alice's subscription did not contain the de1 VLESS URI"
printf '%s\n' "$VLESS_URI" >"$WORK/vless.uri"
chmod 600 "$WORK/vless.uri"

cat >"$WORK/vless_to_xray.py" <<'PY'
import json, sys
from urllib.parse import urlsplit, parse_qs, unquote
u = urlsplit(open(sys.argv[1]).read().strip())
q = {k:v[0] for k,v in parse_qs(u.query).items()}
assert u.scheme == "vless" and q["security"] == "reality" and q["type"] == "tcp", q
user = {"id":unquote(u.username), "encryption":"none"}
if q.get("flow"): user["flow"] = q["flow"]
stream = {"network":"tcp", "security":"reality", "realitySettings": {
    "serverName":q["sni"], "fingerprint":q["fp"], "publicKey":q["pbk"], "shortId":q["sid"], "spiderX":q.get("spx", "")}}
conf = {"log":{"loglevel":"warning"}, "inbounds":[{"listen":"127.0.0.1", "port":int(sys.argv[2]),
        "protocol":"socks", "settings":{"auth":"noauth", "udp":False}}],
        "outbounds":[{"protocol":"vless", "settings":{"vnext":[{"address":u.hostname, "port":u.port, "users":[user]}]},
                       "streamSettings":stream}]}
json.dump(conf, open(sys.argv[3], "w"))
PY
python3 "$WORK/vless_to_xray.py" "$WORK/vless.uri" 10808 "$WORK/xray-op-a.json"
python3 "$WORK/vless_to_xray.py" "$WORK/vless.uri" 10809 "$WORK/xray-op-b.json"
chmod 600 "$WORK/xray-op-a.json" "$WORK/xray-op-b.json"

# Mint the same admin-profile token a separate MCP client receives from the panel owner.
inpanel env MG_ADMIN_URL="$ADMIN" MG_OWNER_COOKIE="${COOKIE#__Host-sid=}" MG_TOTP_SECRET="$TOTP_SECRET" \
  "$BIN/mcptoken" -profile admin -name censor-sim >"$MCP_TOKEN" 2>"$WORK/mcptoken.log" || die "could not mint the lab MCP token"
OWNER_STEP=$(( $(date +%s) / 30 ))
printf 'MG_ADMIN_URL=%s\nMG_OWNER_COOKIE=%s\nMG_TOTP_SECRET=%s\n' "$ADMIN" "${COOKIE#__Host-sid=}" "$TOTP_SECRET" >>"$MCP_SECRETS"
chmod 600 "$MCP_TOKEN" "$MCP_SECRETS"
say "profiles, owner settings and alice's test subscription are ready"

start_xray() { # <operator> <socks-port> <config>
  local op=$1 port=$2 config=$3
  spawn_in "censor-$op" "$WORK/xray-$op.log" "$BIN/main" run -c "$config"
  wait_for 25 "Xray in $op" inop "$op" ss -Hltn "sport = :$port"
}
start_xray op-a 10808 "$WORK/xray-op-a.json"
start_xray op-b 10809 "$WORK/xray-op-b.json"
say "real Xray VLESS clients are ready in the two operator namespaces"

post_client_report() { # <operator> <verdict> <dl-bytes> <stalled>
  local op=$1 verdict=$2 bytes=$3 stalled=$4 link url body code region=region-a
  [ "$op" != op-b ] || region=region-b
  link=$(api UserService/GetSubscriptionLink "$(jq -nc --arg u "$USER_ID" '{userId:$u,rotate:true}')")
  url=$(jq -r .url <<<"$link")
  body=$(jq -nc --arg op "$op" --arg region "$region" --arg host "$DE1" --arg verdict "$verdict" --argjson port 8443 \
    --argjson bytes "$bytes" --argjson stalled "$stalled" \
    '{v:1,vpn:false,direct_checked:true,network:{operator:$op,country:"RU",region:$region,type:"mobile"},
      servers:[{type:"vless",host:$host,port:$port,verdict:$verdict,dl_bytes:$bytes,dl_stalled:$stalled}]}')
  code=$(inop "$op" curl -ksS -m 20 -o "$WORK/probe-response.json" -w '%{http_code}' -X POST "${url%/}/probe" \
    -H 'Content-Type: application/json' -d "$body" 2>"$WORK/probe-curl.log") || true
  [ "$code" = 200 ] && jq -e '.accepted == 1' "$WORK/probe-response.json" >/dev/null || die "kl!ck API report was not stored for $op"
}

call_mcp() { # <tool> <args-json> <result-file>
  inpanel "$BIN/mcpe2e" -url "$ADMIN" -token-file "$MCP_TOKEN" -secret-file "$MCP_SECRETS" \
    -addresses "$DE1,$NL1,$MSK1,10.77.10.1" \
    -call-tool "$1" -call-args "$2" -call-result-file "$3"
}
run_checks_now() { # Retry only while the panel's one-second tagged manual-check gate is occupied by a scheduled round.
  local attempt args
  args=$(jq -nc --arg id "$H2_INBOUND_ID" '{node:"de1",inbound:$id}')
  for ((attempt = 1; attempt <= 35; attempt++)); do
    if call_mcp checks_run_now "$args" "$WORK/checks-run-now.json" >"$WORK/checks-run-attempt.log" 2>&1; then
      cat "$WORK/checks-run-attempt.log"
      return 0
    fi
    sleep 1
  done
  cat "$WORK/checks-run-attempt.log" >&2
  return 1
}

trigger_tspu() { # expected de1's folded verdict
  local want=$1 output status i
  for ((i = 0; i < 60; i++)); do
    output=$(api TspuProbeService/TestTspuProbe '{}') || die "TSPU probe API failed"
    status=$(jq -r '.run.status // empty' <<<"$output")
    [ "$status" != busy ] && break
    [ "$i" -gt 0 ] || say "the probe node is busy; retrying at the tagged one-second test interval"
    sleep 1
  done
  if ! jq -e --arg want "$want" '.run.status == "ok" and any(.nodes[]; .name == "de1" and .verdict == $want)' <<<"$output" >/dev/null; then
    jq -c '{run:.run,nodes:[.nodes[]?|{name,verdict,ports}]}' <<<"$output" >&2
    die "TSPU probe did not measure de1 as $want"
  fi
  printf '%s' "$output" >"$WORK/tspu-last.json"
  say "TspuProbeService.TestTspuProbe measured de1 as $want"
}

tcp_result() { # <operator> <port> [host]: connected | refused | timeout | other
  inop "$1" python3 - "${3:-$DE1}" "$2" <<'PY'
import socket,sys
s=socket.socket(); s.settimeout(2)
try: s.connect((sys.argv[1],int(sys.argv[2]))); print("connected")
except TimeoutError: print("timeout")
except ConnectionRefusedError: print("refused")
except OSError: print("other")
finally: s.close()
PY
}

stop_node() { stop_pid "${NODE_PID[$1]}"; }
restart_node() {
  local name=$1 ns=$2
  spawn_in "$ns" "$WORK/node-$name.log" env NODE_SSL_CERT_FILE="$WORK/tlsdest.pem" bash -c \
    'mkdir -p /etc/systemd/journald.conf.d; mount -t tmpfs tmpfs /etc/sysctl.d; mount -t tmpfs tmpfs /etc/systemd/journald.conf.d; exec "$@"' _ \
    "$BIN/mistgate-node" run --state-dir "$WORK/node/$name"
  NODE_PID[$name]=$LAST_PID
}
two_nodes_online() { api NodeService/ListNodes '{}' | jq -e '[.nodes[] | select(.status == "NODE_STATUS_ONLINE")] | length >= 2' >/dev/null; }
three_nodes_online() { api NodeService/ListNodes '{}' | jq -e '[.nodes[] | select(.status == "NODE_STATUS_ONLINE")] | length == 3' >/dev/null; }

reset_client_reports() {
  owner_stepup
  api ClientProbeService/DisconnectClientProbe '{}' >/dev/null
  owner_stepup
  api ClientProbeService/UpdateClientProbe '{"enabled":true}' >/dev/null
}

SCENARIO_OK=1
measurement_assert() { # <label> <expected> <actual>
  local label=$1 expected=$2 actual=$3
  if [ "$expected" = "$actual" ]; then
    say "measured PASS $label: $actual"
  else
    say "measured FAIL $label: expected $expected, got $actual"
    SCENARIO_OK=0 FAILED=1
  fi
}
measurement_range() { # <label> <min> <max> <actual>
  local label=$1 minv=$2 maxv=$3 actual=$4
  if [[ "$actual" =~ ^[0-9]+$ ]] && (( actual >= minv && actual <= maxv )); then
    say "measured PASS $label: $actual bytes"
  else
    say "measured FAIL $label: expected ${minv}..${maxv} bytes, got $actual"
    SCENARIO_OK=0 FAILED=1
  fi
}

record_diagnosis() { # <label> <node> <class> <scope> <min-confidence> [access-report]
  local label=$1 node=$2 class=$3 scope=$4 minconf=$5 with_report=${6:-} result=$WORK/diagnosis.json rc=0 row pass expected got conf
  local args=("$BIN/mcpe2e" -url "$ADMIN" -token-file "$MCP_TOKEN" -secret-file "$MCP_SECRETS" \
    -addresses "$DE1,$NL1,$MSK1,10.77.10.1" \
    -diagnose "$node" -country RU -expect-class "$class" -expect-scope "$scope" -min-confidence "$minconf" \
    -case "$label" -result-file "$result")
  [ "$with_report" != yes ] || args+=( -access-report )
  inpanel "${args[@]}" || rc=$?
  row=$(cat "$result" 2>/dev/null || printf '{}')
  pass=$(jq -r '.pass // false' <<<"$row")
  expected=$(jq -r 'if .scope == "" then .expected else .expected+"/"+.scope end' <<<"$row")
  got=$(jq -r 'if .got_scope == "" then .got else .got+"/"+.got_scope end' <<<"$row")
  conf=$(jq -r '.confidence // "unknown"' <<<"$row")
  if [ "$pass" = true ] && [ "$rc" = 0 ] && [ "$SCENARIO_OK" = 1 ]; then
    RESULTS+=("$(printf '%-24s %-28s %-28s %-12s PASS' "$label" "$expected" "$got" "$conf")")
  else
    RESULTS+=("$(printf '%-24s %-28s %-28s %-12s FAIL' "$label" "$expected" "$got" "$conf")")
    FAILED=1
  fi
  CURRENT_SCENARIO=""
  SCENARIO_OK=1
}

skip_row() {
  RESULTS+=("$(printf '%-24s %-28s %-28s %-12s SKIP' "$1" "$1" "$2" '-')")
}

download_via_xray() { # <operator> <socks-port> <timeout-seconds> <label>
  local op=$1 port=$2 timeout=$3 label=$4 out
  out=$(inop "$op" curl -sS --socks5-hostname "127.0.0.1:$port" --connect-timeout 3 --max-time "$timeout" \
    -o /dev/null -w '%{size_download} %{http_code}' "http://198.51.100.100:8080/blob" 2>"$WORK/curl-$label.log") || true
  printf '%s' "$out"
}

enable_bsbord_lab() {
  owner_stepup
  api BsbordService/UpdateBsbord "$(jq -nc --arg k 'bsk_live_censor_lab' \
    '{enabled:true,key:$k,units:{opKeys:["op-a|mobile|on","op-b|mobile|on"]}}')" >/dev/null
}
disable_bsbord_lab() {
  owner_stepup
  api BsbordService/UpdateBsbord '{"enabled":false}' >/dev/null
}
whitelist_facts_ready() {
  local checks
  checks=$(api HealthService/GetChecks "$(jq -nc --arg id "${NODE_ID[de1]}" '{nodeId:$id}')") || return 1
  jq -e 'any(.countryChecks[]?;
    ([.vantages[]? | select(.source == "globalping" and .state == "reach")] | length >= 2) and
    ([.vantages[]? | select(.source == "bsbord" and .kind == "mobile" and .state == "whitelist")] | length >= 2))' <<<"$checks" >/dev/null
}

printf '%-24s %-28s %-28s %-12s %s\n' scenario expected got confidence result

# ---------------------------------------------------------------- ip_blocked: every packet to de1 is dropped for both operators.
say "scenario ip_blocked"
CURRENT_SCENARIO=ip_blocked
SCENARIO_OK=1
new_table censor_ip_blocked
nft_tspu add rule inet censor_ip_blocked forward ip daddr "$DE1" drop
COUNTRY_AFTER=$(date +%s)
[ "$(tcp_result op-a 8443)" = timeout ] && a=timeout || a=other
[ "$(tcp_result op-b 8443)" = timeout ] && b=timeout || b=other
measurement_assert ip_blocked_op_a timeout "$a"
measurement_assert ip_blocked_op_b timeout "$b"
trigger_tspu blocked
wait_globalping_state "$COUNTRY_AFTER" dark
record_diagnosis ip_blocked de1 ip_blocked node medium
remove_table censor_ip_blocked

# ---------------------------------------------------------------- port_blocked: TCP 443 still accepts; TCP 8443 is cut.
say "scenario port_blocked"
CURRENT_SCENARIO=port_blocked
SCENARIO_OK=1
new_table censor_port_blocked
nft_tspu add rule inet censor_port_blocked forward ip daddr "$DE1" tcp dport 8443 drop
trigger_tspu partial
[ "$(tcp_result op-a 8443)" = timeout ] && a=timeout || a=other
[ "$(tcp_result op-b 443)" = connected ] && b=connected || b=other
measurement_assert port_blocked_tcp8443 timeout "$a"
measurement_assert port_blocked_tcp443 connected "$b"
record_diagnosis port_blocked de1 port_blocked port:8443/tcp high yes
remove_table censor_port_blocked
api ProfileService/UpdateInbound "$(jq -nc --arg id "$CONTROL_INBOUND_ID" '{inboundId:$id,enabled:false}')" >/dev/null

# ---------------------------------------------------------------- udp_blocked: TSPU TCP view is open; in-country UDP checks are measured through the drop.
say "scenario udp_blocked"
CURRENT_SCENARIO=udp_blocked
SCENARIO_OK=1
new_table censor_udp_blocked
nft_tspu add rule inet censor_udp_blocked forward ip daddr "$DE1" ip protocol udp drop
H2_INBOUND=$(api ProfileService/CreateInbound "$(jq -nc --arg p "$H2_PROFILE" --arg n "${NODE_ID[de1]}" '{profileId:$p,nodeId:$n,allowLossyPort:true}')")
H2_INBOUND_ID=$(jq -r '.inbound.id' <<<"$H2_INBOUND")
api ProfileService/UpdateInbound "$(jq -nc --arg id "$H2_INBOUND_ID" '{inboundId:$id,directInSubscriptions:false}')" >/dev/null
h2_inbound_active() {
  api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[de1]}" '{nodeId:$n}')" \
    | jq -e --arg id "$H2_INBOUND_ID" 'any(.inbounds[]; .id == $id and .state == "INBOUND_STATE_ACTIVE" and .directInSubscriptions != true)' >/dev/null
}
wait_for 90 "de1's Hysteria2 inbound to become active under the UDP drop" h2_inbound_active
trigger_tspu open
say "running the real Hysteria2 check through the production check service"
run_checks_now || die "checks_run_now failed after retries"
jq -c '{scheduled,skipped,complete,rows:[.rows[]|{node:.node_name,cells:[.cells[]|{inbound_id,status,at_unix,error_code}]}]}' \
  "$WORK/checks-run-now.json"
call_mcp path_probe '{"from":"msk1","to":"de1","kind":"udp"}' "$WORK/path-probe.json" || die "path_probe failed"
jq -e 'any(.ports[]; .verdict == "broken")' "$WORK/path-probe.json" >/dev/null || die "path_probe did not measure broken UDP from msk1"
call_mcp node_ports '{"node":"de1","check":true}' "$WORK/node-ports-blocked.json" || die "node_ports UDP check failed"
jq -c '{sender,error_code,ports:[.ports[]|{port,verdict,sender,sender_verdict,confirm_sender,confirm_verdict,checked_unix}]}' "$WORK/node-ports-blocked.json"
jq -e 'any(.ports[]; (.sender == "msk1" and .sender_verdict == "broken" and .confirm_sender == "nl1" and .confirm_verdict == "ok") or
  (.sender == "nl1" and .sender_verdict == "ok" and .confirm_sender == "msk1" and .confirm_verdict == "broken"))' \
  "$WORK/node-ports-blocked.json" >/dev/null || die "node_ports did not measure the broken in-country sender and clean confirmer"
call_mcp node_ports '{"node":"de1"}' "$WORK/node-ports-stored.json" || die "stored node_ports read failed"
jq -e 'any(.ports[]; (.sender == "msk1" and .sender_verdict == "broken" and .confirm_sender == "nl1" and .confirm_verdict == "ok") or
  (.sender == "nl1" and .sender_verdict == "ok" and .confirm_sender == "msk1" and .confirm_verdict == "broken"))' \
  "$WORK/node-ports-stored.json" >/dev/null || die "node_ports did not persist per-sender UDP results"
jq -c '{node_name,ports:[.ports[]|{port,verdict,sender,checked_unix,inbounds}]}' "$WORK/node-ports-stored.json"
call_mcp block_diagnosis '{"node":"de1","country":"RU"}' "$WORK/udp-diagnosis-debug.json" || die "block_diagnosis debug read failed"
jq -c '.nodes[] | {node,country,country_verdict,country_confidence,tspu_open_ports,tspu_blocked_ports,classes:[.classes[]|{class,scope,confidence,detail,unknown}]}' \
  "$WORK/udp-diagnosis-debug.json"
record_diagnosis udp_blocked de1 udp_blocked protocol:udp high
remove_table censor_udp_blocked
stop_node nl1
wait_for 30 "the msk1-only clean UDP confirmation" probe_node_ready
call_mcp node_ports '{"node":"de1","check":true}' "$WORK/node-ports-clear.json" || die "clean UDP refresh failed"
jq -c '{sender,error_code,udp_filtered_unix,ports:[.ports[]|{port,verdict,sender,sender_verdict,confirm_sender,confirm_verdict,checked_unix}]}' "$WORK/node-ports-clear.json"
jq -e '.sender == "msk1" and .error_code == null and all(.ports[]; .sender_verdict == "ok")' \
  "$WORK/node-ports-clear.json" >/dev/null || die "the clean in-country UDP check did not select msk1"
call_mcp node_ports '{"node":"de1"}' "$WORK/node-ports-clear-stored.json" || die "clean stored node_ports read failed"
jq -e 'all(.ports[]; .sender == "msk1" and .sender_verdict == "ok" and .confirm_verdict != "broken")' "$WORK/node-ports-clear-stored.json" >/dev/null \
  || die "clean UDP results were not persisted"
restart_node nl1 censor-nl1
wait_for 45 "nl1 after the clean UDP confirmation" two_nodes_online

# ---------------------------------------------------------------- tcp_throttled: let the VLESS handshake and the first ~16 KiB through, then freeze TCP replies.
say "scenario tcp_throttled"
CURRENT_SCENARIO=tcp_throttled
SCENARIO_OK=1
new_table censor_tcp_throttled
nft_tspu add rule inet censor_tcp_throttled forward ip saddr "$DE1" tcp sport 8443 ct reply bytes '>' 16000 drop
trigger_tspu open
for i in 1 2 3; do
  out=$(download_via_xray op-a 10808 6 "throttle-$i")
  read -r bytes status <<<"$out"
  measurement_range "tcp_throttled download $i" 8192 65536 "${bytes:-0}"
  measurement_assert "tcp_throttled HTTP response $i" 200 "${status:-000}"
  if [[ "${bytes:-}" =~ ^[0-9]+$ ]] && (( bytes >= 8192 )); then post_client_report op-a freeze "$bytes" true; fi
done
record_diagnosis tcp_throttled de1 tcp_throttled protocol:vless high
remove_table censor_tcp_throttled
reset_client_reports

# ---------------------------------------------------------------- whitelist_mode: only the control and service addresses are on the mobile destinations list.
say "scenario whitelist_mode"
CURRENT_SCENARIO=whitelist_mode
SCENARIO_OK=1
new_table censor_whitelist
for source in 10.77.11.1 10.77.12.1; do
  nft_tspu add rule inet censor_whitelist forward ip saddr "$source" ip daddr != 198.51.100.10 ip daddr != 198.51.100.100 drop
done
COUNTRY_AFTER=$(date +%s)
[ "$(tcp_result op-a 8443)" = timeout ] && a=timeout || a=other
[ "$(tcp_result op-b 8443)" = timeout ] && b=timeout || b=other
[ "$(tcp_result op-a 443 198.51.100.10)" = connected ] && ca=connected || ca=other
measurement_assert whitelist_op_a_node timeout "$a"
measurement_assert whitelist_op_b_node timeout "$b"
measurement_assert whitelist_op_a_control connected "$ca"
enable_bsbord_lab
wait_for 60 "measured Globalping fixed reaches and Bsbord mobile drops for de1" whitelist_facts_ready
trigger_tspu open
wait_globalping_state "$COUNTRY_AFTER" reach
record_diagnosis whitelist_mode de1 whitelist_mode node high
remove_table censor_whitelist
disable_bsbord_lab

# ---------------------------------------------------------------- operator_specific: drop the same node only for op-a; op-b downloads the full body.
say "scenario operator_specific"
CURRENT_SCENARIO=operator_specific
SCENARIO_OK=1
new_table censor_operator_specific
nft_tspu add rule inet censor_operator_specific forward ip saddr 10.77.11.1 ip daddr "$DE1" drop
trigger_tspu open
for i in 1 2 3 4; do
  out=$(download_via_xray op-a 10808 5 "operator-a-$i")
  read -r bytes status <<<"$out"
  measurement_assert "operator_specific op-a TCP result $i" 0 "${bytes:-0}"
  post_client_report op-a tcp_blocked "${bytes:-0}" false
done
for i in 1 2; do
  out=$(download_via_xray op-b 10809 8 "operator-b-$i")
  read -r bytes status <<<"$out"
  measurement_assert "operator_specific op-b download $i" 1048576 "${bytes:-0}"
  measurement_assert "operator_specific op-b HTTP response $i" 200 "${status:-000}"
  post_client_report op-b ok "${bytes:-0}" false
done
record_diagnosis operator_specific de1 operator_specific node medium
remove_table censor_operator_specific
reset_client_reports

# ---------------------------------------------------------------- not_blocked_but_broken: the host answers, but its intended inbound failed to bind.
say "scenario not_blocked_but_broken"
CURRENT_SCENARIO=not_blocked_but_broken
SCENARIO_OK=1
spawn_in censor-de1 "$WORK/broken-listener.log" python3 -c \
  'import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(("0.0.0.0",9443)); s.listen(16); time.sleep(3600)'
wait_for 10 "the test listener on de1 tcp/9443" ip netns exec censor-de1 ss -Hltn 'sport = :9443'
BROKEN_SETTINGS=$(jq -c '.port = 9443' <<<"$VLESS_SETTINGS")
BROKEN_PROFILE=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$BROKEN_SETTINGS" '{protocol:"vless",name:"censor-broken",settingsJson:$s}')" | jq -r .profile.id)
api ProfileService/CreateInbound "$(jq -nc --arg p "$BROKEN_PROFILE" --arg n "${NODE_ID[de1]}" '{profileId:$p,nodeId:$n}')" >/dev/null
broken_inbound_failed() {
  api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[de1]}" '{nodeId:$n}')" \
    | jq -e 'any(.inbounds[]; .port == 9443 and .state == "INBOUND_STATE_FAILED")' >/dev/null
}
wait_for 60 "the production node report to store the failed 9443 inbound" broken_inbound_failed
BROKEN_ID=$(api NodeService/GetNode "$(jq -nc --arg n "${NODE_ID[de1]}" '{nodeId:$n}')" \
  | jq -r '.inbounds[] | select(.port == 9443) | .id' | head -n 1)
new_table censor_not_blocked_broken
trigger_tspu open
[ "$(tcp_result op-b 9443)" = connected ] && c=connected || c=other
measurement_assert broken_host_port_reachable connected "$c"
record_diagnosis not_blocked_but_broken de1 not_blocked_but_broken "inbound:$BROKEN_ID" high
remove_table censor_not_blocked_broken

skip_row protocol_fingerprinted "requires a UDP protocol's lost-answer link counters while a second UDP protocol works; this fleet has only one UDP inbound"
skip_row regional "requires real geographically separated vantages for the same operator; synthetic labels in local namespaces are not honest regional evidence"
