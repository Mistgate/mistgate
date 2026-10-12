# The SSH install of a node, end to end, for both editions of scripts/e2e-wsl.sh (the VPS panel and --edge, the Cloudflare
# edition). Sourced by it, never run alone: it uses the helpers, variables and the isolation of the main script (private
# network namespace, $WORK, $BIN, api, mutate, stepup, say, pass, note, die, wait_for, spawn, stop_pid, free_port, SECRETS ...).
#
# THE HOST THE PANEL INSTALLS ON. A real OpenSSH sshd (the system service stays masked: this one has a configuration file
# of its own, listens on an address of this run's network namespace and dies with it) on a public-looking address that lives
# on the namespace's lo (the panel refuses private ones). It runs in a mount namespace of its own (unshare -m), so what the
# install does to "the host" never reaches the machine the test runs on, and the test itself keeps its /root and /etc:
#   - /etc/passwd and /etc/shadow: private copies with one-time passwords for root and for one unprivileged login
#   - /root, /var/lib, /usr/local and /etc/systemd/system: empty tmpfs mounts (the agent lands in /root, /usr/local/bin and
#     /var/lib/mistgate-node, its unit in /etc/systemd/system)
#   - /etc/ssl/certs/ca-certificates.crt: the system roots plus the run's CA (the edge edition's Worker serves a certificate
#     of that CA, and the agent the install enrols over the link must trust it)
#   - /usr/local/sbin/systemctl: a stand-in for systemd. Only the mistgate-node unit does anything: `restart` starts the
#     ExecStart line of the unit file the agent wrote, detached, like a service. Any other unit is left alone.
#   - /usr/local/sbin/cat: runs /bin/cat, except for the upload (`cat > /root/mistgate-node.new`) when a test asks it to
#     be slow, or to keep only the first bytes and exit 0 (what a dropped connection leaves behind: the S1 spike saw it).
# Flags are files in $SSHN_D/flags: hold-agent (the unit is installed but `restart` starts nothing) and cat-mode.
#
# WHAT IT PROVES, per edition: the wizard (host key, wrong password, wrong host key), a cancelled install, a node retired
# while the install waits for it, a truncated upload that must not be installed, and the full install: the agent comes
# online, the subscription lists its inbound, a real Hysteria2 client uses it. On the edge also: a reload of the
# Provisioner's script in the middle of an upload (the job resumes), and the Go panel under load from another Durable
# Object while a job runs. The functions of an edition set SSHN_* before calling them (see edge.sh and e2e-wsl.sh).

SSHN_IP=45.67.89.10 # a public-looking address (the panel refuses private ones); it lives on this namespace's lo only
SSHN_USER=mgsshtest # an unprivileged login of this run, not a root and without sudo
SSHN_TAG=v0.1.0     # the release tag of the edge edition's fake release
SSHN_D=""
SSHN_PID=""
SSHN_MNT=""
SSHN_CLOSED_CODE=ssh_fingerprint_unavailable # what a closed port answers; the edge edition's sockets say ssh_target_refused
SSHN_NODE_BIN_SHA="" # sha256 of the agent the panel is expected to upload (the release's, or the bundle's)
SSHN_MADE_RUN_SSHD=""

# sshn_host <command...>: run something in the sshd's mount namespace, i.e. on "the host"
sshn_host() { nsenter -t "$SSHN_PID" -m -- "$@"; }

# ---------------------------------------------------------------- sshd and the host around it

