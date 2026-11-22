#!/usr/bin/env bash
# The upgrade lab: what a person on the previous stable release meets when they move to this tree. Runs as root inside WSL
# or any Linux box, in a private network namespace and mount namespace (`unshare -n -m`, loopback only: nothing leaves the
# machine, nothing of the host is touched), and removes its work directory when it ends.
#
#   MSYS_NO_PATHCONV=1 wsl -d Ubuntu -u root -- bash scripts/e2e/upgrade.sh --old DIR --new DIR [--key FILE] [--signbundle FILE] [--only a,b] [--keep] [--work DIR]
#
# --old DIR   holds `mistgate` and `mistgate-node` of the previous stable release (built from its tag, linux/amd64)
# --new DIR   the same two from this tree. Both pairs are stamped with ONE throwaway release public key (-X
#             github.com/mistgate/mistgate/internal/buildinfo.ReleaseKey=...), --key is its private half (made by
#             `mistgate release keygen`); --signbundle is scripts/e2e/signbundle built for this machine. Without them the
#             node-update step (E) is skipped.
#             Build each pair with CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-s -w -X BI.Version=vX
#             -X BI.Built=$(git log -1 --format=%ct) -X BI.ReleaseKey=<public key>" ./cmd/mistgate (and ./cmd/mistgate-node), BI being the
#             package above, in a checkout of the old tag and in this tree. No web build is needed: without web/dist the panel runs
#             (only the user's own page, a browser view of the subscription link, is not served; this lab does not compare it).
# --only      stages to run, comma separated, always in the order below. Default: all. With --keep, a later run can take up only
#             some stages again (the work directory keeps the data of the seed; use --work DIR with a directory that survives).
#
# The stages (the letters are those of the upgrade check list):
#   seed       the old panel (set up, one admin with a password and an authenticator code), an old agent on one node, two more nodes
#              that never connect, groups, profiles of every protocol the old release had, people with limits and terms, devices,
#              a WARP account, DNS presets, subscription settings, an own decoy site, a scheduled task, a NODE_DOWN alert, a backup file.
#              Then everything the old panel shows is captured (admin API, subscription links in every format, the tables of the
#              database) and the data directory is kept as data-old/.
#   upgrade    A: the new panel on the old data directory: migrations, start, the same captures, the difference.
#   oldnode    D: the old agent against the new panel: session, state, what the panel says about it.
#   rollback   B: the old binary on a migrated database (a copy), as an owner would start it after a failed update.
#   restore    C: the backup the old panel made, restored by the new binary (command line) and checked by the new panel.
#   prefix     A: a default install (the admin under a secret path): does the owner's session survive?
#   volume     A: the new panel on a copy of the data with millions of event and audit rows: how long the migrations take.
#   nodeupdate E: a signed bundle of the new agent is rolled out to the old agent. A lab without internet cannot pass the panel's
#              gate (a client through every profile to the internet): the agent's side is what this checks.
#   rollback2  B: the same as rollback, after the new panel's features were used (a TrustTunnel profile, new settings).
#
# Everything the lab writes is under the work directory; names and addresses are invented (example.com, 192.0.2.0/24, 2001:db8::/32).
# Secrets (admin password, authenticator secret, session cookie, enrollment token) are never printed.

set -Eeuo pipefail

OLD="" NEW="" KEY="" SIGNBUNDLE="" ONLY="" KEEP="" WORK=""
while [ $# -gt 0 ]; do
  case $1 in
    --old) OLD=${2:?--old needs a directory}; shift ;;
    --new) NEW=${2:?--new needs a directory}; shift ;;
    --key) KEY=${2:?--key needs a file}; shift ;;
    --signbundle) SIGNBUNDLE=${2:?--signbundle needs a file}; shift ;;
    --only) ONLY=${2:?--only needs a list}; shift ;;
    --work) WORK=${2:?--work needs a directory}; shift ;;
    --keep) KEEP=1 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done
[ -d "$OLD" ] && [ -d "$NEW" ] || { echo "--old and --new are required (directories with mistgate and mistgate-node)" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 2; }
for tool in jq curl python3 openssl unshare ip mount tar; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done

# ---------------------------------------------------------------- outer: a private network and mount namespace, re-run inside

if [ "${MG_UP_INNER:-}" != 1 ]; then
  OLD=$(cd "$OLD" && pwd) NEW=$(cd "$NEW" && pwd)
  [ -z "$KEY" ] || KEY=$(realpath "$KEY")
  [ -z "$SIGNBUNDLE" ] || SIGNBUNDLE=$(realpath "$SIGNBUNDLE")
  args=(--old "$OLD" --new "$NEW")
  [ -z "$KEY" ] || args+=(--key "$KEY")
  [ -z "$SIGNBUNDLE" ] || args+=(--signbundle "$SIGNBUNDLE")
  [ -z "$ONLY" ] || args+=(--only "$ONLY")
  [ -z "$WORK" ] || args+=(--work "$WORK")
  [ -z "$KEEP" ] || args+=(--keep)
  exec env MG_UP_INNER=1 unshare -n -m bash "$(realpath "${BASH_SOURCE[0]}")" "${args[@]}"
fi

ip link set lo up
# the agent writes sysctl and journald drop-ins: hide the real directories behind memory
mkdir -p /etc/sysctl.d /etc/systemd/journald.conf.d
mount -t tmpfs tmpfs /etc/sysctl.d
mount -t tmpfs tmpfs /etc/systemd/journald.conf.d

: "${WORK:=$(mktemp -d /tmp/mg-upgrade.XXXXXX)}"
mkdir -p "$WORK"
chmod 700 "$WORK"
BIN=$WORK/bin
mkdir -p "$BIN/old" "$BIN/new"
cp "$OLD/mistgate" "$OLD/mistgate-node" "$BIN/old/"
cp "$NEW/mistgate" "$NEW/mistgate-node" "$BIN/new/"
chmod +x "$BIN"/old/* "$BIN"/new/*
T0=$(date +%s)
PUB=18443 ADM=18444 # a private namespace: nothing else listens here
ADMIN=http://localhost:$ADM
PANEL_URL=https://localhost:$PUB
NODE_NAME=de1
RESULTS=() PIDS=() CURL_INSECURE="" COOKIE="" LAST_PID="" PANEL_PID="" NODE_PID="" TOTP_LAST=0 STEPUP_UNTIL=0
STATE=$WORK/state.env
VOLUME_EVENTS=${VOLUME_EVENTS:-3000000} VOLUME_AUDIT=${VOLUME_AUDIT:-500000}

say() { printf '[%3ds] %s\n' $(($(date +%s) - T0)) "$*"; }
step() { printf '\n=== %s\n' "$*"; }
ok() { say "OK    $*"; RESULTS+=("OK    $*"); }
finding() { say "FIND  $*"; RESULTS+=("FIND  $*"); }
note() { say "note  $*"; RESULTS+=("note  $*"); }
die() { say "FAIL  $*"; RESULTS+=("FAIL  $*"); exit 1; }

spawn() { "$@" & LAST_PID=$!; PIDS+=("$LAST_PID"); }
alive() { local st; [ -n "${1:-}" ] && st=$(ps -o stat= -p "$1" 2>/dev/null) && [ -n "$st" ] && [ "${st:0:1}" != Z ]; }
stop_pid() {
  local pid=$1 i
  alive "$pid" || { wait "$pid" 2>/dev/null || true; return 0; }
  kill -TERM "$pid" 2>/dev/null || true
  for ((i = 0; i < 150; i++)); do alive "$pid" || break; sleep 0.1; done
  alive "$pid" && kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
cleanup() {
  local rc=$? p
  trap - ERR
  set +e
  for ((p = ${#PIDS[@]} - 1; p >= 0; p--)); do stop_pid "${PIDS[$p]}"; done
  if [ -n "$KEEP" ]; then say "work directory kept: $WORK"; else rm -rf "$WORK"; fi
  echo
  echo "================ summary"
  printf '%s\n' "${RESULTS[@]}"
  # exit 0 before the last stage ran (a stray signal, an exit inside a stage) is not a pass
  if [ "$rc" = 0 ] && [ -z "${FINISHED:-}" ]; then rc=1; echo "UPGRADE LAB INTERRUPTED: the script exited before its last stage"; fi
  printf 'findings: %s
' "$(printf '%s
' "${RESULTS[@]}" | grep -c '^FIND')"
  [ "$rc" = 0 ] && echo "UPGRADE LAB DONE in $(($(date +%s) - T0)) s" || echo "UPGRADE LAB FAILED (exit $rc)"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
trap 'say "FAIL  unexpected error at line $LINENO"; RESULTS+=("FAIL  unexpected error at line $LINENO")' ERR

wait_for() { local n=$1 what=$2 i; shift 2; for ((i = 0; i < n; i++)); do if "$@" >/dev/null 2>&1; then return 0; fi; sleep 1; done; say "timeout after ${n} s waiting for: $what"; return 1; }

# ---------------------------------------------------------------- admin API (Connect, JSON)

api() { # <Service/Method> [json] -> body; non-200 is an error (the body goes to stderr)
  local out code pkg=mistgate.admin.v1
  case $1 in TasksService/*) pkg=mistgate.tasks.v1 ;; esac
  out=$(curl -sS -m 60 ${CURL_INSECURE:+-k} -w $'\n%{http_code}' -X POST "$ADMIN/api/$pkg.$1" -H 'Content-Type: application/json' ${COOKIE:+-H "Cookie: $COOKIE"} -d "${2:-{\}}") || return 1
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [ "$code" != 200 ]; then echo "api $1 -> HTTP $code: $out" >&2; return 1; fi
  printf '%s' "$out"
}
totp_code() {
  python3 - "$TOTP_SECRET" <<'PY'
import base64, hashlib, hmac, struct, sys, time
b32 = sys.argv[1].upper()
key = base64.b32decode(b32 + "=" * (-len(b32) % 8))
h = hmac.new(key, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
o = h[-1] & 15
print("%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 10**6))
PY
}
fresh_totp() { # waits for a 30 s step newer than the last one used (a code is single use); the step is kept in a file, as this runs in $(...)
  local step last=0
  [ ! -f "$WORK/totp.last" ] || last=$(<"$WORK/totp.last")
  while step=$(($(date +%s) / 30)); [ "$step" -le "$last" ]; do sleep 1; done
  echo "$step" >"$WORK/totp.last"
  totp_code
}
stepup() {
  local out now
  now=$(date +%s)
  [ "$STEPUP_UNTIL" -gt $((now + 90)) ] && return 0
  out=$(api AuthService/FinishStepUp "$(jq -nc --arg c "$(fresh_totp)" '{totpCode:$c}')") || return 1
  STEPUP_UNTIL=$(jq -r '.stepUpUntilUnix | tonumber' <<<"$out")
}
mutate() { stepup && api "$@"; }
panel_up() { curl -sf -m 3 ${CURL_INSECURE:+-k} -o /dev/null "$ADMIN/api/mistgate.admin.v1.AuthService/GetLoginInfo" -H 'Content-Type: application/json' -d '{}'; }

# session_cookie <header file>: the session cookie a response set (the name is __Host-sid, or sid on the plain-HTTP admin listener of a newer panel)
session_cookie() { sed -n 's/^[Ss]et-[Cc]ookie: \(\(__Host-\)\{0,1\}sid=[^;]*\).*/\1/p' "$1" | tr -d '\r'; }

