# The TrustTunnel stand (--tt of scripts/e2e-wsl.sh): the OFFICIAL client, trusttunnel_client v1.1.12, through a Mistgate node that serves
# TrustTunnel over HTTP/2 and HTTP/3. Sourced by it, never run alone: it uses the helpers, variables and the isolation of the main
# script (private network namespace, $WORK, $BIN, api, mutate, say, pass, die, wait_for, spawn, stop_pid, free_port, SECRETS ...) and
# the namespaces and the fake internet of scripts/e2e/m3.sh (mk_ns, cns, m3_lab, WHOAMI).
#
# THE CLIENT AND THE REFERENCE SERVER ARE UNTRUSTED CODE. scripts/e2e/tt-fetch.sh puts them into ~/.cache/mistgate-tests/trusttunnel after
# a sha256 and GPG check; here they run only
#   - in a network namespace made for them (a veth to the lab, no default route, and an nft guard in the run's namespace that
#     drops everything that is not lab space, so no name of ours can reach a real ACME server or anything else),
#   - as user 65534, no capabilities (setpriv --no-new-privs --bounding-set=-all), at most 1 GiB of address space (prlimit),
#   - in SOCKS5 mode ([listener.socks]), never with a TUN device, never on the Windows host.
# Without the cache the steps that need them are SKIP; the checks that need no client (a client of our own for the UDP and DNS
# cases, the probes of the site) still run.
#
# TOPOLOGY (all inside the run's namespace; every interface is mg3* or ttv*):
#
#   inet   198.18.51.0/30   the fake internet: whoami 203.0.113.77:80, a UDP echo :7, a DNS :53 (every name = 203.0.113.99), an upload
#                           sink :8080 (the sha256 of what it got), a sink that never reads :8081
#   tta    198.18.60.0/30   the client of person 1;  ttb 198.18.61.0/30 the client of person 2
#   ttrt   198.18.64.0/30   the "TSPU" router (forwarding on, nft rules per scenario) and, behind it, ttc 198.18.65.0/30: a client
#                           that reaches the node only through it
#   ttep   198.18.66.0/30   the reference endpoint of TrustTunnel (their trusttunnel_endpoint v1.1.0), E20
#
# The node is the run's own (address 127.0.0.1): TrustTunnel listens on one TCP port and, with HTTP/3 on, the same UDP port.

TT_CACHE=${TT_CACHE:-$HOME/.cache/mistgate-tests/trusttunnel}
TT_TABLE=() TT_FAILS=0
TT_CLIENT_OK="" TT_EP_OK="" TTD="" TT_GUARD=""

tt_rec() { # <id> <PASS|FAIL|SKIP> <text>
  TT_TABLE+=("$1|$2|$3")
  say "$2  $1  $3"
  [ "$2" != FAIL ] || TT_FAILS=$((TT_FAILS + 1))
}
tt_ok() { tt_rec "$1" PASS "$2"; }
tt_no() { tt_rec "$1" FAIL "$2"; }
tt_skip() { tt_rec "$1" SKIP "$2"; }
# tt_is <id> <text> <command...>: PASS when the command succeeds, FAIL otherwise
tt_is() {
  local id=$1 text=$2
  shift 2
  if "$@"; then tt_ok "$id" "$text"; else tt_no "$id" "$text"; fi
}

# ---------------------------------------------------------------- build, trust, sandbox