sshn_up() {
  local d=$WORK/sshn
  [ -x /usr/sbin/sshd ] || die "the SSH install steps need openssh-server (/usr/sbin/sshd); apt-get install openssh-server (the system service stays masked)"
  for tool in nsenter unshare ssh-keygen python3; do
    command -v "$tool" >/dev/null || die "the SSH install steps need $tool"
  done
  SSHN_D=$d
  mkdir -p "$d/flags" "$d/shim"
  ssh-keygen -q -t ed25519 -N '' -f "$d/host_ed25519" || die "ssh-keygen failed"
  SSHN_FP=$(ssh-keygen -lf "$d/host_ed25519.pub" | awk '{print $2}')
  SSHN_ROOT_PASS=$(openssl rand -hex 12) SSHN_USER_PASS=$(openssl rand -hex 12)
  SECRETS+=("$SSHN_ROOT_PASS" "$SSHN_USER_PASS")
  # private copies of passwd and shadow: root and the unprivileged login get the one-time passwords
  python3 - "$d" "$SSHN_USER" "$SSHN_ROOT_PASS" "$SSHN_USER_PASS" <<'PY'
import subprocess, sys
d, user, root_pw, user_pw = sys.argv[1:5]
def crypt(p):
    return subprocess.run(["openssl", "passwd", "-6", "-stdin"], input=p, capture_output=True, text=True, check=True).stdout.strip()
shadow = []
for line in open("/etc/shadow").read().splitlines():
    f = line.split(":")
    if f[0] == "root":
        f[1] = crypt(root_pw)
        f[2:] = ["19000", "0", "99999", "7", "", "", ""]
    shadow.append(":".join(f))
shadow.append("%s:%s:19000:0:99999:7:::" % (user, crypt(user_pw)))
open(d + "/shadow", "w").write("\n".join(shadow) + "\n")
passwd = open("/etc/passwd").read()
if not passwd.endswith("\n"):
    passwd += "\n"
open(d + "/passwd", "w").write(passwd + "%s:x:61999:65534::%s:/bin/sh\n" % (user, d))
PY
  chmod 600 "$d/shadow"
  cat /etc/ssl/certs/ca-certificates.crt ${CURL_CA:+"$CURL_CA"} >"$d/ca-bundle.crt"
  sshn_write_shims
  # sshd insists on its privilege separation directory; the mount namespace cannot hide one that the host lacks
  [ -d /run/sshd ] || { mkdir -p /run/sshd && SSHN_MADE_RUN_SSHD=1; }
  cat >"$d/sshd_config" <<CONF
Port 22
ListenAddress $SSHN_IP
HostKey $d/host_ed25519
PidFile $d/sshd.pid
AuthorizedKeysFile none
PasswordAuthentication yes
KbdInteractiveAuthentication no
PubkeyAuthentication no
UsePAM no
PermitRootLogin yes
PermitUserEnvironment yes
AllowUsers $SSHN_USER root
StrictModes no
LogLevel VERBOSE
CONF
  cat >"$d/launch.sh" <<LAUNCH
set -e
mount --bind $d/passwd /etc/passwd
mount --bind $d/shadow /etc/shadow
mount --bind $d/ca-bundle.crt /etc/ssl/certs/ca-certificates.crt
mount -t tmpfs -o size=1g,mode=0700 tmpfs /root
mount -t tmpfs tmpfs /var/lib
mount -t tmpfs tmpfs /usr/local
mount -t tmpfs tmpfs /etc/systemd/system
mkdir /var/lib/mistgate-node /usr/local/bin /usr/local/sbin /root/.ssh
chmod 700 /root/.ssh
cp $d/shim/systemctl $d/shim/cat /usr/local/sbin/
chmod 755 /usr/local/sbin/systemctl /usr/local/sbin/cat
# the PATH of a command run over SSH: the stand-ins first, then the usual
echo 'PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' >/root/.ssh/environment
exec /usr/sbin/sshd -D -e -f $d/sshd_config -E $WORK/sshd.log
LAUNCH
  ip addr show dev lo | grep -q "inet $SSHN_IP/32" || ip addr add "$SSHN_IP/32" dev lo
  /usr/sbin/sshd -t -f "$d/sshd_config" || die "the sshd configuration is not valid"
  # unshare execs bash and bash execs sshd, so LAST_PID is the sshd and /proc/<pid>/ns/mnt is the host's mount namespace
  spawn unshare -m bash "$d/launch.sh"
  SSHN_PID=$LAST_PID
  sshd_listens() { ss -ltn "sport = :22" | grep -q "$SSHN_IP:22"; }
  wait_for 15 "sshd to listen on $SSHN_IP:22" sshd_listens || { tail -n 10 "$WORK/sshd.log" 2>/dev/null; die "sshd did not start"; }
  SSHN_MNT=$(readlink "/proc/$SSHN_PID/ns/mnt")
  [ "$SSHN_MNT" != "$(readlink /proc/$$/ns/mnt)" ] || die "the sshd shares the mount namespace of the test"
  sshn_host test -x /usr/local/sbin/systemctl || die "the host's systemd stand-in is not in place"
}

sshn_write_shims() {
  local d=$SSHN_D
  # the shebang first: the agent runs `systemctl` with exec (no shell to fall back on a script without one)
  { printf '#!/bin/sh\nF=%q\nLOG=%q\nAGENT_LOG=%q\n' "$d/flags" "$WORK/sshn-shim.log" "$WORK/node-ssh.log"; cat <<'SH'
echo "systemctl $*" >>"$LOG"
case "$1" in
  restart | start)
    case "${2:-}" in
      mistgate-node | mistgate-node.service) ;;
      *) exit 0 ;;
    esac
    if [ -e "$F/hold-agent" ]; then echo "systemctl: the agent is held back" >>"$LOG"; exit 0; fi
    if [ -s "$F/agent.pid" ] && kill -0 "$(cat "$F/agent.pid")" 2>/dev/null; then
      kill "$(cat "$F/agent.pid")" 2>/dev/null || true
      sleep 1
    fi
    cmd=$(sed -n 's/^ExecStart=//p' /etc/systemd/system/mistgate-node.service | head -n 1)
    [ -n "$cmd" ] || { echo "systemctl: the unit has no ExecStart" >&2; exit 1; }
    # detached from the ssh session (own session, no inherited pipes), like a service
    # shellcheck disable=SC2086 # $cmd is a command line of words on purpose
    setsid sh -c 'echo $$ >"$1"; shift; exec "$@"' sh "$F/agent.pid" $cmd </dev/null >>"$AGENT_LOG" 2>&1 &
    exit 0 ;;
  is-active) exit 3 ;;
  *) exit 0 ;;