# sign in with the password and an authenticator code (a new session; the old cookie is not needed)
login() {
  local hdr=$WORK/login.hdr out
  out=$(curl -sS -m 40 -D "$hdr" -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/PasswordLogin" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg l owner --arg p "$ADMIN_PASSWORD" --arg c "$(fresh_totp)" '{login:$l, password:$p, totpCode:$c}')") || return 1
  grep -q '^HTTP/[0-9.]* 200' "$hdr" || { echo "PasswordLogin: $(head -1 "$hdr") $out" >&2; return 1; }
  COOKIE=$(session_cookie "$hdr")
  [ -n "$COOKIE" ] || { echo "PasswordLogin: no session cookie in the answer" >&2; return 1; }
  STEPUP_UNTIL=$(($(date +%s) + 280))
}

# ---------------------------------------------------------------- processes

# start_panel <old|new> [env assignments...]: serves $DATA on the public listener (TLS) and the admin one (plain HTTP)
start_panel() {
  local which=$1
  shift
  spawn env MISTGATE_HEALTH_BLIP_WINDOW=3s "$@" "$BIN/$which/mistgate" serve --data-dir "$DATA" --listen "127.0.0.1:$PUB" \
    --tls-cert "$WORK/pub.crt" --tls-key "$WORK/pub.key" --agent-addr "127.0.0.1:$PUB" >>"$WORK/panel-$which.log" 2>&1
  PANEL_PID=$LAST_PID
  wait_for 90 "the $which panel to answer" panel_up || { tail -n 15 "$WORK/panel-$which.log"; die "the $which panel did not start"; }
}
stop_panel() { [ -z "$PANEL_PID" ] || stop_pid "$PANEL_PID"; PANEL_PID=""; }
start_node() { spawn "$BIN/$1/mistgate-node" run --state-dir "$NODE_STATE" >>"$WORK/node-$1.log" 2>&1; NODE_PID=$LAST_PID; }
stop_node() { [ -z "$NODE_PID" ] || stop_pid "$NODE_PID"; NODE_PID=""; }

node_json() { api NodeService/ListNodes '{"includeRetired":true}' | jq -c --arg n "$1" '.nodes[] | select(.name == $n)'; }
node_status() { node_json "$1" | jq -r .status; }
node_online() { [ "$(node_status "$NODE_NAME")" = NODE_STATUS_ONLINE ]; }
# a node with no internet shows NO_TRAFFIC once its synthetic checks have failed: connected is what an agent test needs
node_connected() { api NodeService/GetNodeRuntime "{\"nodeId\":\"${DE1:-}\"}" | jq -e '.nodes[0].connected == true'; }
node_offline() { [ "$(node_status "$NODE_NAME")" != NODE_STATUS_ONLINE ]; }

save_state() { { for v in "$@"; do printf '%s=%q\n' "$v" "${!v}"; done; } >>"$STATE"; chmod 600 "$STATE"; }
load_state() { [ ! -f "$STATE" ] || . "$STATE"; }

DATA=$WORK/data NODE_STATE=$WORK/node-state
mkdir -p "$WORK/cap"

# ---------------------------------------------------------------- captures: what the panel shows, before and after

# the compare tool: flattens two JSON documents to path = value and prints what is only in one and what differs. Keys that
# change by themselves (clocks, counters, uptime) are not compared.
cat >"$WORK/jdiff.py" <<'PY'
import json, re, sys
VOLATILE = re.compile(r"(?i)(^|\.)(nowUnix|lastSeen\w*|uptime\w*|rtt\w*|ageS|at\w*Unix|\w*Unix|startedAt|connectedSince|updatedAt|checkedAt|cpu\w*|mem\w*|load\w*|rx\w*|tx\w*|sessionId|session\w*|elapsed\w*|pid|goroutines|rss\w*|heap\w*|latency\w*)$")
def flat(o, p=""):
    if isinstance(o, dict):
        for k, v in o.items():
            yield from flat(v, f"{p}.{k}" if p else k)
    elif isinstance(o, list):
        # a list of objects with a unique id (or name) is keyed by it: a new element in the middle does not shift the rest
        key = next((k for k in ("id", "name", "code") if o and all(isinstance(i, dict) and isinstance(i.get(k), str) for i in o)
                    and len({i[k] for i in o}) == len(o)), None)
        for i, v in enumerate(o):
            yield from flat(v, f"{p}[{v[key] if key else i}]")
    else:
        yield p, o
def load(f):
    try:
        return dict(flat(json.load(open(f))))
    except Exception as e:
        return {"<unreadable>": str(e)}
a, b = load(sys.argv[1]), load(sys.argv[2])
def leaf(p): return re.sub(r"\[[^\]]*\]", "", p).split(".")[-1]
added = sorted(k for k in b if k not in a)
removed = sorted(k for k in a if k not in b)
changed = sorted(k for k in a if k in b and a[k] != b[k] and not VOLATILE.search(leaf(k)))
for k in added: print("  + %s = %s" % (k, json.dumps(b[k])[:90]))
for k in removed: print("  - %s = %s" % (k, json.dumps(a[k])[:90]))
for k in changed: print("  ~ %s: %s -> %s" % (k, json.dumps(a[k])[:60], json.dumps(b[k])[:60]))
PY

# cap <label>: the admin API as one JSON file per call under $WORK/cap/<label>/, ids and names as they are
cap() {
  local d=$WORK/cap/$1 id name did
  mkdir -p "$d"
  api UserService/ListUsers '{}' | jq -S . >"$d/users.json"
  for id in $(jq -r '.users[].id' "$d/users.json"); do
    name=$(jq -r --arg i "$id" '.users[] | select(.id == $i) | .name' "$d/users.json")
    api UserService/GetUser "{\"userId\":\"$id\"}" | jq -S . >"$d/user-$name.json"
    for did in $(jq -r '.devices[]?.id' "$d/user-$name.json"); do
      api DeviceService/GetDeviceConfigs "{\"deviceId\":\"$did\"}" 2>/dev/null | jq -S . >"$d/devcfg-$name-$did.json" || true
    done
    api UserService/GetSubscriptionLink "{\"userId\":\"$id\"}" 2>/dev/null | python3 -c 'import hashlib, json, sys; d = json.load(sys.stdin); p = d.pop("pagePassword", ""); d["pagePasswordSha"] = hashlib.sha256(p.encode()).hexdigest()[:10] if p else ""; print(json.dumps(d, sort_keys=True))' | jq -S . >"$d/sublink-$name.json" || true
  done
  api GroupService/ListGroups '{}' | jq -S . >"$d/groups.json"
  api ProfileService/ListProfiles '{}' | jq -S . >"$d/profiles.json"
  for id in $(jq -r '.profiles[].id' "$d/profiles.json"); do
    api ProfileService/GetProfile "{\"profileId\":\"$id\"}" | jq -S . >"$d/profile-$id.json"
  done
  api NodeService/ListNodes '{"includeRetired":true}' | jq -S . >"$d/nodes.json"
  for id in $(jq -r '.nodes[].id' "$d/nodes.json"); do
    name=$(jq -r --arg i "$id" '.nodes[] | select(.id == $i) | .name' "$d/nodes.json")
    api NodeService/GetNode "{\"nodeId\":\"$id\"}" | jq -S . >"$d/node-$name.json"
    api WarpService/GetWarp "{\"nodeId\":\"$id\"}" | jq -S . >"$d/warp-$name.json"
  done
  api SubscriptionService/GetSubscriptionSettings '{}' | jq -S . >"$d/subscription-settings.json"
  api DnsService/ListDnsPresets '{}' | jq -S . >"$d/dns-presets.json"
  api InstanceService/GetInstance '{}' | jq -S . >"$d/instance.json"
  api BackupService/GetBackupSettings '{}' | jq -S . >"$d/backup-settings.json"
  api TelegramService/GetTelegram '{}' | jq -S . >"$d/telegram.json"
  api HealthService/ListAlerts '{}' | jq -S . >"$d/alerts.json"
  api FleetService/ListEvents '{"limit":200}' | jq -S . >"$d/events.json"
  api AuthService/GetSecuritySettings '{}' | jq -S . >"$d/security.json"
  api ClientApiService/GetClientApi '{}' | jq -S . >"$d/client-api.json" 2>/dev/null || true
  api ClientCompatService/GetClientCompat '{}' | jq -S . >"$d/client-compat.json" 2>/dev/null || true
  api UpdateService/GetUpdates '{}' | jq -S . >"$d/updates.json"
  api TasksService/ListTasks '{}' | jq -S . >"$d/tasks.json" 2>/dev/null || true
  api AuthService/ListSessions '{}' | jq -S . >"$d/sessions.json" 2>/dev/null || true
}