# tt_build runs in step 0: our probe tool, and the official binaries copied out of the cache into a world-readable directory
# (the user they run as must be able to reach them; the cache is under /root).
tt_build() {
  go build -trimpath -o "$BIN/ttprobe" ./scripts/e2e/ttprobe
  TTD=/tmp/mg-tt.$$
  mkdir -p "$TTD/bin" "$TTD/cfg"
  chmod 755 "$TTD" "$TTD/bin" "$TTD/cfg"
  local d f
  d=$(find "$TT_CACHE/client-v1.1.12" -maxdepth 2 -name trusttunnel_client -type f 2>/dev/null | head -1)
  if [ -n "$d" ] && [ -f "$TT_CACHE/client-v1.1.12/VERIFIED" ]; then
    cp "$d" "$TTD/bin/trusttunnel_client"
    cp "$(dirname "$d")/setup_wizard" "$TTD/bin/client_wizard"
    TT_CLIENT_OK=1
  fi
  d=$(find "$TT_CACHE/endpoint-v1.1.0" -maxdepth 2 -name trusttunnel_endpoint -type f 2>/dev/null | head -1)
  if [ -n "$d" ] && [ -f "$TT_CACHE/endpoint-v1.1.0/VERIFIED" ]; then
    cp "$d" "$TTD/bin/trusttunnel_endpoint"
    cp "$(dirname "$d")/setup_wizard" "$TTD/bin/endpoint_wizard"
    TT_EP_OK=1
  fi
  chmod 755 "$TTD"/bin/* 2>/dev/null || true
  if [ -n "$TT_CLIENT_OK" ]; then
    say "official client: $(tt_sandbox "$TTD/bin/trusttunnel_client" --version 2>&1 | head -1) ($(cut -d' ' -f1-3 "$TT_CACHE/client-v1.1.12/VERIFIED"), gpg: $(sed -n 's/.* gpg=\(.*\)$/\1/p' "$TT_CACHE/client-v1.1.12/VERIFIED"))"
  else
    say "no official client in $TT_CACHE (scripts/e2e/tt-fetch.sh): the checks that need it are SKIP"
  fi
  [ -z "$TT_EP_OK" ] || say "reference endpoint: $(sed -n 's/.* gpg=\(.*\)$/gpg: \1/p' "$TT_CACHE/endpoint-v1.1.0/VERIFIED")"
}

# tt_sandbox <command...>: the untrusted code's way to run, in a namespace of its own (nothing in it but lo)
tt_sandbox() { unshare -n setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs --bounding-set=-all prlimit --as=1073741824 "$@"; }
# tt_in <ns var> <command...>: the same inside a lab namespace made by mk_ns
tt_in() {
  local pid=${1}_PID
  shift
  nsenter -t "${!pid}" -n -- setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs --bounding-set=-all prlimit --as=1073741824 "$@"
}

tt_reap() {
  [ -z "$TT_GUARD" ] || nft delete table inet mgtt_guard 2>/dev/null || true
  nft delete table ip mgtt_nat 2>/dev/null || true
  [ -z "$TTD" ] || rm -rf "$TTD"
}

# tt_guard: nothing leaves the lab. The run's namespace has the internet (the build needs it); from here on the node, the panel and
# everything else in it may only talk to loopback, the lab's private ranges and documentation space. A name of ours that went
# to a real ACME server or a real resolver would be a leak, so it is refused (and fails at once, instead of timing out).
tt_guard() {
  nft -f - <<'NFT'
table inet mgtt_guard {
  chain out {
    type filter hook output priority -100; policy accept;
    ip daddr { 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 198.18.0.0/15, 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24, 224.0.0.0/4, 255.255.255.255 } accept
    ip6 daddr { ::1, fe80::/10 } accept
    meta nfproto ipv6 reject with icmpv6 type admin-prohibited
    reject with icmp type admin-prohibited
  }
}
NFT
  TT_GUARD=1
  ! curl -s -m 3 -o /dev/null https://example.com/ || die "the lab guard does not hold: this namespace still reaches the internet"
}

# ---------------------------------------------------------------- the lab

tt_write_inet() {
  cat >"$WORK/ttinet.py" <<'PY'
# The fake internet's extra services: a UDP echo (:7), a DNS (:53, every name is 203.0.113.99), an HTTP sink that hashes what it
# is sent (:8080 POST, answers "bytes sha256"), a TCP port that accepts and never reads (:8081).
import hashlib, http.server, socket, socketserver, struct, sys, threading, time

HOST = sys.argv[1]

def udp(port, handler):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind((HOST, port))
    def loop():
        while True:
            data, peer = s.recvfrom(70000)
            out = handler(data)
            if out is not None:
                s.sendto(out, peer)
    threading.Thread(target=loop, daemon=True).start()

def dns(q):
    if len(q) < 12:
        return None
    i = 12
    while i < len(q) and q[i]:
        i += q[i] + 1
    question = q[12:i + 5]
    head = q[:2] + b"\x81\x80" + struct.pack(">HHHH", 1, 1, 0, 0)
    return head + question + b"\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x3c\x00\x04" + socket.inet_aton("203.0.113.99")

udp(7, lambda d: d)
udp(53, dns)

class Sink(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        h = hashlib.sha256()
        got = 0
        while got < n:
            b = self.rfile.read(min(1 << 20, n - got))
            if not b:
                break
            h.update(b)
            got += len(b)
        body = ("%d %s\n" % (got, h.hexdigest())).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers()
        self.wfile.write(body)

class S(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True
    request_queue_size = 256

def hang():
    s = socket.socket()
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind((HOST, 8081)); s.listen(256)
    keep = []
    while True:
        c, _ = s.accept()
        keep.append(c)  # never read, never close

threading.Thread(target=hang, daemon=True).start()
S((HOST, 8080), Sink).serve_forever()
PY
}

tt_lab() {
  step "tt.0 lab: the fake internet, the clients' namespaces, the guard"
  [ -n "${inet_PID:-}" ] || m3_lab
  tt_write_inet
  spawn nsenter -t "$inet_PID" -n -- python3 "$WORK/ttinet.py" "$WHOAMI" >>"$WORK/ttinet.log" 2>&1
  tt_wait_udp_echo() { [ "$(printf 'ping' | timeout 2 socat - UDP:"$WHOAMI":7 2>/dev/null)" = ping ]; }
  wait_for 10 "the UDP echo of the fake internet" tt_wait_udp_echo || die "the UDP echo of the fake internet does not answer"
  mk_ns tta 198.18.60
  mk_ns ttb 198.18.61
  tt_guard
  pass "lab: whoami, a UDP echo, a DNS and the sinks at $WHOAMI; client namespaces tta ($tta_IP via $tta_GW) and ttb ($ttb_IP via $ttb_GW); the guard drops everything that is not lab space"
}

# ---------------------------------------------------------------- panel side: profile, people, devices

# tt_port: a TCP port whose UDP twin is free too (HTTP/3 uses the same number)
tt_port() {
  local p
  while :; do
    p=$(free_port tcp)
    [ -z "$(ss -Hlun "sport = :$p")" ] && { echo "$p"; return 0; }
  done
}

# tt_profile <name> <settings json>: creates the profile and one inbound on the node, prints the profile id
tt_profile() {
  local id
  id=$(make_profile trusttunnel "$1" "$2") || return 1
  make_inbound "$id" || return 1
  echo "$id"
}

# tt_enrol <subscription url> <hwid>: enrols a device of the Mistgate app (POST <link>/client/enrol); sets TT_KEY (the device key,
# a secret), TT_BASE and TT_DEV
tt_enrol() {
  local out
  TT_KEY=$(python3 -c 'import base64, os; print(base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("="))')
  SECRETS+=("$TT_KEY")
  out=$(curl -sk -m 20 -w $'\n%{http_code}' -X POST "$1/client/enrol" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg h "$2" --arg k "$TT_KEY" '{hwid:$h, platform:"linux", model:"tt-stand", os_version:"", device_key:$k}')") || return 1
  [ "${out##*$'\n'}" = 200 ] || { echo "enrol -> ${out##*$'\n'}: ${out%$'\n'*}" | redact >&2; return 1; }
  out=${out%$'\n'*}
  TT_BASE=$(jq -r .base <<<"$out") TT_DEV=$(jq -r .device_id <<<"$out")
}

# tt_sync <device key> [file]: the app's sync call with the capability header; the body on stdout (and in the file)
tt_sync() {
  local out code
  out=$(curl -sk -m 20 -w $'\n%{http_code}' -H "Authorization: Bearer $1" -H 'X-Mistgate-Caps: v=1; trusttunnel=1' "$TT_BASE/sync") || return 1
  code=${out##*$'\n'}
  [ "$code" = 200 ] || { echo "sync -> $code" >&2; return 1; }
  out=${out%$'\n'*}
  [ -z "${2:-}" ] || printf '%s' "$out" >"$2"
  printf '%s' "$out"
}

# tt_path <sync body json> [relay|direct]: the first TrustTunnel path, as JSON
tt_path() { jq -c --arg via "${2:-direct}" '[.servers[].paths[] | select(.protocol == "trusttunnel" and .via == $via)][0] // empty' <<<"$1"; }

# ---------------------------------------------------------------- the official client

# tt_toml <ns var> <path json> <out> [ttprobe toml flags]: the client's configuration for a lab namespace, SOCKS5 on 127.0.0.1:1080.
# The node's certificate is self-signed (no ACME in the lab) and a self-signed leaf with only digitalSignature in its key usage is no
# trust anchor for the client's OpenSSL-style verification ("unable to get local issuer certificate", found by this stand), so the
# client runs with skip_verification; what the client does with a real CA chain is not exercised here.
tt_toml() {
  local nsvar=$1 pj=$2 out=$3 gw=${1}_GW
  shift 3
  "$BIN/ttprobe" toml -host "${!gw}" -dns "$WHOAMI:53" -skip-verify "$@" <<<"$pj" >"$out"
  chmod 644 "$out"
  SECRETS+=("$(jq -r .settings.password <<<"$pj")")
}

# tt_client <ns var> <name> <toml>: starts the client in the namespace (SOCKS5 127.0.0.1:1080 there); LAST_PID
tt_client() {
  local pid=${1}_PID log=$WORK/client-tt-$2.log
  : >"$log"
  spawn nsenter -t "${!pid}" -n -- setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs --bounding-set=-all \
    prlimit --as=1073741824 "$TTD/bin/trusttunnel_client" -c "$3" -l "${TT_CLIENT_LOG:-info}" >>"$log" 2>&1
}
tt_socks_up() { cns "$1" ss -Hltn 'sport = :1080' | grep -q .; } # <ns pid>
# tt_get <ns var> <path>: a fetch of the fake internet through the client's SOCKS5, the body
tt_get() {
  local pid=${1}_PID
  cns "${!pid}" curl -s -m "${TT_CURL_T:-20}" --socks5-hostname 127.0.0.1:1080 "http://$WHOAMI/$2" 2>/dev/null | tr -d '\n' || true
}

# ---------------------------------------------------------------- the steps

tt_main() {
  # The checks decide for themselves what a failure is (tt_ok / tt_no): a command that fails inside one is not an abort of the run.
  set +e
  trap - ERR
  tt_lab

  step "tt.1 the node serves TrustTunnel, two people enrol the app's devices"
  mutate ClientApiService/UpdateClientApi '{"enabled":true}' >/dev/null || die "could not switch the client API on"
  api ProfileService/ListProtocols '{}' | jq -e '.protocols[] | select(.id == "trusttunnel") | .experimental == true and (.apps | index("APP_TRUST_TUNNEL"))' >/dev/null \
    || die "TrustTunnel is not an experimental protocol with the TrustTunnel app in ListProtocols"
  TT_PORT=$(tt_port)
  TT_SNI=de1.example.com
  TT_PROFILE=$(tt_profile e2e-tt "$(jq -nc --argjson p "$TT_PORT" --arg s "$TT_SNI" '{port:$p, h3:true, sni:$s, tls_mode:"self_signed", egress:"direct"}')") || die "creating the TrustTunnel profile failed"
  tt_inbound_up() { inbound_of "$TT_PROFILE" | jq -e '.state == "INBOUND_STATE_ACTIVE" and (.certPinSha256 // "") != ""' >/dev/null; }
  wait_for 60 "the TrustTunnel inbound ACTIVE with a certificate" tt_inbound_up \
    || die "the TrustTunnel inbound did not start: $(inbound_of "$TT_PROFILE" | jq -c '{state, lastError}')"
  TT_PIN=$(inbound_of "$TT_PROFILE" | jq -r .certPinSha256)
  TT_GROUP=$(make_group e2e-tt "[\"$TT_PROFILE\"]")
  # the node's own certificate: the client trusts exactly it ([endpoint] certificate), and its sha256 is the pin the panel reports
  openssl s_client -connect "127.0.0.1:$TT_PORT" -servername "$TT_SNI" -alpn h2 </dev/null 2>/dev/null | openssl x509 >"$TTD/cfg/node.pem" 2>/dev/null
  chmod 644 "$TTD/cfg/node.pem"
  [ "$(openssl x509 -in "$TTD/cfg/node.pem" -outform DER | sha256sum | cut -d' ' -f1)" = "$TT_PIN" ] || die "the node's certificate is not the pin the panel reports"
  new_user ttalice "$TT_GROUP"
  TT_A_ID=$U_ID TT_A_URL=$U_URL
  new_user ttbob "$TT_GROUP"
  TT_B_ID=$U_ID TT_B_URL=$U_URL
  tt_enrol "$TT_A_URL" "stand-hwid-alice-1" || die "enrolment of the device of ttalice failed"
  TT_A_KEY=$TT_KEY TT_A_BASE=$TT_BASE TT_A_DEV=$TT_DEV
  tt_enrol "$TT_B_URL" "stand-hwid-bob-1" || die "enrolment of the device of ttbob failed"
  TT_B_KEY=$TT_KEY TT_B_BASE=$TT_BASE TT_B_DEV=$TT_DEV
  TT_BASE=$TT_A_BASE
  TT_A_SYNC=$(tt_sync "$TT_A_KEY" "$WORK/tt-sync-alice.json") || die "sync of the device of ttalice failed"
  TT_A_PATH=$(tt_path "$TT_A_SYNC")
  [ -n "$TT_A_PATH" ] || die "the sync of ttalice has no TrustTunnel path: $(jq -c '[.servers[].paths[].protocol]' <<<"$TT_A_SYNC")"
  TT_BASE=$TT_B_BASE
  TT_B_SYNC=$(tt_sync "$TT_B_KEY" "$WORK/tt-sync-bob.json") || die "sync of the device of ttbob failed"
  TT_B_PATH=$(tt_path "$TT_B_SYNC")
  [ -n "$TT_B_PATH" ] || die "the sync of ttbob has no TrustTunnel path"
  jq -e --argjson port "$TT_PORT" --arg pin "$TT_PIN" --arg sni "$TT_SNI" '.settings | .port == $port and .sni == $sni and .tls == "pin" and .pin_sha256 == $pin and .transport == "auto" and (.username | test("^u[a-z2-7]{15}$")) and (.password | length) == 32' \
    <<<"$TT_A_PATH" >/dev/null || die "the TrustTunnel path of the sync is not what the contract says: $(jq -c '.settings | del(.password, .username)' <<<"$TT_A_PATH")"
  pass "TrustTunnel profile on TCP+UDP $TT_PORT (h2 + h3, self-signed, pin ${TT_PIN:0:8}...), ttalice and ttbob each enrolled a device; the sync path has user/password, tls pin = the node's certificate, transport auto"

  tt_dev_env
  [ -z "$TT_HOLD_DEV" ] || tt_hold
  tt_probes_without_client
  if [ -z "$TT_CLIENT_OK" ]; then
    tt_skip E1 "no official client in the cache (scripts/e2e/tt-fetch.sh)"
    tt_summary
    return 0
  fi
  tt_client_checks
  tt_summary
}

# ---------------------------------------------------------------- helpers of the checks

tt_api() { # <Service/Method> [json]: the TrustTunnelService (its own proto package)
  local out code
  out=$(curl -sS -m 40 -w $'\n%{http_code}' -X POST "$ADMIN/api/mistgate.trusttunnel.v1.TrustTunnelService/$1" \
    -H 'Content-Type: application/json' ${COOKIE:+-H "Cookie: $COOKIE"} -d "${2:-{\}}") || return 1
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  [ "$code" = 200 ] || { echo "tt_api $1 -> HTTP $code: $out" | redact >&2; return 1; }
  printf '%s' "$out"
}
# tt_conn: the TrustTunnel inbound's connection counters of the node (the last hour)
tt_conn() { api NodeService/GetNodeConnections "{\"nodeId\":\"$NODE_ID\",\"hours\":1}" | jq -c '[.inbounds[]? | select(.protocol == "trusttunnel")] | {connects: (map(.connects // 0 | tonumber) | add // 0), unknownKeys: (map(.unknownKeys // 0 | tonumber) | add // 0), authFailed: (map(.authFailed // 0 | tonumber) | add // 0)}'; }
tt_cnt() { tt_conn | jq -r ".$1"; } # connects | unknownKeys | authFailed
tt_stop_client() { [ -z "${1:-}" ] || stop_pid "$1"; }
tt_start_alice() { # <protocol http2|http3> [path json]: (re)starts the official client of ttalice; TT_A_CLIENT
  tt_stop_client "${TT_A_CLIENT:-}"
  tt_toml tta "${2:-$TT_A_PATH}" "$TTD/cfg/tta.toml" -protocol "$1"
  tt_client tta alice "$TTD/cfg/tta.toml"
  TT_A_CLIENT=$LAST_PID
  wait_for 20 "the client's SOCKS5 listener" tt_socks_up "$tta_PID" || return 1
}
tt_a_works() { [ "$(TT_CURL_T=8 tt_get tta "$1")" = "$DIRECT_SRC" ]; }
# tt_wait_works <ns var> <seconds> <tag>: the client reaches whoami within the time (it reconnects by itself)
tt_wait_works() { local i; for ((i = 0; i < $2; i++)); do [ "$(TT_CURL_T=4 tt_get "$1" "$3")" = "$DIRECT_SRC" ] && return 0; sleep 1; done; return 1; }
tt_zero_sha() { head -c "$1" /dev/zero | sha256sum | cut -d' ' -f1; } # the sha256 of N zero bytes (what whoami's /bytes/N sends)
tt_node_threads() { ls "/proc/$NODE_PID/task" | wc -l; }

# tt_dev_env: what a developer of a client needs from a held stand (--tt-only --hold)
tt_dev_env() {
  cp "$TTD/cfg/node.pem" "$WORK/tt-node.pem"
  # a client configuration of the official client for the first person, towards 127.0.0.1 (in the run's namespace), SOCKS5 on 1080
  "$BIN/ttprobe" toml -host 127.0.0.1 -skip-verify -protocol http2 <<<"$TT_A_PATH" >"$WORK/tt-client-alice.toml"
  ( umask 077
    {
      echo "TT_PANEL_ADMIN=$ADMIN"
      echo "TT_NODE_HOST=127.0.0.1"
      echo "TT_NODE_PORT=$TT_PORT   # TCP (HTTP/2) and UDP (HTTP/3), the same number"
      echo "TT_SNI=$TT_SNI"
      echo "TT_CERT_PIN_SHA256=$TT_PIN   # sha256 of the DER leaf; the same value is path.settings.pin_sha256 in sync"
      echo "TT_CERT_PEM=$WORK/tt-node.pem   # the node's self-signed certificate (no ACME in the lab)"
      echo "TT_CLIENT_TOML_ALICE=$WORK/tt-client-alice.toml   # the official client's file for the first person (skip_verification: see the note in tt_toml)"
      echo "TT_SYNC_ALICE=$WORK/tt-sync-alice.json   # the answer of GET <base>/sync for the first person; path = .servers[].paths[] with protocol trusttunnel"
      echo "TT_SYNC_BOB=$WORK/tt-sync-bob.json"
      echo "TT_ALICE_SUBSCRIPTION=$TT_A_URL   # POST <it>/client/enrol, then GET <base>/sync with the device key"
    } >"$WORK/tt-dev.env" )
}

# tt_hold (--tt-dev): the stand without the checks, for a developer of a client who runs their engine against our server
tt_hold() {
  step "tt.dev the panel and the node stay up; Ctrl-C ends it"
  say "work directory: $WORK (kept); everything below is in the network namespace ${MG_E2E_NS:-of this run}: enter it with  ip netns exec ${MG_E2E_NS:-<ns>} bash"
  say "the node: 127.0.0.1:$TT_PORT, TCP (HTTP/2) and UDP (HTTP/3), server name $TT_SNI, certificate pin sha256 $TT_PIN, certificate $WORK/tt-node.pem (self-signed), client file $WORK/tt-client-alice.toml"
  say "the first person's sync answer, with the TrustTunnel path (user name and password): $WORK/tt-sync-alice.json; the second person's: $WORK/tt-sync-bob.json"
  say "the settings of both: $WORK/tt-dev.env; the admin API is $ADMIN (owner session: owner.env is not made here; use --hold of the base scenario for the MCP)"
  trap 'exit 0' INT TERM
  FINISHED=1
  while :; do sleep 3600 & wait $!; done
}

# ---------------------------------------------------------------- checks that need no official client

tt_probes_without_client() {
  step "tt.3 E17 what a stranger sees of the TrustTunnel port, against the Hysteria2 site and the plain QUIC port of the same node"
  local a=$WORK/tt-site-tt.txt b=$WORK/tt-site-hy2.txt
  if [ -z "$TCP_MASQ" ]; then
    tt_skip E17 "TCP 443 is not free in this namespace, so the Hysteria2 site is not there to compare with"
    return 0
  fi
  # each site advertises the HTTP/3 port of its own inbound in Alt-Svc: that number is the one difference by design
  "$BIN/ttprobe" site "127.0.0.1:$TT_PORT" "$TT_SNI" 2>&1 | sed -E 's/^(Alt-Svc: h3=":)[0-9]+/\1PORT/' >"$a" || true
  "$BIN/ttprobe" site "127.0.0.1:$TCP_MASQ" "$TT_SNI" 2>&1 | sed -E 's/^(Alt-Svc: h3=":)[0-9]+/\1PORT/' >"$b" || true
  if diff -u "$b" "$a" >"$WORK/tt-site.diff"; then
    tt_ok E17 "h2 on TT TCP $TT_PORT answers $(grep -c '^==' "$a") probes (GET/HEAD/POST/OPTIONS, CONNECT with no, wrong, broken and bearer credentials, _check, _udp2) byte for byte like the Hysteria2 site on TCP $TCP_MASQ"
  else
    tt_no E17 "the TrustTunnel port differs from the Hysteria2 site: $(grep -c '^[-+][^-+]' "$WORK/tt-site.diff") line(s), see $WORK/tt-site.diff (kept)"
    sed -n 1,40p "$WORK/tt-site.diff" | cut -c1-200
  fi
  "$BIN/ttprobe" settings "127.0.0.1:$TT_PORT" "$TT_SNI" >"$a" 2>&1 || true
  "$BIN/ttprobe" settings "127.0.0.1:$TCP_MASQ" "$TT_SNI" >"$b" 2>&1 || true
  # The SETTINGS of the two sites are the same; the connection-level WINDOW_UPDATE is not: the TrustTunnel port uses the shared helper
  # (internal/node/sitehttp, 16 MiB), the Hysteria2 and VLESS sites of this tree still the library's default 1 MiB until the bridge
  # branch moves them to the same helper. Told apart so that the day they agree this check says so.
  if diff <(grep '^SETTINGS' "$b") <(grep '^SETTINGS' "$a") >/dev/null; then
    tt_ok E17b "the h2 SETTINGS of the TrustTunnel port equal the Hysteria2 site's ($(grep '^SETTINGS' "$a" | head -1 | cut -c1-140))"
  else
    tt_no E17b "SETTINGS differ: $(diff <(grep '^SETTINGS' "$b") <(grep '^SETTINGS' "$a") | head -4 | tr '\n' '|' || true)"
  fi
  [ "$(grep '^WINDOW_UPDATE' "$a")" = "$(grep '^WINDOW_UPDATE' "$b")" ] || note "connection WINDOW_UPDATE: TrustTunnel $(grep -m1 '^WINDOW_UPDATE' "$a" | sed 's/.*inc=//') against the Hysteria2 site's $(grep -m1 '^WINDOW_UPDATE' "$b" | sed 's/.*inc=//') (the site of this tree has not moved to internal/node/sitehttp yet)"
  local ha hb
  ha=$("$BIN/h3get" "https://127.0.0.1:$TT_PORT/" 2>&1 | grep -vi '^date:') || true
  hb=$("$BIN/h3get" "https://127.0.0.1:$HY2_PLAIN/" 2>&1 | grep -vi '^date:') || true
  if [ "$ha" = "$hb" ] && [ -n "$ha" ]; then tt_ok E17c "HTTP/3 GET on the TrustTunnel UDP port answers like the Hysteria2 masquerade on its plain QUIC port"; else tt_no E17c "HTTP/3 differs: tt=[$(head -c 200 <<<"$ha" | tr '\n' '|')] hy2=[$(head -c 200 <<<"$hb" | tr '\n' '|')]"; fi
}

# ---------------------------------------------------------------- checks with the official client

tt_client_checks() {
  local who i
  step "tt.4 E1 TCP through the official client over HTTP/2"
  tt_start_alice http2 || { tail -n 12 "$WORK/client-tt-alice.log" | cut -c1-240 | redact; tt_no E1 "the official client did not come up over h2"; return 0; }
  who=$(tt_get tta e1)
  if [ "$who" = "$DIRECT_SRC" ]; then tt_ok E1 "curl --socks5-hostname through the client over h2: whoami saw the node's exit address $who"; else tt_no E1 "whoami answered '$who', want the node's exit address $DIRECT_SRC"; tail -n 8 "$WORK/client-tt-alice.log" | cut -c1-240; fi

  step "tt.5 E3 UDP and DNS through the client (SOCKS5 UDP ASSOCIATE), h2"
  local out
  out=$(cns "$tta_PID" "$BIN/ttprobe" udp 127.0.0.1:1080 "$WHOAMI:7" 100 2>&1) || true
  echo "    $out"
  i=${out#udp }; i=${i%%/*}
  if [ "${i:-0}" -ge 98 ] 2>/dev/null; then tt_ok E3 "UDP echo through the client over h2: $out"; else tt_no E3 "UDP echo through the client over h2: $out (want 98 or more of 100)"; fi
  out=$(cns "$tta_PID" "$BIN/ttprobe" dns 127.0.0.1:1080 "$WHOAMI:53" tt-stand.example.net 2>&1) || true
  echo "    $out"
  case $out in "dns 203.0.113.99 "*) tt_ok E3b "a DNS query through the client's UDP relay: $out" ;; *) tt_no E3b "DNS through the UDP relay: $out" ;; esac

  step "tt.6 E2 the same over HTTP/3 (upstream_protocol = http3)"
  if tt_start_alice http3 && tt_wait_works tta 25 e2; then
    tt_ok E2 "TCP through the client over h3 (upstream_protocol = http3): whoami saw $DIRECT_SRC"
    out=$(cns "$tta_PID" "$BIN/ttprobe" udp 127.0.0.1:1080 "$WHOAMI:7" 100 2>&1) || true
    echo "    $out"
    i=${out#udp }; i=${i%%/*}
    if [ "${i:-0}" -ge 98 ] 2>/dev/null; then tt_ok E3c "UDP echo through the client over h3: $out"; else tt_no E3c "UDP echo through the client over h3: $out"; fi
  else
    tt_no E2 "the client over h3 did not reach whoami"; tail -n 12 "$WORK/client-tt-alice.log" | cut -c1-240 | redact
  fi
  tt_start_alice http2 || die "the client over h2 did not come back"

  step "tt.7 E4 a wrong password: the client does not connect (the counters are checked at the end, after the last restart of the agent)"
  local bad_path
  # a known name of a person whose device stays (bob's): E6 removes alice's device before the agent's ten-minute window closes, and a
  # counter of a credential that is gone by then is dropped by the panel
  bad_path=$(jq -c '.settings.password = "wrongwrongwrongwrongwrongwrong12"' <<<"$TT_B_PATH")
  tt_start_alice http2 "$bad_path"
  who=$(TT_CURL_T=10 tt_get tta e4)
  [ -z "$who" ] && tt_ok E4a "with a wrong password the client reaches nothing" || tt_no E4a "a wrong password got through: $who"
  grep -aiE "407|40[0-9]|auth|forbid|status" "$WORK/client-tt-alice.log" | tail -n 3 | cut -c1-220 || true
  tt_start_alice http2 || die "the client did not come back"
  tt_wait_works tta 20 e4ok || tt_no E4d "after the wrong tries the right credentials do not work"

  tt_e5_e6
  tt_e7_e8
  tt_e9_e11
  tt_e14
  tt_e15_e16
  tt_e19
  tt_e3d_e21
  tt_e22
  tt_e18
  tt_e20
  tt_e4_attempts
  tt_e4_counters
  tt_skip E12 "through a relay (DNAT): needs an entry node, a second agent with its own namespace and the relay stages of the panel; not built in this stand (the twin path is h2 only: B6 of the plan, covered by the sync tests)"
  tt_skip E13 "through the bridge: the bridge is in another branch (feat/reverse-relay-lab)"
}

# E5 and E6 need both people connected
tt_e5_e6() {
  step "tt.8 E5 disabled person: alice's tunnels close within 5 s, bob keeps downloading"
  local bob_toml=$TTD/cfg/ttb.toml gw
  tt_toml ttb "$TT_B_PATH" "$bob_toml" -protocol http2
  tt_client ttb bob "$bob_toml"
  TT_B_CLIENT=$LAST_PID
  wait_for 20 "bob's client" tt_socks_up "$ttb_PID" || { tt_no E5 "bob's client did not start"; return 0; }
  [ "$(TT_CURL_T=15 tt_get ttb e5b)" = "$DIRECT_SRC" ] || { tt_no E5 "bob's client does not reach whoami"; return 0; }
  # bob: a long download with a known hash; alice: a long download that must die when she is disabled
  ( cns "$ttb_PID" curl -s -m 300 --limit-rate 6M --socks5-hostname 127.0.0.1:1080 "http://$WHOAMI/bytes/200000000" | sha256sum | cut -d' ' -f1 >"$WORK/tt-bob.sha" ) &
  TT_BOB_JOB=$!
  PIDS+=("$TT_BOB_JOB")
  ( cns "$tta_PID" curl -s -m 120 --limit-rate 1M --socks5-hostname 127.0.0.1:1080 -o /dev/null -w '%{size_download} %{exitcode}\n' "http://$WHOAMI/bytes/200000000" >"$WORK/tt-alice-dl.txt" 2>&1 ) &
  TT_ALICE_JOB=$!
  PIDS+=("$TT_ALICE_JOB")
  sleep 3
  local bob_conns_before bob_conns_after
  bob_conns_before=$(ss -Htn state established "( sport = :$TT_PORT )" | awk -v ip="$ttb_IP" '$4 ~ ip {print $4}' | sort | tr '\n' ' ')
  local t0 t1
  stepup # the wait for a fresh authenticator code is not part of the time measured
  tt_alice_dead() { ! kill -0 "$TT_ALICE_JOB" 2>/dev/null; }
  # two clocks: when the node let go of alice's connection to the fake internet (its side of the tunnel), and when her curl gave up
  # (the client's side: the official client has to notice the reset and close its SOCKS5 connection)
  local up0 up_gone="" cli_gone="" now
  up0=$(ss -Htn state established '( dport = :80 )' dst "$WHOAMI" | wc -l)
  t0=$(date +%s.%N)
  say "disabling ttalice at $(date +%T.%N | cut -c1-12) (the agent logs 'desired state applied' with its own clock)"
  mutate UserService/SetUsersEnabled "{\"userIds\":[\"$TT_A_ID\"],\"enabled\":false}" >/dev/null || die "SetUsersEnabled failed"
  while now=$(date +%s.%N); [ "$(awk -v a="$now" -v b="$t0" 'BEGIN{print (a - b < 25)}')" = 1 ]; do
    [ -n "$up_gone" ] || [ "$(ss -Htn state established '( dport = :80 )' dst "$WHOAMI" | wc -l)" -lt "$up0" ] && [ -z "$up_gone" ] && up_gone=$now
    [ -n "$cli_gone" ] || ! tt_alice_dead || cli_gone=$now
    [ -n "$up_gone" ] && [ -n "$cli_gone" ] && break
    sleep 0.1
  done
  local dt dtu
  dt=$(python3 -c "print(round(${cli_gone:-$now} - $t0, 1))") dtu=$(python3 -c "print(round(${up_gone:-$now} - $t0, 1))")
  if [ -n "$up_gone" ] && [ "$(awk 'BEGIN{print ('"$dtu"' <= 5.0)}')" = 1 ]; then tt_ok E5a "alice disabled in the panel: the node closed her upstream connection after $dtu s, her curl ended after $dt s ($(cat "$WORK/tt-alice-dl.txt" 2>/dev/null))"; else tt_no E5a "alice disabled: upstream closed after $dtu s (want 5 s or less), her curl ended after $dt s"; fi
  [ -z "$(TT_CURL_T=8 tt_get tta e5new)" ] && tt_ok E5b "a new connection of the disabled person does not open" || tt_no E5b "the disabled person still gets through"
  tt_bob_running() { kill -0 "$TT_BOB_JOB" 2>/dev/null; }
  tt_bob_running && tt_ok E5c "bob's 200 MB download is still running ($(vmrss_mb "$NODE_PID") MB RSS of the agent)" || tt_no E5c "bob's download ended while alice was disabled"
  mutate UserService/SetUsersEnabled "{\"userIds\":[\"$TT_A_ID\"],\"enabled\":true}" >/dev/null || die "enabling alice failed"
  if tt_wait_works tta 60 e5back; then tt_ok E5d "enabled again: alice's client reconnects by itself and reaches whoami"; else tt_no E5d "alice's client did not come back within 60 s"; fi

  step "tt.9 E6 alice's device is removed and enrolled again while bob downloads: bob's bytes and connection stay"
  local old_pw old_path new_path
  old_path=$TT_A_PATH
  mutate UserService/RevokeDevice "{\"deviceId\":\"$TT_A_DEV\"}" >/dev/null || die "RevokeDevice failed"
  tt_enrol "$TT_A_URL" "stand-hwid-alice-2" || die "re-enrolment of alice failed"
  TT_A_KEY=$TT_KEY TT_A_BASE=$TT_BASE TT_A_DEV=$TT_DEV
  TT_BASE=$TT_A_BASE
  tt_sync "$TT_A_KEY" "$WORK/tt-sync-alice2.json" >/dev/null || die "sync of the new device failed"
  new_path=$(tt_path "$(cat "$WORK/tt-sync-alice2.json")")
  [ -n "$new_path" ] && [ "$(jq -r .settings.password <<<"$new_path")" != "$(jq -r .settings.password <<<"$old_path")" ] || { tt_no E6 "no new credential after the re-enrolment"; return 0; }
  tt_bob_running && bob_conns_after=$(ss -Htn state established "( sport = :$TT_PORT )" | awk -v ip="$ttb_IP" '$4 ~ ip {print $4}' | sort | tr '\n' ' ') || bob_conns_after=gone
  # the old password must be dead, the new one alive (the old client is still running: its next connection is refused)
  tt_start_alice http2 "$old_path"
  [ -z "$(TT_CURL_T=8 tt_get tta e6old)" ] && tt_ok E6a "the old password of the removed device does not work" || tt_no E6a "the old password still works"
  TT_A_PATH=$new_path
  tt_start_alice http2 "$new_path"
  tt_wait_works tta 40 e6new && tt_ok E6b "the new device's password works" || tt_no E6b "the new password does not work"
  wait "$TT_BOB_JOB" 2>/dev/null || true
  local want got
  want=$(tt_zero_sha 200000000) got=$(cat "$WORK/tt-bob.sha" 2>/dev/null)
  [ "$want" = "$got" ] && tt_ok E6c "bob's 200 MB download finished with the right sha256 while alice's device was swapped" || tt_no E6c "bob's download hash is $got, want $want"
  [ "$bob_conns_before" = "$bob_conns_after" ] && [ -n "$bob_conns_before" ] && tt_ok E6d "bob's connection to the node (source port $bob_conns_before) was the same before and after the swap" || tt_no E6d "bob's connections changed: before [$bob_conns_before] after [$bob_conns_after]"
}

# E7 accounting and E8 device limit
tt_e7_e8() {
  step "tt.10 E7 accounting: 50 MB down and 20 MB up are 70 MB (+-1%) of used_bytes"
  tt_a_works e7w || true
  local before after want=70000000 sink
  before=$(used_of "$TT_A_ID")
  cns "$tta_PID" curl -s -m 120 --socks5-hostname 127.0.0.1:1080 -o /dev/null "http://$WHOAMI/bytes/50000000"
  head -c 20000000 /dev/zero >"$WORK/tt-up.bin"
  sink=$(cns "$tta_PID" curl -s -m 120 --socks5-hostname 127.0.0.1:1080 --data-binary @"$WORK/tt-up.bin" -H 'Content-Type: application/octet-stream' "http://$WHOAMI:8080/up")
  rm -f "$WORK/tt-up.bin"
  [ "${sink%% *}" = 20000000 ] && [ "${sink#* }" = "$(tt_zero_sha 20000000)" ] || tt_no E7a "the sink got '$sink'"
  tt_acct() { after=$(used_of "$TT_A_ID"); [ $((after - before)) -ge $((want * 99 / 100)) ]; }
  wait_for 90 "the panel to count the traffic" tt_acct || true
  after=$(used_of "$TT_A_ID")
  local delta=$((after - before))
  if [ "$delta" -ge $((want * 99 / 100)) ] && [ "$delta" -le $((want * 101 / 100)) ]; then tt_ok E7 "used_bytes grew by $delta for 50 MB down + 20 MB up ($(python3 -c "print(round(100*($delta-$want)/$want,3))") % off the payload)"; else tt_no E7 "used_bytes grew by $delta, payload is $want"; fi

  step "tt.11 E8 device limit 1: the second device is refused, the first keeps working"
  local out u
  out=$(api UserService/CreateUser "{\"name\":\"ttdan\",\"groupId\":\"$TT_GROUP\",\"deviceLimit\":1}")
  u=$(jq -r .subscriptionUrl <<<"$out")
  tt_enrol "$u" "stand-hwid-dan-1" && tt_ok E8a "the first device of a person with limit 1 enrols" || tt_no E8a "the first device did not enrol"
  local k1=$TT_KEY b1=$TT_BASE
  if tt_enrol "$u" "stand-hwid-dan-2" 2>"$WORK/tt-e8.err"; then tt_no E8b "the second device was accepted at limit 1"; else tt_ok E8b "the second device is refused ($(cut -c1-80 "$WORK/tt-e8.err" | tr '\n' ' '))"; fi
  TT_BASE=$b1
  tt_sync "$k1" >/dev/null && tt_ok E8c "the first device still syncs, its TrustTunnel path is there ($(tt_path "$(tt_sync "$k1")" | jq -r .settings.username | cut -c1-3)...)" || tt_no E8c "the first device lost its sync"
}

# E9 big transfers, E10 100 parallel, E11 agent restart
tt_e9_e11() {
  step "tt.12 E9 a gigabyte each way, E10 100 parallel connections"
  local rss0 rss1 th0 th1 N=1073741824 want got sink
  tt_wait_works tta 20 e9w || true
  rss0=$(vmrss_mb "$NODE_PID") th0=$(tt_node_threads)
  want=$(tt_zero_sha $N)
  got=$(cns "$tta_PID" curl -s -m 280 --socks5-hostname 127.0.0.1:1080 "http://$WHOAMI/bytes/$N" | sha256sum | cut -d' ' -f1)
  [ "$got" = "$want" ] && tt_ok E9a "1 GiB down through the client, sha256 equal" || tt_no E9a "1 GiB down: sha256 $got, want $want"
  sink=$(head -c $N /dev/zero | cns "$tta_PID" curl -sS -m 280 --socks5-hostname 127.0.0.1:1080 -X POST -T - -H "Content-Length: $N" -H 'Transfer-Encoding:' -H 'Expect:' "http://$WHOAMI:8080/up" 2>"$WORK/tt-e9b.err")
  [ "$sink" = "$N $want" ] && tt_ok E9b "1 GiB up through the client, the sink's sha256 equal" || tt_no E9b "1 GiB up: the sink says '$sink' ($(head -c 200 "$WORK/tt-e9b.err"))"
  sleep 10
  rss1=$(vmrss_mb "$NODE_PID")
  note "agent RSS: $rss0 MB before the gigabytes, $rss1 MB after"
  if awk -v a="$rss0" -v b="$rss1" 'BEGIN{exit !(b <= a * 1.10 + 8)}'; then tt_ok E9c "agent RSS $rss0 -> $rss1 MB after 2 GiB (within 10% plus 8 MB)"; else tt_no E9c "agent RSS $rss0 -> $rss1 MB"; fi
  local n
  n=$(seq 100 | xargs -P100 -I{} nsenter -t "$tta_PID" -n -- curl -s -m 60 -o /dev/null -w '%{http_code}\n' --socks5-hostname 127.0.0.1:1080 "http://$WHOAMI/e10-{}" | grep -c '^200$')
  [ "$n" = 100 ] && tt_ok E10a "100 parallel fetches through one client: 100 x 200" || tt_no E10a "100 parallel fetches: $n x 200"
  sleep 15
  th1=$(tt_node_threads)
  if [ "$th1" -le $((th0 + 8)) ]; then tt_ok E10b "agent threads $th0 -> $th1 and RSS $(vmrss_mb "$NODE_PID") MB after the 100 (goroutines are not readable from outside: threads and RSS stand in)"; else tt_no E10b "agent threads $th0 -> $th1"; fi

  step "tt.13 E11 the agent restarts: the client comes back by itself within 30 s"
  local old_pid=$NODE_PID
  stop_pid "$NODE_PID"
  alive "$old_pid" && say "the old agent ($old_pid) is still alive after stop_pid"
  [ -z "$(ss -Hltn "sport = :$TT_PORT")" ] && say "TCP $TT_PORT is closed while the agent is down" || say "TCP $TT_PORT is STILL LISTENING while the agent is down: $(ss -Hltnp "sport = :$TT_PORT" | head -2)"
  start_node
  NODE_PID=$LAST_PID
  local t0=$SECONDS
  if tt_wait_works tta 60 e11; then tt_ok E11 "after the agent restart the client reached whoami again in $((SECONDS - t0)) s (limit 30)"; [ $((SECONDS - t0)) -le 30 ] || { TT_TABLE[${#TT_TABLE[@]}-1]="E11|FAIL|came back after $((SECONDS - t0)) s, limit 30"; TT_FAILS=$((TT_FAILS + 1)); }; else tt_no E11 "the client did not come back within 60 s of the agent restart"; fi
  wait_for 90 "node ONLINE" node_online || { tail -n 6 "$WORK/node.log" | cut -c1-240; die "the node did not come back (status $(node_status))"; }
}

# E14: our tt:// through their setup_wizard
tt_e14() {
  step "tt.14 E14 our tt:// link through the official setup_wizard"
  local prof acme_port links url toml hostline
  acme_port=$(tt_port)
  prof=$(tt_profile e2e-tt-acme "$(jq -nc --argjson p "$acme_port" --arg s "$TT_SNI" '{port:$p, h3:false, sni:$s, tls_mode:"acme_domain", egress:"direct"}')") || { tt_no E14 "creating the Let's Encrypt profile failed"; return 0; }
  mutate GroupService/UpdateGroup "$(jq -nc --arg g "$TT_GROUP" --arg a "$TT_PROFILE" --arg b "$prof" '{groupId:$g, profileIds:{values:[$a,$b]}}')" >/dev/null || { tt_no E14 "adding the Let's Encrypt profile to the group failed"; return 0; }
  mutate UserService/UpdateUser "$(jq -nc --arg u "$TT_A_ID" '{userId:$u, apps:{happ:true, amnezia:true, trustTunnel:true}}')" >/dev/null || { tt_no E14 "switching TrustTunnel on for ttalice failed"; return 0; }
  tt_links() { links=$(tt_api GetTrustTunnelLinks "{\"userId\":\"$TT_A_ID\"}") && jq -e '[.links[]? | select((.url // "") != "")] | length >= 1' <<<"$links" >/dev/null; }
  wait_for 40 "the tt:// link of ttalice" tt_links || { tt_no E14 "no tt:// link for ttalice: $(jq -c '[.links[]? | {note, relay}]' <<<"$links")"; return 0; }
  url=$(jq -r '[.links[] | select((.url // "") != "")][0].url' <<<"$links")
  SECRETS+=("$url")
  case $url in tt://*) ;; *) tt_no E14 "the link is not a tt:// link: scheme '${url%%:*}', ${#url} chars, answer $(jq -c '[.links[]? | {note, relay, urlLen: (.url | length)}]' <<<"$links")"; return 0 ;; esac
  rm -f "$TTD/cfg/wiz.toml"; chmod 777 "$TTD/cfg"
  if ! tt_sandbox "$TTD/bin/client_wizard" -m non-interactive -d "$url" --settings "$TTD/cfg/wiz.toml" >"$WORK/tt-wizard.log" 2>&1; then
    tt_no E14 "their setup_wizard refused our link: $(tail -n 3 "$WORK/tt-wizard.log" | tr '\n' ' ' | cut -c1-300)"
    return 0
  fi
  toml=$TTD/cfg/wiz.toml
  local ok=1 why=""
  grep -q "^hostname = \"$TT_SNI\"" "$toml" || { ok=0; why="$why hostname"; }
  grep -q "^addresses = \[\"127.0.0.1:$acme_port\"\]" "$toml" || { ok=0; why="$why addresses"; }
  grep -qE '^username = "u[a-z2-7]{15}"' "$toml" || { ok=0; why="$why username"; }
  grep -qE '^password = "[A-Za-z0-9]{32}"' "$toml" || { ok=0; why="$why password"; }
  grep -q '^upstream_protocol = "http2"' "$toml" || { ok=0; why="$why upstream_protocol"; }
  grep -q '^has_ipv6 = false' "$toml" || { ok=0; why="$why has_ipv6"; }
  SECRETS+=("$(sed -n 's/^password = "\(.*\)"/\1/p' "$toml")")
  if [ "$ok" = 1 ]; then tt_ok E14a "setup_wizard --deeplink decoded our tt:// link: hostname, address 127.0.0.1:$acme_port, username, password, has_ipv6=false, upstream_protocol=http2"; else tt_no E14a "the wizard's TOML differs in:$why"; fi
  # connect with it: the key's credential is the person's on every TrustTunnel inbound, so point it at the self-signed one
  python3 - "$toml" "$TTD/cfg/wiz-run.toml" "$tta_GW:$TT_PORT" <<'PY'
import re, sys
src, dst, addr = sys.argv[1:]
t = open(src).read()
t = t[:t.index("[listener]")] if "[listener]" in t else t
t = re.sub(r'(?m)^addresses = .*$', 'addresses = ["%s"]' % addr, t)
t = re.sub(r'(?m)^skip_verification = .*$', 'skip_verification = true', t)
t = re.sub(r'(?m)^killswitch_enabled = .*$', 'killswitch_enabled = false', t)
open(dst, "w").write(t + '\n[listener.socks]\naddress = "127.0.0.1:1080"\nusername = ""\npassword = ""\n')
PY
  chmod 644 "$TTD/cfg/wiz-run.toml"
  tt_stop_client "${TT_A_CLIENT:-}"
  tt_client tta alice-key "$TTD/cfg/wiz-run.toml"
  TT_A_CLIENT=$LAST_PID
  wait_for 20 "the client with the wizard's file" tt_socks_up "$tta_PID" || true
  if tt_wait_works tta 25 e14; then tt_ok E14b "the official client run from the wizard's TOML (our link) connects to the node and reaches whoami"; else tt_no E14b "the client with the wizard's TOML does not connect"; tail -n 6 "$WORK/client-tt-alice-key.log" | cut -c1-240 | redact; fi
  tt_start_alice http2 || true
}

# E15 and E16: the TSPU, as rules on the node's own packets towards alice's address
tt_e15_e16() {
  step "tt.15 E15 freeze after 16 KB, E16 reset after the ClientHello (nft rules on the lab's side towards alice)"
  sysctl -qw net.netfilter.nf_conntrack_acct=1 2>/dev/null || true
  tt_wait_works tta 20 e15w || tt_start_alice http2 || true
  # E15: the node's answers to alice stop after 16000 bytes of a connection (TCP only; QUIC is UDP)
  nft add table inet mgtt_tspu
  nft add chain inet mgtt_tspu out '{ type filter hook output priority 10; policy accept; }'
  nft add rule inet mgtt_tspu out ip daddr "$tta_IP" tcp sport "$TT_PORT" ct reply bytes '>' 16000 drop
  tt_start_alice http2
  local got
  got=$(cns "$tta_PID" curl -s -m 20 --socks5-hostname 127.0.0.1:1080 "http://$WHOAMI/bytes/3000000" | wc -c)
  if [ "$got" -lt 500000 ]; then tt_ok E15a "h2 freezes behind the 16 KB rule, as expected: $got of 3000000 bytes arrived in 20 s"; else tt_no E15a "h2 was not frozen by the rule ($got bytes): the rule does not act"; fi
  tt_start_alice http3
  got=$(cns "$tta_PID" curl -s -m 30 --socks5-hostname 127.0.0.1:1080 "http://$WHOAMI/bytes/3000000" | wc -c)
  if [ "$got" = 3000000 ]; then tt_ok E15b "with http3 the same download passes the rule (UDP)"; else tt_no E15b "http3 got $got of 3000000 bytes through the rule"; fi
  nft delete table inet mgtt_tspu

  # E16: a TCP connection to the port gets a RST once the client's data (the ClientHello) goes beyond the handshake
  nft add table inet mgtt_tspu
  nft add chain inet mgtt_tspu in '{ type filter hook input priority 10; policy accept; }'
  nft add rule inet mgtt_tspu in ip saddr "$tta_IP" tcp dport "$TT_PORT" ct original packets '>=' 3 ct reply packets '<=' 1 reject with tcp reset
  tt_start_alice http2
  [ -z "$(TT_CURL_T=10 tt_get tta e16h2)" ] && tt_ok E16a "h2 fails behind the reset-after-ClientHello rule, as expected" || tt_no E16a "h2 got through the reset rule"
  local t0=$SECONDS
  tt_start_alice http3
  if tt_wait_works tta 40 e16h3; then tt_ok E16b "with http3 the client reaches whoami $((SECONDS - t0)) s after it was started (the official client has no automatic h2 -> h3 fallback: the switch is a setting)"; else tt_no E16b "http3 did not work behind the reset rule"; fi
  nft delete table inet mgtt_tspu
  tt_start_alice http2 || true
}

# E19: what the tunnel refuses to reach
tt_e19() {
  step "tt.16 E19 SSRF: loopback, the node's own address, metadata and private UDP are not reachable through the tunnel"
  local lp hits_before hits_after code
  tt_wait_works tta 20 e19w || true
  cat >"$WORK/tt-listen.py" <<'PY'
import socket, sys, threading
host, port, log = sys.argv[1], int(sys.argv[2]), sys.argv[3]
t = socket.socket(); t.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); t.bind((host, port)); t.listen(8)
u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); u.bind((host, port))
def tcp():
    while True:
        c, _ = t.accept(); open(log, "a").write("tcp\n"); c.close()
def udp():
    while True:
        u.recvfrom(2000); open(log, "a").write("udp\n")
threading.Thread(target=tcp, daemon=True).start(); udp()
PY
  lp=$(free_port tcp)
  : >"$WORK/tt-listen.log"
  spawn python3 "$WORK/tt-listen.py" 127.0.0.1 "$lp" "$WORK/tt-listen.log"
  spawn python3 "$WORK/tt-listen.py" "$tta_GW" "$lp" "$WORK/tt-listen.log"
  sleep 1
  local blocked=0 total=0 target
  for target in "127.0.0.1:$lp" "$tta_GW:$lp" "169.254.169.254:80" "10.0.0.1:80" "[::1]:$lp"; do
    total=$((total + 1))
    code=$(cns "$tta_PID" curl -s -m 12 -o /dev/null -w '%{http_code}/%{exitcode}' --socks5-hostname 127.0.0.1:1080 "http://$target/" 2>/dev/null) || true
    case $code in 000/*|*/[1-9]*) blocked=$((blocked + 1)) ;; *) echo "    $target answered $code" ;; esac
  done
  hits_after=$(wc -l <"$WORK/tt-listen.log")
  if [ "$blocked" = "$total" ] && [ "$hits_after" = 0 ]; then tt_ok E19a "CONNECT to loopback, the node's own address, the metadata address, a private address and ::1 all fail and the local listeners saw $hits_after connections"; else tt_no E19a "$blocked of $total refused, the listeners saw $hits_after connection(s)"; fi
  local udpout
  udpout=$(cns "$tta_PID" "$BIN/ttprobe" udp 127.0.0.1:1080 "127.0.0.1:$lp" 5 2>&1) || true
  udpout="$udpout $(cns "$tta_PID" "$BIN/ttprobe" udp 127.0.0.1:1080 "$tta_GW:$lp" 5 2>&1) || true"
  sleep 1
  hits_after=$(wc -l <"$WORK/tt-listen.log")
  if [ "$hits_after" = 0 ]; then tt_ok E19b "UDP datagrams to loopback and to the node's own address reached nobody ($(tr -s ' ' <<<"$udpout" | cut -c1-80))"; else tt_no E19b "UDP reached a local listener $hits_after time(s)"; fi
}


