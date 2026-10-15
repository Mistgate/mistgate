#!/usr/bin/env bash
# The TSPU probe of the country check (internal/tspuprobe, agent command CountryProbe), end to end in network namespaces.
#
#   MSYS_NO_PATHCONV=1 wsl -d Ubuntu -u root -- bash scripts/e2e/tspu.sh [--keep]   (from the repo root)
#
# Needs: go, ip, nft, python3, setpriv, unshare, nsenter. Runs as root, but ONLY inside a private network namespace made at
# the start (`unshare -n -m`): the host's interfaces, routes and nft tables, Docker's included, are neither visible nor
# touched, and everything vanishes with the last process. Nothing in it talks to a real address: the "internet" is
# documentation space (RFC 5737) and the links are RFC 1918.
#
# TOPOLOGY (anonymous namespaces, a veth pair on every link, forwarding on in every router). Link N is 10.77.N.0/30, the
# left end is .1 and the right end .2:
#
#   probe --1-- r1 --2-- r2 --3-- tspu --4-- r3 --5-- far
#
#   hop 1 = r1, 2 = r2, 3 = tspu, 4 = r3, 5 = far. Every router answers ICMP Time Exceeded. The "TSPU" is a router whose nft
#   rule, at prerouting (before the routing decision and the TTL check, so a packet that would expire there dies silently
#   too), drops every TCP SYN to the blocked addresses; for any other address it is an ordinary hop 3. (The real box is a
#   transparent bridge and no hop at all; for the method the difference is only where the numbers start.) `far` owns the
#   destination addresses, all on its loopback:
#
#   198.51.100.10   the control target (stands for ya.ru): TCP 443 listens
#   198.51.100.66   the blocked reference (stands for a long-blocked address): TCP 443 listens, the TSPU drops its SYNs
#   203.0.113.10    node de1: 443 listens, 8443 does not (RST)
#   203.0.113.20    node nl1: 443 listens, but the TSPU drops its SYNs
#   203.0.113.30    node de2: nothing is dropped, 443 listens
#
# The probe runs in the `probe` namespace as an unprivileged user with every capability dropped (CapEff 0), which is the
# proof that it needs no CAP_NET_RAW.

set -Eeuo pipefail
SELF=$(readlink -f "$0")
ROOT=$(cd "$(dirname "$SELF")/../.." && pwd)

if [ -z "${MG_TSPU_NS:-}" ]; then
  [ "$(id -u)" = 0 ] || { echo "run as root (wsl -d Ubuntu -u root)" >&2; exit 2; }
  for tool in go ip nft python3 setpriv unshare nsenter; do
    command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
  done
  WORK=$(mktemp -d /tmp/mg-tspu.XXXXXX)
  # built outside the namespace (the toolchain may need the network); the namespace itself has none
  (cd "$ROOT" && go build -o "$WORK/tspuprobe" ./scripts/e2e/tspuprobe)
  chmod 755 "$WORK"
  exec env MG_TSPU_NS=1 MG_TSPU_WORK="$WORK" unshare -n -m -- bash "$SELF" "$@"
fi

WORK=$MG_TSPU_WORK
KEEP=""
[ "${1:-}" != --keep ] || KEEP=1
T0=$(date +%s)
RESULTS=()
PIDS=()
say() { printf '[%3ds] %s\n' $(($(date +%s) - T0)) "$*"; }
pass() { say "PASS  $*"; RESULTS+=("PASS  $*"); }
die() { say "FAIL  $*"; RESULTS+=("FAIL  $*"); exit 1; }