# subs <label>: every person's subscription link fetched as the apps do. One file per person and client: the headers (sorted,
# without the clock) and the body.
SUB_CLIENTS=("browser|Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36|"
  "curl|curl/8.5.0|" "happ|Happ/2.7.0/ios|" "mihomo|mihomo/1.19.31|" "xrayjson|v2rayNG/1.9.0|?format=xray-json" "singbox|sing-box/1.12.0|?format=sing-box")
subs() {
  local d=$WORK/cap/$1/subs c name ua q url user f
  mkdir -p "$d"
  for user in $(jq -r '.users[].name' "$WORK/cap/$1/users.json"); do
    url=$(jq -r .url "$WORK/cap/$1/sublink-$user.json" 2>/dev/null) || continue
    [ -n "$url" ] && [ "$url" != null ] || continue
    for c in "${SUB_CLIENTS[@]}"; do
      IFS='|' read -r name ua q <<<"$c"
      f=$d/$user-$name
      # the link points at the public listener (TLS, the lab's own certificate)
      curl -sS -m 30 -k -D "$f.hdr" -o "$f.body" -A "$ua" -H "X-HWID: lab-$user" -H "X-Device-OS: Linux" -H "X-Device-Model: lab" \
        "$url$q" 2>"$f.err" || echo "curl failed: $(cat "$f.err")" >"$f.hdr"
      # the headers without the date, sorted: they are part of what an app sees
      sed -i 's/\r$//; /^[Dd]ate:/d; /^[Cc]ontent-[Ll]ength:/d' "$f.hdr"
      sort -f -o "$f.hdr" "$f.hdr"
    done
  done
}

# dbdump <label> <data dir>: the tables of the database as they are on disk: row counts and a hash of each table's content
dbdump() {
  python3 - "$2/mistgate.db" "$WORK/cap/$1/db.json" <<'PY'
import hashlib, json, sqlite3, sys
con = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True)
out = {"tables": {}, "schema_version": con.execute("select coalesce(max(version_id),0) from goose_db_version").fetchone()[0]}
for (t,) in con.execute("select name from sqlite_master where type='table' and name not like 'sqlite_%' order by 1").fetchall():
    cols = [c[1] for c in con.execute('pragma table_info("%s")' % t)]
    n = con.execute('select count(*) from "%s"' % t).fetchone()[0]
    h = hashlib.sha256()
    for row in con.execute('select * from "%s" order by %s' % (t, ",".join('"%s"' % c for c in cols))):
        h.update(repr(row).encode())
    out["tables"][t] = {"rows": n, "cols": cols, "sha": h.hexdigest()[:12]}
json.dump(out, open(sys.argv[2], "w"), indent=1, sort_keys=True)
PY
}

# ---------------------------------------------------------------- stage seed

make_profile() { api ProfileService/CreateProfile "$(jq -nc --arg p "$1" --arg n "$2" --arg s "$3" '{protocol:$p, name:$n, settingsJson:$s}')" | jq -r .profile.id; }
make_inbound() { api ProfileService/CreateInbound "{\"profileId\":\"$1\",\"nodeId\":\"$2\"}" >/dev/null; }
default_settings() { api ProfileService/ListProtocols '{}' | jq -c --arg p "$1" '.protocols[] | select(.id == $p) | .defaultSettingsJson | fromjson'; }
mutate_ok() { mutate "$@" >/dev/null || die "$1 failed"; }