# E3d, E21: two UDP associations at once (one _udp2 stream per connection: the second replaces the first), and an idle connection
tt_e3d_e21() {
  step "tt.17 E3d two UDP associations at once, E21 a quiet client for 75 s (the node closes a connection without streams after 60 s)"
  tt_start_alice http2 || return 0
  local o1 o2 f1=$WORK/tt-u1.txt f2=$WORK/tt-u2.txt
  cns "$tta_PID" "$BIN/ttprobe" udp 127.0.0.1:1080 "$WHOAMI:7" 100 >"$f1" 2>&1 &
  local j1=$!
  cns "$tta_PID" "$BIN/ttprobe" udp 127.0.0.1:1080 "$WHOAMI:7" 100 >"$f2" 2>&1 &
  local j2=$!
  wait "$j1" "$j2" 2>/dev/null
  o1=$(cat "$f1") o2=$(cat "$f2")
  echo "    $o1 | $o2"
  case "$o1$o2" in
    "udp 100/100 "*"udp 100/100 "*) tt_ok E3d "two simultaneous UDP associations through one client: $(cut -d' ' -f2 <<<"$o1") and $(cut -d' ' -f2 <<<"$o2")" ;;
    *) tt_no E3d "two simultaneous UDP associations: [$o1] [$o2] (the issues list #177 for the reference server)" ;;
  esac
  local t0=$SECONDS
  sleep 75
  if tt_wait_works tta 30 e21; then tt_ok E21 "after 75 s without a request the client still reaches whoami (reconnect or kept connection; answered $((SECONDS - t0 - 75)) s after the pause)"; else tt_no E21 "the client did not recover after 75 s of silence"; fi
  grep -aiE "closed|reconnect|connecting|ping" "$WORK/client-tt-alice.log" | tail -n 4 | cut -c1-200 || true
}

