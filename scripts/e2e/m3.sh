# Tunnel steps of scripts/e2e-wsl.sh (AmneziaWG, WARP, Mihomo, an old node). Sourced by it, never run alone: it uses the
# helpers, variables and the isolation of the main script (private network namespace, $WORK, $BIN, api, mutate,
# say, pass, die, wait_for, spawn, stop_pid, free_port, SECRETS ...).
#
# TOPOLOGY. Everything runs inside the private namespace of the run. Clients and the fake internet are anonymous
# child namespaces made with `unshare -n` (nothing to name, nothing to leave behind: they vanish with their
# last process), each joined to the run's namespace by a veth pair out of 198.18.0.0/15 with the run's side on .1:
#
#   inet  198.18.51.0/30   the "internet": whoami at 203.0.113.77 (a documentation address) and, with --warp, a
#                          kernel WireGuard peer that plays Cloudflare WARP (answers /cdn-cgi/trace with warp=on)
#   cli1, cli2 ...         198.18.50.0/30 ...: a real amneziawg-go client or mihomo
#
# The panel gives every config the node's address (127.0.0.1 here), so the clients replace the host by their
# veth gateway. All interface names start with mg3, so a stray one is easy to spot; the node's own mgawg*/mgwarp
# links and tables live in the run's namespace and go with it (step 10 checks that retire removed them first).

M3_CACHE=${MG3_TEST_CACHE:-$HOME/.cache/mistgate-tests}
WHOAMI=203.0.113.77
DIRECT_SRC=198.18.51.1 # what the fake internet sees of traffic that leaves the node directly
WARP_SRC=172.16.0.2    # ... of traffic that leaves through WARP (the address of the imported account)
NS_SEQ=0

# ---------------------------------------------------------------- namespaces

# mk_ns <name> <198.18.x base, e.g. 198.18.50>: a namespace joined to this one by a veth. Sets <name>_PID (any
# process in it, for nsenter), <name>_GW (this side, what the namespace dials) and <name>_IP.
mk_ns() {
  local name=$1 base=$2 pid
  NS_SEQ=$((NS_SEQ + 1))
  spawn unshare -n sleep 100000
  pid=$LAST_PID
  wait_for 5 "namespace $name" nsenter -t "$pid" -n true || die "could not enter the namespace $name"
  ip link add "mg3v$NS_SEQ" type veth peer name "mg3w$NS_SEQ"
  ip link set "mg3w$NS_SEQ" netns "$pid"
  ip addr add "$base.1/30" dev "mg3v$NS_SEQ"
  ip link set "mg3v$NS_SEQ" up
  nsenter -t "$pid" -n -- ip link set lo up
  nsenter -t "$pid" -n -- ip addr add "$base.2/30" dev "mg3w$NS_SEQ"
  nsenter -t "$pid" -n -- ip link set "mg3w$NS_SEQ" up
  printf -v "${name}_PID" %s "$pid"
  printf -v "${name}_GW" %s "$base.1"
  printf -v "${name}_IP" %s "$base.2"
}
cns() { local pid=$1; shift; nsenter -t "$pid" -n -- "$@"; } # run a command in a namespace made by mk_ns

# ---------------------------------------------------------------- the fake internet

write_whoami() {
  cat >"$WORK/whoami.py" <<'PY'
# A web server for the fake internet. Answers with the address it saw the request come from, so a test can tell
# which way the traffic left the node. /bytes/N sends N zero bytes, /cdn-cgi/trace and /generate_204 answer the
# WARP health probes of the node (warp=on only for the WARP account's address). Every request is logged:
# "<unix time> <source> <path>".
import http.server, socketserver, sys, time

host, port, log = sys.argv[1], int(sys.argv[2]), sys.argv[3]
WARP = "172.16.0.2"

class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"
    def log_message(self, *a): pass
    def do_GET(self):
        src = self.client_address[0]
        with open(log, "a") as f:
            f.write("%.3f %s %s\n" % (time.time(), src, self.path))
        p = self.path.split("?")[0]
        if p.startswith("/bytes/"):
            n = int(p.split("/")[2])
            self.send_response(200); self.send_header("Content-Length", str(n)); self.end_headers()
            chunk = b"\0" * 65536
            try:
                while n > 0:
                    k = min(n, len(chunk)); self.wfile.write(chunk[:k]); n -= k
            except OSError:
                pass
            return
        if p == "/generate_204":
            self.send_response(204); self.end_headers(); return
        if p == "/cdn-cgi/trace":
            body = ("fl=lab\nip=%s\ncolo=TST\nwarp=%s\n" % (src, "on" if src == WARP else "off")).encode()
        else:
            body = (src + "\n").encode()
        self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers()
        self.wfile.write(body)

class S(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True

S((host, port), H).serve_forever()
PY
}

m3_lab() {
  step "tunnels.0 lab: the fake internet (whoami at $WHOAMI) behind a veth, clients get namespaces of their own"
  mk_ns inet 198.18.51
  cns "$inet_PID" ip addr add "$WHOAMI/32" dev lo
  ip route add "$WHOAMI/32" via "$inet_IP" # the direct way to it, for traffic that does not go into WARP
  write_whoami
  : >"$WORK/whoami.log"
  spawn nsenter -t "$inet_PID" -n -- python3 "$WORK/whoami.py" "$WHOAMI" 80 "$WORK/whoami.log" >>"$WORK/whoami.out.log" 2>&1
  WHOAMI_PID=$LAST_PID
  local src
  wait_for 10 "whoami" curl -s -m 2 "http://$WHOAMI/lab-check" || die "the fake internet does not answer"
  src=$(curl -s -m 5 "http://$WHOAMI/lab-check" | tr -d '\n')
  [ "$src" = "$DIRECT_SRC" ] || die "whoami saw $src from the run's namespace, expected $DIRECT_SRC"
  pass "fake internet up: $WHOAMI answers through $inet_IP and sees this namespace as $DIRECT_SRC"
}

# who_from <ns pid> <tag>: the address whoami saw for a request made from a namespace ("" = no answer)
who_from() { cns "$1" curl -s -m 10 "http://$WHOAMI/$2" 2>/dev/null | tr -d '\n' || true; }
who_socks() { curl -s -m 15 --socks5-hostname "127.0.0.1:$1" "http://$WHOAMI/$2" 2>/dev/null | tr -d '\n' || true; }
# log_srcs <path prefix>: the distinct sources whoami logged for paths that start with it
log_srcs() { awk -v p="/$1" 'index($3, p) == 1 {print $2}' "$WORK/whoami.log" | sort -u | tr '\n' ' '; }

# ---------------------------------------------------------------- AWG helpers

# awg_go <3.1|2.0>: path of the amneziawg-go client of that generation, built from the pinned source once and cached
# (the same cache and pins as internal/node/awg/awgtest, which the node tests use).
awg_go() {
  local f=$1 dir mod ver
  dir=$M3_CACHE/awgc-$f
  if [ ! -x "$dir/amneziawg-go" ]; then
    case $f in
      3.1) mod=github.com/amnezia-vpn/amneziawg-go/v3 ver=v3.1.20260828 ;;
      2.0) mod=github.com/amnezia-vpn/amneziawg-go ver=v0.2.17 ;;
      *) die "awg_go: unknown generation $f" ;;
    esac
    mkdir -p "$dir"
    (cd "$WORK" && GOBIN=$dir GOFLAGS=-mod=mod GOTOOLCHAIN=auto go install "$mod@$ver") >"$WORK/build-awgc-$f.log" 2>&1 \
      || { tail -n 5 "$WORK/build-awgc-$f.log"; die "building amneziawg-go $ver failed"; }
  fi
  echo "$dir/amneziawg-go"
}

m3_build() {
  go build -trimpath -o "$BIN/awgclient" ./scripts/e2e/awgclient
  awg_go 3.1 >/dev/null
  awg_go 2.0 >/dev/null
  say "amneziawg-go clients: v3.1.20260828 and v0.2.17 ($M3_CACHE)"
}

# awg_settings <port> [jq filter over the settings]: the default settings of an awg profile (own free networks,
# the panel picks the first free slot when subnet4/6 are absent)
awg_settings() {
  api ProfileService/ListProtocols '{}' | jq -c --argjson p "$1" ".protocols[] | select(.id == \"awg\") | .defaultSettingsJson | fromjson
    | del(.subnet4, .subnet6) | .port = \$p | ${2:-.}"
}

# make_profile <protocol> <name> <settings json>: prints the profile id
make_profile() {
  api ProfileService/CreateProfile "$(jq -nc --arg p "$1" --arg n "$2" --arg s "$3" '{protocol:$p, name:$n, settingsJson:$s}')" | jq -r .profile.id
}
make_inbound() { api ProfileService/CreateInbound "{\"profileId\":\"$1\",\"nodeId\":\"$NODE_ID\"}" >/dev/null; }
make_group() { api GroupService/CreateGroup "$(jq -nc --arg n "$1" --argjson p "$2" '{name:$n, profileIds:$p}')" | jq -r .group.id; }
# mutate_to <variable> <Service/Method> [json]: mutate in THIS shell (inside $(...) the step-up bookkeeping of the call would be
# lost and the next call would reuse a one-time code), with stdout and stderr in the variable; returns mutate's status
mutate_to() {
  local __v=$1 rc=0
  shift
  mutate "$@" >"$WORK/mutate.out" 2>&1 || rc=$?
  printf -v "$__v" %s "$(<"$WORK/mutate.out")"
  return "$rc"
}
inbounds_active_all() { api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -e 'all(.inbounds[]; .state == "INBOUND_STATE_ACTIVE")'; }
inbound_of() { api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c --arg p "$1" '.inbounds[] | select(.profileId == $p)'; }