cleanup() {
  local rc=$? p
  set +e
  for ((p = ${#PIDS[@]} - 1; p >= 0; p--)); do kill -KILL "${PIDS[$p]}" 2>/dev/null; done
  [ -n "$KEEP" ] || rm -rf "$WORK"
  printf '\n'
  printf '%s\n' "${RESULTS[@]}"
  [ "$rc" = 0 ] && echo "ALL PASS" || echo "FAILED (exit $rc)"
  exit "$rc"
}
trap cleanup EXIT

ip link set lo up

# ---------------------------------------------------------------- namespaces and links

spawn() { "$@" & LAST_PID=$!; PIDS+=("$LAST_PID"); disown "$LAST_PID"; }
declare -A NSPID
mk() {
  local name=$1
  spawn unshare -n sleep 100000
  NSPID[$name]=$LAST_PID
  local i
  for ((i = 0; i < 50; i++)); do nsenter -t "${NSPID[$name]}" -n true 2>/dev/null && break; sleep 0.1; done
  nsenter -t "${NSPID[$name]}" -n true || die "namespace $name did not start"
  nsenter -t "${NSPID[$name]}" -n -- ip link set lo up
  nsenter -t "${NSPID[$name]}" -n -- sysctl -qw net.ipv4.ip_forward=1
}
ns() { local name=$1; shift; nsenter -t "${NSPID[$name]}" -n -- "$@"; }

# link <a> <b> <net, e.g. 10.77.2>: a gets .1 and b gets .2 on a veth pair.
SEQ=0
link() {
  local a=$1 b=$2 net=$3
  SEQ=$((SEQ + 1))
  ip link add "tsa$SEQ" type veth peer name "tsb$SEQ"
  ip link set "tsa$SEQ" netns "${NSPID[$a]}"
  ip link set "tsb$SEQ" netns "${NSPID[$b]}"
  ns "$a" ip addr add "$net.1/30" dev "tsa$SEQ"
  ns "$b" ip addr add "$net.2/30" dev "tsb$SEQ"
  ns "$a" ip link set "tsa$SEQ" up
  ns "$b" ip link set "tsb$SEQ" up
}

CONTROL=198.51.100.10
BLOCKED_REF=198.51.100.66
DE1=203.0.113.10
NL1=203.0.113.20
DE2=203.0.113.30

topology() {
  local n
  for n in probe r1 r2 tspu r3 far; do mk "$n"; done
  link probe r1 10.77.1
  link r1 r2 10.77.2
  link r2 tspu 10.77.3
  link tspu r3 10.77.4
  link r3 far 10.77.5
  # left side of every router: the probe network; right side: the "internet"
  ns probe ip route add default via 10.77.1.2
  ns r1 ip route add default via 10.77.2.2
  ns r2 ip route add 10.77.1.0/30 via 10.77.2.1
  ns r2 ip route add default via 10.77.3.2
  ns tspu ip route add 10.77.1.0/30 via 10.77.3.1
  ns tspu ip route add 10.77.2.0/30 via 10.77.3.1
  ns tspu ip route add default via 10.77.4.2
  ns r3 ip route add 10.77.1.0/30 via 10.77.4.1
  ns r3 ip route add 10.77.2.0/30 via 10.77.4.1
  ns r3 ip route add 10.77.3.0/30 via 10.77.4.1
  ns r3 ip route add default via 10.77.5.2
  ns far ip route add default via 10.77.5.1
  local a
  for a in $CONTROL $BLOCKED_REF $DE1 $NL1 $DE2; do ns far ip addr add "$a/32" dev lo; done
  ns tspu nft -f - <<EOF
table inet tspu {
  set blocked { type ipv4_addr; elements = { $BLOCKED_REF, $NL1 } }
  chain pre {
    type filter hook prerouting priority -300; policy accept;
    ip daddr @blocked tcp flags & (syn | ack) == syn drop
  }
}
EOF
}

# ---------------------------------------------------------------- the far side and the probe

write_listener() {
  cat >"$WORK/listen.py" <<'PY'
# Accepts and holds TCP connections on the given addr:port pairs (a stand-in for a node, or for the control target).
import socket, sys, threading, time
def serve(a):
    s = socket.socket()
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    host, port = a.rsplit(":", 1)
    s.bind((host, int(port)))
    s.listen(64)
    while True:
        c, _ = s.accept()
        threading.Thread(target=lambda c=c: (time.sleep(20), c.close()), daemon=True).start()
for a in sys.argv[1:]:
    threading.Thread(target=serve, args=(a,), daemon=True).start()
time.sleep(100000)
PY
}

# probe_as_nobody <args of tspuprobe>: the probe in the probe namespace, as an unprivileged user with every capability
# dropped from the permitted, inheritable, ambient and bounding sets and no way to get any back.
probe_as_nobody() {
  ns probe setpriv --reuid=65534 --regid=65534 --clear-groups --inh-caps=-all --bounding-set=-all --no-new-privs \
    "$WORK/tspuprobe" "$@"
}

# field <json> <jq-free python expression on d>
field() { python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(eval(sys.argv[2]))' "$1" "$2"; }

expect() { # <what> <got> <want>
  [ "$2" = "$3" ] || die "$1: got '$2', want '$3'"
}

topology
say "topology up: probe - r1 - r2 - tspu - r3 - far"
if [ -n "${MG_EXP:-}" ]; then . "$MG_EXP"; exit 0; fi

write_listener
# every SYN the probe sends, to check afterwards that only the addresses it was given were probed
spawn nsenter -t "${NSPID[probe]}" -n -- tcpdump -i any -nn -l -w "$WORK/probe.pcap" 'tcp[tcpflags] & (tcp-syn|tcp-ack) == tcp-syn'
TCPDUMP_PID=$LAST_PID
spawn nsenter -t "${NSPID[far]}" -n -- python3 "$WORK/listen.py" \
  $CONTROL:443 $BLOCKED_REF:443 $DE1:443 $NL1:443 $DE2:443 $DE2:80 $DE2:8080 $DE2:8443 $DE1:80 $DE1:8080
sleep 1

# sanity of the fake network itself: the TSPU drops SYNs to the blocked addresses and to nothing else
ns probe python3 - "$CONTROL" "$BLOCKED_REF" "$DE1" "$NL1" <<'PY' || die "the fake network does not behave as designed"
import socket, sys
def reach(h):
    s = socket.socket(); s.settimeout(2)
    try: s.connect((h, 443)); return True
    except OSError: return False
    finally: s.close()
control, ref, de1, nl1 = sys.argv[1:]
assert reach(control) and reach(de1), "open addresses must connect"
assert not reach(ref) and not reach(nl1), "blocked addresses must not connect"
PY
pass "the fake network: the open addresses connect, the two blocked ones do not"

# ---------------------------------------------------------------- 1. verdicts, dpi_hop, no capability

step1() {
  local out
  out=$(probe_as_nobody -control $CONTROL -ref $BLOCKED_REF $DE1:443 $DE1:8443 $NL1:443 $DE2:443) || die "the probe failed to run: $out"
  say "$out"
  expect "uid" "$(field "$out" 'd["uid"]')" 65534
  expect "CapEff" "$(field "$out" 'd["cap_eff"]')" 0000000000000000
  expect "raw socket" "$(field "$out" 'd["raw_socket"]')" denied
  expect "error" "$(field "$out" 'd.get("error","")')" ""
  expect "dpi_hop" "$(field "$out" 'd["dpi_hop"]')" 2
  expect "open node" "$(field "$out" 'd["verdicts"]["'$DE1':443"]')" open
  expect "closed port" "$(field "$out" 'd["verdicts"]["'$DE1':8443"]')" refused
  expect "blocked node" "$(field "$out" 'd["verdicts"]["'$NL1':443"]')" blocked
  expect "node with nothing dropped" "$(field "$out" 'd["verdicts"]["'$DE2':443"]')" open
}

step1
pass "dpi_hop = 2 (the router in front of the TSPU at hop 3); an open node is open, a closed port refused, the node the TSPU drops blocked"
pass "the probe ran as uid 65534 with CapEff 0000000000000000 and could not open a raw socket: no CAP_NET_RAW needed"

# ---------------------------------------------------------------- 2. the control does not get past: not a verdict

step2() {
  local out
  ns tspu nft add element inet tspu blocked "{ $CONTROL }"
  out=$(probe_as_nobody -control $CONTROL -ref $BLOCKED_REF $DE1:443 $NL1:443) || die "the probe failed to run: $out"
  say "$out"
  ns tspu nft delete element inet tspu blocked "{ $CONTROL }"
  expect "error" "$(field "$out" 'd.get("error","")')" control_failed
  expect "dpi_hop" "$(field "$out" 'd["dpi_hop"]')" 2
  expect "verdicts" "$(field "$out" 'len(d.get("verdicts") or {})')" 0
}
step2
pass "a control target the TSPU drops answers control_failed (dpi_hop still 2) and no verdict about any node"

# ---------------------------------------------------------------- 3. no way to place the TSPU

step3() {
  local out
  out=$(probe_as_nobody -control $CONTROL -ref $DE2 $DE1:443) || die "the probe failed to run: $out"
  expect "a reference that connects" "$(field "$out" 'd.get("error","")')" no_dpi_hop
  expect "its reason" "$(field "$out" 'd.get("reason","")')" ref_open
  out=$(probe_as_nobody -control $CONTROL $DE1:443) || die "the probe failed to run: $out"
  expect "no reference" "$(field "$out" 'd.get("error","")')" no_dpi_hop
  expect "its reason" "$(field "$out" 'd.get("reason","")')" no_ref
  # no router answers: the probe's own firewall drops every Time Exceeded, as a path that hides its hops would
  ns probe nft -f - <<EOF
table inet hidden {
  chain in {
    type filter hook input priority 0; policy accept;
    icmp type time-exceeded drop
  }
}
EOF
  out=$(probe_as_nobody -control $CONTROL -ref $BLOCKED_REF $DE1:443) || die "the probe failed to run: $out"
  ns probe nft delete table inet hidden
  expect "a path with no answering router" "$(field "$out" 'd.get("error","")')" no_dpi_hop
  expect "its reason" "$(field "$out" 'd.get("reason","")')" no_hops
}
step3
pass "no_dpi_hop says why: reason=ref_open for a reference that connects, no_ref for none, no_hops when no router answers"

# ---------------------------------------------------------------- 4. ICMP rate limit: a parallel pass lies, the alone pass does not

step4() {
  local out n
  # the TSPU router answers at most 2 Time Exceeded a second after a burst of 2: eight probes at once to its hop lose most
  ns tspu nft -f - <<EOF
table inet tspulimit {
  counter dropped {}
  chain out {
    type filter hook output priority 0; policy accept;
    icmp type time-exceeded limit rate over 2/second burst 2 packets counter name dropped drop
  }
}
EOF
  out=$(probe_as_nobody -control $CONTROL -ref $BLOCKED_REF -hop-limit 1     $DE1:443 $DE1:80 $DE1:8080 $DE2:443 $DE2:80 $DE2:8080 $DE2:8443 $NL1:443) || die "the probe failed to run: $out"
  say "$out"
  n=$(ns tspu nft list counter inet tspulimit dropped | awk '/packets/{print $2}')
  ns tspu nft delete table inet tspulimit
  [ "${n:-0}" -ge 3 ] || die "the rate limit dropped only ${n:-0} answers: the test did not provoke what it is for"
  expect "error" "$(field "$out" 'd.get("error","")')" ""
  for t in $DE1:443 $DE1:80 $DE1:8080 $DE2:443 $DE2:80 $DE2:8080 $DE2:8443; do
    expect "open $t under the limit" "$(field "$out" 'd["verdicts"]["'$t'"]')" open
  done
  expect "blocked node under the limit" "$(field "$out" 'd["verdicts"]["'$NL1':443"]')" blocked
  say "the rate limit dropped $n answers"
}
step4
pass "ICMP rate limit on the TSPU router: the parallel pass loses answers, the alone pass finds them (7 open stay open, the blocked one stays blocked)"

# ---------------------------------------------------------------- 5. only the given addresses were probed

step5() {
  kill -INT "$TCPDUMP_PID" 2>/dev/null || true
  sleep 1
  local dst d
  dst=$(tcpdump -nn -r "$WORK/probe.pcap" 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i==">"){split($(i+1),a,"."); print a[1]"."a[2]"."a[3]"."a[4]}}' | sort -u)
  say "SYN destinations: $(echo $dst)"
  [ -n "$dst" ] || die "no SYN was captured"
  for d in $dst; do
    case $d in
      $CONTROL|$BLOCKED_REF|$DE1|$NL1|$DE2) ;;
      *) die "the probe sent a SYN to $d, which nobody asked for" ;;
    esac
  done
}
step5
pass "every SYN the probe sent went to one of the five addresses it was given"