esac
SH
  } >"$d/shim/systemctl"
  { printf '#!/bin/sh\nF=%q\nSHIM=%q\nLOG=%q\n' "$d/flags" "$d/shim" "$WORK/sshn-shim.log"; cat <<'SH'
if [ "$#" -eq 0 ] && [ -s "$F/cat-mode" ] && [ "$(readlink /proc/$$/fd/1)" = /root/mistgate-node.new ]; then
  exec python3 "$SHIM/cat.py" "$(/bin/cat "$F/cat-mode")" "$LOG"
fi
exec /bin/cat "$@"
SH
  } >"$d/shim/cat"
  cat >"$d/shim/cat.py" <<'PY'
# cat > /root/mistgate-node.new, misbehaving: slow:<bytes per second> or truncate:<bytes to keep>. Reads all of its input
# either way and exits 0 when the input ends, as cat does on a truncated stream. The log line says how far it got.
import os, signal, sys, time

mode, _, arg = sys.argv[1].partition(":")
limit = int(arg)
total = kept = 0
t0 = time.time()
signal.signal(signal.SIGTERM, lambda *_: sys.exit(1))
signal.signal(signal.SIGHUP, lambda *_: sys.exit(1))
try:
    while True:
        chunk = os.read(0, 65536)
        if not chunk:
            break
        total += len(chunk)
        if mode == "slow":
            os.write(1, chunk)
            kept += len(chunk)
            time.sleep(len(chunk) / limit)
        else:
            part = chunk[: max(0, limit - kept)]
            if part:
                os.write(1, part)
                kept += len(part)
finally:
    with open(sys.argv[2], "a") as log:
        log.write("cat-shim %s read=%d written=%d after=%.1fs\n" % (sys.argv[1], total, kept, time.time() - t0))
PY
}