# new_user <name> <group id>: sets U_ID, U_URL, U_TOKEN, U_PW (the page password, a secret)
new_user() {
  local out
  out=$(api UserService/CreateUser "{\"name\":\"$1\",\"groupId\":\"$2\"}")
  U_ID=$(jq -r .user.id <<<"$out") U_URL=$(jq -r .subscriptionUrl <<<"$out") U_PW=$(jq -r '.pagePassword // ""' <<<"$out")
  U_TOKEN=${U_URL##*/}
  SECRETS+=("$U_TOKEN")
  [ -z "$U_PW" ] || SECRETS+=("$U_PW")
}
user_of() { api UserService/GetUser "{\"userId\":\"$1\"}"; }
used_of() { user_of "$1" | jq -r '(.user.usedBytes // 0) | tonumber'; }

# add_device <user id> <profile id> <label>: sets D_ID and writes $WORK/dev-<label>.conf (0600) and dev-<label>.vpn; the
# keys of the config join SECRETS
add_device() {
  local out f=$WORK/dev-$3
  out=$(api DeviceService/CreateAwgDevice "$(jq -nc --arg u "$1" --arg p "$2" --arg l "$3" '{userId:$u, profileId:$p, platform:"linux", label:$l}')") || return 1
  save_device "$out" "$f"
}
# save_device <response json> <path prefix>: the first config of a device response goes to <prefix>.conf and .vpn
save_device() {
  D_ID=$(jq -r .device.id <<<"$1")
  (umask 077; jq -r '.configs[0].conf' <<<"$1" >"$2.conf"; jq -r '.configs[0].vpnKey' <<<"$1" >"$2.vpn")
  [ -s "$2.conf" ] && [ "$(head -1 "$2.conf")" = "[Interface]" ] || return 1
  local k
  while read -r k; do SECRETS+=("$k"); done < <(sed -n 's/^\(PrivateKey\|PresharedKey\|HeaderProtectionKey\) = //p' "$2.conf")
}
conf_get() { sed -n "s/^$2 = //p" "$1" | head -1; } # <conf> <key>

# start_awg_client <name> <ns var> <conf> <3.1|2.0> <endpoint host:port>: LAST_PID, 0 once the client is up
start_awg_client() {
  local name=$1 nsvar=$2 conf=$3 flavor=$4 ep=$5 i pid log=$WORK/client-awg-$1.log
  pid=${nsvar}_PID
  : >"$log"
  spawn nsenter -t "${!pid}" -n -- "$BIN/awgclient" -conf "$conf" -bin "$(awg_go "$flavor")" -iface "mg3c$NS_SEQ$name" -endpoint "$ep" -flavor "$flavor" >>"$log" 2>&1
  for ((i = 0; i < 100; i++)); do
    grep -q '^ready ' "$log" && { AWG_SOCK=$(awk '/^ready /{print $3}' "$log"); return 0; }
    alive "$LAST_PID" || return 1
    sleep 0.1
  done
  return 1
}
# hs_of <uapi socket>: the client's last handshake (unix seconds, 0 = none)
hs_of() {
  python3 - "$1" <<'PY'
import socket, sys
s = socket.socket(socket.AF_UNIX); s.settimeout(5); s.connect(sys.argv[1]); s.sendall(b"get=1\n\n")
buf = b""
while not buf.endswith(b"\n\n"):
    d = s.recv(65536)
    if not d:
        break
    buf += d
hs = 0
for l in buf.decode().split("\n"):
    if l.startswith("last_handshake_time_sec="):
        hs = int(l.split("=")[1])
print(hs)
PY
}
# gw_of <profile id>: the node's address inside the tunnel (the first host of the profile's IPv4 network)
gw_of() {
  api ProfileService/GetProfile "{\"profileId\":\"$1\"}" | jq -r .settingsJson | jq -r .subnet4 \
    | python3 -c 'import ipaddress, sys; print(ipaddress.ip_network(sys.stdin.read().strip())[1])'
}
ping_ok() { cns "$1" ping -c1 -W1 "$2" >/dev/null 2>&1; } # <ns pid> <addr>
# decode_vpn <vpn:// key>: what Qt's qUncompress and a JSON parser make of it, as the AmneziaVPN import does:
# prints "<client_priv_key> <server_pub_key> <port>" of the first container's last_config
decode_vpn() {
  python3 - "$1" <<'PY'
import base64, json, struct, sys, zlib
k = sys.argv[1].strip()
assert k.startswith("vpn://"), "no vpn:// prefix"
k = k[6:]
raw = base64.urlsafe_b64decode(k + "=" * (-len(k) % 4))
n = struct.unpack(">I", raw[:4])[0]            # qCompress: big-endian length, then a zlib stream
data = zlib.decompress(raw[4:])
assert n == len(data), "qCompress length %d != %d" % (n, len(data))
j = json.loads(data)
assert j["format_version"] == 1 if "format_version" in j else True
c = j["containers"][0]
assert c["container"] in ("amnezia-awg", "amnezia-awg2"), c["container"]
assert j["defaultContainer"] == c["container"]
lc = json.loads(c["awg"]["last_config"])         # a JSON document inside a string, as Amnezia stores it
assert isinstance(c["awg"]["port"], str) and isinstance(lc["port"], int)
print(lc["client_priv_key"], lc["server_pub_key"], lc["port"])
PY
}

# ================================================================ AWG
m3_awg() {
  step "tunnels.1 AWG: profiles 3.1 and 2.0, an inbound each, a user, devices, real amneziawg-go clients"
  mk_ns cli1 198.18.50
  mk_ns cli2 198.18.52
  AWG31_PORT=$(free_port udp) AWG20_PORT=$(free_port udp)
  AWG31=$(make_profile awg e2e-awg31 "$(awg_settings "$AWG31_PORT")")
  local o20
  local p20
  p20=$(api AwgService/ListMimicryPresets '{}' | jq -r '[.presets[] | select(.versions | index("2.0"))][0].id')
  [ -n "$p20" ] && [ "$p20" != null ] || die "no mimicry preset offers AWG 2.0"
  o20=$(api AwgService/GenerateObfuscation "{\"version\":\"2.0\",\"preset\":\"$p20\",\"mtu\":1280}" | jq -r .obfuscationJson)
  AWG20=$(make_profile awg e2e-awg20 "$(awg_settings "$AWG20_PORT" ".version = \"2.0\" | .obfuscation = ($o20)")")
  [ -n "$AWG31" ] && [ -n "$AWG20" ] || die "CreateProfile(awg) returned no id"
  local want=$(($(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq '.inbounds | length') + 2))
  make_inbound "$AWG31"
  make_inbound "$AWG20"
  wait_for 90 "both awg inbounds ACTIVE" inbounds_active "$want" \
    || die "awg inbounds did not become ACTIVE: $(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[] | select(.protocol == "awg") | {port, state, lastError}')"
  # what the agent did on the host, in this namespace
  [ "$(ip -o link show | grep -c ' mgawg')" -ge 2 ] || die "no mgawg* links after two awg inbounds: $(ip -o link show | awk -F': ' '{print $2}' | tr '\n' ' ')"
  ss -Hlun "sport = :$AWG31_PORT" | grep -q . || die "nothing listens on UDP $AWG31_PORT"
  ss -Hlun "sport = :$AWG20_PORT" | grep -q . || die "nothing listens on UDP $AWG20_PORT"
  nft list table inet mistgate_awg >"$WORK/nft-awg.out" 2>&1 || die "the agent installed no nft table mistgate_awg"
  grep -q masquerade "$WORK/nft-awg.out" || die "mistgate_awg has no masquerade"
  awg_up() { inbound_of "$AWG31" | jq -e '.awg.ifaceUp == true and (.awg.backend | length) > 0'; }
  wait_for 90 "AwgHealth of the 3.1 inbound at the panel" awg_up || die "the panel got no AwgHealth: $(inbound_of "$AWG31")"
  pass "3.1 on UDP $AWG31_PORT and 2.0 on UDP $AWG20_PORT ACTIVE: mgawg links, listeners, nft mistgate_awg; AwgHealth '$(inbound_of "$AWG31" | jq -r '.awg.backend')' reached the panel"

  # ---- a user with a device on each profile
  AWG_GROUP=$(make_group e2e-awg "[\"$AWG31\",\"$AWG20\"]")
  new_user carol "$AWG_GROUP"
  CAROL=$U_ID CAROL_URL=$U_URL CAROL_PW=$U_PW
  add_device "$CAROL" "$AWG31" dev31 || die "CreateAwgDevice(3.1) failed"
  DEV31=$D_ID
  add_device "$CAROL" "$AWG20" dev20 || die "CreateAwgDevice(2.0) failed"
  DEV20=$D_ID
  grep -q "^Endpoint = 127.0.0.1:$AWG31_PORT$" "$WORK/dev-dev31.conf" || die "the 3.1 config has an unexpected Endpoint: $(conf_get "$WORK/dev-dev31.conf" Endpoint)"
  grep -q '^HeaderProtectionKey = ' "$WORK/dev-dev31.conf" || die "the 3.1 config has no HeaderProtectionKey"
  ! grep -q '^\(HeaderProtectionKey\|RandomTrailers\|DisableCookies\|ContentPaddingAddition\) ' "$WORK/dev-dev20.conf" || die "the 2.0 config carries 3.x keys"
  local vp
  vp=$(decode_vpn "$(cat "$WORK/dev-dev31.vpn")") || die "the vpn:// key of the 3.1 device does not decode as Qt qCompress + JSON"
  [ "${vp%% *}" = "$(conf_get "$WORK/dev-dev31.conf" PrivateKey)" ] || die "vpn:// and .conf carry different client keys"
  decode_vpn "$(cat "$WORK/dev-dev20.vpn")" >/dev/null || die "the vpn:// key of the 2.0 device does not decode"
  user_of "$CAROL" | jq -e --arg a "$AWG31" --arg b "$AWG20" '[.devices[] | .awgProfileId] | (index($a) != null and index($b) != null)' >/dev/null \
    || die "GetUser does not list both AWG devices: $(user_of "$CAROL" | jq -c '[.devices[] | {id, awgProfileId}]')"
  pass "carol: two devices (3.1, 2.0); .conf has the node endpoint and the right key set per generation; vpn:// = zlib+JSON with the same client key, port string in awg and number in last_config"

  # ---- real clients through the node
  GW31=$(gw_of "$AWG31") GW20=$(gw_of "$AWG20")
  T_HS=$(date +%s) # before the start: the client handshakes at once (keepalive), possibly before start_awg_client returns
  start_awg_client c31 cli1 "$WORK/dev-dev31.conf" 3.1 "$cli1_GW:$AWG31_PORT" || { tail -n 8 "$WORK/client-awg-c31.log" | redact; die "the 3.1 client did not start"; }
  C31=$LAST_PID C31_SOCK=$AWG_SOCK
  wait_for 40 "a ping to the node through the 3.1 tunnel" ping_ok "$cli1_PID" "$GW31" || { tail -n 8 "$WORK/client-awg-c31.log" | redact; die "no handshake: the 3.1 client cannot ping $GW31"; }
  [ "$(hs_of "$C31_SOCK")" -ge "$T_HS" ] || die "the client counts no fresh handshake"
  SRC=$(who_from "$cli1_PID" awg31)
  [ "$SRC" = "$DIRECT_SRC" ] || die "3.1: the fake internet saw '$SRC', expected the node's $DIRECT_SRC (masquerade)"
  pass "3.1 client: handshake in $(($(date +%s) - T_HS)) s, ping to $GW31 through the tunnel, the fake internet sees the node ($SRC)"
  start_awg_client c20 cli2 "$WORK/dev-dev20.conf" 2.0 "$cli2_GW:$AWG20_PORT" || { tail -n 8 "$WORK/client-awg-c20.log" | redact; die "the 2.0 client did not start"; }
  C20=$LAST_PID
  wait_for 40 "a ping through the 2.0 tunnel" ping_ok "$cli2_PID" "$GW20" || { tail -n 8 "$WORK/client-awg-c20.log" | redact; die "no handshake: the 2.0 client (v0.2.17) cannot ping $GW20"; }
  SRC=$(who_from "$cli2_PID" awg20)
  [ "$SRC" = "$DIRECT_SRC" ] || die "2.0: the fake internet saw '$SRC'"
  pass "2.0 client (v0.2.17) on the other profile of the same node: handshake, ping, the fake internet sees the node"
  # the internet itself, as the owner's users would use it: the address a public service sees is the node's
  local via direct
  direct=$(curl -s -m 15 https://api.ipify.org || true)
  via=$(cns "$cli1_PID" curl -s -m 20 https://api.ipify.org || true)
  if [ -n "$direct" ] && [ "$direct" = "$via" ]; then
    pass "public internet through the 3.1 tunnel: a public service sees the node's own address (the same as from the run's namespace)"
  else
    note "public internet check inconclusive (no echo service answered, or the two answers differ)"
  fi
}

m3_awg_traffic() {
  step "tunnels.2 AWG: traffic reaches the panel, a new key, a critical settings change, revoke"
  local before after got ping_pid ts
  before=$(used_of "$CAROL")
  got=$(cns "$cli1_PID" curl -s -m 120 -o /dev/null -w '%{size_download}' "http://$WHOAMI/bytes/20000000")
  [ "$got" = 20000000 ] || die "the transfer through the 3.1 tunnel delivered $got of 20000000 bytes"
  used_gt() { [ "$(used_of "$CAROL")" -ge $((before + 19000000)) ]; }
  wait_for 40 "usedBytes to follow the transfer" used_gt || die "the panel shows $(used_of "$CAROL") bytes for a 20 MB transfer (was $before)"
  sleep 11 # one more stats cycle: a number that is still growing would be a double count
  after=$(($(used_of "$CAROL") - before))
  [ "$after" -le 26000000 ] || die "the panel counted $after bytes for a 20000000 byte transfer"
  user_of "$CAROL" | jq -e --arg d "$DEV31" '.devices[] | select(.id == $d) | .online == true and ((.lastHandshakeUnix // "0") | tonumber) > 0' >/dev/null \
    || die "GetUser: the 3.1 device is not online with a handshake time: $(user_of "$CAROL" | jq -c '.devices[] | {id, online, lastHandshakeUnix}')"
  peers_seen() { inbound_of "$AWG31" | jq -e '.awg.peers >= 1 and .awg.peersHandshaken >= 1'; }
  wait_for 60 "the peer in the inbound status" peers_seen || die "inbound status: $(inbound_of "$AWG31" | jq -c .awg)"
  pass "20 MB through the tunnel: the panel counted $after bytes (ratio $(python3 -c "print(round($after / 20000000, 3))")), the device is online with a handshake, the inbound reports the peer"

  # ---- rotate: the old key stops at once, the address stays
  ts=$(date +%s)
  local out
  out=$(api DeviceService/RotateDeviceKeys "{\"deviceId\":\"$DEV31\"}")
  cp "$WORK/dev-dev31.conf" "$WORK/dev-dev31.old.conf"
  save_device "$out" "$WORK/dev-dev31" || die "RotateDeviceKeys returned no config"
  [ "$(conf_get "$WORK/dev-dev31.conf" PrivateKey)" != "$(conf_get "$WORK/dev-dev31.old.conf" PrivateKey)" ] || die "rotation kept the private key"
  [ "$(conf_get "$WORK/dev-dev31.conf" Address)" = "$(conf_get "$WORK/dev-dev31.old.conf" Address)" ] || die "rotation changed the tunnel address"
  cut_old() { ! ping_ok "$cli1_PID" "$GW31" && ! ping_ok "$cli1_PID" "$GW31"; }
  wait_for 30 "the old key to stop working" cut_old || die "after RotateDeviceKeys the old key still passes traffic"
  say "the old key was cut $(($(date +%s) - ts)) s after RotateDeviceKeys"
  stop_pid "$C31"
  start_awg_client c31b cli1 "$WORK/dev-dev31.conf" 3.1 "$cli1_GW:$AWG31_PORT" || die "the client with the new key did not start"
  C31=$LAST_PID C31_SOCK=$AWG_SOCK
  wait_for 40 "the new key to work" ping_ok "$cli1_PID" "$GW31" || die "the new key does not handshake"
  pass "RotateDeviceKeys: the old key stopped passing traffic, same address, the new key handshakes"

  # ---- a critical change of the profile
  local ver cur s1 impact
  ver=$(api ProfileService/GetProfile "{\"profileId\":\"$AWG31\"}" | jq -r '.profile.version')
  cur=$(api ProfileService/GetProfile "{\"profileId\":\"$AWG31\"}" | jq -r .settingsJson)
  s1=$(jq -r '.obfuscation.s1' <<<"$cur")
  NEW=$(jq -c 'if .obfuscation.s1 > 100 then .obfuscation.s1 -= 1 else .obfuscation.s1 += 1 end
                | if .obfuscation.s2 > 100 then .obfuscation.s2 -= 1 else .obfuscation.s2 += 1 end' <<<"$cur")
  mutate_to impact ProfileService/UpdateProfile "$(jq -nc --arg p "$AWG31" --arg s "$NEW" --argjson v "$ver" '{profileId:$p, settingsJson:$s, expectedVersion:$v, dryRun:true}')" \
    || die "UpdateProfile(dry run) failed: $impact"
  jq -e '(.impact.devicesNeedReissue // 0) >= 1 and ((.impact.criticalFields // []) | length) >= 1 and (.impact.inboundsRestarted // 0) >= 1' <<<"$impact" >/dev/null \
    || die "the dry run does not name the impact (devices to reissue, critical fields, restarts): $(jq -c .impact <<<"$impact")"
  say "dry run: $(jq -c '.impact | {inboundsRestarted, usersOnline, devicesNeedReissue, criticalFields}' <<<"$impact")"
  [ "$(inbound_of "$AWG31" | jq -r .state)" = INBOUND_STATE_ACTIVE ] || die "the dry run changed the inbound"
  ts=$(date +%s)
  mutate ProfileService/UpdateProfile "$(jq -nc --arg p "$AWG31" --arg s "$NEW" --argjson v "$ver" '{profileId:$p, settingsJson:$s, expectedVersion:$v}')" >/dev/null || die "UpdateProfile failed"
  cut_old() { ! ping_ok "$cli1_PID" "$GW31" && ! ping_ok "$cli1_PID" "$GW31"; }
  wait_for 60 "the old settings to stop working" cut_old || die "after the critical change the client with the old settings still passes traffic"
  say "the client with the old settings was cut $(($(date +%s) - ts)) s after UpdateProfile"
  wait_for 60 "every inbound ACTIVE" inbounds_active_all || die "an inbound is not ACTIVE after the change"
  stale_now() { user_of "$CAROL" | jq -e --arg d "$DEV31" '.devices[] | select(.id == $d) | .stale == true'; }
  wait_for 20 "the device marked stale" stale_now || die "GetUser does not mark the device stale: $(user_of "$CAROL" | jq -c '.devices[] | {id, stale}')"
  out=$(api DeviceService/GetDeviceConfigs "{\"deviceId\":\"$DEV31\"}")
  cp "$WORK/dev-dev31.conf" "$WORK/dev-dev31.stale.conf"
  save_device "$out" "$WORK/dev-dev31" || die "GetDeviceConfigs returned no config"
  [ "$(conf_get "$WORK/dev-dev31.conf" S1)" != "$s1" ] || die "the reissued config still has S1 = $s1"
  [ "$(conf_get "$WORK/dev-dev31.conf" PrivateKey)" = "$(conf_get "$WORK/dev-dev31.stale.conf" PrivateKey)" ] || die "reissuing for a profile change must keep the device key"
  not_stale() { user_of "$CAROL" | jq -e --arg d "$DEV31" '.devices[] | select(.id == $d) | (.stale // false) == false'; }
  wait_for 20 "the stale mark gone after the configs were fetched" not_stale || die "the stale mark stays after GetDeviceConfigs"
  stop_pid "$C31"
  start_awg_client c31c cli1 "$WORK/dev-dev31.conf" 3.1 "$cli1_GW:$AWG31_PORT" || die "the client with the reissued config did not start"
  C31=$LAST_PID C31_SOCK=$AWG_SOCK
  wait_for 60 "the reissued config to work" ping_ok "$cli1_PID" "$GW31" || die "the reissued config does not handshake"
  pass "critical change (S1/S2): the dry run named the impact, the old settings were cut, the device is marked stale, GetDeviceConfigs clears it and the new config handshakes with the same key"

  # ---- the 2.0 client was not touched by a change of another profile
  ping_ok "$cli2_PID" "$GW20" || die "the 2.0 session died when an unrelated profile changed"

  # ---- revoke
  (cns "$cli1_PID" ping -D -i 0.2 -w 14 "$GW31" >"$WORK/revoke-ping.log" 2>&1 || true) &
  ping_pid=$!
  PIDS+=("$ping_pid")
  sleep 3
  ts=$(date +%s.%N)
  api UserService/RevokeDevice "{\"deviceId\":\"$DEV31\"}" >/dev/null || die "RevokeDevice failed"
  wait "$ping_pid" 2>/dev/null || true
  local last gap
  last=$(sed -n 's/^\[\([0-9.]*\)\] .* bytes from .*/\1/p' "$WORK/revoke-ping.log" | tail -1)
  [ -n "$last" ] || die "the revoke ping saw no answer at all"
  gap=$(python3 -c "print(round(float('$last') - float('$ts'), 1))")
  python3 -c "import sys; sys.exit(0 if float('$last') <= float('$ts') + 8 else 1)" || die "the last answer came ${gap} s after RevokeDevice: the revoked device kept its session"
  user_of "$CAROL" | jq -e --arg d "$DEV31" '[.devices[] | select(.id == $d)] | length == 0' >/dev/null || die "the revoked device is still listed"
  ! ping_ok "$cli1_PID" "$GW31" || die "the revoked client still pings the node"
  ping_ok "$cli2_PID" "$GW20" || die "revoking one device cut another user device (2.0)"
  pass "RevokeDevice: the last answer to a 0.2 s ping came ${gap} s after the call (<= 8 s), the device is gone and the client dead; the 2.0 device is untouched"
}

# ---------------------------------------------------------------- self-service on the public page
# ss_call <path after the link> <json>: a POST to the user's page; sets SS_CODE and SS_BODY (not a subshell: callers read them)
ss_call() {
  local out
  if out=$(curl -sk -m 20 -b "$WORK/ss.jar" -w $'\n%{http_code}' -X POST "$CAROL_URL$1" -H 'Content-Type: application/json' -d "$2" -D "$WORK/ss.hdr"); then
    SS_CODE=${out##*$'\n'} SS_BODY=${out%$'\n'*}
  else
    SS_CODE=000 SS_BODY=""
  fi
}
m3_selfservice() {
  step "tunnels.3 self-service on the user's page: add, configs, rotate, rename, revoke, an unknown token, the limit, the switch"
  local out id
  # The page is locked by default (the page password): without the cookie every call answers 401 locked, a wrong
  # password is refused, the right one sets the cookie that the calls below carry (a browser would do the same).
  : >"$WORK/ss.jar"
  ss_call /devices "{\"profile_id\":\"$AWG31\",\"platform\":\"windows\",\"label\":\"locked\"}"
  [ "$SS_CODE" = 401 ] && grep -q '"locked"' <<<"$SS_BODY" || die "a locked page: POST /devices answered HTTP $SS_CODE, want 401 locked"
  [ "$(curl -sk -m 10 -o /dev/null -w '%{http_code}' -X POST "$CAROL_URL/unlock" -H 'Content-Type: application/json' -d '{"password":"nope-nope"}')" = 401 ] \
    || die "a wrong page password was not refused with 401"
  [ -n "$CAROL_PW" ] || die "CreateUser returned no page password"
  [ "$(curl -sk -m 10 -c "$WORK/ss.jar" -o /dev/null -w '%{http_code}' -X POST "$CAROL_URL/unlock" -H 'Content-Type: application/json' -d "$(jq -nc --arg p "$CAROL_PW" '{password:$p}')")" = 200 ] \
    || die "the page password did not unlock the page"
  grep -q mg_page "$WORK/ss.jar" || die "unlocking set no mg_page cookie"
  ss_call /devices "{\"profile_id\":\"$AWG31\",\"platform\":\"windows\",\"label\":\"ss laptop\"}"
  out=$SS_BODY
  [ "$SS_CODE" = 200 ] || die "POST /devices: HTTP $SS_CODE: $(redact <<<"$out" | head -c 200)"
  id=$(jq -r .device.id <<<"$out")
  jq -r '.configs[0].conf' <<<"$out" | head -1 | grep -q '^\[Interface\]' || die "the self-service answer carries no .conf"
  jq -r '.configs[0].conf' <<<"$out" | sed -n 's/^\(PrivateKey\|PresharedKey\|HeaderProtectionKey\) = //p' | while read -r k; do echo "$k"; done >"$WORK/ss.keys"
  while read -r k; do SECRETS+=("$k"); done <"$WORK/ss.keys"; rm -f "$WORK/ss.keys"
  user_of "$CAROL" | jq -e --arg d "$id" '[.devices[] | select(.id == $d)] | length == 1' >/dev/null || die "the admin API does not list the device the page created"
  # a mutating call without a JSON body or with a foreign type is refused
  curl -sk -m 10 -b "$WORK/ss.jar" -o /dev/null -w '%{http_code}' -X POST "$CAROL_URL/devices" -d 'x=1' | grep -q '^4' || die "a form post to /devices was not refused"
  ss_call "/devices/$id/configs" '{}'
  out=$SS_BODY
  [ "$SS_CODE" = 200 ] && jq -e '.configs | length == 1' <<<"$out" >/dev/null || die "configs: HTTP $SS_CODE"
  ss_call "/devices/$id/rotate" '{}'
  out=$SS_BODY
  [ "$SS_CODE" = 200 ] && jq -e '.configs | length == 1' <<<"$out" >/dev/null || die "rotate: HTTP $SS_CODE"
  ss_call "/devices/$id/rename" '{"label":"ss renamed"}'
  out=$SS_BODY
  [ "$SS_CODE" = 200 ] || die "rename: HTTP $SS_CODE"
  user_of "$CAROL" | jq -e --arg d "$id" '.devices[] | select(.id == $d) | .model == "ss renamed"' >/dev/null || die "the admin API does not show the new label"
  # a device of somebody else answers like one that does not exist
  ss_call "/devices/$DEV20/configs" '{}'
  [ "$SS_CODE" = 200 ] || die "the user's own second device: HTTP $SS_CODE"
  ss_call "/devices/dev_nope/configs" '{}'
  [ "$SS_CODE" = 404 ] || die "an unknown device id: HTTP $SS_CODE, want 404"
  ss_call "/devices/$id/revoke" '{}'
  out=$SS_BODY
  [ "$SS_CODE" = 200 ] && jq -e '.ok == true' <<<"$out" >/dev/null || die "revoke: HTTP $SS_CODE"
  user_of "$CAROL" | jq -e --arg d "$id" '[.devices[] | select(.id == $d)] | length == 0' >/dev/null || die "the revoked device is still listed"
  pass "self-service: add (conf in the answer, device visible to the admin), configs, rotate, rename, revoke, a form post refused, an unknown device 404"

  # an unknown token gets the decoy page and is counted as a miss
  local fake code
  fake=$(openssl rand -hex 16)
  code=$(curl -sk -m 10 -o "$WORK/ss-decoy.body" -w '%{http_code}' -X POST "https://localhost:$PUB/$fake/devices" -H 'Content-Type: application/json' -d '{}')
  [ "$code" = 404 ] || die "an unknown token: HTTP $code, want the decoy 404"
  ! grep -q '"error"' "$WORK/ss-decoy.body" || die "an unknown token got a JSON error instead of the decoy"
  pass "an unknown token: HTTP 404 with the decoy page, no JSON that would tell a real link from a fake one"

  # the switch
  local settings
  settings=$(api SubscriptionService/GetSubscriptionSettings '{}' | jq -c .settings)
  mutate SubscriptionService/UpdateSubscriptionSettings "$(jq -nc --argjson s "$settings" '{settings: ($s | .userPage.allowDeviceSelfService = false)}')" >/dev/null || die "could not switch self-service off"
  ss_call /devices "{\"profile_id\":\"$AWG31\",\"platform\":\"windows\",\"label\":\"off\"}"
  [ "$SS_CODE" = 403 ] || die "with self-service off, POST /devices answered HTTP $SS_CODE, want 403"
  ss_call /devices '{}'
  grep -q self_service_disabled <<<"$SS_BODY" || die "the 403 does not say self_service_disabled"
  mutate SubscriptionService/UpdateSubscriptionSettings "$(jq -nc --argjson s "$settings" '{settings: ($s | .userPage.allowDeviceSelfService = true)}')" >/dev/null || die "could not switch self-service on"
  pass "the admin switch: self-service off -> 403 self_service_disabled; on again"

  # the write budget (20 an hour per token): renames are cheap writes
  local n=0 got429=""
  for ((n = 0; n < 40; n++)); do
    ss_call "/devices/$DEV20/rename" '{"label":"limit"}'
    [ "$SS_CODE" = 429 ] && { got429=$n; break; }
  done
  [ -n "$got429" ] || die "40 self-service writes in a row never hit the limit"
  grep -qi '^retry-after: *[0-9]' "$WORK/ss.hdr" || die "the 429 carries no Retry-After"
  pass "the write limit: HTTP 429 with Retry-After after $got429 further writes (the budget is 20 an hour per token)"
}

# ---------------------------------------------------------------- per-device signature packets
# sig_verdict <conf> <quic|dns>: dissects the I1..I5 of a client config the way a protocol dissector would (tshark is not
# needed in the lab): prints a one-line verdict and fails on anything a real client would not send. quic: one datagram, an
# Initial of QUIC v1 of at least 1200 bytes whose Length field is exactly what follows; dns: three queries (A, AAAA,
# HTTPS) for the same name, each with an EDNS0 OPT record and its own id. The encrypted ClientHello inside the Initial
# (SNI, key share) is parsed by internal/panel/protocols/awg/mimicry TestRealQUICServerAnswersTheInitial against a real
# QUIC server.
sig_verdict() {
  python3 - "$1" "$2" <<'PY'
import re, struct, sys
conf, kind = sys.argv[1], sys.argv[2]
I = {}
for l in open(conf):
    m = re.match(r'(I[1-5]) = (.*)$', l.strip())
    if m:
        I[m.group(1)] = m.group(2)
def expand(s):
    assert re.fullmatch(r'(<[^>]*>)+', s), "text outside the tags: " + s[:40]
    out, ids = b"", 0
    for t in re.findall(r'<([^>]*)>', s):
        k, _, v = t.partition(" ")
        if k == "b":
            out += bytes.fromhex(v[2:])
        elif k in ("r", "rc", "rd"):
            out += b"\0" * int(v)
        elif k == "t":
            out += b"\0\0\0\0"
        else:
            raise SystemExit("unknown tag <%s>" % t)
    return out
def varint(d, i):
    n = 1 << (d[i] >> 6)
    v = d[i] & 0x3f
    for b in d[i + 1:i + n]:
        v = v << 8 | b
    return v, i + n
if kind == "quic":
    assert I.get("I1") and not any(I.get(k) for k in ("I2", "I3", "I4", "I5")), "a QUIC chain is one packet"
    d = expand(I["I1"])
    assert len(d) >= 1200, "the datagram is %d bytes, a client pads an Initial to 1200" % len(d)
    assert d[0] & 0xc0 == 0xc0 and (d[0] >> 4) & 3 == 0, "not a long-header Initial: %#x" % d[0]
    assert d[1:5] == b"\0\0\0\1", "not QUIC v1"
    dl = d[5]; assert 8 <= dl <= 20, "dcid length %d" % dl
    i = 6 + dl; sl = d[i]; assert sl <= 20, "scid length %d" % sl
    i += 1 + sl
    tok, i = varint(d, i); assert tok == 0, "an Initial of a first flight has no token"
    ln, i = varint(d, i)
    assert i + ln == len(d), "Length says %d, %d bytes follow" % (ln, len(d) - i)
    print("QUIC v1 Initial, %d bytes, dcid %d scid %d, Length field exact, one datagram, no malformed field" % (len(d), dl, sl))
else:
    names, types, ids = set(), [], set()
    pk = [I[k] for k in ("I1", "I2", "I3", "I4", "I5") if I.get(k)]
    assert len(pk) == 3, "a DNS chain is three packets, got %d" % len(pk)
    for s in pk:
        d = expand(s)
        ids.add(d[:2])
        assert d[2:12] == bytes([1, 0, 0, 1, 0, 0, 0, 0, 0, 1]), "header is not a standard query with one question and one additional"
        i, labels = 12, []
        while d[i]:
            labels.append(d[i + 1:i + 1 + d[i]].decode()); i += 1 + d[i]
        names.add(".".join(labels))
        qtype, qclass = struct.unpack(">HH", d[i + 1:i + 5]); assert qclass == 1
        types.append(qtype)
        i += 5
        assert d[i] == 0 and struct.unpack(">H", d[i + 1:i + 3])[0] == 41 and len(d) == i + 11, "no well-formed EDNS0 OPT record at the end"
    assert sorted(types) == [1, 28, 65], "types %s, want A, AAAA, HTTPS" % types
    assert len(names) == 1 and len(next(iter(names)).split(".")) >= 2, "names %s" % names
    print("DNS triplet for %s: A, AAAA, HTTPS (order %s), EDNS0 OPT in each, no malformed field" % (next(iter(names)), types))
PY
}

m3_awg_signatures() {
  step "tunnels.3b AWG per-device signature packets: a QUIC Initial (I1) and a DNS triplet (I1-I3), another set for every device; real clients handshake and pass traffic with them"
  mk_ns cli5 198.18.55
  local odns oquic dport qport want out
  # both presets are set explicitly: the default preset of a new profile is dns (its .conf fits a QR code), so a
  # profile that needs the QUIC Initial asks for quic, and the DNS one does not depend on what the default is
  odns=$(api AwgService/GenerateObfuscation '{"version":"3.1","preset":"dns","mtu":1280}' | jq -r .obfuscationJson)
  [ -n "$odns" ] && [ "$odns" != null ] || die "GenerateObfuscation(3.1, dns) returned nothing"
  oquic=$(api AwgService/GenerateObfuscation '{"version":"3.1","preset":"quic","mtu":1280}' | jq -r .obfuscationJson)
  [ -n "$oquic" ] && [ "$oquic" != null ] || die "GenerateObfuscation(3.1, quic) returned nothing"
  dport=$(free_port udp) qport=$(free_port udp)
  SIGDNS=$(make_profile awg e2e-awg-dns "$(awg_settings "$dport" ".obfuscation = ($odns | .per_device_signature = true)")")
  [ -n "$SIGDNS" ] && [ "$SIGDNS" != null ] || die "CreateProfile(awg, dns preset) returned no id"
  SIGQ=$(make_profile awg e2e-awg-quic "$(awg_settings "$qport" ".obfuscation = ($oquic | .per_device_signature = true)")")
  [ -n "$SIGQ" ] && [ "$SIGQ" != null ] || die "CreateProfile(awg, quic preset) returned no id"
  want=$(($(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq '.inbounds | length') + 2))
  make_inbound "$SIGDNS"
  make_inbound "$SIGQ"
  wait_for 90 "the DNS- and QUIC-preset inbounds ACTIVE" inbounds_active "$want" || die "the DNS- and QUIC-preset inbounds did not become ACTIVE"
  new_user frank "$(make_group e2e-awg-sig "[\"$SIGQ\",\"$SIGDNS\"]")"
  FRANK=$U_ID
  add_device "$FRANK" "$SIGQ" sigq1 || die "CreateAwgDevice (QUIC, first) failed"
  SIGQ1=$D_ID
  add_device "$FRANK" "$SIGQ" sigq2 || die "CreateAwgDevice (QUIC, second) failed"
  add_device "$FRANK" "$SIGDNS" sigdns || die "CreateAwgDevice (DNS) failed"
  local v1 v2
  v1=$(sig_verdict "$WORK/dev-sigq1.conf" quic) || die "the QUIC I1 of the first device is not a valid Initial: $v1"
  v2=$(sig_verdict "$WORK/dev-sigq2.conf" quic) || die "the QUIC I1 of the second device is not a valid Initial: $v2"
  local a b c
  a=$(conf_get "$WORK/dev-sigq1.conf" I1) b=$(conf_get "$WORK/dev-sigq2.conf" I1) c=$(conf_get "$WORK/dev-dev31.conf" I1)
  [ -n "$a" ] && [ "$a" != "$b" ] && [ "$a" != "$c" ] && [ "$b" != "$c" ] || die "devices of one profile share an I1: per-device signatures are not in effect"
  out=$(api DeviceService/GetDeviceConfigs "{\"deviceId\":\"$SIGQ1\"}")
  [ "$(jq -r '.configs[0].conf' <<<"$out" | sed -n 's/^I1 = //p')" = "$a" ] || die "showing a device's config again changed its I1"
  v2=$(sig_verdict "$WORK/dev-sigdns.conf" dns) || die "the DNS chain is not a valid triplet: $v2"
  say "first QUIC device: $v1"
  say "DNS device: $v2"
  GWQ=$(gw_of "$SIGQ") GWD=$(gw_of "$SIGDNS")
  T_HS=$(date +%s)
  start_awg_client sigq1 cli5 "$WORK/dev-sigq1.conf" 3.1 "$cli5_GW:$qport" || { tail -n 8 "$WORK/client-awg-sigq1.log" | redact; die "the per-device QUIC client did not start"; }
  SIGPID=$LAST_PID
  wait_for 40 "a ping through the tunnel with the per-device QUIC I1" ping_ok "$cli5_PID" "$GWQ" || { tail -n 8 "$WORK/client-awg-sigq1.log" | redact; die "no handshake with a per-device QUIC I1"; }
  HSD=$(($(date +%s) - T_HS))
  [ "$(hs_of "$AWG_SOCK")" -ge "$T_HS" ] || die "the client with the QUIC I1 counts no fresh handshake"
  got=$(cns "$cli5_PID" curl -s -m 60 -o /dev/null -w '%{size_download}' "http://$WHOAMI/bytes/5000000")
  [ "$got" = 5000000 ] || die "5 MB through the tunnel with the QUIC I1: $got bytes"
  pass "per-device QUIC I1 (profile with preset quic set explicitly): two devices carry two different Initials, both unlike the default profile's I1, each a valid QUIC v1 Initial; the client handshakes in $HSD s and moves 5 MB"
  stop_pid "$SIGPID"
  T_HS=$(date +%s)
  start_awg_client sigdns cli5 "$WORK/dev-sigdns.conf" 3.1 "$cli5_GW:$dport" || { tail -n 8 "$WORK/client-awg-sigdns.log" | redact; die "the per-device DNS client did not start"; }
  SIGPID=$LAST_PID
  wait_for 40 "a ping through the tunnel with the DNS triplet" ping_ok "$cli5_PID" "$GWD" || { tail -n 8 "$WORK/client-awg-sigdns.log" | redact; die "no handshake with a DNS triplet in I1-I3"; }
  HSD=$(($(date +%s) - T_HS))
  [ "$(hs_of "$AWG_SOCK")" -ge "$T_HS" ] || die "the client with the DNS triplet counts no fresh handshake"
  got=$(cns "$cli5_PID" curl -s -m 60 -o /dev/null -w '%{size_download}' "http://$WHOAMI/bytes/5000000")
  [ "$got" = 5000000 ] || die "5 MB through the tunnel with the DNS triplet: $got bytes"
  pass "per-device DNS triplet (I1-I3: A, AAAA, HTTPS for one name): valid queries, the client handshakes in $HSD s and moves 5 MB"
  stop_pid "$SIGPID"
}

# ================================================================ Mihomo
mihomo_bin() {
  local p=$M3_CACHE/mihomo-v1.19.32
  if [ ! -x "$p" ]; then
    mkdir -p "$M3_CACHE/mihomo-build"
    (cd "$WORK" && GOBIN=$M3_CACHE/mihomo-build GOFLAGS=-mod=mod GOTOOLCHAIN=auto go install github.com/metacubex/mihomo@v1.19.32) >"$WORK/build-mihomo.log" 2>&1 \
      && mv "$M3_CACHE/mihomo-build/mihomo" "$p" || return 1
  fi
  echo "$p"
}
m3_mihomo() {
  step "tunnels.4 Mihomo: the YAML profile of the panel runs in a real mihomo, one proxy at a time"
  local mh code
  if ! mh=$(mihomo_bin); then
    tail -n 3 "$WORK/build-mihomo.log" 2>/dev/null
    note "mihomo v1.19.32 is not in $M3_CACHE and could not be built: the Mihomo steps are skipped"
    return 0
  fi
  # the user gets the hysteria2 profile as well, so the profile has all three kinds of proxy
  mutate GroupService/UpdateGroup "$(jq -nc --arg g "$AWG_GROUP" --arg a "$AWG31" --arg b "$AWG20" --arg c "$PROFILE_ID" '{groupId:$g, profileIds:{values:[$a,$b,$c]}}')" >/dev/null \
    || die "could not add the hysteria2 profile to carol's group"
  have_yaml() { curl -sk -m 15 -A 'mihomo/1.19.32' -D "$WORK/mh.hdr" "$CAROL_URL" -o "$WORK/mihomo.yaml" && grep -q '^proxies:' "$WORK/mihomo.yaml" && python3 -c "
import sys, yaml
d = yaml.safe_load(open('$WORK/mihomo.yaml'))
sys.exit(0 if len(d.get('proxies') or []) >= 3 else 1)"; }
  wait_for 40 "a Mihomo profile with three proxies" have_yaml || die "no usable Mihomo profile: $(head -c 300 "$WORK/mihomo.yaml" | redact)"
  grep -qi '^content-type: text/yaml' "$WORK/mh.hdr" || die "the Mihomo profile is not served as text/yaml: $(grep -i '^content-type' "$WORK/mh.hdr")"
  grep -qi '^profile-title:' "$WORK/mh.hdr" || die "the Mihomo response has no profile-title"
  # the file holds secrets: its keys join the redaction list
  python3 - "$WORK/mihomo.yaml" >"$WORK/mh.keys" <<'PY'
import sys, yaml
for p in yaml.safe_load(open(sys.argv[1]))["proxies"]:
    for k in ("private-key", "pre-shared-key", "password", "auth"):
        if p.get(k): print(p[k])
    for k in (p.get("amnezia-wg-option") or {}):
        if k == "header-protection-key": print(p["amnezia-wg-option"][k])
PY
  while read -r k; do SECRETS+=("$k"); done <"$WORK/mh.keys"; rm -f "$WORK/mh.keys"
  python3 - "$WORK/mihomo.yaml" <<'PY' || die "the Mihomo profile is not what the clients expect (names, groups, dns, rules)"
import sys, yaml
d = yaml.safe_load(open(sys.argv[1]))
names = [p["name"] for p in d["proxies"]]
assert len(set(names)) == len(names), "duplicate proxy names"
types = sorted(p["type"] for p in d["proxies"])
assert "wireguard" in types and "hysteria2" in types, types
assert any(p["type"] == "wireguard" and (p.get("amnezia-wg-option") or {}).get("version") == 3 for p in d["proxies"]), "no AWG 3.x proxy"
assert any(p["type"] == "wireguard" and "version" not in (p.get("amnezia-wg-option") or {"version": 0}) for p in d["proxies"]), "no AWG 2.0 proxy (amnezia-wg-option without version)"
g = d["proxy-groups"][0]
assert g["type"] == "select" and set(names) <= set(g["proxies"]), g
assert d["dns"]["enable"] and d["rules"] and d["rules"][-1].startswith("MATCH,"), "dns/rules"
PY
  mkdir -p "$WORK/mh-test"
  "$mh" -t -d "$WORK/mh-test" -f "$WORK/mihomo.yaml" >"$WORK/mihomo-t.log" 2>&1 || { redact <"$WORK/mihomo-t.log" | tail -n 5; die "mihomo -t rejects the profile the panel served"; }
  pass "the Mihomo profile: text/yaml, three proxies (hysteria2, AWG 3.x, AWG 2.0), one select group, dns and rules, and \`mihomo -t\` accepts it as served"

  # one proxy at a time through a real mihomo in a namespace of its own: the profile's own hosts are the node's
  # address (127.0.0.1 here), so they are replaced by the veth gateway; everything else stays as served
  mk_ns cli3 198.18.53
  local count i n name port
  count=$(python3 -c "import yaml; print(len(yaml.safe_load(open('$WORK/mihomo.yaml'))['proxies']))")
  for ((i = 0; i < count; i++)); do
    port=$((20000 + i))
    name=$(python3 - "$WORK/mihomo.yaml" "$i" "$WORK/mh-$i.yaml" "$cli3_GW" "$port" <<'PY'
import sys, yaml
src, i, out, gw, port = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4], int(sys.argv[5])
d = yaml.safe_load(open(src))
for p in d["proxies"]:
    p["server"] = gw
d.update({"mixed-port": port, "allow-lan": False, "mode": "rule", "log-level": "info"})
d["rules"] = ["MATCH," + d["proxies"][i]["name"]]
yaml.safe_dump(d, open(out, "w"), allow_unicode=True)
print(d["proxies"][i]["type"] + (":awg" + str((d["proxies"][i].get("amnezia-wg-option") or {}).get("version", "")) if d["proxies"][i]["type"] == "wireguard" else ""))
PY
)
    mkdir -p "$WORK/mh-$i"
    spawn nsenter -t "$cli3_PID" -n -- "$mh" -d "$WORK/mh-$i" -f "$WORK/mh-$i.yaml" >"$WORK/client-mihomo-$i.log" 2>&1
    MH=$LAST_PID
    mh_log=$WORK/client-mihomo-$i.log
    mh_ready() { grep -q listening "$mh_log"; }
    wait_for 15 "mihomo to listen" mh_ready || { tail -n 5 "$WORK/client-mihomo-$i.log" | redact; die "mihomo ($name) did not start"; }
    mh_src() { cns "$cli3_PID" curl -s -m 20 --socks5-hostname "127.0.0.1:$port" "http://$WHOAMI/mihomo$i" 2>/dev/null | tr -d '\n'; }
    code=""
    for ((n = 0; n < 6; n++)); do
      code=$(mh_src || true)
      [ "$code" = "$DIRECT_SRC" ] && break
      sleep 2
    done
    if [ "$code" != "$DIRECT_SRC" ]; then
      tail -n 8 "$mh_log" | redact
      die "mihomo through $name: the fake internet saw '$code', expected the node ($DIRECT_SRC)"
    fi
    say "mihomo proxy $i ($name): the fake internet sees the node"
    stop_pid "$MH"
  done
  pass "real mihomo ($(basename "$mh")): all $count proxies of the served profile (hysteria2 with hop and pin, AWG 3.x, AWG 2.0) pass traffic to the fake internet via the node"
}

# ================================================================ WARP
m3_warp() {
  step "tunnels.5 WARP: a fake peer plays Cloudflare, hysteria2 and AWG leave through it, a dead link fails closed"
  local fw_priv fw_pub wn_priv wn_pub
  fw_priv=$(wg genkey) fw_pub=$(wg pubkey <<<"$fw_priv")
  wn_priv=$(wg genkey) wn_pub=$(wg pubkey <<<"$wn_priv")
  SECRETS+=("$fw_priv" "$wn_priv")
  # the peer: kernel WireGuard in the inet namespace, one allowed address (the account's), the probes of the
  # node (any port-80 traffic that arrives through the tunnel) are answered by whoami as Cloudflare's trace
  cns "$inet_PID" ip link add wgfake type wireguard
  cns "$inet_PID" wg set wgfake private-key <(printf %s "$fw_priv") listen-port 2408 peer "$wn_pub" allowed-ips "$WARP_SRC/32"
  cns "$inet_PID" ip link set wgfake up
  cns "$inet_PID" ip route add "$WARP_SRC/32" dev wgfake
  cns "$inet_PID" sysctl -qw net.ipv4.conf.all.rp_filter=0
  cns "$inet_PID" nft -f - <<NFT
table ip mg3lab {
  chain pre {
    type nat hook prerouting priority dstnat; policy accept;
    iifname "wgfake" tcp dport 80 ip daddr != $WHOAMI dnat to $WHOAMI:80
  }
}
NFT

  # the hysteria2 and AWG profiles that leave through WARP, before the account exists: the agent must refuse to
  # start them (no silent fallback to a direct exit)
  local hp ap hport aport want
  hport=$(free_port udp) aport=$(free_port udp)
  hp=$(make_profile hysteria2 e2e-hy2-warp "$(echo "$SETTINGS" | jq -c --argjson p "$hport" '.tls_mode = "self_signed" | .port = $p | .hop = {from: 0, to: 0} | .egress = "warp"')")
  ap=$(make_profile awg e2e-awg-warp "$(awg_settings "$aport" '.egress = "warp"')")
  [ -n "$hp" ] && [ -n "$ap" ] || die "could not create the WARP profiles"
  HY2WARP=$hp AWGWARP=$ap
  want=$(($(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq '.inbounds | length') + 2))
  make_inbound "$hp"
  make_inbound "$ap"
  refused() { inbound_of "$1" | jq -e '.state == "INBOUND_STATE_FAILED" and (.lastError | test("warp"))'; }
  wait_for 60 "the hysteria2 egress=warp inbound to fail without an account" refused "$hp" || die "without a WARP account the hy2 inbound is: $(inbound_of "$hp" | jq -c '{state, lastError}')"
  wait_for 60 "the awg egress=warp inbound to fail without an account" refused "$ap" || die "without a WARP account the awg inbound is: $(inbound_of "$ap" | jq -c '{state, lastError}')"
  pass "egress=warp without an account: both inbounds FAILED ($(inbound_of "$hp" | jq -r .lastError | head -c 60)), nothing falls back to a direct exit"

  # the account: an imported wgcf profile pointing at the fake peer
  local conf
  conf=$(printf '[Interface]\nPrivateKey = %s\nAddress = %s/32\nMTU = 1280\n\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s:2408\n' "$wn_priv" "$WARP_SRC" "$fw_pub" "$inet_IP")
  mutate WarpService/ImportWarp "$(jq -nc --arg n "$NODE_ID" --arg c "$conf" '{nodeId:$n, profileConf:$c}')" >"$WORK/warp-import.json" || die "ImportWarp failed"
  ! grep -qF "$wn_priv" "$WORK/warp-import.json" || die "the ImportWarp answer carries the private key"
  jq -e '.account.source == "WARP_SOURCE_IMPORTED" and .account.addressV4 == "'"$WARP_SRC"'/32"' "$WORK/warp-import.json" >/dev/null || die "ImportWarp answered: $(jq -c .account "$WORK/warp-import.json")"
  warp_up() { api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -e '.health.state == "WARP_STATE_UP"'; }
  wait_for 120 "WARP UP" warp_up || die "WARP did not come up: $(api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -c '{agentSupports, pendingApply, applyError, health: .health | {state, lastError, lastHandshakeUnix, probeCloudflareOk, probeOtherOk}}')"
  api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -e '.agentSupports == true and .health.probeCloudflareOk == true and .health.probeOtherOk == true and .health.warpFlag == "on"' >/dev/null || die "GetWarp: the probes are not both green"
  ip -o link show mgwarp >/dev/null 2>&1 || die "no mgwarp link in the run's namespace"
  ip rule show | grep -q 'lookup 51820' || die "no ip rule into the WARP table"
  nft list table inet mistgate_warp >/dev/null 2>&1 || nft list table ip mistgate_warp >/dev/null 2>&1 || die "no nft table mistgate_warp"
  api NodeService/ListNodes '{}' | jq -e --arg n "$NODE_NAME" '.nodes[] | select(.name == $n) | .warp.state == "WARP_STATE_UP"' >/dev/null || die "the node list does not show WARP UP"
  pass "ImportWarp (private key not echoed): WARP UP, probe A warp=on and probe B ok, mgwarp, ip rule and nft table in place, the node list shows it"
  wait_for 90 "the WARP inbounds to start" inbounds_active_all || die "inbounds after the account: $(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[] | select(.state != "INBOUND_STATE_ACTIVE") | {protocol, state, lastError}')"

  # users: bob leaves through WARP, dave directly (control), erin through AWG into WARP
  local g
  g=$(make_group e2e-warp "[\"$hp\"]")
  new_user bob "$g"
  BOB_URL=$U_URL
  g=$(make_group e2e-direct "[\"$PROFILE_ID\"]")
  new_user dave "$g"
  DAVE_URL=$U_URL
  g=$(make_group e2e-awgwarp "[\"$ap\"]")
  new_user erin "$g"
  ERIN=$U_ID
  sub_uri() { curl -sk -m 15 -A 'Happ/3.0' "$1" | base64 -d 2>/dev/null | grep -m1 '^hysteria2://'; }
  have_uri() { [ -n "$(sub_uri "$1")" ]; }
  wait_for 40 "bob's hysteria2 line" have_uri "$BOB_URL" || die "bob's subscription has no hysteria2 line"
  wait_for 40 "dave's hysteria2 line" have_uri "$DAVE_URL" || die "dave's subscription has no hysteria2 line"
  local bob_uri dave_uri
  bob_uri=$(sub_uri "$BOB_URL") dave_uri=$(sub_uri "$DAVE_URL")
  for u in "$bob_uri" "$dave_uri"; do
    SECRETS+=("$(URI=$u python3 -c 'import os; from urllib.parse import urlsplit, unquote; print(unquote(urlsplit(os.environ["URI"]).username or ""))')")
    SECRETS+=("$(URI=$u python3 -c 'import os; from urllib.parse import urlsplit, parse_qs; print(parse_qs(urlsplit(os.environ["URI"]).query).get("obfs-password", [""])[0])')")
  done
  BOB_SOCKS=$(free_port tcp) DAVE_SOCKS=$(free_port tcp)
  URI=$bob_uri start_client bob "$BOB_SOCKS" || { tail -n 8 "$WORK/client-bob.log" | redact; die "bob's hysteria2 client did not connect"; }
  BOB_C=$LAST_PID
  URI=$dave_uri start_client dave "$DAVE_SOCKS" || { tail -n 8 "$WORK/client-dave.log" | redact; die "dave's hysteria2 client did not connect"; }
  DAVE_C=$LAST_PID
  add_device "$ERIN" "$ap" erin || die "CreateAwgDevice for erin failed"
  mk_ns cli4 198.18.54
  start_awg_client erin cli4 "$WORK/dev-erin.conf" 3.1 "$cli4_GW:$aport" || { tail -n 8 "$WORK/client-awg-erin.log" | redact; die "erin's client did not start"; }
  ERIN_C=$LAST_PID
  GWE=$(gw_of "$ap")
  wait_for 40 "erin's tunnel" ping_ok "$cli4_PID" "$GWE" || die "erin's AWG client does not handshake"
  BOB_SRC=$(who_socks "$BOB_SOCKS" bob) DAVE_SRC=$(who_socks "$DAVE_SOCKS" dave) ERIN_SRC=$(who_from "$cli4_PID" erin)
  [ "$BOB_SRC" = "$WARP_SRC" ] || die "hysteria2 egress=warp: the fake internet saw '$BOB_SRC', expected the WARP address $WARP_SRC"
  [ "$DAVE_SRC" = "$DIRECT_SRC" ] || die "hysteria2 direct: the fake internet saw '$DAVE_SRC', expected the node $DIRECT_SRC"
  [ "$ERIN_SRC" = "$WARP_SRC" ] || die "AWG egress=warp: the fake internet saw '$ERIN_SRC', expected the WARP address $WARP_SRC"
  pass "exits: hysteria2 egress=warp -> $BOB_SRC (WARP), hysteria2 direct -> $DAVE_SRC (node), AWG egress=warp -> $ERIN_SRC (WARP)"

  # ---- fail closed: the link of the tunnel goes down
  ip link set mgwarp down
  T_DOWN=$(date +%s)
  sleep 2
  local b e d
  b=$(who_socks "$BOB_SOCKS" bob-down1) e=$(who_from "$cli4_PID" erin-down1) d=$(who_socks "$DAVE_SOCKS" dave-down1)
  [ -z "$b" ] || die "hysteria2 egress=warp with the link down: the fake internet still answered '$b'"
  [ -z "$e" ] || die "AWG egress=warp with the link down: the fake internet still answered '$e'"
  [ "$d" = "$DIRECT_SRC" ] || die "the direct hysteria2 profile broke with the WARP link down (saw '$d')"
  pass "link down: hysteria2 and AWG clients on egress=warp get nothing (no direct exit), the direct profile is unaffected"
  # ... and whatever else was asked meanwhile never left the node directly
  for ((i = 0; i < 3; i++)); do who_socks "$BOB_SOCKS" bob-down2 >/dev/null; who_from "$cli4_PID" erin-down2 >/dev/null; sleep 1; done
  local leaks
  leaks=$(awk '$3 ~ /^\/(bob|erin)/ && $2 != "'"$WARP_SRC"'" {print $3}' "$WORK/whoami.log" | wc -l)
  [ "$leaks" = 0 ] || die "$leaks request(s) of the WARP users reached the fake internet by another way than WARP: $(log_srcs bob) $(log_srcs erin)"
  # the link comes back: the manager (30 s health loop, re-assert after three failures) must restore the route on its own
  ip link set mgwarp up
  T_UP=$(date +%s)
  seen_down=""
  healed() {
    api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -e '.health.state == "WARP_STATE_DOWN"' >/dev/null 2>&1 && seen_down=1
    [ "$(who_socks "$BOB_SOCKS" bob-heal)" = "$WARP_SRC" ]
  }
  wait_for 300 "the manager to restore the route after the link came back" healed || die "5 minutes after the link came back, egress=warp still fails closed: $(api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.health | {state, lastError}')"
  HEAL=$(($(date +%s) - T_UP))
  wait_for 30 "AWG egress=warp restored" sh -c "[ \"\$(nsenter -t $cli4_PID -n -- curl -s -m 8 http://$WHOAMI/erin-heal | tr -d '\n')\" = $WARP_SRC ]" || die "the AWG client's WARP exit did not come back"
  pass "link up again: the route came back on its own after ${HEAL} s (the panel saw the tunnel DOWN meanwhile: ${seen_down:-no, it healed between two health reports})"

  # ---- pause: the owner switches WARP off, nothing leaks, nothing is lost for good
  mutate WarpService/SetWarpEnabled "{\"nodeId\":\"$NODE_ID\",\"enabled\":false}" >/dev/null || die "SetWarpEnabled(false) failed"
  paused() { [ -z "$(who_socks "$BOB_SOCKS" bob-pause)" ] && ! ip -o link show mgwarp >/dev/null 2>&1; }
  wait_for 60 "the paused tunnel to cut the WARP users" paused || die "after the pause the WARP users still get through or the link is still there"
  [ -z "$(who_from "$cli4_PID" erin-pause)" ] || die "after the pause the AWG user of egress=warp still reaches the fake internet"
  [ "$(who_socks "$DAVE_SOCKS" dave-pause)" = "$DIRECT_SRC" ] || die "the direct profile broke when WARP was paused"
  api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -e '.account != null and (.account.enabled // false) == false' >/dev/null || die "GetWarp does not show the pause"
  mutate WarpService/SetWarpEnabled "{\"nodeId\":\"$NODE_ID\",\"enabled\":true}" >/dev/null || die "SetWarpEnabled(true) failed"
  resumed() { [ "$(who_socks "$BOB_SOCKS" bob-resume)" = "$WARP_SRC" ] && [ "$(who_from "$cli4_PID" erin-resume)" = "$WARP_SRC" ]; }
  wait_for 120 "WARP to resume" resumed || die "WARP did not resume after SetWarpEnabled(true)"
  leaks=$(awk '$3 ~ /^\/(bob|erin)/ && $2 != "'"$WARP_SRC"'" {print $3}' "$WORK/whoami.log" | wc -l)
  [ "$leaks" = 0 ] || die "$leaks request(s) of the WARP users left the node by another way than WARP during the whole test"
  pass "pause: link removed, WARP users cut and nothing direct for them; resume: both exits are back; over the whole test no request of a WARP user reached the fake internet except through WARP"
  for p in "$BOB_C" "$DAVE_C"; do stop_pid "$p"; done
  stop_pid "$ERIN_C"
}

# ================================================================ an old node
m3_compat() {
  step "tunnels compatibility: a node that cannot do awg/1 or warp/1 is never sent either"
  local port ap in err
  port=$(free_port udp)
  ap=$(make_profile awg e2e-awg-old "$(awg_settings "$port")")
  [ -n "$ap" ] || die "CreateProfile(awg) failed on a panel with an old node"
  make_inbound "$ap"
  old_refused() { inbound_of "$ap" | jq -e '.state == "INBOUND_STATE_FAILED" and (.lastError | startswith("agent_too_old"))'; }
  wait_for 60 "the awg inbound on the old node to be refused" old_refused || die "the awg inbound on an old node is: $(inbound_of "$ap" | jq -c '{state, lastError}')"
  node_online || die "the old node is not ONLINE after an awg inbound was added"
  api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -e '[.inbounds[] | select(.protocol == "hysteria2")] | all(.state == "INBOUND_STATE_ACTIVE")' >/dev/null || die "the hysteria2 inbounds of the old node are no longer ACTIVE"
  ! grep -qi 'awg' "$WORK/node.log" || die "the old node's log mentions awg: it was sent something: $(grep -i awg "$WORK/node.log" | head -2)"
  pass "old node: the awg inbound is FAILED with agent_too_old ($(inbound_of "$ap" | jq -r .lastError | cut -d: -f1)), the node stays ONLINE, hysteria2 untouched, nothing awg in the node's log"
  g=$(make_group e2e-old "[\"$ap\"]")
  new_user olga "$g"
  err=$(api DeviceService/CreateAwgDevice "$(jq -nc --arg u "$U_ID" --arg p "$ap" '{userId:$u, profileId:$p, platform:"linux", label:"x"}')" 2>&1) && die "CreateAwgDevice succeeded against a node that cannot run awg"
  grep -qi 'failed_precondition' <<<"$err" || die "CreateAwgDevice on an old node: expected failed_precondition, got: $err"
  local tos
  tos=$(api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -r .tosUrl)
  mutate_to err WarpService/RegisterWarp "{\"nodeId\":\"$NODE_ID\",\"acceptTos\":true,\"tosUrlShown\":\"$tos\"}" && die "RegisterWarp succeeded for an agent that cannot run warp/1"
  grep -qi 'failed_precondition' <<<"$err" || die "RegisterWarp on an old node: expected failed_precondition, got: $err"
  # a well-formed profile (random keys, an address of the benchmark range): the refusal must be about the agent
  local conf
  conf=$(printf '[Interface]\nPrivateKey = %s\nAddress = 172.16.0.2/32\n\n[Peer]\nPublicKey = %s\nEndpoint = 198.18.0.1:2408\n' "$(openssl rand -base64 32)" "$(openssl rand -base64 32)")
  mutate_to err WarpService/ImportWarp "$(jq -nc --arg n "$NODE_ID" --arg c "$conf" '{nodeId:$n, profileConf:$c}')" && die "ImportWarp succeeded for an agent that cannot run warp/1"
  grep -qi 'failed_precondition' <<<"$err" || die "ImportWarp on an old node: expected failed_precondition, got: $err"
  api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -e '(.agentSupports // false) == false and (.account == null)' >/dev/null || die "GetWarp on an old node: $(api WarpService/GetWarp "{\"nodeId\":\"$NODE_ID\"}" | jq -c .)"
  pass "old node: CreateAwgDevice and RegisterWarp are refused (failed_precondition), ImportWarp is refused, GetWarp says agentSupports=false and holds no account"
}