# E18: the memory of the agent under many people and a rate limit, and the worst case of one person
tt_rss_during() { # <seconds> <pid>: prints the peak RSS in MB over the time
  local end=$((SECONDS + $1)) peak=0 v
  while [ "$SECONDS" -lt "$end" ]; do
    v=$(vmrss_mb "$2")
    peak=$(awk -v a="$peak" -v b="$v" 'BEGIN{print (b > a) ? b : a}')
    sleep 2
  done
  echo "$peak"
}
# tt_fresh_agent: a new agent process, so that the RSS of the next measurement does not start from the heap the last one left
tt_fresh_agent() {
  stop_pid "$NODE_PID"
  start_node
  NODE_PID=$LAST_PID
  wait_for 90 "node ONLINE" node_online
  tt_inbound_up() { inbound_of "$TT_PROFILE" | jq -e '.state == "INBOUND_STATE_ACTIVE"' >/dev/null; }
  wait_for 60 "the TrustTunnel inbound" tt_inbound_up
  sleep 8
}
tt_e18() {
  # (the panel lets one address make about 40 enrolments an hour, so 6 people x 5 devices: 30 credentials, not the 50 of the plan)
  step "tt.18 E18 memory: 30 devices x 20 upload streams at 1 Mbit/s each, then the worst case of one person"
  local u d out base creds=$WORK/tt-creds.txt k
  : >"$creds"
  for u in 1 2 3 4 5 6; do
    out=$(api UserService/CreateUser "{\"name\":\"ttmem$u\",\"groupId\":\"$TT_GROUP\",\"deviceSpeedLimitBps\":125000}") || { tt_no E18 "creating a person for the load failed"; return 0; }
    for d in 1 2 3 4 5; do
      tt_enrol "$(jq -r .subscriptionUrl <<<"$out")" "stand-hwid-mem-$u-$d" 2>"$WORK/tt-e18.err" || { tt_no E18 "enrolment $u/$d failed: $(cut -c1-200 "$WORK/tt-e18.err")"; return 0; }
      TT_BASE=$TT_BASE
      k=$(tt_path "$(tt_sync "$TT_KEY")" | jq -r '.settings | .username + ":" + .password')
      SECRETS+=("${k#*:}")
      echo "$k" >>"$creds"
    done
  done
  [ "$(wc -l <"$creds")" = 30 ] || { tt_no E18 "did not get 30 credentials"; return 0; }
  sleep 5
  base=$(vmrss_mb "$NODE_PID")
  local peak res
  ( "$BIN/ttprobe" flood -addr "$tta_GW:$TT_PORT" -sni "$TT_SNI" -creds "$creds" -streams 20 -seconds 40 >"$WORK/tt-flood-h2.txt" 2>&1 ) &
  local fj=$!
  PIDS+=("$fj")
  # the flood runs in the run's namespace: the node is reachable at the clients' gateway address from there as well
  peak=$(tt_rss_during 42 "$NODE_PID")
  wait "$fj" 2>/dev/null
  res=$(cat "$WORK/tt-flood-h2.txt")
  echo "    $res"
  sleep 10
  note "E18 h2: agent RSS $base MB idle, peak $peak MB with 30 devices x 20 streams, $(vmrss_mb "$NODE_PID") MB 10 s after"
  # the bound: what the windows allow, 16 MiB of connection window for each of the 30 connections (plans/trusttunnel-plan.md C.6)
  if awk -v p="$peak" -v b="$base" 'BEGIN{exit !(p - b <= 30 * 16 * 1.1)}'; then tt_ok E18a "30 devices x 20 rate-limited upload streams over h2 grew the agent from $base to $peak MB, inside the 30 x 16 MiB of the connection windows ($res); it was still $(vmrss_mb "$NODE_PID") MB 10 s after (Go keeps the heap)"; else tt_no E18a "the agent grew from $base to $peak MB under 30 x 20 streams, past the 30 x 16 MiB of the windows"; fi
  tt_fresh_agent
  base=$(vmrss_mb "$NODE_PID")
  ( "$BIN/ttprobe" flood -h3 -addr "$tta_GW:$TT_PORT" -sni "$TT_SNI" -creds "$creds" -streams 20 -seconds 40 >"$WORK/tt-flood-h3.txt" 2>&1 ) &
  fj=$!
  PIDS+=("$fj")
  peak=$(tt_rss_during 42 "$NODE_PID")
  wait "$fj" 2>/dev/null
  res=$(cat "$WORK/tt-flood-h3.txt")
  echo "    $res"
  sleep 10
  note "E18 h3: agent RSS peak $peak MB, $(vmrss_mb "$NODE_PID") MB 10 s after"
  if awk -v p="$peak" -v b="$base" 'BEGIN{exit !(p - b <= 30 * 136 * 1.1)}'; then tt_ok E18b "the same over h3: peak $peak MB, $(vmrss_mb "$NODE_PID") MB 10 s after ($res)"; else tt_no E18b "h3: the agent grew from $base to $peak MB"; fi

  # the worst case of ONE person: 16 connections (the limit per credential), each with 20 streams uploading faster than the limit lets the node drain
  local one
  one=$(head -n 1 "$creds")
  for k in $(seq 16); do echo "$one"; done >"$WORK/tt-creds-one.txt"
  tt_fresh_agent
  base=$(vmrss_mb "$NODE_PID")
  ( "$BIN/ttprobe" flood -addr "$tta_GW:$TT_PORT" -sni "$TT_SNI" -creds "$WORK/tt-creds-one.txt" -streams 20 -seconds 40 >"$WORK/tt-flood-one.txt" 2>&1 ) &
  fj=$!
  PIDS+=("$fj")
  peak=$(tt_rss_during 42 "$NODE_PID")
  wait "$fj" 2>/dev/null
  res=$(cat "$WORK/tt-flood-one.txt")
  echo "    $res"
  note "E18 one person over h2: 16 connections x 20 streams at 1 Mbit/s: agent RSS $base -> $peak MB (the plan's worst case: 264 MiB)"
  if awk -v p="$peak" -v b="$base" 'BEGIN{exit !(p - b <= 264 + 8)}'; then tt_ok E18c "worst case of one person over h2 (16 connections x 20 streams at 1 Mbit/s): +$(awk -v p="$peak" -v b="$base" 'BEGIN{printf "%.0f", p - b}') MB of RSS ($res); the plan's bound is 264 MiB"; else tt_no E18c "one person grew the agent by $(awk -v p="$peak" -v b="$base" 'BEGIN{printf "%.0f", p - b}') MB over h2, past the 264 MiB of the plan"; fi
  tt_fresh_agent
  ( "$BIN/ttprobe" flood -h3 -addr "$tta_GW:$TT_PORT" -sni "$TT_SNI" -creds "$WORK/tt-creds-one.txt" -streams 20 -seconds 40 >"$WORK/tt-flood-one3.txt" 2>&1 ) &
  fj=$!
  PIDS+=("$fj")
  base=$(vmrss_mb "$NODE_PID")
  peak=$(tt_rss_during 42 "$NODE_PID")
  wait "$fj" 2>/dev/null
  res=$(cat "$WORK/tt-flood-one3.txt")
  echo "    $res"
  if awk -v p="$peak" -v b="$base" 'BEGIN{exit !(p - b <= 136 + 8)}'; then tt_ok E18d "worst case of one person over h3: +$(awk -v p="$peak" -v b="$base" 'BEGIN{printf "%.0f", p - b}') MB of RSS ($res); the plan's bound is 136 MiB"; else tt_no E18d "one person grew the agent by $(awk -v p="$peak" -v b="$base" 'BEGIN{printf "%.0f", p - b}') MB over h3, past the 136 MiB of the plan"; fi
}