# sshn_cat_mode [slow:<bytes/s> | truncate:<bytes>]: how the host's upload behaves; no argument: normally
sshn_cat_mode() { if [ $# -gt 0 ]; then printf '%s' "$1" >"$SSHN_D/flags/cat-mode"; else rm -f "$SSHN_D/flags/cat-mode"; fi; }
sshn_hold_agent() { if [ "$1" = on ]; then : >"$SSHN_D/flags/hold-agent"; else rm -f "$SSHN_D/flags/hold-agent"; fi; }

# sshn_reset_host: the host as it was before any install (the files of a node, the shims' flags and state)
sshn_reset_host() {
  local pid
  if [ -s "$SSHN_D/flags/agent.pid" ]; then
    pid=$(cat "$SSHN_D/flags/agent.pid")
    kill "$pid" 2>/dev/null || true
  fi
  : >"$WORK/sshn-shim.log"
  rm -f "$SSHN_D/flags/agent.pid" "$SSHN_D/flags/cat-mode" "$SSHN_D/flags/hold-agent"
  sshn_host sh -c 'rm -rf /var/lib/mistgate-node /root/mistgate-node /root/mistgate-node.new /usr/local/bin/mistgate-node* /etc/systemd/system/mistgate-node.service; mkdir /var/lib/mistgate-node'
}

# sshn_reap: whatever of the host is still running when the test ends: the agent the stand-in started, and anything else in
# the host's mount namespace (an sshd session). Only processes of that namespace, never anything else.
sshn_reap() {
  local p ns
  [ -z "$SSHN_MADE_RUN_SSHD" ] || rmdir /run/sshd 2>/dev/null || true
  [ -n "$SSHN_MNT" ] || return 0
  ns=$(ip netns identify $$ 2>/dev/null || true)
  for p in $(ip netns pids "$ns" 2>/dev/null); do
    [ "$p" != $$ ] || continue
    [ "$(readlink "/proc/$p/ns/mnt" 2>/dev/null)" = "$SSHN_MNT" ] && kill -KILL "$p" 2>/dev/null || true
  done
}

ssh_attempts() { grep -c 'Failed password\|Accepted password' "$WORK/sshd.log" || true; }

# ---------------------------------------------------------------- helpers over the admin API

sshn_start() { # <node name> [username] [password]: starts the install job, sets SSHN_JOB and SSHN_NODE_ID
  local out
  out=$(mutate ProvisioningService/StartNodeProvision "$(jq -nc --arg n "$1" --arg h "$SSHN_IP" --arg f "$SSHN_FP" --arg u "${2:-root}" --arg p "${3:-$SSHN_ROOT_PASS}" \
    '{confirmInstall:true, name:$n, address:$h, countryCode:"DE", location:"Test", provider:"e2e", sshHost:$h, sshPort:22, fingerprint:$f, sshUsername:$u, password:$p}')") \
    || die "StartNodeProvision($1) failed"
  SSHN_JOB=$(jq -r .job.id <<<"$out") SSHN_NODE_ID=$(jq -r .job.nodeId <<<"$out") SSHN_NODE_NAME=$1
  [ -n "$SSHN_JOB" ] && [ "$SSHN_JOB" != null ] || die "StartNodeProvision returned no job: $out"
}
sshn_job() { api ProvisioningService/GetNodeProvision "{\"jobId\":\"$SSHN_JOB\"}" | jq -c .job; }
sshn_jstate() { sshn_job | jq -r .state; }
sshn_jphase() { sshn_job | jq -r .phase; }
sshn_jcode() { sshn_job | jq -r '.errorCode // ""'; }
sshn_events() { api ProvisioningService/ListNodeProvisionEvents "{\"jobId\":\"$SSHN_JOB\",\"limit\":200}" | jq -r '.events[].code'; }
sshn_has_event() { sshn_events | grep -qx "$1"; }
sshn_timeline() { # the journal as code+seconds since the first event, e.g. "created+0 started+0 ... agent_connected+4"
  api ProvisioningService/ListNodeProvisionEvents "{\"jobId\":\"$SSHN_JOB\",\"limit\":200}" \
    | jq -r '.events as $e | [$e[] | "\(.code)+\((.createdUnix | tonumber) - ($e[0].createdUnix | tonumber))"] | join(" ")'
}
sshn_in_phase() { [ "$(sshn_jphase)" = "$1" ]; }
sshn_in_state() { [ "$(sshn_jstate)" = "$1" ]; }
sshn_terminal() { case $(sshn_jstate) in completed | failed | cancelled) return 0 ;; esac; return 1; }
sshn_secret_len() { # the sealed login of the job, in bytes (0: erased)
  ${SSHN_SQL:?} "SELECT length(secret) FROM node_provision_job WHERE id = ?" "$SSHN_JOB"
}
sshn_node_status() { api NodeService/ListNodes '{"includeRetired":true}' | jq -r --arg n "$1" '.nodes[] | select(.name == $n) | .status'; }
sshn_upload_size() { sshn_host stat -c %s /root/mistgate-node.new 2>/dev/null || echo 0; }
sshn_upload_started() { [ "$(sshn_upload_size)" -ge "$1" ]; }

# sshn_retire: the job's node is retired (every scenario leaves the panel as it found it)
sshn_retire() {
  mutate NodeService/RetireNode "{\"nodeId\":\"$SSHN_NODE_ID\",\"confirmName\":\"$SSHN_NODE_NAME\"}" >/dev/null || die "RetireNode($SSHN_NODE_NAME) failed"
}

# sshn_page_cancel: the cancel button of the installer page (there is no RPC for it): POST <admin>/nodes/install
sshn_page_cancel() {
  stepup || die "step-up failed"
  curl -sS -m 40 ${CURL_CA:+--cacert "$CURL_CA" --compressed} -D "$WORK/cancel.hdr" -o "$WORK/cancel.body" -w '%{http_code}' -X POST "$ADMIN/nodes/install" -H "Cookie: $COOKIE" \
    --data-urlencode action=cancel --data-urlencode "job_id=$SSHN_JOB" --data-urlencode confirm_cancel=yes
}

# Load on the panel from another Durable Object while a job runs (the edge edition redefines these): sshn_load_start
# right after the job was started, sshn_load_stop when it is over; the stop fails the run if the load found a fault.
sshn_load_start() { :; }
sshn_load_stop() { :; }

# ---------------------------------------------------------------- scenarios

# sshn_wizard: the checks the wizard makes before an install: the host key without a login, a closed port, a wrong password,
# a wrong host key (the password must not be sent), an unprivileged login (gets in, stops at the preflight) and root (passes it).
sshn_wizard() {
  local err fp before check_ssh out
  fp=$(api ProvisioningService/GetSSHFingerprint "{\"host\":\"$SSHN_IP\",\"port\":22}" | jq -r .fingerprint) || die "GetSSHFingerprint failed"
  [ "$fp" = "$SSHN_FP" ] || die "the wizard read host key '$fp', sshd has '$SSHN_FP'"
  [ "$(ssh_attempts)" = 0 ] || die "reading the host key sent a login to sshd"
  err=$(api ProvisioningService/GetSSHFingerprint "{\"host\":\"$SSHN_IP\",\"port\":2299}" 2>&1 >/dev/null || true)
  grep -q "$SSHN_CLOSED_CODE" <<<"$err" || die "a closed port did not answer $SSHN_CLOSED_CODE: $err"
  stepup || die "step-up failed"
  check_ssh() { jq -nc --arg h "$SSHN_IP" --arg f "$1" --arg u "$2" --arg p "$3" '{host:$h, port:22, fingerprint:$f, username:$u, password:$p}'; }
  err=$(api ProvisioningService/CheckSSH "$(check_ssh "$SSHN_FP" root "wrong-$(openssl rand -hex 6)")" 2>&1 >/dev/null || true)
  grep -q 'ssh_authentication_failed' <<<"$err" || die "a wrong password did not answer ssh_authentication_failed: $err"
  [ "$(ssh_attempts)" = 1 ] || die "sshd saw $(ssh_attempts) login attempts after the wrong password, want 1"
  err=$(api ProvisioningService/CheckSSH "$(check_ssh "$SSHN_FP" "$SSHN_USER" "$SSHN_USER_PASS")" 2>&1 >/dev/null || true)
  grep -q "Accepted password for $SSHN_USER" "$WORK/sshd.log" || die "sshd never accepted the password of the unprivileged login: $err"
  ! grep -q 'ssh_authentication_failed\|ssh_connection\|ssh_target' <<<"$err" || die "the unprivileged login did not get through the login: $err"
  before=$(ssh_attempts)
  err=$(api ProvisioningService/CheckSSH "$(check_ssh "SHA256:$(printf 'A%.0s' $(seq 43))" root "$SSHN_ROOT_PASS")" 2>&1 >/dev/null || true)
  grep -q 'ssh_host_key_changed' <<<"$err" || die "a wrong host key did not answer ssh_host_key_changed: $err"
  [ "$(ssh_attempts)" = "$before" ] || die "the password was sent to a host whose key did not match"
  out=$(api ProvisioningService/CheckSSH "$(check_ssh "$SSHN_FP" root "$SSHN_ROOT_PASS")") || die "root with the right password did not pass the preflight"
  jq -e '.preflight | .distribution == "ubuntu" and .systemd == true and .panelReachable == true and (.alreadyEnrolled // false) == false' <<<"$out" >/dev/null \
    || die "unexpected preflight of the test host: $(jq -c .preflight <<<"$out")"
  pass "SSH wizard: the host key read without a login matches sshd's, a closed port answers $SSHN_CLOSED_CODE, a wrong password is refused by sshd, an unprivileged login gets in and stops at the preflight, a wrong host key never gets the password, root passes the preflight ($(jq -c '.preflight | {distribution, version, systemd, panelReachable}' <<<"$out"))"
}

# sshn_scn_cancel <name>: the owner cancels an install in the middle of the upload (a slow upload, so nothing races)
sshn_scn_cancel() {
  local name=$1 size code
  sshn_reset_host
  sshn_cat_mode "slow:2097152"
  sshn_start "$name"
  sshn_load_start
  wait_for 90 "the job to reach the transfer" sshn_in_phase transfer || die "the job never reached the transfer: $(sshn_job)"
  wait_for 60 "the upload to be under way (3 MB on the host)" sshn_upload_started 3000000 || die "the upload never started: $(sshn_job)"
  sleep 6 # the upload goes on while the load runs
  size=$(sshn_upload_size)
  sshn_in_phase transfer || die "the job left the transfer before the cancel: $(sshn_job)"
  code=$(sshn_page_cancel)
  [ "$code" = 303 ] || die "the installer page answered HTTP $code to the cancel, want 303 (headers: $(tr -d '\r' <"$WORK/cancel.hdr" | grep -iE '^(HTTP|location|content-type)' | tr '\n' ' ') body: $(head -c 300 "$WORK/cancel.body" | tr '\n' ' '))"
  wait_for 60 "the job to end" sshn_terminal || die "the job did not end after the cancel: $(sshn_job)"
  sshn_load_stop
  [ "$(sshn_jstate)" = cancelled ] && [ "$(sshn_jcode)" = remote_outcome_unknown ] || die "after the cancel the job is $(sshn_job), want cancelled/remote_outcome_unknown"
  sshn_has_event cancel_requested && sshn_has_event remote_outcome_unknown || die "the journal lacks cancel_requested / remote_outcome_unknown: $(sshn_events | tr '\n' ' ')"
  ! sshn_has_event enrolling_node && ! sshn_has_event starting_agent || die "the cancelled job went on to enrol or start the agent: $(sshn_events | tr '\n' ' ')"
  [ "$(sshn_secret_len)" = 0 ] || die "the cancelled job still holds its sealed login ($(sshn_secret_len) bytes)"
  sshn_host test ! -e /root/mistgate-node || die "the cancelled job installed the agent"
  sshn_host test ! -e /etc/systemd/system/mistgate-node.service || die "the cancelled job wrote the unit"
  # the stream ends; the host's cat sees the end of its input and exits 0 with a part of the file (nothing waits for it)
  wait_for 30 "the host's upload process to end" sshn_cat_gone || die "the host's upload process is still there after the cancel"
  sshn_retire
  pass "cancel during transfer ($size bytes were on the host): the page answered 303, the job ended cancelled/remote_outcome_unknown, the sealed login was erased, no agent was installed or enrolled; the partial upload is left on the host: $(sshn_upload_size) bytes (job $SSHN_JOB)"
}
sshn_cat_gone() { sshn_host sh -c 'for p in /proc/[0-9]*; do [ "$(readlink $p/fd/1 2>/dev/null)" = /root/mistgate-node.new ] && exit 1; done; exit 0'; }

# sshn_scn_retire_waiting <name>: the owner retires the node while the install waits for the agent
sshn_scn_retire_waiting() {
  local name=$1
  sshn_reset_host
  sshn_hold_agent on
  sshn_cat_mode "slow:4194304"
  sshn_start "$name"
  sshn_load_start
  wait_for 120 "the job to wait for the agent" sshn_in_phase waiting_node || die "the job never reached waiting_node: $(sshn_job)"
  sshn_host test -x /root/mistgate-node && sshn_host test -s /var/lib/mistgate-node/identity.pem && sshn_host test -s /etc/systemd/system/mistgate-node.service \
    || die "waiting_node, but the host has no agent, identity or unit"
  grep -q 'systemctl restart mistgate-node' "$WORK/sshn-shim.log" || die "waiting_node, but nobody asked systemd to start the unit"
  [ -s "$SSHN_D/flags/agent.pid" ] && die "the agent runs although the host held it back"
  sleep 6 # the job waits while the load runs
  sshn_in_phase waiting_node || die "the job left waiting_node by itself: $(sshn_job)"
  sshn_retire
  wait_for 60 "the job to end" sshn_terminal || die "the job did not end after the node was retired: $(sshn_job)"
  sshn_load_stop
  [ "$(sshn_jstate)" = cancelled ] && [ "$(sshn_jcode)" = remote_outcome_unknown ] || die "the job of the retired node is $(sshn_job), want cancelled/remote_outcome_unknown"
  [ "$(sshn_secret_len)" = 0 ] || die "the job of the retired node still holds its sealed login ($(sshn_secret_len) bytes)"
  [ "$(sshn_node_status "$name")" = NODE_STATUS_RETIRED ] || die "the node is $(sshn_node_status "$name"), want RETIRED"
  sshn_hold_agent off
  pass "retire in waiting_node: the agent was installed and enrolled but never started; RetireNode ended the job ($(sshn_jstate)/$(sshn_jcode); the node_retired code only appears when the worker sees the retire before the store does), the sealed login was erased, the node is RETIRED (job $SSHN_JOB)"
}

# sshn_scn_truncated: the host keeps only the first bytes of the upload and says nothing (cat exits 0). The panel must see it
# in the digest it asks the host for, and neither install nor enrol that file.
sshn_scn_truncated() {
  local name=$1
  sshn_reset_host
  sshn_cat_mode "truncate:4194304"
  sshn_start "$name"
  wait_for 120 "the job to end" sshn_terminal || die "the job did not end: $(sshn_job)"
  [ "$(sshn_jstate)" = failed ] && [ "$(sshn_jcode)" = agent_transfer_failed ] || die "a truncated upload ended as $(sshn_job), want failed/agent_transfer_failed"
  grep -q 'cat-shim truncate:4194304 read=[0-9]* written=4194304' "$WORK/sshn-shim.log" || die "the host's cat did not truncate: $(tail -n 3 "$WORK/sshn-shim.log")"
  sshn_host test ! -e /root/mistgate-node.new || die "the truncated file is still on the host ($(sshn_upload_size) bytes)"
  sshn_host test ! -e /root/mistgate-node || die "the truncated file was installed"
  ! sshn_has_event enrolling_node && ! sshn_has_event starting_agent || die "the job went on after the truncated upload: $(sshn_events | tr '\n' ' ')"
  sshn_host test ! -e /var/lib/mistgate-node/identity.pem || die "the node was enrolled after a truncated upload"
  [ "$(sshn_secret_len)" = 0 ] || die "the failed job still holds its sealed login ($(sshn_secret_len) bytes)"
  sshn_retire
  pass "truncated upload: the host kept 4194304 bytes and exited 0; the panel's digest check on the host caught it (failed/agent_transfer_failed), the file was removed, nothing was installed or enrolled, the sealed login was erased (job $SSHN_JOB)"
}

# sshn_scn_full <name> [reload]: the install that must work. reload: the Provisioner's script is changed in the middle of the
# upload (the edge edition: wrangler reloads, the object is reset and the job must resume by itself).
sshn_scn_full() {
  local name=$1 reload=${2:-} t0 secs size kept got want events lines uri save_name save_id timeline
  sshn_reset_host
  [ -z "$reload" ] || sshn_cat_mode "slow:2097152"
  t0=$(date +%s)
  sshn_start "$name"
  if [ -n "$reload" ]; then
    wait_for 90 "the job to reach the transfer" sshn_in_phase transfer || die "the job never reached the transfer: $(sshn_job)"
    wait_for 60 "the upload to be under way (3 MB on the host)" sshn_upload_started 3000000 || die "the upload never started: $(sshn_job)"
    size=$(sshn_upload_size)
    sshn_reload_provisioner
    # the stalled upload is cut with the isolate: the host's cat sees the end of its input and exits 0 with a part of the file
    wait_for 60 "the interrupted upload to end on the host" grep -q 'cat-shim slow' "$WORK/sshn-shim.log" || die "the host's upload never ended after the reload"
    kept=$(sed -n 's/^cat-shim slow:[0-9]* read=[0-9]* written=\([0-9]*\).*/\1/p' "$WORK/sshn-shim.log" | head -1)
    [ "$kept" -lt "$(stat -c %s "$SSHN_NODE_BIN")" ] || die "the interrupted upload was complete ($kept bytes)"
    sshn_cat_mode # the resumed job uploads at full speed
    wait_for 120 "the job to be resumed" sshn_has_event resumed_after_restart || die "the journal has no resumed_after_restart: $(sshn_events | tr '\n' ' ')"
  fi
  wait_for 240 "the job to end" sshn_terminal || die "the job did not end: $(sshn_job) events: $(sshn_events | tr '\n' ' ')"
  [ "$(sshn_jstate)" = completed ] || die "the install ended as $(sshn_job), events: $(sshn_events | tr '\n' ' ')"
  secs=$(($(date +%s) - t0))
  events=$(sshn_events | tr '\n' ' ')
  timeline=$(sshn_timeline)
  for e in started checking_host preparing_host_firewall uploading_agent enrolling_node starting_agent waiting_for_agent agent_connected; do
    sshn_has_event "$e" || die "the journal lacks $e: $events"
  done
  [ -z "$reload" ] || { sshn_has_event resumed_after_restart || die "no resumed_after_restart: $events"; }
  [ "$(sshn_secret_len)" = 0 ] || die "the completed job still holds its sealed login ($(sshn_secret_len) bytes)"
  # the host: the file the panel was meant to upload, installed; the temporary one gone; the unit written; the agent running
  got=$(sshn_host sha256sum /usr/local/bin/mistgate-node | awk '{print $1}')
  want=$SSHN_NODE_BIN_SHA
  [ "$got" = "$want" ] || die "the agent on the host has sha256 ${got:0:12}, the release's is ${want:0:12}"
  sshn_host test ! -e /root/mistgate-node.new || die "the temporary upload is left on the host"
  sshn_host grep -q '^ExecStart=/usr/local/bin/mistgate-node run' /etc/systemd/system/mistgate-node.service || die "the unit has no ExecStart for the agent"
  alive "$(cat "$SSHN_D/flags/agent.pid")" || die "the agent is not running on the host"
  save_name=$NODE_NAME save_id=$NODE_ID # the helpers of the base scenario (node_online, inbounds_active) look at these
  NODE_NAME=$name NODE_ID=$SSHN_NODE_ID
  node_online || die "the install completed but the node is $(node_status)"
  # a profile on the new node and the subscription of an existing user that lists it
  api ProfileService/CreateInbound "{\"profileId\":\"$PROFILE_ID\",\"nodeId\":\"$NODE_ID\"}" >/dev/null || die "CreateInbound on the new node failed"
  wait_for 60 "the inbound ACTIVE on the new node" inbounds_active 1 || die "the inbound did not become ACTIVE: $(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[0] | {state, lastError}')"
  sub_has_node() { fetch_sub 'Happ/3.0' "$WORK/sub-ssh.hdr" | base64 -d 2>/dev/null | grep -q "^hysteria2://.*@$SSHN_IP:$HY2"; }
  wait_for 60 "the subscription to list the new node" sub_has_node || die "the subscription has no hysteria2:// line for $SSHN_IP:$HY2"
  lines=$(fetch_sub 'Happ/3.0' "$WORK/sub-ssh.hdr" | base64 -d)
  [ "$(grep -c '^hysteria2://' <<<"$lines")" = 1 ] || die "expected exactly one hysteria2:// line in the subscription, got: $(grep -c '^hysteria2://' <<<"$lines")"
  URI=$(grep -m1 '^hysteria2://' <<<"$lines")
  SECRETS+=("$(URI=$URI python3 -c 'import os; from urllib.parse import urlsplit, unquote; print(unquote(urlsplit(os.environ["URI"]).username or ""))')")
  uri=$(URI=$URI python3 -c 'import os; from urllib.parse import urlsplit, parse_qs; q = parse_qs(urlsplit(os.environ["URI"]).query).get("obfs-password", [""])[0]; print(q if q != "••••" else "")')
  [ -z "$uri" ] || SECRETS+=("$uri")
  start_client ssh "$SOCKS" || { tail -n 15 "$WORK/client-ssh.log" | redact; die "a Hysteria2 client built from the subscription did not connect to the installed node"; }
  SSH_CLIENT=$LAST_PID
  dl_one "$SOCKS" 1000000 0 -o /dev/null >/dev/null || die "the client connected but cannot download through the installed node"
  stop_pid "$SSH_CLIENT"
  pass "full install$([ -z "$reload" ] || echo " with the Provisioner reloaded in the middle of the upload ($size of $(stat -c %s "$SSHN_NODE_BIN") bytes were on the host, the interrupted cat kept $kept)") in ${secs} s: journal [$timeline], agent sha256 ${want:0:12} on the host, node ONLINE, inbound ACTIVE, the subscription lists $SSHN_IP:$HY2 and a real Hysteria2 client downloaded through it (job $SSHN_JOB)"
  # the owner retires the node: the agent is told, removes its files and exits
  mutate NodeService/RetireNode "{\"nodeId\":\"$NODE_ID\",\"confirmName\":\"$name\"}" | jq -e '.agentNotified == true' >/dev/null || die "RetireNode: the installed agent was not notified"
  wait_for 30 "the installed agent to exit" not_alive "$(cat "$SSHN_D/flags/agent.pid")" || die "the installed agent kept running after the retire"
  [ "$(node_status)" = NODE_STATUS_RETIRED ] || die "node status after the retire: $(node_status)"
  NODE_NAME=$save_name NODE_ID=$save_id
  ! nft list table inet "${NFT_TABLE:-mistgate_node}" >/dev/null 2>&1 || die "the installed agent left its nft table after the retire"
  pass "retire of the installed node: agent notified, exited, its nft table removed, node RETIRED"
}

# sshn_scan_state <dir>...: no plaintext SSH password and no key in the panel's stored state (the sealed copies must not
# reveal them): the database and its journal, the Durable Object files of the edge
sshn_scan_state() {
  local s hits=0
  for s in "$SSHN_ROOT_PASS" "$SSHN_USER_PASS"; do
    if python3 - "$s" "$@" <<'PY'
import os, sys
needle = sys.argv[1].encode()
for root in sys.argv[2:]:
    for dirpath, _, names in os.walk(root):
        for n in names:
            try:
                with open(os.path.join(dirpath, n), "rb") as f:
                    if needle in f.read():
                        print(os.path.join(dirpath, n), file=sys.stderr)
                        sys.exit(0)
            except OSError:
                pass
sys.exit(1)
PY
    then hits=$((hits + 1)); fi
  done
  [ "$hits" = 0 ] || die "$hits SSH password(s) found in plaintext in the panel's stored state"
}

# ---------------------------------------------------------------- the VPS edition

sshn_sql_vps() { # <sql> [bound values...]: rows of the panel's database, columns joined by |
  python3 - "$DATA/mistgate.db" "$@" <<'PY'
import sqlite3, sys
c = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True, timeout=10)
for r in c.execute(sys.argv[2], sys.argv[3:]):
    print("|".join("" if v is None else str(v) for v in r))
PY
}

