# The Cloudflare edition for scripts/e2e-wsl.sh --edge (design/cloudflare-edition/AGENT-LINK.md §7.6). Sourced by it,
# never run alone: it uses the helpers, variables and the isolation of the main script (private network namespace,
# $WORK, $BIN, api, mutate, say, pass, note, die, wait_for, spawn, stop_pid, free_port, SECRETS ...).
#
# The panel is the Go js/wasm build running in a local `wrangler dev` (workerd, miniflare D1 and Durable Objects);
# the node is the same link-only agent a customer would install, talking to the Worker's public WebSocket link.
#
# NOTHING TOUCHES A CLOUDFLARE ACCOUNT. wrangler runs with a HOME of its own (no stored login), without
# CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID, with telemetry off, never with --remote, and the D1 id in its
# configuration is all zeros. The only outbound traffic is the npm install and miniflare's public cf.json.
#
# The Worker is copied into $WORK/worker and installed there: the repo's own edge/worker is never written to.

EDGE_HOST=de1.example.com
EDGE_NS=""

edge_note_ns() { EDGE_NS=$(ip netns identify $$); }

# edge_reap: whatever of workerd / wrangler is still running in this namespace after stop_pid (a KILLed wrangler leaves
# its workerd behind). Only processes of those two programs, never anything else.
edge_reap() {
  local p exe
  [ -n "$EDGE_NS" ] || return 0
  for p in $(ip netns pids "$EDGE_NS" 2>/dev/null); do
    [ "$p" != $$ ] || continue
    exe=$(basename "$(readlink "/proc/$p/exe" 2>/dev/null)" 2>/dev/null || true)
    case $exe in workerd | node) kill -KILL "$p" 2>/dev/null || true ;; esac
  done
}

edge_workerd_pid() { # the workerd of this namespace with the most memory (the main runtime)
  local p best="" kb max=0
  for p in $(ip netns pids "$EDGE_NS" 2>/dev/null); do
    [ "$(basename "$(readlink "/proc/$p/exe" 2>/dev/null)" 2>/dev/null)" = workerd ] || continue
    kb=$(awk '/^VmRSS:/ {print $2}' "/proc/$p/status" 2>/dev/null || echo 0)
    [ "${kb:-0}" -ge "$max" ] && { max=${kb:-0}; best=$p; }
  done
  echo "$best"
}

# ---------------------------------------------------------------- reading the Worker's local state

edge_d1_file() { find "$WORK/wrangler-state/v3/d1" -name '*.sqlite' ! -name metadata.sqlite 2>/dev/null | head -1; }
edge_sql() { # <sql> [bound values...]: rows of the local D1 database, columns joined by |
  python3 - "$(edge_d1_file)" "$@" <<'PY'
import sqlite3, sys
c = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True, timeout=10)
for r in c.execute(sys.argv[2], sys.argv[3:]):
    print("|".join("" if v is None else str(v) for v in r))
PY
}
edge_do_rows() { # rows in _cf_KV across every NodeLink object file (0 when the table does not exist)
  python3 - "$WORK/wrangler-state/v3/do/mistgate-edge-NodeLink" <<'PY'
import glob, sqlite3, sys
n = 0
for f in glob.glob(sys.argv[1] + "/*.sqlite"):
    if f.endswith("metadata.sqlite"):
        continue
    c = sqlite3.connect("file:%s?mode=ro" % f, uri=True, timeout=10)
    try:
        n += c.execute("SELECT count(*) FROM _cf_KV").fetchone()[0]
    except sqlite3.OperationalError:
        pass
print(n)
PY
}
edge_session() { edge_sql "SELECT session FROM node_live WHERE node_id = ?" "$NODE_ID"; }

# ---------------------------------------------------------------- build

edge_tls() { # a CA and a leaf for de1.example.com, valid two days
  local d=$WORK/tls
  mkdir -p "$d"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=mg-e2e-edge-ca \
    -keyout "$d/ca.key" -out "$d/ca.pem" 2>/dev/null
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj "/CN=$EDGE_HOST" \
    -keyout "$d/leaf.key" -out "$d/leaf.csr" 2>/dev/null
  # 127.0.0.1 too: miniflare answers its /cdn-cgi/ handlers (the scheduled trigger) only to a local Host, not to de1.example.com
  printf 'subjectAltName=DNS:%s,DNS:localhost,IP:127.0.0.1\nbasicConstraints=CA:FALSE\nextendedKeyUsage=serverAuth\n' "$EDGE_HOST" >"$d/ext"
  openssl x509 -req -in "$d/leaf.csr" -CA "$d/ca.pem" -CAkey "$d/ca.key" -CAcreateserial -days 2 -extfile "$d/ext" \
    -out "$d/leaf.pem" 2>/dev/null
  CURL_CA=$d/ca.pem
}