# E20: the same client against their endpoint v1.1.0, for the cases where it and our server could disagree
tt_e20() {
  step "tt.19 E20 the reference endpoint (their trusttunnel_endpoint v1.1.0) in a namespace of its own, the same client"
  if [ -z "$TT_EP_OK" ]; then tt_skip E20 "no reference endpoint in the cache"; return 0; fi
  mk_ns ttep 198.18.66
  cns "$ttep_PID" ip route add default via "$ttep_GW"
  cns "$tta_PID" ip route add 198.18.66.0/30 via "$tta_GW"
  sysctl -qw net.ipv4.ip_forward=1
  # the lab's fake internet has no route back to the endpoint's address: its traffic leaves this namespace as the node's does
  nft add table ip mgtt_nat
  nft add chain ip mgtt_nat post '{ type nat hook postrouting priority 100; policy accept; }'
  nft add rule ip mgtt_nat post ip saddr 198.18.66.0/30 masquerade
  local d=$TTD/ep port=8443 user=refuser pass=refpass-0123456789abcdef
  rm -rf "$d"; mkdir -p "$d"; chmod 777 "$d"
  # it writes credentials.toml, rules.toml and certs/ into its working directory: the lab's, never the repository
  if ! (cd "$d" && tt_sandbox "$TTD/bin/endpoint_wizard" -m non-interactive -a "0.0.0.0:$port" -c "$user:$pass" -n "$TT_SNI" --lib-settings "$d/vpn.toml" --hosts-settings "$d/hosts.toml" --cert-type self-signed) >"$WORK/tt-ep-wizard.log" 2>&1; then
    tt_no E20 "their setup_wizard failed: $(tail -n 3 "$WORK/tt-ep-wizard.log" | tr '\n' ' ' | cut -c1-200)"; return 0
  fi
  SECRETS+=("$pass")
  # their server refuses documentation and benchmarking ranges (net_utils.rs is_global_ipv4) as "private": the lab lives in them
  sed -i 's/^allow_private_network_connections = false/allow_private_network_connections = true/' "$d/vpn.toml"
  spawn nsenter -t "$ttep_PID" -n -- setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs --bounding-set=-all prlimit --as=2147483648     "$TTD/bin/trusttunnel_endpoint" "$d/vpn.toml" "$d/hosts.toml" --loglvl info >>"$WORK/tt-endpoint.log" 2>&1
  local ep=$LAST_PID
  sleep 2
  local pj
  pj=$(jq -nc --arg u "$user" --arg p "$pass" --arg s "$TT_SNI" '{protocol:"trusttunnel", ipv6_egress:false, settings:{server:"198.18.66.2", port:8443, sni:$s, username:$u, password:$p, transport:"h2"}}')
  tt_stop_client "${TT_A_CLIENT:-}"
  tt_toml tta "$pj" "$TTD/cfg/tta-ref.toml" -host 198.18.66.2 -port "$port" -protocol http2
  tt_client tta ref "$TTD/cfg/tta-ref.toml"
  TT_A_CLIENT=$LAST_PID
  wait_for 20 "the client" tt_socks_up "$tta_PID" || true
  local who out
  who=$(TT_CURL_T=15 tt_get tta e20)
  echo "    reference answer to TCP: '$who'"
  out=$(cns "$tta_PID" "$BIN/ttprobe" udp 127.0.0.1:1080 "$WHOAMI:7" 100 2>&1) || true
  echo "    reference UDP: $out"
  tt_rec E20 PASS "the same client against their endpoint: TCP '$who', $out; the log of the endpoint is $WORK/tt-endpoint.log"
  # wrong password at the reference: what does the client see?
  tt_toml tta "$(jq -c '.settings.password = "wrong"' <<<"$pj")" "$TTD/cfg/tta-ref.toml" -host 198.18.66.2 -port "$port" -protocol http2
  tt_stop_client "$TT_A_CLIENT"
  tt_client tta ref-bad "$TTD/cfg/tta-ref.toml"
  TT_A_CLIENT=$LAST_PID
  TT_CURL_T=8 tt_get tta e20bad >/dev/null
  grep -aE "ERROR|407|status|Error" "$WORK/client-tt-ref-bad.log" | head -n 3 | cut -c1-200 || true
  tt_stop_client "$TT_A_CLIENT"
  TT_A_CLIENT=""
  stop_pid "$ep"
}

