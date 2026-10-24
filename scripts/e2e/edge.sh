# The Cloudflare edition for scripts/e2e-wsl.sh --edge (design/cloudflare-edition/AGENT-LINK.md §7.6). Sourced by it,
# never run alone: it uses the helpers, variables and the isolation of the main script (private network namespace,
# $WORK, $BIN, api, mutate, say, pass, note, die, wait_for, spawn, stop_pid, free_port, SECRETS ...).
#
# The panel is the five Go js/wasm roles (cmd/mistgate-edge-{link,public,admin,jobs,provision}) behind the FRONT router, all six
# scripts of the split edition running in one local `wrangler dev` (workerd, miniflare D1 and Durable Objects); the node is the
# same link-only agent a customer would install, talking to FRONT's public WebSocket link.
#
# NOTHING TOUCHES A CLOUDFLARE ACCOUNT. wrangler runs with a HOME of its own (no stored login), without
# CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID, with telemetry off, never with --remote, and the D1 id in its
# configuration is all zeros. The only outbound traffic is the npm install and miniflare's public cf.json.
#
# The Worker is copied into $WORK/worker and installed there: the repo's own edge/worker is never written to.
#
# The six wrangler configs are the ones the release is described by: `go run ./scripts/edgeconfig -dev` renders them from
# release.NewEdgeConfig (FRONT first, so it is the one that listens; the others are reached by service binding). The cron of
# JOBS is fired through wrangler's local explorer API (`/cdn-cgi/local/explorer/api/local/scheduled?worker=`), which reaches a
# script that is not the listening one. The SSH steps (2d, 8b, 10b; scripts/e2e/sshnode.sh) drive the wizard and real installs
# through the Provisioner object of mistgate-edge-provision against a real OpenSSH sshd in this namespace. The wasm is built as
# a release would be: a tag, a release key, and the download base pointed (-X update.releaseDownloadBase) at a fake release
# served from this namespace and signed with that key.

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

EDGE_ROLES="link public admin jobs provision" # the Go roles, one wasm each (cmd/mistgate-edge-<role>); FRONT has no wasm
EDGE_NAME=mistgate-edge                       # the base name of the scripts: FRONT is $EDGE_NAME, a role $EDGE_NAME-<role>