edge_build() {
  local w=$WORK/worker src=$REPO/edge/worker
  mkdir -p "$w/dist"
  GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$w/dist/panel.wasm" ./cmd/mistgate-edge
  cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$w/dist/wasm_exec.js"
  cp -r "$src/src" "$w/src"
  cp "$src/package.json" "$src/pnpm-lock.yaml" "$src/tsconfig.json" "$w/"
  WASM_BYTES=$(stat -c %s "$w/dist/panel.wasm") WASM_GZ=$(gzip -9 -c "$w/dist/panel.wasm" | wc -c)
  # The measurement hook: the Worker's `main` becomes a wrapper that serves /__mgmem (the size of the Go program's
  # linear memory) and hands everything else to the unmodified src/index.ts. Only in this run's copy.
  cat >"$w/src/memhook.ts" <<'TS'
// Records the WebAssembly memories of this isolate (measurement only; not part of the product).
const mems: WebAssembly.Memory[] = ((globalThis as { __mgMems?: WebAssembly.Memory[] }).__mgMems ??= []);
const real = WebAssembly.instantiate.bind(WebAssembly) as (...a: unknown[]) => Promise<unknown>;
(WebAssembly as unknown as { instantiate: unknown }).instantiate = async (...args: unknown[]) => {
  const out = (await real(...args)) as { exports?: { mem?: WebAssembly.Memory }; instance?: { exports?: { mem?: WebAssembly.Memory } } };
  const mem = out?.exports?.mem ?? out?.instance?.exports?.mem;
  if (mem) mems.push(mem);
  return out;
};
TS
  cat >"$w/src/measure.ts" <<'TS'
import "./memhook";
import worker from "./index";
import type { Env } from "./env";
export { Limiter, NodeLink, PanelLink } from "./index";

export default {
  ...worker,
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    if (new URL(request.url).pathname === "/__mgmem") {
      const mems = (globalThis as { __mgMems?: WebAssembly.Memory[] }).__mgMems ?? [];
      return Response.json({ instances: mems.length, wasmBytes: mems.map((m) => m.buffer.byteLength) });
    }
    return worker.fetch!(request, env, ctx);
  },
} satisfies ExportedHandler<Env>;
TS
  (cd "$w" && CI=true pnpm install --frozen-lockfile --store-dir "$WORK/pnpm-store" >"$WORK/pnpm-install.out" 2>&1) \
    || { tail -n 20 "$WORK/pnpm-install.out"; die "pnpm install failed"; }
  sed -e 's/replace-with-your-d1-database-id/00000000-0000-0000-0000-000000000000/' \
    -e "s|^directory = .*|directory = \"$REPO/web/dist\"|" \
    -e 's|^main = .*|main = "src/measure.ts"|' "$src/wrangler.example.toml" >"$w/wrangler.toml"
  grep -q '00000000-0000-0000-0000-000000000000' "$w/wrangler.toml" || die "wrangler.toml: the zero D1 id was not set"
  grep -q '^main = "src/measure.ts"' "$w/wrangler.toml" || die "wrangler.toml: main was not replaced (template changed?)"
  grep -q "^directory = \"$REPO/web/dist\"" "$w/wrangler.toml" || die "wrangler.toml: assets directory was not set (template changed?)"
  MASTER_KEY=$(openssl rand -hex 32)
  ADMIN_SECRET=$(openssl rand -hex 12) SUB_SECRET=$(openssl rand -hex 12)
  SECRETS+=("$MASTER_KEY" "$ADMIN_SECRET" "$SUB_SECRET")
  {
    echo "MASTER_KEY=$MASTER_KEY"
    echo "PUBLIC_URL=https://$EDGE_HOST:$PUB"
    echo "ADMIN_PREFIX=/$ADMIN_SECRET/"
    echo "SUB_PREFIX=/$SUB_SECRET/"
  } >"$w/.dev.vars"
  edge_tls
}

# ---------------------------------------------------------------- the Worker

edge_start_worker() {
  mkdir -p "$WORK/home" "$WORK/wrangler-logs"
  # no stored login, no account, no telemetry; --remote is never passed
  spawn env -C "$WORK/worker" -u CLOUDFLARE_API_TOKEN -u CLOUDFLARE_ACCOUNT_ID -u CLOUDFLARE_API_KEY -u CLOUDFLARE_EMAIL \
    HOME="$WORK/home" XDG_CONFIG_HOME="$WORK/home/.config" XDG_CACHE_HOME="$WORK/home/.cache" XDG_DATA_HOME="$WORK/home/.local/share" \
    WRANGLER_SEND_METRICS=false WRANGLER_LOG_PATH="$WORK/wrangler-logs" CI=true \
    node_modules/.bin/wrangler dev --ip 127.0.0.1 --port "$PUB" --local-protocol https \
    --https-key-path "$WORK/tls/leaf.key" --https-cert-path "$WORK/tls/leaf.pem" --persist-to "$WORK/wrangler-state" \
    </dev/null >>"$WORK/wrangler.log" 2>&1
  WRANGLER_PID=$LAST_PID
  wait_for 120 "wrangler dev to be ready" grep -q 'Ready on' "$WORK/wrangler.log" \
    || { tail -n 30 "$WORK/wrangler.log" | redact; die "wrangler dev did not start"; }
  ! grep -qiE 'wrangler login|log in to|not authenticated|CLOUDFLARE_API_TOKEN|oauth' "$WORK/wrangler.log"     || { grep -iE 'wrangler login|log in to|not authenticated|CLOUDFLARE_API_TOKEN|oauth' "$WORK/wrangler.log" | head -3; die "wrangler asked for a Cloudflare login: stop (the run must not touch an account)"; }
}

# --compressed: a client that sends no Accept-Encoding still gets a gzip body from the panel through wrangler dev (see the report)
edge_get() { curl -sS --compressed --cacert "$CURL_CA" -m 30 "$@"; }
edge_url() { echo "https://$EDGE_HOST:$PUB$1"; }
edge_cron() { edge_get -o /dev/null -w '%{http_code}\n' "https://127.0.0.1:$PUB/cdn-cgi/handler/scheduled?cron=*+*+*+*+*"; }

edge_mem() { # prints the wasm linear memory in MB (largest instance)
  edge_get "$(edge_url /__mgmem)" | jq -r '[.wasmBytes[]] | max // 0 | . / 1048576 | . * 10 | round / 10'
}
edge_workerd_rss() { vmrss_mb "$(edge_workerd_pid)"; }

# The Worker's own log with wrangler's per-request lines taken out: wrangler prints the path of every request, and a
# path holds the secret prefixes. That is wrangler's dev server, not the panel; the rest is what the panel logs.
edge_worker_log() { grep -vE '^\[wrangler:info\] [A-Z]+ |/setup#|^No admin yet' "$WORK/wrangler.log" || true; }

# ---------------------------------------------------------------- scenario