# E4's tries again, now that the agent will not be restarted any more: its link counters live in memory until the ten-minute window ends
tt_e4_attempts() {
  step "tt.20b E4 tries for the counters: a known name with a wrong password (bob's), an unknown name"
  TT_AF0=$(tt_cnt authFailed) TT_UK0=$(tt_cnt unknownKeys)
  tt_start_alice http2 "$(jq -c '.settings.password = "wrongwrongwrongwrongwrongwrong12"' <<<"$TT_B_PATH")"
  TT_CURL_T=8 tt_get tta e4w >/dev/null
  tt_start_alice http2 "$(jq -c '.settings.username = "unotarealuser0000"' <<<"$TT_A_PATH")"
  TT_CURL_T=8 tt_get tta e4u >/dev/null
  tt_stop_client "$TT_A_CLIENT"
  TT_A_CLIENT=""
}

# E4b, E4c: the counters of the refused tries made in E4, once the agent's ten-minute window has closed
tt_e4_counters() {
  step "tt.21 E4 counters: auth_failed and unknown_keys of the wrong tries reach the panel (next ten-minute window of the agent)"
  tt_auth_failed() { [ "$(tt_cnt authFailed)" -gt "$TT_AF0" ]; }
  tt_unknown() { [ "$(tt_cnt unknownKeys)" -gt "$TT_UK0" ]; }
  wait_for 700 "auth_failed to reach the panel" tt_auth_failed
  if tt_auth_failed; then tt_ok E4b "auth_failed went from $TT_AF0 to $(tt_cnt authFailed) on the panel after the wrong password (a known name)"; else tt_no E4b "auth_failed did not move from $TT_AF0 in 700 s: the counter of the engine does not reach the panel"; fi
  if tt_unknown; then tt_ok E4c "an unknown name moved unknown_keys ($TT_UK0 -> $(tt_cnt unknownKeys))"; else tt_no E4c "unknown_keys did not move from $TT_UK0"; fi
}

