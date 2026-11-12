#!/usr/bin/env bash
# Memory of the node's VLESS engine on two cores, side by side: a checkout of main (Xray 26.3.27) and this one. Not part of the e2e
# suite (it takes ~20 minutes); run it when the core changes.
#
#   bash scripts/e2e/memprofile.sh [--idle 10m] [--runs 3] <checkout of main>
#
# Each side builds internal/node/vless/memprofile_test.go (build tag memprofile; the script copies it into the other checkout for
# the run and removes it again) and runs it <runs> times in parallel: a REALITY RAW+Vision inbound with 90 users, a matrix of 85
# external Xray clients (the same binary on both sides) three times in a row, 500 half-sent ClientHellos held and released, and the
# idle time. It prints the mean of the runs per phase: live heap after a GC (KiB), bytes the runtime holds from the OS (KiB),
# goroutines, resident set size (KiB). Everything is on loopback, in this process; no container, namespace or service is touched,
# and only the processes the script started are stopped.
set -Eeuo pipefail

IDLE=10m RUNS=3
while [ $# -gt 1 ]; do
  case $1 in
    --idle) IDLE=$2; shift 2 ;;
    --runs) RUNS=$2; shift 2 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done
BASE=${1:?usage: memprofile.sh [--idle 10m] [--runs 3] <checkout of main>}
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
BASE=$(cd "$BASE" && pwd)
TEST=internal/node/vless/memprofile_test.go
WORK=$(mktemp -d /tmp/mg-mem.XXXXXX)
export GOFLAGS=-buildvcs=false GOTOOLCHAIN=${GOTOOLCHAIN:-auto} CGO_ENABLED=0
PIDS=()
cleanup() {
  local rc=$? p
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  [ "$BASE" = "$HERE" ] || rm -f "$BASE/$TEST"
  rm -rf "$WORK"
  exit "$rc"
}
trap cleanup EXIT

# the Xray client that plays the apps: 26.3.27, built from the module source, cached with the e2e clients
CACHE=${MG3_TEST_CACHE:-$HOME/.cache/mistgate-tests}
XRAY=$CACHE/xray-v1.260327.0
if [ ! -x "$XRAY" ]; then
  mkdir -p "$CACHE" "$WORK/xb"
  (cd "$WORK" && GOBIN=$WORK/xb GOFLAGS=-mod=mod go install github.com/xtls/xray-core/main@v1.260327.0) && mv "$WORK/xb/main" "$XRAY"
fi

cp "$HERE/$TEST" "$BASE/$TEST"
for side in main branch; do
  dir=$BASE
  [ "$side" = branch ] && dir=$HERE
  (cd "$dir" && go test -p 1 -tags memprofile -c -o "$WORK/vless-$side.test" ./internal/node/vless/) || { echo "cannot build the $side test binary" >&2; exit 1; }
  echo "$side: $(cd "$dir" && git log --oneline -1) $(cd "$dir" && grep -m1 'xtls/xray-core ' go.mod | awk '{print $2}')"
done

: >"$WORK/rows.tsv"
for side in main branch; do
  dir=$BASE
  [ "$side" = branch ] && dir=$HERE
  for ((r = 1; r <= RUNS; r++)); do
    (cd "$dir/internal/node/vless" && MEMPROFILE_XRAY=$XRAY MEMPROFILE_LABEL="$side/$r" MEMPROFILE_IDLE=$IDLE MEMPROFILE_OUT="$WORK/rows.tsv" \
      "$WORK/vless-$side.test" -test.run TestMemProfile -test.timeout 90m >"$WORK/run-$side-$r.log" 2>&1) &
    PIDS+=("$!")
  done
done
for p in "${PIDS[@]}"; do wait "$p" || { echo "a run failed; logs:" >&2; tail -n 20 "$WORK"/run-*.log >&2; exit 1; }; done
PIDS=()

python3 - "$WORK/rows.tsv" <<'PY'
import collections, sys
rows = collections.defaultdict(list)
order = []
for line in open(sys.argv[1]):
    label, phase, heap, sysm, gor, rss = line.rstrip("\n").split("\t")
    side = label.split("/")[0]
    if phase not in order:
        order.append(phase)
    rows[(side, phase)].append((int(heap), int(sysm), int(gor), int(rss)))
def mean(side, phase, i):
    v = [r[i] for r in rows[(side, phase)]]
    return sum(v) / len(v) if v else float("nan")
print("%-10s | %-24s | %-24s | %-18s | %-24s" % ("phase", "live heap KiB main/branch", "Sys KiB main/branch", "goroutines", "RSS KiB main/branch"))
for ph in order:
    cells = []
    for i in range(4):
        a, b = mean("main", ph, i), mean("branch", ph, i)
        cells.append("%8.0f /%8.0f %+5.1f%%" % (a, b, (b - a) / a * 100) if a == a and a else "n/a")
    print("%-10s | %s | %s | %s | %s" % (ph, cells[0], cells[1], cells[2], cells[3]))
print()
print("per run (RSS KiB):")
for side in ("main", "branch"):
    for ph in order:
        print("  %-6s %-9s %s" % (side, ph, " ".join(str(r[3]) for r in rows[(side, ph)])))
PY