edge_run() {
  local i
  edge_note_ns
  NODE_NAME=node-a
  PUB=$(free_port tcp) HY2=$(free_port udp) SOCKS=$(free_port tcp)
  HOP_FROM=$((30000 + RANDOM % 20000))
  HOP_TO=$((HOP_FROM + 9))
  TCP_MASQ=""
  cd "$REPO"

  step "0. build: the wasm panel, the Worker copy, wrangler, the node agent, the hysteria client"
  [ -f web/dist/index.html ] || die "web/dist is missing: build the SPA first (cd web && pnpm build)"
  edge_build
  go build -trimpath -o "$BIN/mistgate-node" ./cmd/mistgate-node
  NODE_BIN=$BIN/mistgate-node
  build_hysteria
  pass "built the wasm panel ($((WASM_BYTES / 1048576)) MB, $((WASM_GZ / 1048576)) MB gzipped), the Worker copy (installed in the work directory, zero D1 id), the node agent and the hysteria client"

  step "1. wrangler dev: the Worker answers on $EDGE_HOST:$PUB, first admin from the setup link in its log"
  edge_start_worker
  PANEL_PID=$(edge_workerd_pid)
  # the link audience: the agent requires challenge.audience == the host of its link URL; the Worker takes it from the
  # request URL. A wrangler that rewrote the host would show here as a node that never comes online (step 2).
  CODE=$(edge_get -o "$WORK/root.html" -w '%{http_code}' "$(edge_url /)") || die "the Worker did not answer"
  [ "$CODE" = 200 ] && grep -q 'Coming soon' "$WORK/root.html" || die "GET / is not the decoy page (HTTP $CODE)"
  wait_for 30 "the setup link in the Worker log" grep -q '/setup#' "$WORK/wrangler.log" || die "no setup link in the Worker log"
  SETUP_LINE=$(grep -m1 -o 'https://[^ ]*/setup#[^ ]*' "$WORK/wrangler.log")
  SETUP_TOKEN=${SETUP_LINE##*#}
  [ -n "$SETUP_TOKEN" ] && [ "$SETUP_LINE" = "https://$EDGE_HOST:$PUB/$ADMIN_SECRET/setup#$SETUP_TOKEN" ] || die "the setup link does not point at the configured admin prefix"
  # the link is printed on purpose (it is how the operator gets it); it is not a leak to look for in the log
  ADMIN=https://$EDGE_HOST:$PUB/$ADMIN_SECRET
  ADMIN_LOGIN=e2e-admin ADMIN_PASSWORD=$(openssl rand -hex 16)
  SECRETS+=("$ADMIN_PASSWORD")
  BEGIN=$(api AuthService/BeginSetup "$(jq -nc --arg t "$SETUP_TOKEN" --arg l "$ADMIN_LOGIN" --arg p "$ADMIN_PASSWORD" \
    '{setupToken:$t, displayName:"E2E admin", method:"SETUP_METHOD_PASSWORD", login:$l, password:$p}')")
  CEREMONY=$(jq -r .ceremonyId <<<"$BEGIN") TOTP_SECRET=$(jq -r .totpSecret <<<"$BEGIN")
  SECRETS+=("$TOTP_SECRET")
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
  curl -sS --compressed --cacert "$CURL_CA" -m 40 -D "$WORK/setup.hdr" -o /dev/null -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/FinishSetup" \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg t "$SETUP_TOKEN" --arg c "$CEREMONY" --arg code "$(totp_code)" '{setupToken:$t, ceremonyId:$c, totpCode:$code}')"
  grep -q '^HTTP/[0-9.]* 200' "$WORK/setup.hdr" || die "FinishSetup failed: $(head -1 "$WORK/setup.hdr")"
  COOKIE=$(sed -n 's/^[Ss]et-[Cc]ookie: \(__Host-sid=[^;]*\).*/\1/p' "$WORK/setup.hdr" | tr -d '\r')
  [ -n "$COOKIE" ] || die "FinishSetup set no session cookie"
  SECRETS+=("${COOKIE#*=}")
  TOTP_LAST=$(($(date +%s) / 30)) STEPUP_UNTIL=$(($(date +%s) + 280))
  api AuthService/Me | jq -e '.admin.id' >/dev/null || die "the session cookie does not work"
  stepup() {
    local out now step
    now=$(date +%s)
    [ "$STEPUP_UNTIL" -gt $((now + 90)) ] && return 0
    while step=$(($(date +%s) / 30)); [ "$step" -le "$TOTP_LAST" ]; do sleep 1; done
    out=$(api AuthService/FinishStepUp "$(jq -nc --arg c "$(totp_code)" '{totpCode:$c}')") || return 1
    TOTP_LAST=$step
    STEPUP_UNTIL=$(jq -r '.stepUpUntilUnix | tonumber' <<<"$out")
  }
  mutate() { stepup && api "$@"; }
  pass "the Worker serves the decoy at /, the setup link in its log works once, first admin created, the session works (admin under the secret prefix)"

  step "2. enrolment over the link, node online, inbound running, desired state over the link"
  ENROLL=$(api NodeService/CreateEnrollment "{\"name\":\"$NODE_NAME\",\"address\":\"127.0.0.1\"}")
  NODE_ID=$(jq -r .node.id <<<"$ENROLL")
  ENROLL_LINE=$(jq -r .installCommand <<<"$ENROLL" | grep -m1 ' enroll ')
  flag() { sed -n "s/.* --$1 \([^ ]*\).*/\1/p" <<<"$ENROLL_LINE"; }
  E_LINK=$(flag link-url) E_PIN=$(flag ca-sha256) E_TOKEN=$(flag token)
  [ -n "$E_LINK" ] && [ -n "$E_PIN" ] && [ -n "$E_TOKEN" ] || die "cannot parse the install command"
  [[ $E_LINK == wss://$EDGE_HOST:$PUB/*/ ]] || die "the install command's --link-url is not wss://$EDGE_HOST:$PUB/<prefix>/"
  LINK_SECRET=${E_LINK#wss://$EDGE_HOST:$PUB/}
  LINK_SECRET=${LINK_SECRET%/}
  SECRETS+=("$E_TOKEN" "$LINK_SECRET")
  [ "$LINK_SECRET" != "$ADMIN_SECRET" ] && [ "$LINK_SECRET" != "$SUB_SECRET" ] || die "the link prefix equals another secret prefix"
  say "install command: enroll --link-url wss://$EDGE_HOST:$PUB/[prefix]/ --ca-sha256 ${E_PIN:0:8}... --token [secret]"
  # the agent trusts the run's CA for the Worker's TLS (SSL_CERT_FILE); the panel itself is trusted by the pin and LinkAccept
  SSL_CERT_FILE=$CURL_CA "$NODE_BIN" enroll --link-url "$E_LINK" --ca-sha256 "$E_PIN" --token "$E_TOKEN" \
    --state-dir "$NODE_STATE" >"$WORK/node-enroll.log" 2>&1 || { redact <"$WORK/node-enroll.log"; die "enroll over the link failed"; }
  start_node() { SSL_CERT_FILE=$CURL_CA spawn "$NODE_BIN" run --state-dir "$NODE_STATE" >>"$WORK/node.log" 2>&1; }
  start_node
  NODE_PID=$LAST_PID
  wait_for 90 "node ONLINE over the link" node_online || { tail -n 12 "$WORK/node.log" | redact; die "the node never became ONLINE over the link (status: $(node_status))"; }
  GEN1=$(edge_session)
  [ -n "$GEN1" ] || die "node ONLINE but node_live has no row for it"
  SETTINGS=$(api ProfileService/ListProtocols '{}' | jq -c --argjson p "$HY2" --argjson f "$HOP_FROM" --argjson t "$HOP_TO" \
    '.protocols[] | select(.id == "hysteria2") | .defaultSettingsJson | fromjson
     | .tls_mode = "self_signed" | .port = $p | .hop = {from: $f, to: $t} | .obfs.password = "$generate"')
  PROFILE_ID=$(api ProfileService/CreateProfile "$(jq -nc --arg s "$SETTINGS" '{protocol:"hysteria2", name:"e2e-hy2", settingsJson:$s}')" | jq -r .profile.id)
  api ProfileService/CreateInbound "{\"profileId\":\"$PROFILE_ID\",\"nodeId\":\"$NODE_ID\"}" >/dev/null
  wait_for 60 "inbound ACTIVE" inbounds_active 1 || die "inbound did not become ACTIVE: $(api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}" | jq -c '.inbounds[0] | {state, lastError}')"
  nft list table inet "$NFT_TABLE" >"$WORK/nft.out" 2>/dev/null
  grep -q "dport $HOP_FROM-$HOP_TO redirect to :$HY2" "$WORK/nft.out" || die "the agent did not install the port-hop redirect in nft table $NFT_TABLE"
  grep -q 'desired state applied' "$WORK/node.log" || die "the agent never logged an applied desired state"
  pass "node ONLINE over the link (node_live session $GEN1), inbound ACTIVE on UDP $HY2 after the desired state came over the link, nft hop rule $HOP_FROM-$HOP_TO installed"

  step "2b. doctor: the agent's report is stored, RunDoctor goes through Remote.Ask"
  doctor_json() { api HealthService/GetDoctor "{\"nodeId\":\"$NODE_ID\"}"; }
  doctor_ready() { doctor_json | jq -e '.nodes[0] | .agentSupported and .hasReport and (.items | length) >= 10'; }
  wait_for 120 "the node's doctor report" doctor_ready || die "no doctor report: $(doctor_json | jq -c '.nodes[0] | {agentSupported, hasReport, items: (.items | length)}')"
  DOC=$(doctor_json | jq -c '.nodes[0] | {checks: (.items | length), ok: ([.items[] | select(.status == "DOCTOR_STATUS_OK")] | length), warn: ([.items[] | select(.status == "DOCTOR_STATUS_WARN")] | length), fail: ([.items[] | select(.status == "DOCTOR_STATUS_FAIL")] | length)}')
  RUN_AGE=$(api HealthService/RunDoctor "{\"nodeId\":\"$NODE_ID\"}" | jq -r '.nodes[0] | select(.hasReport) | .ageS // 0')
  [ -n "$RUN_AGE" ] && [ "$RUN_AGE" -le 10 ] || die "RunDoctor did not return a fresh report (age '$RUN_AGE')"
  pass "doctor report stored ($DOC); RunDoctor reached the agent through the object and came back with a fresh report (age ${RUN_AGE} s)"

  step "2c. the agent stops and starts again; the Worker is reloaded"
  node_offline() { [ "$(node_status)" != NODE_STATUS_ONLINE ]; }
  LOG_MARK=$(stat -c %s "$WORK/node.log")
  new_log() { tail -c "+$((LOG_MARK + 1))" "$WORK/node.log"; }
  T_STOP=$(date +%s)
  stop_pid "$NODE_PID"
  wait_for 150 "the panel to see the node go" node_offline || die "the node is still ONLINE 150 s after its agent stopped"
  OFF_AFTER=$(($(date +%s) - T_STOP))
  [ -z "$(edge_sql "SELECT 1 FROM node_live WHERE node_id = ?" "$NODE_ID")" ] || note "node_live still has a row for the stopped node (a crash-style end; it is offline by liveness)"
  LOG_MARK=$(stat -c %s "$WORK/node.log")
  start_node
  NODE_PID=$LAST_PID
  wait_for 90 "node ONLINE again" node_online || die "the node did not come back after its agent restarted (status: $(node_status))"
  wait_for 60 "the inbound ACTIVE" inbounds_active 1 || die "the inbound is not ACTIVE after the agent restarted"
  sleep 3
  GEN2=$(edge_session)
  new_log | grep -q 'restored persisted state' || die "the restarted agent did not restore its persisted state"
  new_log | grep -q 'connected to panel' || die "the restarted agent never logged a connection to the panel"
  ! new_log | grep -q 'desired state applied.*full=true' || die "the restarted agent was sent a full desired state: $(new_log | grep 'desired state applied' | head -2)"
  pass "agent stopped: the panel showed it offline ${OFF_AFTER} s later (the closed step ran with the socket's close, no liveness wait); started: ONLINE again (session $GEN1 -> $GEN2), persisted state restored, no full DesiredState sent (the Hello matched the stored digest)"

  # touching the Worker's source makes wrangler reload: the runtime restarts, the object is reset, the agent reconnects
  LOG_MARK=$(stat -c %s "$WORK/node.log")
  GEN_BEFORE=$GEN2
  echo "// reload $(date +%s)" >>"$WORK/worker/src/index.ts"
  gen_after() { [ "$(edge_session)" = "$((GEN_BEFORE + 1))" ]; }
  wait_for 120 "the agent to reconnect as generation $((GEN_BEFORE + 1))" gen_after || die "after the reload node_live shows session '$(edge_session)', want $((GEN_BEFORE + 1))"
  wait_for 60 "node ONLINE after the reload" node_online || die "node not ONLINE after the Worker reload"
  grep -q 'Reloading local server\|Reloading' "$WORK/wrangler.log" || note "wrangler did not log a reload (the generation changed all the same)"
  ! new_log | grep -q 'desired state applied.*full=true' || die "after the reload the agent was sent a full desired state"
  inbounds_active 1 >/dev/null || wait_for 60 "the inbound ACTIVE after the reload" inbounds_active 1 || die "inbound not ACTIVE after the reload"
  PANEL_PID=$(edge_workerd_pid)
  pass "Worker source touched: wrangler reloaded, the agent reconnected as generation $((GEN_BEFORE + 1)), still no full DesiredState, inbound ACTIVE"

  step "3. user, subscription"
  GROUP_ID=$(api GroupService/CreateGroup "{\"name\":\"e2e\",\"profileIds\":[\"$PROFILE_ID\"]}" | jq -r .group.id)
  CREATED=$(api UserService/CreateUser "{\"name\":\"alice\",\"groupId\":\"$GROUP_ID\"}")
  USER_ID=$(jq -r .user.id <<<"$CREATED") SUB_URL=$(jq -r .subscriptionUrl <<<"$CREATED")
  SUB_TOKEN=${SUB_URL##*/}
  SECRETS+=("$SUB_TOKEN")
  [ "$SUB_URL" = "https://$EDGE_HOST:$PUB/$SUB_SECRET/$SUB_TOKEN" ] || die "unexpected subscription URL shape"
  fetch_sub() { curl -sS --compressed --cacert "$CURL_CA" -m 15 -A "$1" -D "$2" "$SUB_URL"; }
  have_line() { fetch_sub 'Happ/3.0' "$WORK/sub.hdr" | base64 -d 2>/dev/null | grep -q '^hysteria2://'; }
  wait_for 40 "hysteria2:// line in the subscription" have_line || die "subscription has no hysteria2:// line"
  BODY_HAPP=$(fetch_sub 'Happ/3.0' "$WORK/sub-happ.hdr")
  hdr() { tr -d '\r' <"$1" | sed -n "s/^$2: *//Ip" | head -1; }
  grep -q '^HTTP/[0-9.]* 200' "$WORK/sub-happ.hdr" || die "subscription is not 200"
  hdr "$WORK/sub-happ.hdr" subscription-userinfo | grep -Eq '^upload=[0-9]+; download=[0-9]+; total=[0-9]+; expire=[0-9]+$' || die "bad subscription-userinfo"
  LINES=$(base64 -d <<<"$BODY_HAPP")
  [ "$(grep -c '^hysteria2://' <<<"$LINES")" = 1 ] || die "expected exactly one hysteria2:// line"
  URI=$(grep -m1 '^hysteria2://' <<<"$LINES")
  AUTH=$(URI=$URI python3 -c 'import os; from urllib.parse import urlsplit, unquote; print(unquote(urlsplit(os.environ["URI"]).username or ""))')
  SECRETS+=("$AUTH")
  OBFS_PW=$(URI=$URI python3 -c 'import os; from urllib.parse import urlsplit, parse_qs; print(parse_qs(urlsplit(os.environ["URI"]).query).get("obfs-password", [""])[0])')
  [ -n "$OBFS_PW" ] && [ "$OBFS_PW" != "••••" ] && SECRETS+=("$OBFS_PW")
  grep -q 'insecure=1' <<<"$URI" && grep -q 'pinSHA256=' <<<"$URI" && grep -q 'obfs=salamander' <<<"$URI" || die "the self-signed URI must carry insecure=1, pinSHA256 and obfs=salamander"
  pass "subscription: 200, userinfo header, one hysteria2:// line with pin and obfs (credentials created on the first fetch reached the node)"

  step "4. real Hysteria2 client, download through the node; the panel counts it"
  start_client main "$SOCKS" || { tail -n 15 "$WORK/client-main.log" | redact; die "client did not connect"; }
  MAIN_CLIENT=$LAST_PID
  burst "$SOCKS"
  DL_BYTES=$BURST_BYTES
  [ "$DL_BYTES" -ge 20000000 ] || die "downloaded only $DL_BYTES bytes"
  MIN_USED=$((DL_BYTES * 95 / 100))
  T_SEEN=$(date +%s)
  wait_for 40 "user usedBytes >= 95% of the download" used_ge "$MIN_USED" || die "after 40 s the panel shows $(used_bytes) bytes, the client downloaded $DL_BYTES"
  LAG=$(($(date +%s) - T_SEEN))
  sleep 11
  USED_BYTES=$(used_bytes)
  RATIO=$(python3 -c "print(round($USED_BYTES / $DL_BYTES, 4))")
  [ "$USED_BYTES" -le $((DL_BYTES * 110 / 100)) ] || die "panel counted $USED_BYTES bytes for a $DL_BYTES byte download (ratio $RATIO)"
  user_json | jq -e '.user.online == true' >/dev/null || die "GetUser: user is not online while the client is connected"
  pass "downloaded $DL_BYTES bytes (4 x 20 MB, ~${BURST_MBPS} Mbit/s) through the node; used bytes grew to $USED_BYTES (ratio $RATIO, visible ${LAG} s after the download)"

  step "5. one isolate under mixed load: 20 parallel subscription fetches, link steps, fan-out and a cron tick (checklist item 10)"
  pick_source "$SOCKS" || die "no speed-test server answered through the tunnel"
  socks_curl "$SOCKS" -m 100 --limit-rate 700K -o /dev/null -w '%{http_code} %{size_download}\n' $(dl_args "$SRC" 40000000) >"$WORK/mix.res" 2>"$WORK/mix.err" || true &
  MIX=$!
  PIDS+=("$MIX")
  sleep 4
  : >"$WORK/mix.fetch"
  for i in $(seq 20); do
    ( c=$(curl -sS --compressed --cacert "$CURL_CA" -m 40 -o /dev/null -w '%{http_code}' -A 'Happ/3.0' "$SUB_URL" 2>&1) || c="curl-failed"; echo "$c" >>"$WORK/mix.fetch" ) &
    PIDS+=("$!")
  done
  # an admin change in the middle of it: two fan-outs back to back (a poke for the node while stats frames arrive)
  api UserService/SetUsersEnabled "{\"userIds\":[\"$USER_ID\"],\"enabled\":false}" >/dev/null &
  PIDS+=("$!")
  edge_cron >"$WORK/mix.cron" 2>&1 &
  CRON1=$!
  PIDS+=("$CRON1")
  fetches_done() { [ "$(wc -l <"$WORK/mix.fetch")" -ge 20 ]; }
  wait_for 60 "the 20 fetches to finish" fetches_done || die "only $(wc -l <"$WORK/mix.fetch") of 20 subscription fetches finished: hung invocation?"
  wait "$CRON1" || true
  api UserService/SetUsersEnabled "{\"userIds\":[\"$USER_ID\"],\"enabled\":true}" >/dev/null || die "re-enabling alice failed"
  BAD=$(grep -vc '^200$' "$WORK/mix.fetch" || true)
  [ "$BAD" = 0 ] || die "$BAD of 20 parallel subscription fetches did not return 200: $(sort "$WORK/mix.fetch" | uniq -c | tr '\n' ' ')"
  grep -q '^200$' "$WORK/mix.cron" || die "the cron tick did not return 200: $(cat "$WORK/mix.cron")"
  sleep 15 # let the poke steps, the stats frames and the background work settle
  ! grep -q 'Cannot perform I/O on behalf of a different request' "$WORK/wrangler.log" || die "the Worker log has 'Cannot perform I/O on behalf of a different request'"
  ! grep -qiE 'never generate a response|Promise will never complete|hung' "$WORK/wrangler.log" || { grep -iE 'never generate a response|Promise will never complete|hung' "$WORK/wrangler.log" | head -3; die "the Worker log reports a hung invocation"; }
  node_online || die "the node went offline during the mixed load"
  wait_for 60 "the long download to end" not_alive "$MIX" || true
  PEAK_WASM_MIX=$(edge_mem)
  pass "20 parallel subscription fetches (all 200), a cron tick (200) and two admin fan-outs while the agent sent stats frames: no hung invocation, no cross-request I/O error in the Worker log; wasm memory $PEAK_WASM_MIX MB"

  step "6. quota overrun: alice's credential is removed within one stats interval"
  stop_pid "$MAIN_CLIENT"
  QSOCKS=$(free_port tcp)
  start_client quota "$QSOCKS" || die "client did not connect for the quota test"
  QUOTA_CLIENT=$LAST_PID
  sleep 12
  socks_curl "$QSOCKS" -m 110 --limit-rate 1M -o /dev/null -w '%{http_code} %{size_download}\n' $(dl_args "$SRC" 60000000) >"$WORK/quota.res" 2>"$WORK/quota.err" || true &
  QDL=$!
  PIDS+=("$QDL")
  sleep 3
  U1=$(used_bytes)
  QUOTA=$((U1 + 4000000))
  T_Q=$(date +%s.%N)
  mutate UserService/UpdateUser "{\"userId\":\"$USER_ID\",\"quotaBytes\":$QUOTA}" >/dev/null || die "UpdateUser(quota) failed"
  T_LIM="" T_KICK=""
  for ((i = 0; i < 360; i++)); do
    if [ -z "$T_LIM" ] && [ "$(user_json | jq -r .user.status)" = USER_STATUS_LIMITED ]; then T_LIM=$(python3 -c "import time; print(round(time.time() - $T_Q, 1))"); fi
    if grep -q 'Application error' "$WORK/client-quota.log"; then T_KICK=$(python3 -c "import time; print(round(time.time() - $T_Q, 1))"); break; fi
    sleep 0.5
  done
  [ -n "$T_KICK" ] || die "the node did not kick alice's session within 180 s of the quota being set (limited after '${T_LIM:-never}' s)"
  [ -n "$T_LIM" ] || die "alice's session was kicked but her status never showed LIMITED"
  wait_for 60 "the quota download to end" not_alive "$QDL" || die "the download kept running after the kick"
  user_json | jq -e '.user.status == "USER_STATUS_LIMITED"' >/dev/null || die "alice is not LIMITED: $(user_json | jq -c '.user | {status, usedBytes, quotaBytes}')"
  stop_pid "$QUOTA_CLIENT"
  RP=$(free_port tcp)
  if start_client refused-q "$RP"; then die "a new client connected although alice is over her quota"; fi
  pass "quota set to used + 4 MB: alice LIMITED after ${T_LIM} s, her session kicked ${T_KICK} s after the change, a new client is refused (credential removed by the fan-out)"
  mutate UserService/UpdateUser "{\"userId\":\"$USER_ID\",\"quotaBytes\":0}" >/dev/null || die "UpdateUser(quota 0) failed"
  try_again() {
    local p
    p=$(free_port tcp)
    if start_client again "$p"; then
      AGAIN_CLIENT=$LAST_PID
      dl_one "$p" 1000000 0 -o /dev/null >/dev/null && return 0
    fi
    stop_pid "$LAST_PID"
    return 1
  }
  T_ON=$(date +%s)
  wait_for 60 "a client to work after the quota was lifted" try_again || die "after lifting the quota no client gets through"
  stop_pid "$AGAIN_CLIENT"
  pass "quota lifted: a fresh client connects and downloads again (after $(($(date +%s) - T_ON)) s)"

  step "7. disable the user: the fan-out kicks the session and a new client is refused; enable: it works again"
  DSOCKS=$(free_port tcp)
  start_client dis "$DSOCKS" || die "client did not connect for the disable test"
  DIS_CLIENT=$LAST_PID
  socks_curl "$DSOCKS" -m 110 --limit-rate 1M -o /dev/null -w '%{http_code} %{size_download}\n' $(dl_args "$SRC" 60000000) >"$WORK/dis.res" 2>"$WORK/dis.err" || true &
  DDL=$!
  PIDS+=("$DDL")
  sleep 4
  T_OFF=$(date +%s.%N)
  api UserService/SetUsersEnabled "{\"userIds\":[\"$USER_ID\"],\"enabled\":false}" | jq -e '.users[0].status == "USER_STATUS_DISABLED"' >/dev/null || die "SetUsersEnabled(false) did not disable"
  KICK=""
  for ((i = 0; i < 300; i++)); do
    if grep -q 'Application error' "$WORK/client-dis.log"; then KICK=$(python3 -c "import time; print(round(time.time() - $T_OFF, 1))"); break; fi
    sleep 0.1
  done
  [ -n "$KICK" ] || die "the node did not kick the session within 30 s of the user being disabled"
  wait_for 60 "the running download to end" not_alive "$DDL" || die "the download kept running 60 s after the user was disabled"
  stop_pid "$DIS_CLIENT"
  RP=$(free_port tcp)
  if start_client refused-d "$RP"; then die "a new client connected although the user is disabled"; fi
  SUB_OFF=$(fetch_sub 'Happ/3.0' "$WORK/sub-off.hdr" | base64 -d 2>/dev/null || true)
  grep -q '^hysteria2://off@0\.0\.0\.0:1/' <<<"$SUB_OFF" || die "the disabled user's subscription has no entry that says why"
  api UserService/SetUsersEnabled "{\"userIds\":[\"$USER_ID\"],\"enabled\":true}" | jq -e '.users[0].status == "USER_STATUS_ACTIVE"' >/dev/null || die "SetUsersEnabled(true) did not enable"
  T_ON=$(date +%s)
  wait_for 60 "a client to work after enabling" try_again || die "after re-enabling the user no client gets through"
  stop_pid "$AGAIN_CLIENT"
  pass "disabled: session kicked ${KICK} s after SetUsersEnabled(false), a new client is refused, the subscription says why; enabled: a fresh client downloads again (after $(($(date +%s) - T_ON)) s)"

  step "8. expiry 70 s ahead, then the cron path: the scheduled handler marks alice expired and the fan-out cuts her off"
  ESOCKS=$(free_port tcp)
  EXP=$(($(date +%s) + 70))
  mutate UserService/UpdateUser "{\"userId\":\"$USER_ID\",\"expiresUnix\":$EXP}" >/dev/null || die "UpdateUser(expiry) failed"
  start_client exp "$ESOCKS" || die "client did not connect for the expiry test"
  EXP_CLIENT=$LAST_PID
  dl_one "$ESOCKS" 1000000 0 -o /dev/null >/dev/null || die "the client cannot download before the expiry"
  socks_curl "$ESOCKS" -m 150 --limit-rate 400K -o /dev/null -w '%{http_code} %{size_download}\n' $(dl_args "$SRC" 60000000) >"$WORK/exp.res" 2>"$WORK/exp.err" || true &
  EDL=$!
  PIDS+=("$EDL")
  while [ "$(date +%s)" -le $((EXP + 3)) ]; do sleep 1; done
  STATUS_BEFORE=$(user_json | jq -r .user.status)
  T_C=$(date +%s.%N)
  CODE=$(edge_cron) || die "the scheduled request failed"
  [ "$CODE" = 200 ] || die "the scheduled handler answered HTTP $CODE"
  is_expired() { user_json | jq -e '.user.status == "USER_STATUS_EXPIRED"'; }
  wait_for 30 "alice to be EXPIRED after the cron tick" is_expired || die "after the cron tick alice is $(user_json | jq -r .user.status)"
  T_EXP=$(python3 -c "import time; print(round(time.time() - $T_C, 1))")
  KICK=""
  for ((i = 0; i < 300; i++)); do
    if grep -q 'Application error' "$WORK/client-exp.log"; then KICK=$(python3 -c "import time; print(round(time.time() - $T_C, 1))"); break; fi
    sleep 0.1
  done
  [ -n "$KICK" ] || die "the node did not kick the session within 30 s of the cron tick"
  stop_pid "$EXP_CLIENT"
  RP=$(free_port tcp)
  if start_client refused-e "$RP"; then die "a new client connected although alice is expired"; fi
  pass "expiry reached, cron tick (HTTP 200): alice EXPIRED after ${T_EXP} s, her session kicked ${KICK} s after the tick, a new client is refused (status just before the tick: ${STATUS_BEFORE#USER_STATUS_})"
  mutate UserService/UpdateUser "{\"userId\":\"$USER_ID\",\"expiresUnix\":0}" >/dev/null || die "UpdateUser(expiry 0) failed"

  step "9. measurements of this run (numbers, not pass/fail)"
  RSS_WORKERD=$(edge_workerd_rss)
  WASM_MB=$(edge_mem)
  printf '    %-52s %s\n' "wasm file" "$((WASM_BYTES / 1048576)) MB ($((WASM_GZ / 1048576)) MB gzip -9)"
  printf '    %-52s %s\n' "wasm linear memory after the e2e mix (limit 128 MB)" "$WASM_MB MB"
  printf '    %-52s %s\n' "workerd RSS at the end (whole runtime, all isolates)" "$RSS_WORKERD MB"
  printf '    %-52s %s\n' "node agent: offline after stop" "${OFF_AFTER} s"
  note "measured: wasm file $WASM_BYTES bytes ($WASM_GZ gzip), wasm memory ${WASM_MB} MB, workerd RSS ${RSS_WORKERD} MB"

  step "10. retire the node: the agent removes its files, the object keeps nothing, node_live is gone"
  [ "$(edge_do_rows)" -gt 0 ] || die "precondition: the NodeLink object has no stored rows before the retire (_cf_KV is empty)"
  [ -n "$(edge_sql "SELECT 1 FROM node_live WHERE node_id = ?" "$NODE_ID")" ] || die "precondition: node_live has no row before the retire"
  nft list table inet "$NFT_TABLE" >/dev/null 2>&1 || die "precondition: the nft table is gone before the retire"
  RET=$(api NodeService/RetireNode "{\"nodeId\":\"$NODE_ID\",\"confirmName\":\"$NODE_NAME\"}")
  jq -e '.agentNotified == true' <<<"$RET" >/dev/null || die "RetireNode: the connected agent was not notified"
  wait_for 30 "the agent to exit" not_alive "$NODE_PID" || die "the node agent kept running after the retire"
  NODE_RC=0
  wait "$NODE_PID" || NODE_RC=$?
  [ "$NODE_RC" = 0 ] || die "the agent exited with code $NODE_RC after the retire (want 0)"
  ! nft list table inet "$NFT_TABLE" >/dev/null 2>&1 || die "nft table $NFT_TABLE is still there after the retire"
  [ ! -e "$SYSCTL_FILE" ] && [ ! -e "$JOURNALD_FILE" ] || die "the agent's sysctl or journald drop-in is still there after the retire"
  [ "$(node_status)" = NODE_STATUS_RETIRED ] || die "node status after the retire: $(node_status)"
  no_do_rows() { [ "$(edge_do_rows)" = 0 ]; }
  wait_for 30 "the object's stored session to be deleted" no_do_rows || die "the NodeLink object still has $(edge_do_rows) stored row(s) after the retire"
  [ -z "$(edge_sql "SELECT 1 FROM node_live WHERE node_id = ?" "$NODE_ID")" ] || die "node_live still has a row for the retired node"
  api NodeService/ListNodes '{}' | jq -e --arg n "$NODE_NAME" '[(.nodes // [])[] | select(.name == $n)] | length == 0' >/dev/null || die "a retired node is still in the default list"
  pass "retired: agent notified, exited 0, nft table, sysctl and journald drop-ins removed, node RETIRED and hidden, the object's storage holds no rows (_cf_KV empty), node_live row gone"

  step "11. secrets never reach the logs (Worker log without wrangler's own request lines; wrangler's file log; node and client logs)"
  LEAKS=0
  edge_worker_log >"$WORK/wrangler-worker-output.txt"
  cat "$WORK"/wrangler-logs/* 2>/dev/null | grep -vE '^\[wrangler:info\] [A-Z]+ |/setup#' >"$WORK/wrangler-file-log.txt" || true
  for s in "${SECRETS[@]}"; do
    [ ${#s} -ge 8 ] || continue
    for f in "$WORK"/*.log "$WORK/wrangler-worker-output.txt" "$WORK/wrangler-file-log.txt" "$WORK"/pnpm-install.out; do
      [ -f "$f" ] || continue
      [ "$f" != "$WORK/wrangler.log" ] || continue # raw: wrangler's own request lines print the URL paths; checked filtered below
      if grep -qF -- "$s" "$f"; then
        LEAKS=$((LEAKS + 1))
        say "secret of ${#s} chars starting '${s:0:2}' found in: $(basename "$f")"
      fi
    done
  done
  [ "$LEAKS" = 0 ] || die "$LEAKS secret(s) found in logs"
  ! grep -q 'Cannot perform I/O on behalf of a different request' "$WORK/wrangler.log" "$WORK/wrangler-file-log.txt"     || die "the Worker log has 'Cannot perform I/O on behalf of a different request'"
  # wrangler's dev server prints the path of every request: the secret prefixes are in those lines, by its design
  PATHS=$(grep -cE '^\[wrangler:info\] [A-Z]+ ' "$WORK/wrangler.log" || true)
  pass "no secret (admin password, TOTP, cookie, master key, enrolment token, subscription token, hysteria credentials) in the Worker's output, wrangler's file log, or the node and client logs; wrangler's own request lines ($PATHS) print URL paths and are excluded"
  ! grep -qE 'Error|error' "$WORK/wrangler-worker-output.txt" || note "the Worker log has error lines: $(grep -E 'Error|error' "$WORK/wrangler-worker-output.txt" | sort | uniq -c | sort -rn | head -5 | tr '\n' ';')"
}