# sshn_run_vps: after the first node was retired (the host's single-instance things, the nft table, TCP 443 and the hop
# range, are free again). The agent comes from the trusted bundle in the data directory: the one step 2d of the base
# scenario restored. Needs those base steps, so --m3-only and --old-node skip it.
sshn_run_vps() {
  local manifest=$DATA/dist/manifest.json
  step "10b. SSH install: the wizard and four installs on a real OpenSSH host (VPS edition: the agent is the trusted bundle's, the node enrols over mTLS)"
  SSHN_SQL=sshn_sql_vps
  [ "$(jq -r '.files | length' "$manifest")" = 1 ] || die "the data directory has no single-file bundle to install from"
  SSHN_NODE_BIN=$DATA/dist/$(jq -r '.files[0].name' "$manifest")
  SSHN_NODE_BIN_SHA=$(jq -r '.files[0].sha256' "$manifest")
  [ "$(sha256sum "$SSHN_NODE_BIN" | cut -d' ' -f1)" = "$SSHN_NODE_BIN_SHA" ] || die "the bundle's binary does not match its manifest"
  sshn_up
  sshn_wizard
  sshn_scn_cancel sshn-cancel
  sshn_scn_retire_waiting sshn-wait
  sshn_scn_truncated sshn-trunc
  sshn_scn_full sshn-full
  sshn_scan_state "$DATA"
  pass "the stored state holds no plaintext SSH password (database and journal of the data directory)"
}