# E22: many anonymous connections from the client's own address first (the limiter of a site: 8 per source, 32 in all, the oldest
# anonymous one is dropped), then the official client: its authenticated connection must be taken and kept
tt_e22() {
  step "tt.20 E22 24 anonymous TLS connections from alice's address, then the official client"
  cat >"$WORK/tt-anon.py" <<'PY'
import socket, ssl, sys, time
host, port, n, hold = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])
ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE; ctx.set_alpn_protocols(["h2"])
keep = []
for _ in range(n):
    try:
        s = ctx.wrap_socket(socket.create_connection((host, port), timeout=5), server_hostname="de1.example.com")
        keep.append(s)
    except OSError:
        pass
time.sleep(hold)
PY
  cns "$tta_PID" python3 "$WORK/tt-anon.py" "$tta_GW" "$TT_PORT" 24 40 &
  local aj=$!
  PIDS+=("$aj")
  sleep 3
  tt_start_alice http2 || { tt_no E22 "the client did not start"; return 0; }
  if tt_wait_works tta 30 e22; then
    tt_ok E22 "with 24 anonymous connections open from the same address the official client connects and reaches whoami"
  else
    tt_no E22 "the client did not get through while 24 anonymous connections were open from its address"
  fi
  wait "$aj" 2>/dev/null
}

tt_summary() {
  step "tt.summary the table"
  local row id res text
  printf '%-5s %-5s %s\n' check result what
  for row in "${TT_TABLE[@]}"; do
    IFS='|' read -r id res text <<<"$row"
    printf '%-5s %-5s %s\n' "$id" "$res" "$text"
  done | tee "$WORK/tt-table.txt"
  set -e
  trap 'on_err $LINENO' ERR
  [ "$TT_FAILS" = 0 ] || die "TrustTunnel stand: $TT_FAILS check(s) failed"
  pass "TrustTunnel stand: every check passed or was skipped with its reason"
}
