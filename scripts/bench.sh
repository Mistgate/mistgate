#!/usr/bin/env bash
# The engine benchmark suite (internal/node/bench): Hysteria2, VLESS (REALITY over TCP and over XHTTP) and AmneziaWG between
# network namespaces, with netem variants, compared with internal/node/bench/baseline.json. Runs as root on Linux; started from
# Windows (Git Bash, PowerShell) or macOS it runs itself in WSL as root. Read docs/en/reference/benchmarks.md first.
#
#   scripts/bench.sh [options]
#     --engines  hysteria2,vless-tcp,vless-xhttp,awg   (default: all)
#     --variants clean,rtt50-loss1,mtu1280             (default: all)
#     --seconds  N         length of each throughput phase, default 4
#     --repeat   N         runs per case, the median is reported (default 1; use 3 for a baseline)
#     --threshold P        how much worse than the baseline fails, "15", "15%" or "0.15" (default 15%)
#     --out FILE           where the JSON report goes (default: bench-report.json in the repository root)
#     --update             write the report into the baseline instead of comparing with it
#     --list               print what would run and exit
#
# Isolation: every namespace is named mg3-awg-b-* and the engines run inside them; the host's own interfaces, routes and nft
# tables are never touched. Run it on a throwaway machine or in WSL, never on a fleet host. It needs go, ip (iproute2), tc (the
# netem variants) and nft (AmneziaWG); a missing tool skips what needs it with a message. WSL_DISTRO picks the distribution
# (default Ubuntu).
set -euo pipefail
cd "$(dirname "$0")/.."

engines="" variants="" seconds="" repeat="" threshold="" out="" update="" list=""
args=("$@")
while [ $# -gt 0 ]; do
  case $1 in
    --engines) engines=${2:?--engines needs a list}; shift ;;
    --variants) variants=${2:?--variants needs a list}; shift ;;
    --seconds) seconds=${2:?--seconds needs a number}; shift ;;
    --repeat) repeat=${2:?--repeat needs a number}; shift ;;
    --threshold) threshold=${2:?--threshold needs a value}; shift ;;
    --out) out=${2:?--out needs a file}; shift ;;
    --update) update=1 ;;
    --list) list=1 ;;
    -h|--help) sed -n "2,21p" "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

# Not Linux, or not root: run in WSL as root, from the same checkout.
if [ "$(uname -s)" != Linux ] || [ "$(id -u)" != 0 ]; then
  if ! command -v wsl.exe >/dev/null 2>&1; then
    echo "scripts/bench.sh needs root on Linux (or WSL on Windows: wsl.exe was not found)" >&2
    exit 2
  fi
  distro=${WSL_DISTRO:-Ubuntu}
  here=$(pwd -W 2>/dev/null || pwd)
  repo=$(MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -u root wslpath -a "$here" | tr -d '\r')
  MSYS_NO_PATHCONV=1 exec wsl.exe -d "$distro" -u root -- bash "$repo/scripts/bench.sh" "${args[@]}"
fi

for tool in go ip; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done
for tool in tc nft; do
  command -v "$tool" >/dev/null || echo "note: $tool is missing, what needs it will be skipped" >&2
done
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto}
export MISTGATE_BENCH=1
[ -z "$engines" ] || export MISTGATE_BENCH_ENGINES=$engines
[ -z "$variants" ] || export MISTGATE_BENCH_VARIANTS=$variants
[ -z "$seconds" ] || export MISTGATE_BENCH_SECONDS=$seconds
[ -z "$repeat" ] || export MISTGATE_BENCH_REPEAT=$repeat
[ -z "$threshold" ] || export MISTGATE_BENCH_THRESHOLD=$threshold
[ -z "$update" ] || export MISTGATE_BENCH_UPDATE=1
export MISTGATE_BENCH_JSON=${out:-$(pwd)/bench-report.json}
export MISTGATE_BENCH_BASELINE=$(pwd)/internal/node/bench/baseline.json

if [ -n "$list" ]; then
  echo "engines:  ${engines:-hysteria2,vless-tcp,vless-xhttp,awg}"
  echo "variants: ${variants:-clean,rtt50-loss1,mtu1280}"
  echo "report:   $MISTGATE_BENCH_JSON"
  echo "baseline: $MISTGATE_BENCH_BASELINE"
  exit 0
fi

# the test binary is built once and run from a temporary directory: the helpers are copies of it, started inside the namespaces
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go test -c -tags bench -o "$tmp/bench.test" ./internal/node/bench
cd "$tmp"
"$tmp/bench.test" -test.run '^TestBench$' -test.v -test.count=1 -test.timeout 0