edge_build() {
  local w=$WORK/worker src=$REPO/edge/worker role n
  mkdir -p "$w/dist"
  WASM_BYTES=0 WASM_GZ=0
  for role in $EDGE_ROLES; do
    GOOS=js GOARCH=wasm go build -tags nethttpomithttp2 -trimpath -ldflags="-s -w -X $SSHN_BI.Version=$SSHN_TAG -X $SSHN_BI.ReleaseKey=$REL_PUB -X $SSHN_UPD.releaseDownloadBase=http://127.0.0.1:$REL_PORT/ -X $TG_PKG.defaultAPIBase=http://127.0.0.1:$TG_PORT" \
      -o "$w/dist/$role.wasm" "./cmd/mistgate-edge-$role"
    n=$(stat -c %s "$w/dist/$role.wasm")
    WASM_BYTES=$((WASM_BYTES + n)) WASM_GZ=$((WASM_GZ + $(gzip -9 -c "$w/dist/$role.wasm" | wc -c)))
    printf '%s %s\n' "$role" "$n" >>"$WORK/wasm-sizes"
  done
  # a -X on a variable the linker does not see as a constant string is silently ignored: look for the value in the program that
  # uses it (the Provisioner downloads the agent from the release)
  grep -qa "http://127.0.0.1:$REL_PORT/" "$w/dist/provision.wasm" || die "provision.wasm does not carry the fake release's address: -X update.releaseDownloadBase did not take"
  # the same for the fake Telegram, in every role that can send: a security notice from admin, the tick from jobs, a producer from
  # link and provision. public (the decoy and the subscription) does not reach the Telegram code, so the linker drops the variable there.
  for role in link admin jobs provision; do
    grep -qa "http://127.0.0.1:$TG_PORT" "$w/dist/$role.wasm" || die "$role.wasm does not carry the fake Telegram's address: -X telegram.defaultAPIBase did not take"
  done
  cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$w/dist/wasm_exec.js"
  cp -r "$src/src" "$w/src"
  cp "$src/package.json" "$src/pnpm-lock.yaml" "$src/tsconfig.json" "$w/"
  # The measurement hook, in this run's copy only: every role that serves requests (public, admin, link) answers /__mgmem with the
  # size of its Go program's linear memory, and FRONT hands /__mgmem?role=<role> to the role's service binding.
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
export function mgMem(): Response {
  return Response.json({ instances: mems.length, wasmBytes: mems.map((m) => m.buffer.byteLength) });
}
TS
  # handler.ts is the fetch of the three serving roles: answer /__mgmem before the panel, and load the hook first
  sed -i -e '1i import { mgMem } from "./memhook";' \
    -e 's|^  return async (request: Request, env: PanelEnv, ctx: ExecutionContext): Promise<Response> => {$|&\n    if (new URL(request.url).pathname === "/__mgmem") return mgMem();|' "$w/src/handler.ts"
  grep -q '__mgmem' "$w/src/handler.ts" || die "src/handler.ts: the measurement hook was not applied (the file changed?)"
  cat >"$w/src/measure-front.ts" <<'TS'
import front from "./front";
import type { FrontEnv } from "./env";
export { Limiter } from "./limiter";
export { NodeLink } from "./nodelink";

export default {
  ...front,
  async fetch(request: Request, env: FrontEnv, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/__mgmem") {
      const role = { public: env.PUBLIC, admin: env.ADMIN, link: env.LINK }[url.searchParams.get("role") ?? ""];
      return role ? role.fetch(new Request(`${url.origin}/__mgmem`)) : new Response("no such role", { status: 404 });
    }
    return front.fetch!(request, env, ctx);
  },
} satisfies ExportedHandler<FrontEnv>;
TS
  (cd "$w" && CI=true pnpm install --frozen-lockfile --store-dir "$WORK/pnpm-store" >"$WORK/pnpm-install.out" 2>&1) \
    || { tail -n 20 "$WORK/pnpm-install.out"; die "pnpm install failed"; }
  # The six configs are rendered from release.NewEdgeConfig, the description the release bundle carries; only this run's
  # paths are set here: FRONT's main (the measuring wrapper) and the assets directory of public and admin.
  go run ./scripts/edgeconfig -out "$w" -dev || die "rendering the wrangler configs failed"
  EDGE_CONFIGS=$(go run ./scripts/edgeconfig -dev -list | sed 's/^/-c /' | tr '\n' ' ')
  [ "$(wc -w <<<"$EDGE_CONFIGS")" = 12 ] || die "expected six wrangler configs, got: $EDGE_CONFIGS"
  for role in front $EDGE_ROLES; do
    grep -q '00000000-0000-0000-0000-000000000000' "$w/wrangler.$role.dev.toml" || die "wrangler.$role.dev.toml: the zero D1 id was not set"
  done
  sed -i 's|^main = .*|main = "src/measure-front.ts"|' "$w/wrangler.front.dev.toml"
  grep -q '^main = "src/measure-front.ts"' "$w/wrangler.front.dev.toml" || die "wrangler.front.dev.toml: main was not replaced"
  for role in public admin; do
    sed -i "s|^directory = .*|directory = \"$REPO/web/dist\"|" "$w/wrangler.$role.dev.toml"
    grep -q "^directory = \"$REPO/web/dist\"" "$w/wrangler.$role.dev.toml" || die "wrangler.$role.dev.toml: the assets directory was not set"
  done
  MASTER_KEY=$(openssl rand -hex 32)
  ADMIN_SECRET=$(openssl rand -hex 12) SUB_SECRET=$(openssl rand -hex 12)
  # the setup token is the deployer's: the Worker gets its hash only (SETUP_TOKEN_SHA256) and never prints a link
  SETUP_TOKEN=$(openssl rand -hex 32)
  SETUP_HASH=$(printf %s "$SETUP_TOKEN" | sha256sum | cut -d' ' -f1)
  SECRETS+=("$MASTER_KEY" "$ADMIN_SECRET" "$SUB_SECRET" "$SETUP_TOKEN" "$SETUP_HASH")
  # One .dev.vars next to the six configs, so every script reads it: MASTER_KEY is what the five Go roles share, the rest is read by
  # the admin role alone (it makes the instance) and is ignored by the others.
  {
    echo "MASTER_KEY=$MASTER_KEY"
    echo "SETUP_TOKEN_SHA256=$SETUP_HASH"
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
    node_modules/.bin/wrangler dev $EDGE_CONFIGS --ip 127.0.0.1 --port "$PUB" --local-protocol https \
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
# The cron tick of JOBS. /cdn-cgi/handler/scheduled reaches only the script that listens (FRONT, which has no cron); wrangler's local
# explorer API fires the scheduled handler of any script by name. Prints 200 when the handler finished with outcome "ok".
edge_cron() {
  local out code
  out=$(edge_get -X POST -H 'Content-Type: application/json' -d '{"cron":"* * * * *"}' -w $'\n%{http_code}' \
    "https://127.0.0.1:$PUB/cdn-cgi/local/explorer/api/local/scheduled?worker=$EDGE_NAME-jobs") || { echo curl-failed; return 1; }
  code=${out##*$'\n'}
  if [ "$code" = 200 ] && jq -e '.result.outcome == "ok"' <<<"${out%$'\n'*}" >/dev/null 2>&1; then echo 200; else echo "$code ${out%$'\n'*}" | head -c 300; echo; return 1; fi
}

edge_mem() { # <public|admin|link>: the wasm linear memory of that role's isolate in MB (largest instance)
  edge_get "$(edge_url "/__mgmem?role=${1:?role}")" | jq -r '[.wasmBytes[]] | max // 0 | . / 1048576 | . * 10 | round / 10'
}
edge_workerd_rss() { vmrss_mb "$(edge_workerd_pid)"; }

# The Worker's own log with wrangler's per-request lines taken out: wrangler prints the path of every request, and a
# path holds the secret prefixes. That is wrangler's dev server, not the panel; the rest is what the panel logs.
edge_worker_log() { grep -vE '^\[wrangler:info\] [A-Z]+ ' "$WORK/wrangler.log" || true; }

# ---------------------------------------------------------------- the fake release and the load for the SSH installs

SSHN_BI=github.com/mistgate/mistgate/internal/buildinfo
SSHN_UPD=github.com/mistgate/mistgate/internal/panel/update
SSHN_SQL=edge_sql
TG_PKG=github.com/mistgate/mistgate/internal/panel/telegram
SSHN_CLOSED_CODE=ssh_target_refused # the Worker's sockets cannot tell a closed port from a refusal, and say so

# edge_release_keys: the run's release key pair (never the owner's) and the port of the fake release, before the wasm is built
edge_release_keys() {
  go build -trimpath -o "$BIN/mistgate-tool" ./cmd/mistgate # keygen only
  go build -trimpath -o "$BIN/signbundle" ./scripts/e2e/signbundle
  REL_PUB=$("$BIN/mistgate-tool" release keygen --out "$WORK/release.key" | sed -n 's/^public key: //p')
  [ -n "$REL_PUB" ] || die "release keygen printed no public key"
  SECRETS+=("$(cat "$WORK/release.key")")
  # the release's build time (the binary's and the manifest's must be the same number) and the primary agent's: an hour older, so
  # the panel offers it the release (step 2e)
  REL_BUILT=$(date +%s) OLD_BUILT=$(($(date +%s) - 3600))
  REL_PORT=$(free_port tcp)
  TG_PORT=$(free_port tcp) # the fake Telegram (edge_telegram_serve); its address is built into the wasm like the release's
}

# edge_release_serve: the agent of the fake release (the tag, the key), signed, served over HTTP from this namespace as a
# GitHub release download would be. The Provisioner streams it into the SSH upload and checks it against the signed manifest.
edge_release_serve() {
  local arch d=$WORK/fake-release
  arch=$(go env GOARCH)
  mkdir -p "$WORK/rel/ssh"
  go build -trimpath -ldflags "-X $SSHN_BI.Version=$SSHN_TAG -X $SSHN_BI.Built=$REL_BUILT -X $SSHN_BI.ReleaseKey=$REL_PUB" \
    -o "$WORK/rel/ssh/mistgate-node-linux-$arch" ./cmd/mistgate-node
  "$BIN/signbundle" --key "$WORK/release.key" --version "$SSHN_TAG" --built "$REL_BUILT" --out "$d/$SSHN_TAG" "$WORK/rel/ssh/mistgate-node-linux-$arch" >>"$WORK/signbundle.log" \
    || die "signing the fake release failed"
  SSHN_NODE_BIN=$d/$SSHN_TAG/mistgate-node-linux-$arch
  SSHN_NODE_BIN_SHA=$(sha256sum "$SSHN_NODE_BIN" | cut -d' ' -f1)
  # One line per request in the log: time, request line, status, then range= for a Range header and authorization=present or
  # cookie=present for credentials (the Worker must never forward either to the release host). Behaviour switches are files in
  # $REL_FLAGS that a step creates and removes:
  #   redirect  every download answers 302 to /cdn/<the same path> on this host, as GitHub's does. Off for the SSH steps: their
  #             Go client refuses a redirect that does not stay on GitHub.
  #   cut       one shot: the next full download of the agent sends 1 MiB, stays silent for 10 s, then closes the connection short.
  cat >"$WORK/fake-release.py" <<'PY'
import http.server, os, re, sys, time, urllib.parse

root, port, log, flags = os.path.realpath(sys.argv[1]), int(sys.argv[2]), sys.argv[3], sys.argv[4]
CUT, HOLD = 1 << 20, 10


def flag(name):
    return os.path.exists(os.path.join(flags, name))


def take(name):  # a one-shot flag: true for exactly one caller
    try:
        os.remove(os.path.join(flags, name))
        return True
    except OSError:
        return False


class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def note(self, text):
        with open(log, "a") as f:
            f.write("%d %s\n" % (int(time.time()), text))

    def answer(self, code, extra, headers=()):
        self.note('"%s" %d -%s' % (self.requestline, code, extra))
        self.send_response(code)
        for k, v in headers:
            self.send_header(k, v)
        if code >= 300 and code != 304 and code != 206:
            self.send_header("Content-Length", "0")
        self.end_headers()

    def do_HEAD(self):
        self.serve(False)

    def do_GET(self):
        self.serve(True)

    def serve(self, with_body):
        path = urllib.parse.unquote(urllib.parse.urlsplit(self.path).path)
        rng = self.headers.get("Range", "")
        extra = (" range=" + rng if rng else "") + "".join(" %s=present" % h.lower() for h in ("Authorization", "Cookie") if self.headers.get(h))
        cdn = path.startswith("/cdn/")
        if cdn:
            path = path[len("/cdn"):]
        full = os.path.realpath(os.path.join(root, path.lstrip("/")))
        if not full.startswith(root + os.sep) or not os.path.isfile(full):
            return self.answer(404, extra)
        if not cdn and flag("redirect"):
            return self.answer(302, extra, [("Location", "http://%s/cdn%s" % (self.headers.get("Host", "127.0.0.1:%d" % port), path))])
        size = os.path.getsize(full)
        start, end, code = 0, size - 1, 200
        if rng:
            m = re.fullmatch(r"bytes=(\d+)-(\d*)", rng)
            if not m or int(m.group(1)) >= size:
                return self.answer(416, extra, [("Content-Range", "bytes */%d" % size)])
            start, code = int(m.group(1)), 206
            end = min(int(m.group(2)), size - 1) if m.group(2) else size - 1
        n = end - start + 1
        kind = "application/json" if full.endswith(".json") else "application/octet-stream"
        hdr = [("Content-Type", kind), ("Accept-Ranges", "bytes"), ("Content-Length", str(n))]
        if code == 206:
            hdr.append(("Content-Range", "bytes %d-%d/%d" % (start, end, size)))
        cut = with_body and start == 0 and os.path.basename(full).startswith("mistgate-node-") and flag("cut") and take("cut")
        self.answer(code, extra, hdr)
        if not with_body:
            return
        with open(full, "rb") as f:
            f.seek(start)
            if cut:
                self.wfile.write(f.read(CUT))
                self.wfile.flush()
                self.note("cut %s after %d of %d bytes (silent for %d s first)" % (path, CUT, n, HOLD))
                time.sleep(HOLD)
                self.close_connection = True
                return
            left = n
            while left > 0:
                b = f.read(min(left, 1 << 16))
                if not b:
                    break
                self.wfile.write(b)
                left -= len(b)


class S(http.server.ThreadingHTTPServer):
    def handle_error(self, request, client_address):  # a client that gives up mid-download (a cancelled install) is not an error
        if not isinstance(sys.exc_info()[1], ConnectionError):
            super().handle_error(request, client_address)


S(("127.0.0.1", port), H).serve_forever()
PY
  REL_FLAGS=$WORK/fake-release.flags
  mkdir -p "$REL_FLAGS"
  spawn python3 "$WORK/fake-release.py" "$d" "$REL_PORT" "$WORK/fake-release.log" "$REL_FLAGS"
  wait_for 10 "the fake release to answer" curl -sf -o /dev/null "http://127.0.0.1:$REL_PORT/$SSHN_TAG/manifest.json" || die "the fake release server did not start"
  : >"$WORK/fake-release.log"
}

# edge_telegram_serve: a fake Telegram Bot API on 127.0.0.1 in this namespace (the wasm has its address built in). It answers only
# the fake token; it logs the method of every call to $WORK/tg-calls.txt and every sendMessage as one JSON line {chat_id, text}
# to $WORK/tg-sent.jsonl. getUpdates returns the queued updates with an id >= offset, otherwise [] after a short wait;
# POST /_say {"chat": N, "text": "..."} queues an update from a private chat.
edge_telegram_serve() {
  TG_TOKEN="4242424242:AAE2E$(openssl rand -hex 16)"
  SECRETS+=("$TG_TOKEN")
  TG_BOT_ID=4242424242 TG_BOT_NAME=e2e_alert_bot
  : >"$WORK/tg-calls.txt"
  : >"$WORK/tg-sent.jsonl"
  cat >"$WORK/fake-telegram.py" <<'PY'
import http.server, json, sys, threading, time, urllib.parse

port, token, calls, sent = int(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
lock = threading.Lock()
updates = []
last_id = [1000]


def append(path, line):
    with lock:
        with open(path, "a") as f:
            f.write(line + "\n")


class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # the URL holds the token: log nothing
        pass

    def reply(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        try:
            body = json.loads(self.rfile.read(n) or b"{}")
        except ValueError:
            body = {}
        path = urllib.parse.urlsplit(self.path).path
        if path == "/_say":
            with lock:
                last_id[0] += 1
                updates.append({"update_id": last_id[0], "message": {"message_id": last_id[0], "chat": {"id": body["chat"], "type": "private"}, "text": body["text"]}})
            return self.reply(200, {"ok": True})
        tok, _, method = path[len("/bot"):].partition("/") if path.startswith("/bot") else ("", "", "")
        if tok != token:
            return self.reply(401, {"ok": False, "error_code": 401, "description": "Unauthorized"})
        append(calls, method)
        if method == "getMe":
            return self.reply(200, {"ok": True, "result": {"id": BOT_ID, "is_bot": True, "username": BOT_NAME}})
        if method == "deleteWebhook":
            return self.reply(200, {"ok": True, "result": True})
        if method == "sendMessage":
            append(sent, json.dumps({"chat_id": body.get("chat_id"), "text": body.get("text", "")}))
            return self.reply(200, {"ok": True, "result": {"message_id": 1}})
        if method == "getUpdates":
            offset, deadline = body.get("offset", 0), time.time() + min(body.get("timeout", 0), 2)
            while True:
                with lock:
                    out = [u for u in updates if u["update_id"] >= offset]
                if out or time.time() >= deadline:
                    return self.reply(200, {"ok": True, "result": out})
                time.sleep(0.1)
        return self.reply(404, {"ok": False, "error_code": 404, "description": "Not Found"})


BOT_ID, BOT_NAME = int(sys.argv[5]), sys.argv[6]
http.server.ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
PY
  spawn python3 "$WORK/fake-telegram.py" "$TG_PORT" "$TG_TOKEN" "$WORK/tg-calls.txt" "$WORK/tg-sent.jsonl" "$TG_BOT_ID" "$TG_BOT_NAME" >>"$WORK/fake-telegram.log" 2>&1
  tg_up() { [ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$TG_PORT/bot0/getMe")" = 401 ]; } # it answers, and not to a wrong token
  wait_for 10 "the fake Telegram to answer" tg_up || die "the fake Telegram did not start"
  : >"$WORK/tg-calls.txt"
}

# edge_touch <file under src/>: appends a comment to a source file of the Worker copy; wrangler reloads (the runtime restarts, and
# the isolates and objects with it) and this waits for the reload to be logged.
edge_touch() {
  # (not "n": wait_for has a local of that name, and a function it runs sees that one)
  RELOADS_BEFORE=$(grep -c 'Reloading' "$WORK/wrangler.log" || true)
  echo "// reload $(date +%s)" >>"$WORK/worker/src/$1"
  reloaded() { [ "$(grep -c 'Reloading' "$WORK/wrangler.log" || true)" -gt "$RELOADS_BEFORE" ]; }
  wait_for 60 "wrangler to reload the Workers" reloaded || die "wrangler did not reload after $1 changed"
}

# touching the Provisioner's script (its role entry)
sshn_reload_provisioner() { edge_touch roles/provision.ts; }

# The load of another Durable Object and of stateless requests while a job runs in the Provisioner (design 2.6a: in one
# script the SSH goroutine woke inside a foreign event and the platform refused it): decoy page fetches and admin API
# calls (stateless requests of the main Worker's Go), a cron tick (it kicks the Provisioner too) and an admin fan-out to
# the connected agent, over and over, with the agent's own stats frames arriving at its NodeLink object meanwhile. Kept
# under the public listener's 10 a second (and the subscription's 60 an hour per token, which the earlier steps use up).
# sshn_load_stop fails on any bad answer or a cross-context error in the log.
edge_hammer() {
  local i en=false
  while [ ! -e "$WORK/hammer.stop" ]; do
    for i in 1 2 3 4; do
      ( c=$(edge_get -o /dev/null -w '%{http_code}' "$(edge_url /)" 2>&1) || c=curl-failed; echo "decoy $c" >>"$WORK/hammer.res" ) &
    done
    ( c=$(edge_cron 2>&1) || c=cron-failed; echo "cron $c" >>"$WORK/hammer.res" ) &
    ( api AuthService/Me >/dev/null 2>&1 && echo "admin ok" >>"$WORK/hammer.res" || echo "admin failed" >>"$WORK/hammer.res" ) &
    ( api UserService/SetUsersEnabled "{\"userIds\":[\"$USER_ID\"],\"enabled\":$en}" >/dev/null 2>&1 && echo "fanout ok" >>"$WORK/hammer.res" || echo "fanout failed" >>"$WORK/hammer.res" ) &
    [ "$en" = false ] && en=true || en=false
    wait
    sleep 1
  done
}
sshn_load_start() {
  rm -f "$WORK/hammer.stop"
  : >"$WORK/hammer.res"
  edge_hammer &
  HAMMER_PID=$!
  PIDS+=("$HAMMER_PID")
}
sshn_load_stop() {
  local decoys bad
  : >"$WORK/hammer.stop"
  wait "$HAMMER_PID" || true
  api UserService/SetUsersEnabled "{\"userIds\":[\"$USER_ID\"],\"enabled\":true}" >/dev/null || die "re-enabling alice after the load failed"
  decoys=$(grep -c '^decoy ' "$WORK/hammer.res" || true)
  [ "$decoys" -ge 20 ] || die "the load made only $decoys decoy fetches"
  bad=$(grep -E '^(decoy|cron) ' "$WORK/hammer.res" | grep -vcE '^(decoy|cron) 200$' || true)
  [ "$bad" = 0 ] || die "$bad request(s) of the load failed: $(sort "$WORK/hammer.res" | uniq -c | tr '
' ' ')"
  ! grep -qE '^(admin|fanout) failed' "$WORK/hammer.res" || die "an admin call failed during the load: $(sort "$WORK/hammer.res" | uniq -c | tr '
' ' ')"
  ! grep -q 'Cannot perform I/O on behalf of a different' "$WORK/wrangler.log" || die "the Worker log has a cross-context I/O error ('Cannot perform I/O on behalf of a different ...') after the load"
  ! grep -qiE 'never generate a response|Promise will never complete|hung' "$WORK/wrangler.log" || die "the Worker log reports a hung invocation after the load"
  node_online || die "the primary node went offline during the load"
  SSHN_LOAD_SUMMARY="$decoys decoy fetches, $(grep -c '^cron ' "$WORK/hammer.res" || true) cron ticks (all 200), $(grep -c '^admin ok' "$WORK/hammer.res" || true) admin calls and $(grep -c '^fanout ok' "$WORK/hammer.res" || true) fan-outs to the connected agent, its stats frames meanwhile: no cross-context I/O error, no hung invocation"
  note "load during the job: $SSHN_LOAD_SUMMARY"
}

# ---------------------------------------------------------------- 2e. a node update over the link

# edge_event_id: the id of the newest event of the primary node (0 when none)
edge_event_id() { api FleetService/ListEvents "{\"nodeId\":\"$NODE_ID\",\"limit\":1}" | jq -r '(.events[0].id // "0") | tonumber'; }
edge_node_col() { edge_sql "SELECT $1 FROM node WHERE id = ?" "$NODE_ID"; } # agent_instance_id | agent_built | agent_caps

# The update of the primary agent from the panel's own release, the way an owner does it from the Updates page: the release is
# the fake one (the wasm's releaseDownloadBase points at it, its manifest is signed by the run's key), the panel signs nothing and
# holds no bundle. The cron fires every 5 s instead of every minute. The fake release redirects every download (as GitHub does) and
# cuts the first full download of the agent after 1 MiB, so the agent's resume with a Range header goes through the Worker and the
# redirect. While that first download stalls, the step is SENT and its token is valid: a request with a wrong token must still get
# the decoy's 404 and must not reach the release host.
edge_update_over_link() {
  local rel_file ticks=0 t_start t_cron probed="" code lines0 inst0 eid n cut_at ret
  step "2e. agent update over the link: the panel's own release, through the Worker, with a cut download resumed through a redirect"
  t_start=$(date +%s)
  rel_file=mistgate-node-linux-$(go env GOARCH)
  [ ! -e "$NODE_BIN.prev" ] || die "2e: $NODE_BIN.prev exists before the update"
  cp "$NODE_BIN" "$WORK/node-before-update.bin"
  : >"$REL_FLAGS/redirect"
  : >"$REL_FLAGS/cut"
  node_online || die "2e: the node is not online"

  # the bundle and the node, as the Updates page shows them
  upd | jq -e --arg v "$SSHN_TAG" --argjson b "$REL_BUILT" --arg base "http://127.0.0.1:$REL_PORT/$SSHN_TAG/" \
    '.bundle.status == "BUNDLE_STATUS_TRUSTED" and .bundle.version == $v and (.bundle.built | tonumber) == $b and .bundle.downloadBase == $base and (.bundle.files | length) >= 1' >/dev/null \
    || die "2e: GetUpdates: the release is not a TRUSTED bundle with this run's version, build time and download base: $(upd | jq -c .bundle)"
  upd_node | jq -e --argjson b "$OLD_BUILT" '.supportsUpdate == true and .state == "NODE_UPDATE_STATE_OUTDATED" and (.built | tonumber) == $b' >/dev/null \
    || die "2e: GetUpdates: the node is not OUTDATED at built $OLD_BUILT with update support: $(upd_node)"
  case " $(edge_node_col agent_caps) " in *" update-link/1 "*) ;; *) die "2e: the node's capabilities lack update-link/1: $(edge_node_col agent_caps)" ;; esac
  case " $(edge_node_col agent_caps) " in *" update/1 "*) die "2e: a link-only agent lists update/1: $(edge_node_col agent_caps)" ;; esac
  inst0=$(edge_node_col agent_instance_id)
  [ -n "$inst0" ] || die "2e: the node row has no agent instance id"
  eid=$(edge_event_id)
  lines0=$(wc -l <"$WORK/fake-release.log")

  RID=$(start_rollout "{\"nodeIds\":[\"$NODE_ID\"]}")
  [ -n "$RID" ] && [ "$RID" != null ] || die "2e: StartRollout returned no rollout id"
  say "rollout $RID started; the cron fires every 5 s"
  t_cron=$(date +%s)
  while [ $(($(date +%s) - t_cron)) -lt 240 ]; do
    code=$(edge_cron) || die "2e: a cron tick failed: $code"
    ticks=$((ticks + 1))
    ro_settled && break
    for n in 1 2 3 4 5; do
      if [ -z "$probed" ] && tail -n +"$((lines0 + 1))" "$WORK/fake-release.log" | grep -q '^[0-9]* cut /'; then
        probed=1
        edge_update_probe "$rel_file"
      fi
      sleep 1
    done
  done
  ro_settled || die "2e: the rollout did not settle in 4 minutes ($ticks ticks): $(ro_brief)"
  [ "$(ro_status)" = ROLLOUT_STATUS_DONE ] || die "2e: the rollout ended as $(ro_status): $(ro_brief)"
  [ -n "$probed" ] || die "2e: the fake release never cut a download, so the wrong-token probe and the resume were not exercised: $(tail -n 12 "$WORK/fake-release.log")"
  ro_step | jq -e '.state == "STEP_STATE_PASSED" and ((.errorKey // "") == "")' >/dev/null || die "2e: the step is not PASSED: $(ro_step)"

  # the node: the new build, a new process image (the agent re-executes in place), the old binary kept for a rollback
  node_built_is "$REL_BUILT" || die "2e: GetUpdates shows built $(node_built), want $REL_BUILT"
  [ "$(edge_node_col agent_built)" = "$REL_BUILT" ] || die "2e: node.agent_built is $(edge_node_col agent_built), want $REL_BUILT"
  [ "$(edge_node_col agent_instance_id)" != "$inst0" ] || die "2e: the agent instance id did not change ($inst0): the node was not restarted by the update"
  alive "$NODE_PID" && [ "$(readlink "/proc/$NODE_PID/exe")" = "$NODE_BIN" ] || die "2e: the update did not re-execute pid $NODE_PID in place"
  cmp -s "$NODE_BIN" "$SSHN_NODE_BIN" || die "2e: the installed binary is not the release's"
  cmp -s "$NODE_BIN.prev" "$WORK/node-before-update.bin" || die "2e: $NODE_BIN.prev is not the build before the update"
  wait_for 60 "the agent to commit" test ! -e "$NODE_STATE/update.pending" || die "2e: update.pending never went"
  [ ! -e "$NODE_BIN.new" ] || die "2e: $NODE_BIN.new is left behind"
  node_events_since "$eid" | grep -qx update_committed || die "2e: no update_committed event (events: $(node_events_since "$eid" | sort | uniq -c | tr '\n' ' '))"
  ! node_events_since "$eid" | grep -Eqx 'update_step_failed|update_step_rolled_back' || die "2e: the update failed or rolled back (events: $(node_events_since "$eid" | sort | uniq -c | tr '\n' ' '))"
  wait_for 60 "node ONLINE after the update" node_online || die "2e: the node is not online after the update"
  wait_for 60 "the inbound ACTIVE" inbounds_active 1 || die "2e: the inbound is not ACTIVE after the update"
  [ "$(node_ustate)" = NODE_UPDATE_STATE_UP_TO_DATE ] || die "2e: the node is $(node_ustate), want UP_TO_DATE"

  # the fake release: the cut, then a ranged retry that went through the redirect
  tail -n +"$((lines0 + 1))" "$WORK/fake-release.log" >"$WORK/fake-release.2e"
  cut_at=$(sed -n 's/^[0-9]* cut \/.* after \([0-9]*\) of .*/\1/p' "$WORK/fake-release.2e" | head -1)
  [ -n "$cut_at" ] || die "2e: the fake release logged no cut"
  ret=$(sed -n 's/^[0-9]* "GET \/'"$SSHN_TAG"'\/'"$rel_file"' HTTP[^"]*" 302 - range=bytes=\([0-9]*\)-$/\1/p' "$WORK/fake-release.2e" | head -1)
  [ -n "$ret" ] && [ "$ret" -gt 0 ] && [ "$ret" -le "$cut_at" ] || die "2e: no ranged request for the binary was redirected (302 with Range: bytes=N-, N up to $cut_at): $(grep -c . "$WORK/fake-release.2e") lines: $(tail -n 12 "$WORK/fake-release.2e")"
  grep -q '^[0-9]* "GET /cdn/'"$SSHN_TAG"'/'"$rel_file"' HTTP[^"]*" 206 - range=bytes='"$ret"'-$' "$WORK/fake-release.2e" \
    || die "2e: the redirected retry (Range: bytes=$ret-) did not get 206 from /cdn/: $(tail -n 12 "$WORK/fake-release.2e")"
  ! grep -qE 'authorization=|cookie=' "$WORK/fake-release.2e" || die "2e: the Worker forwarded credentials to the release host: $(grep -E 'authorization=|cookie=' "$WORK/fake-release.2e" | head -3)"
  rm -f "$REL_FLAGS/redirect" "$REL_FLAGS/cut"
  pass "update over the link: StartRollout pinned the TRUSTED release $SSHN_TAG, $ticks cron ticks (every 5 s); the release cut the first download after $cut_at bytes and the agent resumed with Range: bytes=$ret- through the Worker and the redirect (206); step PASSED, rollout DONE, node built $OLD_BUILT -> $REL_BUILT, new instance id, same pid re-executed, .prev kept, update_committed; a wrong token got 404 and never reached the release host; no credentials went upstream ($(($(date +%s) - t_start)) s for 2e)"
}

# edge_update_probe <file>: while the first download stalls (the step is SENT, its token valid), a request with a wrong token, and
# one with none, must get the decoy's 404, and neither may add a line to the fake release's log.
edge_update_probe() {
  local before after c1 c2 url
  url=$(edge_url "/$LINK_SECRET/update/$NODE_ID/$1")
  before=$(wc -l <"$WORK/fake-release.log")
  c1=$(edge_get -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(openssl rand -hex 32)" "$url") || c1=curl-failed
  c2=$(edge_get -o /dev/null -w '%{http_code}' "$url") || c2=curl-failed
  after=$(wc -l <"$WORK/fake-release.log")
  [ "$c1" = 404 ] || die "2e: a download with a wrong token answered HTTP $c1, want 404"
  [ "$c2" = 404 ] || die "2e: a download with no token answered HTTP $c2, want 404"
  [ "$before" = "$after" ] || die "2e: a download with a wrong or missing token reached the release host ($((after - before)) new line(s) in its log): $(tail -n $((after - before)) "$WORK/fake-release.log")"
  say "wrong token: 404; no token: 404; the release host's log did not move ($after lines) while the real download stalled"
}

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
  edge_release_keys
  edge_build
  # The primary agent is a signed build older than the fake release (the run's key, Built an hour back): it lists update-link/1,
  # so step 2e can update it over the link. Its version stays the default.
  go build -trimpath -ldflags "-X $SSHN_BI.Built=$OLD_BUILT -X $SSHN_BI.ReleaseKey=$REL_PUB" -o "$BIN/mistgate-node" ./cmd/mistgate-node
  NODE_BIN=$BIN/mistgate-node
  build_hysteria
  edge_release_serve
  edge_telegram_serve
  pass "built the fake Telegram (its address is built into the four roles that can send) and the five role wasm files ($((WASM_BYTES / 1048576)) MB, $((WASM_GZ / 1048576)) MB gzipped; tag $SSHN_TAG, a release key and the fake release's address built in), the Worker copy (installed in the work directory, zero D1 id), the node agent, the signed fake release of the agent ($(($(stat -c %s "$SSHN_NODE_BIN") / 1048576)) MB) and the hysteria client"

  step "1. wrangler dev: the Worker answers on $EDGE_HOST:$PUB, first admin from the setup token given as SETUP_TOKEN_SHA256 (no link in the log)"
  edge_start_worker
  PANEL_PID=$(edge_workerd_pid)
  # the link audience: the agent requires challenge.audience == the host of its link URL; the Worker takes it from the
  # request URL. A wrangler that rewrote the host would show here as a node that never comes online (step 2).
  CODE=$(edge_get -o "$WORK/root.html" -w '%{http_code}' "$(edge_url /)") || die "the Worker did not answer"
  [ "$CODE" = 200 ] && grep -q 'Coming soon' "$WORK/root.html" || die "GET / is not the decoy page (HTTP $CODE)"
  wait_for 30 "the setup token expiry in the Worker log" grep -q 'setup token of SETUP_TOKEN_SHA256 is accepted until' "$WORK/wrangler.log" \
    || die "the Worker did not offer the setup token (no expiry line in its log)"
  ! grep -q '/setup#' "$WORK/wrangler.log" || die "the Worker printed a setup link although SETUP_TOKEN_SHA256 is set"
  ADMIN=https://$EDGE_HOST:$PUB/$ADMIN_SECRET
  # the setup link a deploy tool builds itself is $ADMIN/setup#<token>; the token it chose is the credential, a stranger's is not
  ! api AuthService/BeginSetup "$(jq -nc --arg t "$(openssl rand -hex 32)" '{setupToken:$t, displayName:"E2E admin", method:"SETUP_METHOD_PASSWORD", login:"stranger", password:"stranger-password-1"}')" >/dev/null 2>&1 \
    || die "BeginSetup accepted a token nobody offered"
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
  pass "the Worker serves the decoy at /, logs only the setup token's expiry (no link, no token), the token chosen by the deployer creates the first admin once, a token nobody offered is refused, the session works (admin under the secret prefix)"

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

  step "2a. node journal over the link: a profile on a busy port fails to start, and the journal shows why without an address"
  journal_check 1

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

  # A source file of PUBLIC only is touched first (design/cloudflare-edition: a release that changes one role must not make the
  # agents reconnect). Outside Cloudflare, wrangler dev reloads its one workerd for any script, so this is a measurement, not a
  # check: it says whether the agent reconnected.
  GEN_PUBLIC=$(edge_session)
  edge_touch roles/public.ts
  public_reconnected() { [ "$(edge_session)" != "$GEN_PUBLIC" ]; }
  if wait_for 40 "the agent to reconnect after PUBLIC alone was reloaded (expected not to, on Cloudflare)" public_reconnected; then
    wait_for 60 "node ONLINE after the PUBLIC reload" node_online || die "node not ONLINE after the PUBLIC script reload"
    PUBLIC_RELOAD_NOTE="wrangler dev restarts its whole runtime for a change in PUBLIC alone: the agent reconnected (session $GEN_PUBLIC -> $(edge_session)); on Cloudflare only that script is replaced"
  else
    node_online || die "node not ONLINE after the PUBLIC script reload"
    PUBLIC_RELOAD_NOTE="a reload of PUBLIC alone left the agent connected (session $GEN_PUBLIC)"
  fi
  note "$PUBLIC_RELOAD_NOTE"
  GEN2=$(edge_session)

  # touching FRONT's source makes wrangler reload: the runtime restarts, the NodeLink object (FRONT's) is reset, the agent reconnects
  LOG_MARK=$(stat -c %s "$WORK/node.log")
  GEN_BEFORE=$GEN2
  edge_touch front.ts
  gen_after() { [ "$(edge_session)" = "$((GEN_BEFORE + 1))" ]; }
  wait_for 120 "the agent to reconnect as generation $((GEN_BEFORE + 1))" gen_after || die "after the reload node_live shows session '$(edge_session)', want $((GEN_BEFORE + 1))"
  wait_for 60 "node ONLINE after the reload" node_online || die "node not ONLINE after the Worker reload"
  grep -q 'Reloading local server\|Reloading' "$WORK/wrangler.log" || note "wrangler did not log a reload (the generation changed all the same)"
  ! new_log | grep -q 'desired state applied.*full=true' || die "after the reload the agent was sent a full desired state"
  inbounds_active 1 >/dev/null || wait_for 60 "the inbound ACTIVE after the reload" inbounds_active 1 || die "inbound not ACTIVE after the reload"
  PANEL_PID=$(edge_workerd_pid)
  pass "Worker source touched: wrangler reloaded, the agent reconnected as generation $((GEN_BEFORE + 1)), still no full DesiredState, inbound ACTIVE"

  step "2d. SSH wizard: the Worker reaches a real OpenSSH through the Provisioner object and connect()"
  sshn_up
  sshn_wizard

  edge_update_over_link

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

  step "3b. Telegram: bot, linking a chat through the cron's poll, no poll once the code is spent, the test message, a security notice without a cron"
  # The wasm talks to the fake Telegram (edge_telegram_serve): $TG_TOKEN is the only token it accepts. Nothing reaches api.telegram.org.
  TG_CHAT=7100200300
  tg_calls() { grep -c "^$1\$" "$WORK/tg-calls.txt" || true; }
  tg_sent() { wc -l <"$WORK/tg-sent.jsonl"; }
  mutate TelegramService/SetTelegramBot "$(jq -nc --arg t "$TG_TOKEN" '{token:$t}')" \
    | jq -e --arg u "$TG_BOT_NAME" '.status.bot.username == $u' >/dev/null || die "SetTelegramBot with the fake token did not report the fake bot"
  [ "$(tg_calls getMe)" -ge 1 ] || die "SetTelegramBot never asked the fake Telegram (getMe): is the API base built in?"
  TG_LINK=$(mutate TelegramService/BeginTelegramLink '{}') || die "BeginTelegramLink failed"
  TG_CODE=$(jq -r .code <<<"$TG_LINK")
  [ ${#TG_CODE} = 12 ] || die "BeginTelegramLink gave a code of ${#TG_CODE} characters, want 12"
  SECRETS+=("$TG_CODE")
  curl -sS -m 5 -X POST "http://127.0.0.1:$TG_PORT/_say" -d "$(jq -nc --argjson c "$TG_CHAT" --arg t "/start $TG_CODE" '{chat:$c, text:$t}')" >/dev/null \
    || die "queueing the /start update in the fake Telegram failed"
  CODE=$(edge_cron) || die "the cron tick that polls Telegram failed: $CODE"
  tg_linked() { api TelegramService/GetTelegram '{}' | jq -e --argjson c "$TG_CHAT" '(.status.mine.chatId | tonumber) == $c and (.status.links | length) == 1'; }
  wait_for 30 "GetTelegram to list the chat the cron's poll linked" tg_linked \
    || die "after the cron tick GetTelegram shows no link: $(api TelegramService/GetTelegram '{}' | jq -c '.status | {mine, links}')"
  jq -e --argjson c "$TG_CHAT" 'select(.chat_id == $c)' "$WORK/tg-sent.jsonl" >/dev/null || die "the fake Telegram got no reply in the linked chat"
  POLLS=$(tg_calls getUpdates)
  [ "$POLLS" -ge 1 ] || die "the cron tick did not poll Telegram"
  CODE=$(edge_cron) || die "the second cron tick failed: $CODE"
  [ "$(tg_calls getUpdates)" = "$POLLS" ] || die "the second cron tick polled Telegram again ($POLLS -> $(tg_calls getUpdates) getUpdates) although the code was spent"
  SENT0=$(tg_sent)
  api TelegramService/SendTelegramTest '{}' >/dev/null || die "SendTelegramTest failed"
  [ "$(($(tg_sent) - SENT0))" = 1 ] || die "SendTelegramTest sent $(($(tg_sent) - SENT0)) messages, want exactly one"
  SENT0=$(tg_sent)
  # a sign-in from an address this isolate has not seen: the notice is flushed after the response that caused it, with no cron tick
  while TG_STEP=$(($(date +%s) / 30)); [ "$TG_STEP" -le "$TOTP_LAST" ]; do sleep 1; done
  TG_CODE_OTP=$(totp_code)
  TG_HTTP=$(edge_get -o /dev/null -w '%{http_code}' -X POST "$ADMIN/api/mistgate.admin.v1.AuthService/PasswordLogin" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg l "$ADMIN_LOGIN" --arg p "$ADMIN_PASSWORD" --arg c "$TG_CODE_OTP" '{login:$l, password:$p, totpCode:$c}')") || TG_HTTP=curl-failed
  TOTP_LAST=$TG_STEP
  [ "$TG_HTTP" = 200 ] || die "the password sign-in answered HTTP $TG_HTTP"
  signin_notice() { tail -n +"$((SENT0 + 1))" "$WORK/tg-sent.jsonl" | jq -e --argjson c "$TG_CHAT" 'select(.chat_id == $c and (.text | contains("E2E admin")))' >/dev/null; }
  wait_for 20 "the sign-in notice, with no cron tick" signin_notice || die "no sign-in notice reached the fake Telegram within 20 s of a fresh sign-in ($(($(tg_sent) - SENT0)) new messages)"
  pass "Telegram: the fake bot is accepted; the cron tick heard /start <code> (chat linked, GetTelegram lists it, the fake got the reply); a second tick made no getUpdates call; SendTelegramTest sent exactly one message; a fresh password sign-in sent its notice without any cron tick"

  if [ -z "${SSH_ONLY:-}" ]; then # ---- the traffic steps 4-8 (--ssh-only skips them)
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
  PEAK_WASM_MIX="public $(edge_mem public), admin $(edge_mem admin), link $(edge_mem link)"
  pass "20 parallel subscription fetches (all 200), a cron tick (200) and two admin fan-outs while the agent sent stats frames: no hung invocation, no cross-request I/O error in the Worker log; wasm memory (MB) $PEAK_WASM_MIX"

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

  fi # ---- end of steps 4-8

  step "8b. installs under load: cancel in the middle of the upload; retire while the install waits for the agent (the primary node is online and busy)"
  sshn_scn_cancel sshn-cancel
  sshn_scn_retire_waiting sshn-wait

  step "9. measurements of this run (numbers, not pass/fail)"
  RSS_WORKERD=$(edge_workerd_rss)
  WASM_MB="public $(edge_mem public), admin $(edge_mem admin), link $(edge_mem link)"
  while read -r role bytes; do
    printf '    %-52s %s\n' "$role.wasm" "$((bytes / 1048576)) MB"
  done <"$WORK/wasm-sizes"
  printf '    %-52s %s\n' "five wasm files together" "$((WASM_BYTES / 1048576)) MB ($((WASM_GZ / 1048576)) MB gzip -9)"
  printf '    %-52s %s\n' "wasm linear memory after the e2e mix (limit 128 MB each)" "$WASM_MB"
  printf '    %-52s %s\n' "workerd RSS at the end (whole runtime, all isolates)" "$RSS_WORKERD MB"
  printf '    %-52s %s\n' "node agent: offline after stop" "${OFF_AFTER} s"
  note "measured: wasm file $WASM_BYTES bytes ($WASM_GZ gzip), wasm memory (MB) ${WASM_MB}, workerd RSS ${RSS_WORKERD} MB"

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

  step "10b. installs once the primary node is gone: a truncated upload; a full install; a full install with the Provisioner reloaded in the middle of the upload"
  sshn_scn_truncated sshn-trunc
  sshn_scn_full sshn-full
  sshn_scn_full sshn-reload reload
  sshn_scan_state "$WORK/wrangler-state"
  pass "the stored state (D1 and the Durable Object files) holds no plaintext SSH password"
  FETCHES=$(grep -c "GET /$SSHN_TAG/mistgate-node-linux-" "$WORK/fake-release.log" || true)
  MANIFESTS=$(grep -c "GET /$SSHN_TAG/manifest.json" "$WORK/fake-release.log" || true)
  [ "$FETCHES" -ge 6 ] && [ "$MANIFESTS" -ge 6 ] || die "the fake release saw $MANIFESTS manifest and $FETCHES binary requests, want one of each per install attempt (at least 6)"
  pass "the agent came from the fake release every time ($MANIFESTS manifests, $FETCHES binary downloads): -X update.releaseDownloadBase and the release key built into the wasm are what the Provisioner used"

  step "11. secrets never reach the logs (Worker log without wrangler's own request lines; wrangler's file log; node and client logs)"
  LEAKS=0
  edge_worker_log >"$WORK/wrangler-worker-output.txt"
  cat "$WORK"/wrangler-logs/* 2>/dev/null | grep -vE '^\[wrangler:info\] [A-Z]+ ' >"$WORK/wrangler-file-log.txt" || true
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
  # the download token of step 2e travels as an Authorization header only: no log of the run (the Worker's, wrangler's, the agent's,
  # the fake release's with the headers it received) may hold a Bearer value or an Authorization header
  CREDS=0
  for f in "$WORK"/*.log "$WORK/wrangler-worker-output.txt" "$WORK/wrangler-file-log.txt"; do
    [ -f "$f" ] || continue
    if grep -qiE '\bbearer\b|authorization[:=]' "$f"; then
      CREDS=$((CREDS + 1))
      say "a Bearer or Authorization value in: $(basename "$f")"
    fi
  done
  [ "$CREDS" = 0 ] || die "$CREDS log(s) hold a Bearer or Authorization value"
  ! grep -q 'Cannot perform I/O on behalf of a different request' "$WORK/wrangler.log" "$WORK/wrangler-file-log.txt"     || die "the Worker log has 'Cannot perform I/O on behalf of a different request'"
  # wrangler's dev server prints the path of every request: the secret prefixes are in those lines, by its design
  PATHS=$(grep -cE '^\[wrangler:info\] [A-Z]+ ' "$WORK/wrangler.log" || true)
  pass "no secret (admin password, TOTP, cookie, master key, enrolment token, subscription token, hysteria credentials, the SSH passwords and the release key of the SSH installs) in the Worker's output, wrangler's file log, or the sshd, installed agent, fake release, node and client logs; wrangler's own request lines ($PATHS) print URL paths and are excluded; no log holds a Bearer value or an Authorization header (the update token of step 2e travelled in one)"
  ! grep -qE 'Error|error' "$WORK/wrangler-worker-output.txt" || note "the Worker log has error lines: $(grep -E 'Error|error' "$WORK/wrangler-worker-output.txt" | sort | uniq -c | sort -rn | head -5 | tr '\n' ';')"
}