stage_seed() {
  step "seed: the previous release ($("$BIN/old/mistgate" version | head -1)) set up and filled through its API"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=localhost \
    -addext subjectAltName=DNS:localhost,IP:127.0.0.1 -keyout "$WORK/pub.key" -out "$WORK/pub.crt" 2>/dev/null
  local out token
  out=$("$BIN/old/mistgate" setup --data-dir "$DATA" --public-url "$PANEL_URL" --admin-listen "127.0.0.1:$ADM")
  token=$(sed -n 's|^Setup link: .*/setup#\(.*\)$|\1|p' <<<"$out")
  [ -n "$token" ] || die "no setup link in the output of setup"
  start_panel old
  ADMIN_PASSWORD=$(openssl rand -hex 14)
  local begin ceremony
  begin=$(api AuthService/BeginSetup "$(jq -nc --arg t "$token" --arg p "$ADMIN_PASSWORD" '{setupToken:$t, displayName:"Owner", method:"SETUP_METHOD_PASSWORD", login:"owner", password:$p}')")
  ceremony=$(jq -r .ceremonyId <<<"$begin")
  TOTP_SECRET=$(jq -r .totpSecret <<<"$begin")
  save_state ADMIN_PASSWORD TOTP_SECRET
  curl -sS -m 40 -D "$WORK/setup.hdr" -o /dev/null -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/FinishSetup" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg t "$token" --arg c "$ceremony" --arg code "$(fresh_totp)" '{setupToken:$t, ceremonyId:$c, totpCode:$code}')"
  grep -q '^HTTP/[0-9.]* 200' "$WORK/setup.hdr" || die "FinishSetup failed"
  COOKIE=$(sed -n 's/^[Ss]et-[Cc]ookie: \(__Host-sid=[^;]*\).*/\1/p' "$WORK/setup.hdr" | tr -d '\r')
  STEPUP_UNTIL=$(($(date +%s) + 280))
  save_state COOKIE
  ok "old panel up, owner created (password + authenticator code), signed in"

  # ---- nodes: de1 gets a real (old) agent; nl1 and fi1 are enrolled but never connect
  local enroll cmd
  enroll=$(mutate NodeService/CreateEnrollment '{"name":"de1","address":"127.0.0.1"}') || die "CreateEnrollment de1"
  DE1=$(jq -r .node.id <<<"$enroll")
  cmd=$(jq -r .installCommand <<<"$enroll" | grep -m1 ' enroll ')
  flag() { sed -n "s/.* --$1 \([^ ]*\).*/\1/p" <<<"$cmd"; }
  "$BIN/old/mistgate-node" enroll --panel "$(flag panel)" --sni "$(flag sni)" --ca-sha256 "$(flag ca-sha256)" --token "$(flag token)" \
    --state-dir "$NODE_STATE" >"$WORK/node-enroll.log" 2>&1 || { sed 's/--token [^ ]*/--token [x]/' "$WORK/node-enroll.log" | tail -5; die "enroll failed"; }
  NL1=$(mutate NodeService/CreateEnrollment '{"name":"nl1","address":"192.0.2.10"}' | jq -r .node.id)
  FI1=$(mutate NodeService/CreateEnrollment '{"name":"fi1","address":"2001:db8::5"}' | jq -r .node.id)
  mutate_ok NodeService/UpdateNode "$(jq -nc --arg n "$DE1" '{nodeId:$n, countryCode:"DE", location:"Falkenstein", provider:"ExampleHost", notes:"main exit"}')"
  mutate_ok NodeService/UpdateNode "$(jq -nc --arg n "$NL1" '{nodeId:$n, countryCode:"NL", location:"Amsterdam", provider:"ExampleHost", bandwidthMbps:1000, publicLabel:"Amsterdam"}')"
  mutate_ok NodeService/UpdateNode "$(jq -nc --arg n "$FI1" '{nodeId:$n, countryCode:"FI", location:"Helsinki", provider:"SampleCloud", notes:"spare"}')"
  start_node old
  wait_for 90 "the old agent ONLINE" node_online || die "the old agent never came ONLINE"
  ok "nodes de1 (old agent ONLINE), nl1 and fi1 (enrolled, never connected)"

  # ---- DNS and the subscription settings
  local dns
  dns=$(mutate DnsService/CreateDnsPreset '{"name":"lab-resolvers","description":"two resolvers and a split","servers":[{"kind":"DNS_SERVER_KIND_PLAIN","address":"192.0.2.53"},{"kind":"DNS_SERVER_KIND_DOH","address":"https://dns.example.com/dns-query"}],"split":[{"suffixes":[".example.net"],"servers":[{"kind":"DNS_SERVER_KIND_PLAIN","address":"192.0.2.54"}]}],"ipv4Only":true,"blockAds":true}') \
    || die "CreateDnsPreset"
  DNS1=$(jq -r .preset.id <<<"$dns")
  local set
  set=$(api SubscriptionService/GetSubscriptionSettings '{}' | jq -c '.settings
    | .title = "Lab VPN" | .announcement = "Maintenance on Sunday" | .supportUrl = "https://support.example.com/help"
    | .updateIntervalHours = 6 | .serverNameTemplate = "{flag} {node} / {profile}"
    | .rules += [{uaContains:"labclient", format:"SUB_FORMAT_MIHOMO_YAML"}]')
  mutate_ok SubscriptionService/UpdateSubscriptionSettings "$(jq -nc --argjson s "$set" --arg d "$DNS1" '{settings: ($s | .defaultDnsPresetId = $d)}')"
  ok "DNS preset and subscription settings (title, announcement, support link, name template, own serving rule)"

  # ---- profiles of every protocol the old release has
  local p
  p=$(default_settings hysteria2 | jq -c '.tls_mode = "self_signed" | .port = 18701 | .hop = {from: 18710, to: 18719} | .obfs.password = "$generate"')
  HY2=$(make_profile hysteria2 lab-hy2 "$p")
  p=$(default_settings hysteria2 | jq -c '.tls_mode = "self_signed" | .port = 18702 | .obfs = {type: "none"} | .masquerade = {type: "decoy"}')
  HY2B=$(make_profile hysteria2 lab-hy2-plain "$p")
  p=$(default_settings vless | jq -c '.port = 18703 | .reality.target_kind = "site" | .reality.target = "www.example.com:443" | .reality.server_names = ["www.example.com"] | .reality.short_ids = ["a1b2c3d4"]')
  VLESS=$(make_profile vless lab-vless-reality "$p") || VLESS=""
  p=$(default_settings awg | jq -c 'del(.subnet4, .subnet6) | .port = 18704')
  AWG31=$(make_profile awg lab-awg31 "$p") || AWG31=""
  p=$(default_settings mtproto | jq -c '.port = 18705 | .fake_tls = {kind: "site", domain: "www.example.com"}')
  MTP=$(make_profile mtproto lab-telegram "$p") || MTP=""
  for p in HY2 HY2B VLESS AWG31 MTP; do [ -n "${!p}" ] && [ "${!p}" != null ] || note "profile $p was not created"; done
  make_inbound "$HY2" "$DE1"; make_inbound "$HY2B" "$DE1"; make_inbound "$HY2" "$NL1"; make_inbound "$HY2" "$FI1"
  [ -z "$VLESS" ] || { make_inbound "$VLESS" "$DE1" || true; make_inbound "$VLESS" "$NL1" || true; }
  [ -z "$AWG31" ] || { make_inbound "$AWG31" "$DE1" || true; make_inbound "$AWG31" "$NL1" || true; }
  [ -z "$MTP" ] || { make_inbound "$MTP" "$DE1" || true; }
  save_state DE1 NL1 FI1 DNS1 HY2 HY2B VLESS AWG31 MTP
  ok "profiles hysteria2 x2, vless, awg, mtproto with inbounds on de1, nl1, fi1"

  # ---- groups and people
  local g1 g2
  g1=$(api GroupService/CreateGroup "$(jq -nc --arg a "$HY2" --arg b "$VLESS" --arg c "$AWG31" --arg d "$MTP" '{name:"Friends", color:"sky", profileIds:([$a,$b,$c,$d]|map(select(length>0)))}')" | jq -r .group.id)
  g2=$(api GroupService/CreateGroup "$(jq -nc --arg a "$HY2" --arg b "$HY2B" --arg d "$DNS1" --arg n "$FI1" '{name:"Family", color:"sand", profileIds:[$a,$b], dnsPresetId:$d, disabledNodeIds:[$n]}')" | jq -r .group.id)
  api GroupService/CreateGroup '{"name":"Spare"}' >/dev/null
  GRP1=$g1 GRP2=$g2
  mk_user() { api UserService/CreateUser "$1" | jq -r .user.id; }
  ALICE=$(mk_user "$(jq -nc --arg g "$g1" '{name:"alice", groupId:$g, quotaBytes:53687091200, quotaReset:"QUOTA_RESET_MONTH", termDays:90, deviceLimit:3, speedLimitBps:5000000, deviceSpeedLimitBps:2500000}')")
  BOB=$(mk_user "$(jq -nc --arg g "$g2" --arg n "$DE1" '{name:"bob", groupId:$g, nodes:{all:false, nodeIds:[$n]}}')")
  CAROL=$(mk_user "$(jq -nc --arg g "$g1" '{name:"carol", groupId:$g, termDays:7, apps:{happ:true, amnezia:false, telegram:true}}')")
  DAVE=$(mk_user "$(jq -nc --arg g "$g2" '{name:"dave", groupId:$g, quotaBytes:1073741824, quotaReset:"QUOTA_RESET_WEEK"}')")
  ERIN=$(mk_user "$(jq -nc --arg g "$g2" '{name:"erin", groupId:$g}')")
  mutate_ok UserService/UpdateUser "$(jq -nc --arg u "$BOB" --arg d "$DNS1" '{userId:$u, subscriptionName:"Bob laptop", dnsPresetId:$d}')"
  mutate_ok UserService/SetUsersEnabled "$(jq -nc --arg u "$CAROL" '{userIds:[$u], enabled:false}')"
  mutate_ok UserService/UpdateUser "$(jq -nc --arg u "$DAVE" '{userId:$u, expiresUnix:1700000000}')"
  save_state GRP1 GRP2 ALICE BOB CAROL DAVE ERIN
  ok "three groups, five people (limits, terms, a speed limit, a node choice, a DNS choice, one disabled, one expired)"

  # ---- devices: AmneziaWG keys of the old panel
  if [ -n "$AWG31" ]; then
    api DeviceService/CreateAwgDevice "$(jq -nc --arg u "$ALICE" --arg p "$AWG31" '{userId:$u, profileId:$p, platform:"ios", label:"alice-phone"}')" >"$WORK/dev-alice-phone.json" \
      && ok "AmneziaWG device of alice created" || note "CreateAwgDevice of alice failed"
    api DeviceService/CreateAwgDevice "$(jq -nc --arg u "$ALICE" --arg p "$AWG31" '{userId:$u, profileId:$p, platform:"linux", label:"alice-laptop"}')" >"$WORK/dev-alice-laptop.json" || true
  fi

  # ---- WARP: an imported account (the panel keeps it; the agent cannot reach anything here)
  local wk conf
  wk=$(python3 -c 'import base64, os; print(base64.b64encode(os.urandom(32)).decode()); print(base64.b64encode(os.urandom(32)).decode())')
  conf=$(printf '[Interface]\nPrivateKey = %s\nAddress = 172.16.0.2/32\nMTU = 1280\n\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 192.0.2.99:2408\n' "$(sed -n 1p <<<"$wk")" "$(sed -n 2p <<<"$wk")")
  if mutate WarpService/ImportWarp "$(jq -nc --arg n "$DE1" --arg c "$conf" '{nodeId:$n, profileConf:$c}')" >"$WORK/warp-import.json"; then ok "WARP account imported on de1"; else note "ImportWarp failed: $(head -c 200 "$WORK/warp-import.json")"; fi

  # ---- an own decoy site on two nodes (the same bundle: one site after the migration of the decoy library), a scheduled task
  python3 - "$WORK/decoy.zip" <<'PYZ'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as z:
    z.writestr("index.html", "<!doctype html><title>Lab studio</title><h1>Lab studio</h1>")
    z.writestr("style.css", "body{font-family:sans-serif}")
PYZ
  local dn
  for dn in "$DE1" "$NL1"; do
    mutate NodeService/SetNodeDecoyMode "$(jq -nc --arg n "$dn" --arg b "$(base64 -w0 "$WORK/decoy.zip")" '{nodeId:$n, mode:"own", bundleArchive:$b}')" >/dev/null       && ok "own decoy site set on a node" || note "SetNodeDecoyMode failed on a node"
  done
  mutate TasksService/CreateTask '{"task":{"name":"Fleet digest","kind":"TASK_KIND_CHECK","enabled":true,"schedule":{"kind":"SCHEDULE_KIND_DAILY","minuteOfDay":450},"steps":[{"tool":"fleet_digest","argsJson":"{\"hours\":24}","conditions":[{"path":"totals.nodes_problem_now","op":"gt","value":"0"}]}]}}' >"$WORK/task.json"     && ok "scheduled task created" || note "CreateTask failed: $(head -c 200 "$WORK/task.json")"

  # ---- the node's inbounds settle
  inbounds_settled() { api NodeService/GetNode "{\"nodeId\":\"$DE1\"}" | jq -e '[.inbounds[] | select(.state == "INBOUND_STATE_PENDING" or .state == "INBOUND_STATE_APPLYING")] | length == 0'; }
  wait_for 90 "the inbounds of de1 to settle" inbounds_settled || note "inbounds of de1 still not settled"
  api NodeService/GetNode "{\"nodeId\":\"$DE1\"}" | jq -r '.inbounds[] | "    inbound \(.protocol) \(.state) \(.lastError // "")"' | cut -c1-160
  ok "seed data in place"
}

# capture_all <label> <data dir>: the admin API, the subscription links in every format, the tables of the database
capture_all() {
  cap "$1"
  subs "$1"
  dbdump "$1" "$2"
}

# backup_download <file>: the encrypted archive the panel makes for "File on this computer", with a passphrase
BACKUP_PASSPHRASE="lab passphrase of the backup"
backup_download() {
  local code
  stepup
  code=$(curl -sS -m 120 -o "$1" -w '%{http_code}' -X POST "$ADMIN/api/backup/download" -H 'Content-Type: application/json' -H "Cookie: $COOKIE" \
    -d "$(jq -nc --arg p "$BACKUP_PASSPHRASE" '{passphrase:$p}')")
  [ "$code" = 200 ] && [ -s "$1" ]
}

stage_seed_finish() {
  local f
  # the apps fetch the links twice: the first fetch of a new device is not the one that settles its credentials
  cap prime; subs prime; subs prime
  # the apps fetch the links: devices of Happ and the others are made by this (one HWID each)
  api UserService/ListUsers '{}' | jq -r '.users[].name' >"$WORK/names.txt"
  # a backup of the panel as it is, made by the old panel
  if backup_download "$WORK/backup-old.tar.gz.age"; then ok "backup made by the old panel ($(stat -c %s "$WORK/backup-old.tar.gz.age") bytes, passphrase)"; else note "the old panel's backup download failed (HTTP $(head -c 200 "$WORK/backup-old.tar.gz.age"))"; fi
  api NodeService/GetNodeRuntime "{\"nodeId\":\"$DE1\"}" | jq -S . >"$WORK/runtime-before.json"
  # the agent goes away: NODE_DOWN opens and stays open when the panel stops
  stop_node
  node_down_active() { api HealthService/ListAlerts "{\"nodeId\":\"$DE1\"}" | jq -e '[(.active // [])[] | select(.kind == "ALERT_KIND_NODE_DOWN")] | length == 1'; }
  wait_for 90 "NODE_DOWN to open" node_down_active || note "no NODE_DOWN alert in 90 s"
  # a second pass of the links with a link to every client, then capture
  capture_all old "$DATA"
  ok "everything the old panel shows is captured ($(find "$WORK/cap/old" -type f | wc -l) files)"
  stop_panel
  sleep 1
  rm -rf "$WORK/data-old"
  cp -a "$DATA" "$WORK/data-old"
  ok "data directory of the old panel kept ($(du -sk "$DATA" | cut -f1) KiB)"
}

# ---------------------------------------------------------------- compare two captures

# mask: tokens, keys and ids that are long are hidden in what the lab prints (the lab's secrets are throwaway, but the habit is free)
mask() { sed -E 's/[A-Za-z0-9+\/=_-]{28,}/<long>/g'; }

# compare_caps <from> <to>: prints the differences; sets CMP_DIFFS to the number of files that differ
CMP_DIFFS=0
compare_caps() {
  local a=$WORK/cap/$1 b=$WORK/cap/$2 f out n
  CMP_DIFFS=0
  for f in "$a"/*.json; do
    f=$(basename "$f")
    [ "$f" = db.json ] && continue
    if [ ! -f "$b/$f" ]; then echo "  [$f] only before"; CMP_DIFFS=$((CMP_DIFFS + 1)); continue; fi
    out=$(python3 "$WORK/jdiff.py" "$a/$f" "$b/$f" | mask)
    if [ -n "$out" ]; then n=$(wc -l <<<"$out"); echo "  [$f] $n difference(s)"; head -n 14 <<<"$out" | sed 's/^/    /'; [ "$n" -le 14 ] || echo "    ... ($((n - 14)) more)"; CMP_DIFFS=$((CMP_DIFFS + 1)); fi
  done
  for f in "$b"/*.json; do f=$(basename "$f"); [ -f "$a/$f" ] || echo "  [$f] only after"; done
  # subscriptions: byte for byte
  local same=0 diff=0
  for f in "$a"/subs/*.body; do
    f=$(basename "$f" .body)
    if cmp -s "$a/subs/$f.body" "$b/subs/$f.body" && cmp -s "$a/subs/$f.hdr" "$b/subs/$f.hdr"; then same=$((same + 1)); else
      diff=$((diff + 1))
      echo "  [subs/$f] differs: body $(cmp -s "$a/subs/$f.body" "$b/subs/$f.body" && echo same || echo "$(wc -c <"$a/subs/$f.body") -> $(wc -c <"$b/subs/$f.body") bytes"), headers $(cmp -s "$a/subs/$f.hdr" "$b/subs/$f.hdr" && echo same || echo differ)"
    fi
  done
  echo "  subscriptions: $same identical, $diff differ"
  CMP_DIFFS=$((CMP_DIFFS + diff))
  # tables
  python3 - "$a/db.json" "$b/db.json" <<'PY'
import json, sys
a, b = (json.load(open(f)) for f in sys.argv[1:3])
ta, tb = a["tables"], b["tables"]
print("  schema version %s -> %s" % (a["schema_version"], b["schema_version"]))
print("  tables only before: %s" % (", ".join(sorted(set(ta) - set(tb))) or "none"))
new = sorted(set(tb) - set(ta))
print("  tables only after (%d): %s" % (len(new), ", ".join("%s(%d)" % (t, tb[t]["rows"]) for t in new)))
for t in sorted(set(ta) & set(tb)):
    x, y = ta[t], tb[t]
    ncol = [c for c in y["cols"] if c not in x["cols"]]
    gone = [c for c in x["cols"] if c not in y["cols"]]
    note = []
    if x["rows"] != y["rows"]: note.append("rows %d -> %d" % (x["rows"], y["rows"]))
    if ncol: note.append("new columns " + ",".join(ncol))
    if gone: note.append("DROPPED columns " + ",".join(gone))
    if not ncol and not gone and x["rows"] == y["rows"] and x["sha"] != y["sha"]: note.append("content changed")
    if note: print("  table %-28s %s" % (t, "; ".join(note)))
PY
}

# ---------------------------------------------------------------- stage upgrade (A)

stage_upgrade() {
  step "A. the new panel ($("$BIN/new/mistgate" version | head -1)) on the data directory of the old one"
  rm -rf "$DATA"
  cp -a "$WORK/data-old" "$DATA"
  : >"$WORK/panel-new.log"
  local t0 t1 db_before
  db_before=$(python3 - "$DATA/mistgate.db" <<'PY'
import sqlite3, sys
print(sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True).execute("select max(version_id) from goose_db_version").fetchone()[0])
PY
)
  t0=$(date +%s.%N)
  start_panel new
  t1=$(date +%s.%N)
  ok "the new panel answers $(python3 -c "print(round($t1 - $t0, 1))") s after its start (database at migration $db_before before)"
  grep -iE 'level=(WARN|ERROR)' "$WORK/panel-new.log" | grep -v 'api.github.com\|GitHub' | mask | cut -c1-260 | sed 's/^/    log: /' | head -20 || true
  grep -iE 'migrat' "$WORK/panel-new.log" | cut -c1-200 | head -5 | sed 's/^/    log: /' || true
  # the old session: still good?
  if me=$(api AuthService/Me 2>&1); then ok "the session cookie of the old panel works on the new one"; else
    finding "the session cookie of the old panel is refused by the new panel (everyone has to sign in again): $(mask <<<"$me" | cut -c1-200)"
    login || die "cannot sign in to the new panel with the password and a code"
    ok "sign-in with password + authenticator code works on the new panel"
  fi
  capture_all post "$DATA"
  echo "--- differences between what the old panel showed and what the new one shows"
  compare_caps old post | tee "$WORK/report-A.txt"
  # the scheduled task of the old panel runs on the new one
  local tid out
  tid=$(api TasksService/ListTasks '{}' | jq -r '.tasks[0].id // empty')
  if [ -n "$tid" ]; then
    out=$(mutate TasksService/RunTaskNow "$(jq -nc --arg t "$tid" '{id:$t}')" 2>&1) || note "A: RunTaskNow was refused: $(mask <<<"$out" | cut -c1-200)"
    task_ran() { api TasksService/ListTaskRuns "$(jq -nc --arg t "$tid" '{taskId:$t}')" | jq -e '[.runs[] | select(.status != "RUN_STATUS_RUNNING")] | length >= 1'; }
    if wait_for 60 "the task run" task_ran; then ok "A: the scheduled task made on the old panel runs on the new one: $(api TasksService/ListTaskRuns "$(jq -nc --arg t "$tid" '{taskId:$t}')" | jq -c '.runs[0] | {status, reason}')"; else finding "A: the scheduled task does not finish a run on the new panel"; fi
  else
    note "A: no scheduled task to run"
  fi
}

# ---------------------------------------------------------------- stage oldnode (D)

# ensure_new_panel: the new panel on the migrated data directory, signed in
ensure_new_panel() {
  if ! alive "$PANEL_PID"; then
    [ -f "$DATA/mistgate.db" ] || cp -a "$WORK/data-old" "$DATA"
    start_panel new
  fi
  api AuthService/Me >/dev/null 2>&1 || login || die "cannot sign in to the new panel"
}

stage_oldnode() {
  step "D. the old agent ($("$BIN/old/mistgate-node" version | head -1)) on the new panel"
  ensure_new_panel
  mkdir -p "$WORK/obs"
  local t0 t1
  node_status_now() { node_status "$NODE_NAME"; }
  ok "before the agent starts the new panel shows de1 as $(node_status_now)"
  t0=$(date +%s.%N)
  start_node old
  wait_for 120 "the old agent ONLINE on the new panel" node_online || { tail -n 20 "$WORK/node-old.log" | cut -c1-240; die "the old agent did not come ONLINE on the new panel"; }
  t1=$(date +%s.%N)
  ok "the old agent is ONLINE $(python3 -c "print(round($t1 - $t0, 1))") s after its start"
  active() { api NodeService/GetNode "{\"nodeId\":\"$DE1\"}" | jq -e '[.inbounds[] | select(.state != "INBOUND_STATE_ACTIVE")] | length == 0'; }
  wait_for 90 "all inbounds of de1 ACTIVE" active || note "not all inbounds are ACTIVE: $(api NodeService/GetNode "{\"nodeId\":\"$DE1\"}" | jq -c '[.inbounds[] | {protocol, state, lastError}]' | cut -c1-300)"
  sleep 20
  api NodeService/GetNode "{\"nodeId\":\"$DE1\"}" | jq -S . >"$WORK/obs/node-de1.json"
  api NodeService/GetNodeRuntime "{\"nodeId\":\"$DE1\"}" | jq -S . >"$WORK/obs/runtime-de1.json"
  local rb ra
  rb=$(jq -c '.nodes[0] | {appliedHash, desiredHash, appliedRevision, desiredRevision}' "$WORK/runtime-before.json")
  ra=$(jq -c '.nodes[0] | {appliedHash, desiredHash, appliedRevision, desiredRevision, inSync}' "$WORK/obs/runtime-de1.json")
  if [ "$(jq -r .desiredHash <<<"$rb")" = "$(jq -r .desiredHash <<<"$ra")" ]; then
    ok "D: what the new panel wants the node to run hashes the same as what the old panel wanted ($(jq -r .desiredHash <<<"$ra")); revision $(jq -r .desiredRevision <<<"$rb") -> $(jq -r .desiredRevision <<<"$ra"), in sync: $(jq -r .inSync <<<"$ra")"
  else
    finding "D: the desired state of the node changed with the panel: before $rb, after $ra"
  fi
  api UpdateService/GetUpdates '{}' | jq -S . >"$WORK/obs/updates.json"
  api HealthService/GetDoctor "{\"nodeId\":\"$DE1\"}" | jq -S . >"$WORK/obs/doctor-de1.json"
  api HealthService/ListAlerts "{\"nodeId\":\"$DE1\"}" | jq -S . >"$WORK/obs/alerts-de1.json"
  api FleetService/ListEvents "{\"nodeId\":\"$DE1\",\"limit\":100}" | jq -S . >"$WORK/obs/events-de1.json"
  api WarpService/GetWarp "{\"nodeId\":\"$DE1\"}" | jq -S . >"$WORK/obs/warp-de1.json"
  api NodeService/GetNodeJournal "{\"nodeId\":\"$DE1\",\"hours\":1}" | jq -S . >"$WORK/obs/journal-de1.json" 2>/dev/null || true
}

# ---------------------------------------------------------------- stage rollback (B)

# rollback_try <label> <data dir to copy>: the OLD binary on a copy of an already migrated database
rollback_try() {
  local label=$1 src=$2 d=$WORK/data-$1 t0 t1 v
  rm -rf "$d"
  cp -a "$src" "$d"
  local saved=$DATA
  DATA=$d
  : >"$WORK/panel-old-$label.log"
  t0=$(date +%s.%N)
  spawn env MISTGATE_HEALTH_BLIP_WINDOW=3s "$BIN/old/mistgate" serve --data-dir "$DATA" --listen "127.0.0.1:$PUB" \
    --tls-cert "$WORK/pub.crt" --tls-key "$WORK/pub.key" --agent-addr "127.0.0.1:$PUB" >>"$WORK/panel-old-$label.log" 2>&1
  PANEL_PID=$LAST_PID
  if ! wait_for 40 "the old panel on the migrated database" panel_up; then
    finding "$label: the v0.1.89 binary does not start on the migrated database: $(grep -iE 'level=(ERROR|WARN)|panic|error' "$WORK/panel-old-$label.log" | grep -v github | head -3 | mask | cut -c1-300 | tr '\n' ' ')"
    stop_pid "$PANEL_PID"; PANEL_PID=""; DATA=$saved
    return 0
  fi
  t1=$(date +%s.%N)
  ok "$label: the v0.1.89 binary starts on the migrated database ($(python3 -c "print(round($t1 - $t0, 1))") s); schema at migration $(python3 - "$DATA/mistgate.db" <<'PY'
import sqlite3, sys
print(sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True).execute("select max(version_id) from goose_db_version").fetchone()[0])
PY
)"
  grep -iE 'level=(WARN|ERROR)' "$WORK/panel-old-$label.log" | grep -v 'api.github.com\|GitHub\|sub.html' | mask | cut -c1-260 | sed 's/^/    log: /' | head -10 || true
  login || { finding "$label: cannot sign in to the old binary on the migrated database"; stop_pid "$PANEL_PID"; PANEL_PID=""; DATA=$saved; return 0; }
  # what the old binary shows, against what it showed before
  capture_all "rb-$label" "$DATA" 2>"$WORK/cap-rb-$label.err" || note "$label: some captures failed: $(mask <"$WORK/cap-rb-$label.err" | head -3 | tr '\n' ' ' | cut -c1-300)"
  echo "--- $label: the old binary on the migrated database, against what it showed on its own database"
  compare_caps old "rb-$label" >"$WORK/report-B-$label.txt"; grep -v '^      ' "$WORK/report-B-$label.txt" | head -40
  # writes: do the old binary's INSERTs and UPDATEs fit the new schema?
  local fails=""
  w() { local what=$1; shift; "$@" >/dev/null 2>"$WORK/w.err" || fails+=" $what($(mask <"$WORK/w.err" | head -c 120))"; }
  mutate_ok UserService/UpdateUser "$(jq -nc --arg u "$ALICE" '{userId:$u, deviceLimit:6}')"
  w CreateUser mutate UserService/CreateUser "$(jq -nc --arg g "$GRP1" '{name:"zed", groupId:$g}')"
  w CreateGroup mutate GroupService/CreateGroup '{"name":"Rollback group"}'
  w UpdateUser mutate UserService/UpdateUser "$(jq -nc --arg u "$ALICE" '{userId:$u, quotaBytes:64424509440}')"
  w CreateEnrollment mutate NodeService/CreateEnrollment '{"name":"se1","address":"192.0.2.77"}'
  w CreateProfile mutate ProfileService/CreateProfile "$(jq -nc --arg s "$(default_settings hysteria2 | jq -c '.tls_mode = "self_signed" | .port = 18777 | .obfs.password = "$generate"')" '{protocol:"hysteria2", name:"rb-hy2", settingsJson:$s}')"
  w CreateDnsPreset mutate DnsService/CreateDnsPreset '{"name":"rb-resolvers","servers":[{"kind":"DNS_SERVER_KIND_PLAIN","address":"192.0.2.60"}]}'
  w CreateAwgDevice mutate DeviceService/CreateAwgDevice "$(jq -nc --arg u "$ALICE" --arg p "$AWG31" '{userId:$u, profileId:$p, platform:"android", label:"alice-tablet"}')"
  w UpdateSubscriptionSettings mutate SubscriptionService/UpdateSubscriptionSettings "$(api SubscriptionService/GetSubscriptionSettings '{}' | jq -c '{settings: (.settings | .announcement = "after rollback")}')"
  w CreateBackup backup_download "$WORK/backup-rb-$label.age"
  if [ -n "$fails" ]; then finding "$label: writes of the old binary on the migrated database failed:$fails"; else ok "$label: nine writes of the old binary (user, group, node, profile, DNS preset, device, settings, backup) work on the migrated database"; fi
  # the old agent is still told what to do
  start_node old
  wait_for 90 "the old agent ONLINE on the old panel" node_online && ok "$label: the old agent connects to the old binary on the migrated database" || finding "$label: the old agent does not come ONLINE on the old binary"
  python3 - "$DATA/mistgate.db" <<'PY'
import sqlite3, sys
c = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True)
print("    integrity_check:", c.execute("pragma integrity_check").fetchone()[0], "; foreign_key_check rows:", len(c.execute("pragma foreign_key_check").fetchall()),
      "; goose max version:", c.execute("select max(version_id) from goose_db_version").fetchone()[0])
PY
  stop_node
  stop_pid "$PANEL_PID"; PANEL_PID=""
  # and forward again: the new panel on what the old binary wrote
  : >"$WORK/panel-new-$label.log"
  spawn env MISTGATE_HEALTH_BLIP_WINDOW=3s "$BIN/new/mistgate" serve --data-dir "$DATA" --listen "127.0.0.1:$PUB"     --tls-cert "$WORK/pub.crt" --tls-key "$WORK/pub.key" --agent-addr "127.0.0.1:$PUB" >>"$WORK/panel-new-$label.log" 2>&1
  PANEL_PID=$LAST_PID
  if wait_for 60 "the new panel again" panel_up && login && api UserService/ListUsers '{}' | jq -e '[.users[].name] | index("zed") != null' >/dev/null; then
    ok "$label: the new panel starts again on the database the old binary wrote to, and shows what the old binary added"
  else
    finding "$label: the new panel does not come back on the database the old binary wrote to"
  fi
  stop_pid "$PANEL_PID"; PANEL_PID=""
  DATA=$saved
}

stage_rollback() {
  step "B. the old binary ($("$BIN/old/mistgate" version | head -1)) on a database the new panel has migrated"
  stop_node
  if alive "$PANEL_PID"; then stop_panel; fi
  [ -f "$DATA/mistgate.db" ] || die "no migrated database to roll back: run the upgrade stage first"
  rm -rf "$WORK/data-migrated"
  cp -a "$DATA" "$WORK/data-migrated"
  rollback_try rb1 "$WORK/data-migrated"
}

# use_new_features: what an owner may do with the new panel before deciding to go back, and what the old agent says to it
use_new_features() {
  ensure_new_panel
  start_node old
  wait_for 90 "the old agent connected to the new panel" node_connected || die "the old agent does not connect to the new panel"
  local tt set out
  tt=$(default_settings trusttunnel | jq -c '.tls_mode = "self_signed" | .port = 18706')
  TT=$(make_profile trusttunnel lab-tt "$tt") || die "cannot create a TrustTunnel profile on the new panel"
  make_inbound "$TT" "$NL1" || true
  make_inbound "$TT" "$DE1" || die "cannot add a TrustTunnel inbound to a node with an old agent"
  # a TCP port shared by two services (new): refused for an agent that cannot share it
  V443=$(make_profile vless lab-v443 "$(default_settings vless | jq -c '.port = 443 | .reality.target_kind = "site" | .reality.target = "www.example.com:443" | .reality.server_names = ["www.example.com"] | .reality.short_ids = ["a1b2c3d4"]')") || true
  M443=$(make_profile mtproto lab-m443 "$(default_settings mtproto | jq -c '.port = 443 | .fake_tls = {kind: "site", domain: "www.example.org"}')") || true
  make_inbound "$V443" "$DE1" || true
  out=$(api ProfileService/CreateInbound "$(jq -nc --arg p "$M443" --arg n "$DE1" '{profileId:$p, nodeId:$n}')" 2>&1) && finding "D: a second service on TCP 443 was accepted for a node with an old agent"     || ok "D: a second service on a TCP port is refused for the old agent: $(grep -o '"message":"[^"]*"' <<<"$out" | cut -c1-80)"
  sleep 20
  out=$(api NodeService/GetNode "{\"nodeId\":\"$DE1\"}" | jq -r --arg p "$TT" '.inbounds[] | select(.profileId == $p) | "\(.state) \(.lastError // "")"')
  case $out in
    INBOUND_STATE_FAILED*agent_too_old*) ok "D: a TrustTunnel inbound on the old agent is FAILED with a plain reason: $(cut -c1-120 <<<"$out")..." ;;
    *) finding "D: a TrustTunnel inbound on the old agent is: $out" ;;
  esac
  mutate_ok GroupService/UpdateGroup "$(jq -nc --arg g "$GRP1" --arg p "$TT" --arg a "$HY2" --arg b "$VLESS" --arg c "$AWG31" --arg d "$MTP" '{groupId:$g, profileIds:{ids:[$a,$b,$c,$d,$p]}}')"
  mutate_ok UserService/UpdateUser "$(jq -nc --arg u "$ALICE" '{userId:$u, deviceLimit:6, apps:{happ:true, amnezia:true, telegram:true, trustTunnel:true}}')"
  set=$(api SubscriptionService/GetSubscriptionSettings '{}' | jq -c --arg g "$GRP1" '.settings
    | .responseHeaders = [{name:"x-lab-name", value:"Lab", formats:[]}]
    | .mihomoTemplates = [{name:"lab", yaml:"proxies: []
proxy-groups: []
rules:
  - MATCH,DIRECT
", groupIds:[$g]}]')
  mutate_ok SubscriptionService/UpdateSubscriptionSettings "$(jq -nc --argjson s "$set" '{settings:$s}')"
  mutate_ok NodeService/UpdateNode "$(jq -nc --arg n "$NL1" '{nodeId:$n, notes:"edited on the new panel"}')"
  stop_node
  ok "new features used on the new panel: TrustTunnel profile and inbounds in a group, the app switched on for a person, response headers, a Mihomo template"
}

stage_rollback2() {
  step "B2. the same, after the new panel's features were used"
  stop_node
  use_new_features
  stop_panel
  rm -rf "$WORK/data-migrated2"
  cp -a "$DATA" "$WORK/data-migrated2"
  rollback_try rb2 "$WORK/data-migrated2"
}

# ---------------------------------------------------------------- stage restore (C)

stage_restore() {
  step "C. the backup the old panel made, restored by the new one"
  [ -s "$WORK/backup-old.tar.gz.age" ] || die "no backup file of the old panel: run the seed stage first"
  stop_node
  alive "$PANEL_PID" && stop_panel
  local f=$WORK/backup-old.tar.gz.age pf=$WORK/backup.pass d=$WORK/data-restored out
  (umask 077; printf '%s' "$BACKUP_PASSPHRASE" >"$pf")

  # C1: the command line, as the installer does it on a new server (--restore)
  rm -rf "$d"
  if out=$("$BIN/new/mistgate" backup restore --passphrase-file "$pf" --file "$f" --data-dir "$d" 2>&1); then
    ok "C1: mistgate backup restore (new binary) opened the archive of the old panel: $(tail -n 2 <<<"$out" | tr '\n' ' ' | mask | cut -c1-200)"
  else
    finding "C1: mistgate backup restore of the old panel's archive failed: $(mask <<<"$out" | tail -n 4 | tr '\n' ' ')"
    return 0
  fi
  DATA_SAVE=$DATA
  DATA=$d
  : >"$WORK/panel-new-restored.log"
  spawn env MISTGATE_HEALTH_BLIP_WINDOW=3s "$BIN/new/mistgate" serve --data-dir "$DATA" --listen "127.0.0.1:$PUB" \
    --tls-cert "$WORK/pub.crt" --tls-key "$WORK/pub.key" --agent-addr "127.0.0.1:$PUB" >>"$WORK/panel-new-restored.log" 2>&1
  PANEL_PID=$LAST_PID
  wait_for 60 "the new panel on the restored data" panel_up || { tail -n 8 "$WORK/panel-new-restored.log" | mask; die "the new panel does not start on the restored data"; }
  login || die "C1: cannot sign in to the restored panel with the old password and code"
  ok "C1: the new panel starts on the restored data (schema $(python3 - "$DATA/mistgate.db" <<'PY'
import sqlite3, sys
print(sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True).execute("select max(version_id) from goose_db_version").fetchone()[0])
PY
)); the owner signs in with the old password and authenticator code"
  capture_all restored "$DATA"
  echo "--- C1: the panel restored from the old backup, against the old panel's captures (the alert and the node state came after the backup)"
  compare_caps old restored | grep -v '^      ' | head -40 | tee "$WORK/report-C.txt"
  # the node (it enrolled before the backup was made) reconnects to the restored panel
  start_node old
  if wait_for 90 "the old agent ONLINE on the restored panel" node_online; then ok "C1: the old agent reconnects to the restored panel by itself"; else finding "C1: the old agent does not reconnect to the restored panel"; fi
  stop_node

  # C2: in the panel: upload and check (the swap itself needs the systemd helper, which this lab does not have)
  local up id
  stepup
  up=$(curl -sS -m 60 -X POST "$ADMIN/api/backup/upload" -H 'Content-Type: application/octet-stream' -H 'X-Mistgate-Filename: backup-old.tar.gz.age' -H "Cookie: $COOKIE" --data-binary "@$f") || true
  id=$(jq -r '.upload_id // empty' <<<"$up" 2>/dev/null || true)
  if [ -n "$id" ]; then
    if out=$(mutate BackupService/PrepareRestore "$(jq -nc --arg i "$id" --arg p "$BACKUP_PASSPHRASE" '{uploadId:$i, passphrase:$p}')" 2>&1); then
      ok "C2: the panel's own check of the old backup passes: $(jq -c '.summary | {madeByVersion, backupNodes, backupUsers, backupProfiles, currentNodes, currentUsers}' <<<"$out")"
    else
      finding "C2: the panel's check of the old backup fails: $(mask <<<"$out" | cut -c1-300)"
    fi
  else
    finding "C2: the upload of the old backup to the new panel failed: $(mask <<<"$up" | cut -c1-200)"
  fi
  stop_panel
  DATA=$DATA_SAVE
}

# ---------------------------------------------------------------- stage volume (A, a database with a lot of history)

# A copy of the old data directory with millions of events and audit rows added in bulk (the one thing here not made through the
# API: the point is the size), then the new panel started on it. The migrations that read whole tables are the cost.
stage_volume() {
  step "A+. the new panel on a database with a lot of history (bulk rows: $VOLUME_EVENTS events, $VOLUME_AUDIT audit rows)"
  local d=$WORK/data-big saved=$DATA t0 t1
  stop_node
  if alive "$PANEL_PID"; then stop_panel; fi
  rm -rf "$d"
  cp -a "$WORK/data-old" "$d"
  python3 - "$d/mistgate.db" "$DE1" "$VOLUME_EVENTS" "$VOLUME_AUDIT" <<'PY'
import random, sqlite3, sys, time
db, node, n_ev, n_au = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
c = sqlite3.connect(db)
c.execute("pragma synchronous=off")
now = int(time.time())
codes = ["engine_started", "state_applied", "profile_added", "warp_state", "node_blip", "agent_started", "bandwidth_measured"]
def ev():
    for i in range(n_ev):
        yield (now - random.randrange(0, 90 * 86400), 1, codes[random.randrange(len(codes))] if i % 50 else "bandwidth_measured", "agent", node, "{}")
def au():
    acts = ["user.update", "user.create", "node.update", "login", "node.bandwidth_measure"]
    for i in range(n_au):
        yield (now - random.randrange(0, 365 * 86400), "adm_lab", acts[random.randrange(len(acts))], "{}" if i % 7 else '{"node_id":"%s"}' % node, "ok")
c.executemany("insert into event (ts, severity, code, source, node_id, params_json) values (?,?,?,?,?,?)", ev())
c.executemany("insert into audit (ts, actor, action, params, result) values (?,?,?,?,?)", au())
c.commit()
c.execute("pragma wal_checkpoint(truncate)")
print("events", c.execute("select count(*) from event").fetchone()[0], "audit", c.execute("select count(*) from audit").fetchone()[0])
PY
  note "volume: database file $(du -m "$d/mistgate.db" | cut -f1) MiB before the migrations"
  DATA=$d
  : >"$WORK/panel-new.log"
  t0=$(date +%s.%N)
  spawn env "MISTGATE_HEALTH_BLIP_WINDOW=3s" "$BIN/new/mistgate" serve --data-dir "$DATA" --listen "127.0.0.1:$PUB" \
    --tls-cert "$WORK/pub.crt" --tls-key "$WORK/pub.key" --agent-addr "127.0.0.1:$PUB" >>"$WORK/panel-new.log" 2>&1
  PANEL_PID=$LAST_PID
  wait_for 600 "the new panel on the big database" panel_up || { tail -n 8 "$WORK/panel-new.log" | mask; die "the new panel does not start on the big database"; }
  t1=$(date +%s.%N)
  ok "volume: the new panel answers $(python3 -c "print(round($t1 - $t0, 1))") s after its start on $(du -m "$DATA/mistgate.db" | cut -f1) MiB of database ($VOLUME_EVENTS events, $VOLUME_AUDIT audit rows)"
  stop_panel
  DATA=$saved
  rm -rf "$d"
}

# ---------------------------------------------------------------- stage nodeupdate (E)

# bundle <dir> <version> <built> <binary...>: a node bundle signed with the lab's release key (not `release sign`: no rebuild check)
sign_bundle() {
  local out=$1 v=$2 b=$3
  shift 3
  "$SIGNBUNDLE" --key "$KEY" --version "$v" --built "$b" --out "$out" "$@" >"$WORK/signbundle.log" 2>&1 || { cat "$WORK/signbundle.log"; die "signing the bundle failed"; }
}
ro_status() { api UpdateService/GetUpdates '{}' | jq -r '.rollout.status // ""'; }
ro_settled() { [ "$(ro_status)" != ROLLOUT_STATUS_RUNNING ]; }
upd_node() { api UpdateService/GetUpdates '{}' | jq -c --arg n "$NODE_NAME" '.nodes[] | select(.name == $n)'; }

stage_nodeupdate() {
  step "E. the old agent updated to the new one by a rollout from the new panel"
  if [ -z "$KEY" ] || [ -z "$SIGNBUNDLE" ]; then note "E: skipped: needs --key and --signbundle"; return 0; fi
  cp "$KEY" "$WORK/release.key"; chmod 600 "$WORK/release.key"; KEY=$WORK/release.key
  cp "$SIGNBUNDLE" "$BIN/signbundle"; chmod +x "$BIN/signbundle"; SIGNBUNDLE=$BIN/signbundle
  ensure_new_panel
  start_node old
  wait_for 90 "the old agent connected" node_connected || die "the old agent does not connect"
  local built ver
  built=$("$BIN/new/mistgate-node" version | awk '/^built/ {print $2}')
  ver=$("$BIN/new/mistgate-node" version | awk 'NR == 1 {print $2}')
  mkdir -p "$WORK/bundle-src"
  cp "$BIN/new/mistgate-node" "$WORK/bundle-src/mistgate-node-linux-amd64"
  # a release also carries the network table: the manifest of the old agent's panel has such a file next to the binaries
  head -c 4096 /dev/urandom >"$WORK/bundle-src/mistgate-asndb-data-any"
  rm -rf "$WORK/bundle-new"
  sign_bundle "$WORK/bundle-new" "$ver" "$built" "$WORK/bundle-src/mistgate-node-linux-amd64" "$WORK/bundle-src/mistgate-asndb-data-any"
  rm -rf "$DATA/dist.new" "$DATA/dist"
  cp -r "$WORK/bundle-new" "$DATA/dist.new" && mv "$DATA/dist.new" "$DATA/dist"
  mutate_ok UpdateService/RescanBundle
  local gu
  gu=$(api UpdateService/GetUpdates '{}')
  echo "    bundle: $(jq -c '.bundle | {status, version, built, files: (.files | length)}' <<<"$gu")"
  echo "    node:   $(upd_node | jq -c '{version, built, state, supportsUpdate, crashGuard}')"
  jq -e '.bundle.status == "BUNDLE_STATUS_TRUSTED"' <<<"$gu" >/dev/null || die "E: the panel does not trust the signed bundle"
  # the rollout, as the owner presses "Update"
  local pin out id i
  for i in $(seq 30); do
    pin=$(api UpdateService/GetUpdates '{}' | jq -c '{expectedVersion: .bundle.version, expectedBuilt: .bundle.built}')
    if out=$(mutate UpdateService/StartRollout "$pin" 2>&1); then id=$(jq -r .rollout.id <<<"$out"); break; fi
    grep -q 'is being installed' <<<"$out" || { finding "E: StartRollout refused: $(mask <<<"$out" | cut -c1-250)"; return 0; }
    sleep 2
  done
  [ -n "${id:-}" ] || { finding "E: StartRollout never accepted"; return 0; }
  say "rollout started"
  # the agent's side: it takes the bundle, replaces itself, starts as the new build and commits
  agent_committed() { grep -q "update committed.*to=$ver" "$WORK/node-old.log"; }
  if wait_for 180 "the old agent to replace itself and commit" agent_committed; then
    ok "E: the old agent (v0.1.89) downloaded, verified and installed the signed bundle of $ver, ran it and committed ($(grep -m1 'binary replaced' "$WORK/node-old.log" | grep -o 'from=[^ ]* to=[^ ]*'))"
  else
    finding "E: the old agent did not commit the update to $ver: $(grep -iE 'update|bundle|refus' "$WORK/node-old.log" | tail -n 4 | mask | cut -c1-250 | tr '
' ' ')"
    return 0
  fi
  seen_new() { [ "$(upd_node | jq -r .version)" = "$ver" ]; }
  if wait_for 60 "the panel to show the new version of the node" seen_new; then ok "E: the panel shows de1 on $ver"; else note "E: the panel still shows $(upd_node | jq -r .version) for de1 (it may already have rolled the node back)"; fi
  synced() { api NodeService/GetNodeRuntime "{\"nodeId\":\"$DE1\"}" | jq -e '.nodes[0].inSync == true and ([.nodes[0].inbounds[] | select(.liveState != "active")] | length == 0)'; }
  if wait_for 60 "the new agent in sync" synced; then ok "E: the new agent holds the state of the panel (in sync, every inbound active)"; else note "E: the new agent is not in sync / not all inbounds live: $(api NodeService/GetNodeRuntime "{\"nodeId\":\"$DE1\"}" | jq -c '.nodes[0] | {inSync, lastApplyStatus, bad: [.inbounds[] | select(.liveState != "active") | {profileName, liveState}]}' | cut -c1-300)"; fi
  # the panel's gate (a client through every profile out to the internet) cannot pass in a lab with no internet: say what it says
  wait_for 420 "the rollout to settle" ro_settled || note "E: the rollout is still running after 7 minutes"
  gu=$(api UpdateService/GetUpdates '{}')
  note "E: the rollout ended $(jq -r '.rollout.status' <<<"$gu") (the lab has no internet: the gate's probe through the tunnel cannot pass): $(jq -c '.rollout | {pauseKey, steps: [(.steps // [])[] | {state, errorKey}]}' <<<"$gu" | cut -c1-300)"
  echo "    node now: $(upd_node | jq -c '{version, built, state}')"
}

# ---------------------------------------------------------------- stage prefix (A, the default install)

# A panel set up as the installer does by default (the admin under a secret path of the public address, no separate listener):
# its session cookie is __Host-sid in both versions, so the owner stays signed in across the upgrade.
stage_prefix() {
  step "A. the default install (the admin under a secret path of the public address): is the session kept?"
  stop_node
  if alive "$PANEL_PID"; then stop_panel; fi
  local d=$WORK/data-prefix out token url saved_admin=$ADMIN saved_data=$DATA saved_cookie=$COOKIE
  rm -rf "$d"
  DATA=$d
  out=$("$BIN/old/mistgate" setup --data-dir "$DATA" --public-url "$PANEL_URL")
  url=$(sed -n 's|^Admin URL: *\(https\{0,1\}://[^ ]*\)$|\1|p' <<<"$out" | head -1)
  token=$(sed -n 's|^Setup link: .*/setup#\(.*\)$|\1|p' <<<"$out")
  [ -n "$url" ] && [ -n "$token" ] || { finding "A: cannot read the admin URL from setup: $(mask <<<"$out" | head -4 | tr '\n' ' ')"; DATA=$saved_data; return 0; }
  ADMIN=${url%/}
  CURL_INSECURE=1
  : >"$WORK/panel-old-prefix.log"
  spawn env "MISTGATE_HEALTH_BLIP_WINDOW=3s" "$BIN/old/mistgate" serve --data-dir "$DATA" --listen "127.0.0.1:$PUB" \
    --tls-cert "$WORK/pub.crt" --tls-key "$WORK/pub.key" --agent-addr "127.0.0.1:$PUB" >>"$WORK/panel-old-prefix.log" 2>&1
  PANEL_PID=$LAST_PID
  wait_for 60 "the old panel (default install)" panel_up || die "the old panel of the default install does not start"
  local pw begin ceremony
  pw=$(openssl rand -hex 14)
  begin=$(api AuthService/BeginSetup "$(jq -nc --arg t "$token" --arg p "$pw" '{setupToken:$t, displayName:"Owner", method:"SETUP_METHOD_PASSWORD", login:"owner", password:$p}')")
  ceremony=$(jq -r .ceremonyId <<<"$begin")
  local saved_totp=$TOTP_SECRET
  TOTP_SECRET=$(jq -r .totpSecret <<<"$begin")
  curl -sk -m 40 -D "$WORK/setup2.hdr" -o /dev/null -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/FinishSetup" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg t "$token" --arg c "$ceremony" --arg code "$(fresh_totp)" '{setupToken:$t, ceremonyId:$c, totpCode:$code}')"
  COOKIE=$(session_cookie "$WORK/setup2.hdr")
  api AuthService/Me >/dev/null || die "A: the session of the old panel (default install) does not work"
  local cname=${COOKIE%%=*}
  stop_panel
  : >"$WORK/panel-new-prefix.log"
  spawn env "MISTGATE_HEALTH_BLIP_WINDOW=3s" "$BIN/new/mistgate" serve --data-dir "$DATA" --listen "127.0.0.1:$PUB" \
    --tls-cert "$WORK/pub.crt" --tls-key "$WORK/pub.key" --agent-addr "127.0.0.1:$PUB" >>"$WORK/panel-new-prefix.log" 2>&1
  PANEL_PID=$LAST_PID
  wait_for 60 "the new panel (default install)" panel_up || die "the new panel does not start on the default install"
  if api AuthService/Me >/dev/null 2>&1; then ok "A: default install: the owner's session ($cname) survives the upgrade"; else finding "A: default install: the owner's session ($cname) does not survive the upgrade"; fi
  stop_panel
  ADMIN=$saved_admin DATA=$saved_data COOKIE=$saved_cookie TOTP_SECRET=$saved_totp
  CURL_INSECURE=""
}

# ---------------------------------------------------------------- run

want() { [ -z "$ONLY" ] || [[ ",$ONLY," == *",$1,"* ]]; }
load_state
if want seed; then stage_seed; stage_seed_finish; fi
if want upgrade; then stage_upgrade; fi
if want oldnode; then stage_oldnode; fi
if want rollback; then stage_rollback; fi
if want restore; then stage_restore; fi
if want prefix; then stage_prefix; fi
if want volume; then stage_volume; fi
if want nodeupdate; then stage_nodeupdate; fi
if want rollback2; then stage_rollback2; fi
FINISHED=1
