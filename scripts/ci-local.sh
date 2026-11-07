#!/usr/bin/env bash
# Runs on this machine what .github/workflows/ci.yml runs, job by job, and prints one line per check: a change is ready
# to push when every line says ok. Picking checks by hand missed real failures before (a js/wasm build narrower than
# CI's, an edge script not run), so this follows the workflow, not memory.
#
#   scripts/ci-local.sh            every job except race (slow)
#   scripts/ci-local.sh --race     with the race job (fleet and agent under -race, 20-35 min)
#   scripts/ci-local.sh go web     only the named jobs: go web edge buf docs watcher edge-worker race
#
# On Windows run it from Git Bash; the edge jobs call PowerShell (pwsh if present, else Windows PowerShell). shellcheck
# is skipped when it is not installed (CI has it).
set -uo pipefail
cd "$(dirname "$0")/.."

jobs=(buf go web edge docs watcher edge-worker)
race=0
want=()
for a in "$@"; do
  case "$a" in
    --race) race=1 ;;
    *) want+=("$a") ;;
  esac
done
[ ${#want[@]} -gt 0 ] && jobs=("${want[@]}")
[ $race -eq 1 ] && jobs+=(race)

ps() { if command -v pwsh >/dev/null 2>&1; then pwsh -NoProfile -File "$@"; else powershell.exe -NoProfile -File "$@"; fi; }

results=()
step() { # name, command...
  local name="$1"; shift
  local log; log=$(mktemp)
  local t0=$SECONDS
  if "$@" >"$log" 2>&1; then
    results+=("ok    $name ($((SECONDS - t0))s)")
  else
    results+=("FAIL  $name ($((SECONDS - t0))s): see $log")
    echo "---- $name failed; last lines:" >&2
    tail -n 25 "$log" >&2
  fi
}

for job in "${jobs[@]}"; do
  case "$job" in
    buf)
      step "buf lint" go tool buf lint
      step "buf generate leaves no diff" bash -c 'go tool buf generate && git diff --quiet -- gen web/src/gen' ;;
    go)
      step "web build (embedded)" bash -c 'cd web && pnpm install --frozen-lockfile >/dev/null && pnpm build'
      step "go vet" go vet ./...
      step "go test" go test ./...
      if command -v shellcheck >/dev/null 2>&1; then step "shellcheck install.sh" shellcheck scripts/install.sh; fi
      step "go test scripts" go test -count=1 ./scripts
      step "go build" go build ./... ;;
    web)
      step "web typecheck" bash -c 'cd web && pnpm typecheck'
      step "web lint" bash -c 'cd web && pnpm lint'
      step "web test" bash -c 'cd web && pnpm test'
      step "web build" bash -c 'cd web && pnpm build' ;;
    edge)
      step "js/wasm build (CI scope)" env GOOS=js GOARCH=wasm go build ./internal/panel/... ./cmd/mistgate/ ./cmd/mistgate-edge... ./edge/...
      step "edge sizes" bash scripts/check-edge-sizes.sh "$(mktemp -d)"
      step "edge store" ps scripts/test-edge-store.ps1
      step "edge entry" ps scripts/test-edge-entry.ps1
      step "edge roles" ps scripts/test-edge-roles.ps1 ;;
    docs)
      step "site build" bash -c 'pnpm --dir site install --frozen-lockfile >/dev/null && pnpm --dir site build'
      step "site test" pnpm --dir site test ;;
    watcher)
      step "watcher typecheck+test" bash -c 'cd deploy/cloudflare-watcher && pnpm install --frozen-lockfile >/dev/null && pnpm typecheck && pnpm test' ;;
    edge-worker)
      step "edge worker typecheck+test" bash -c 'cd edge/worker && pnpm install --frozen-lockfile >/dev/null && pnpm typecheck && pnpm test' ;;
    race)
      step "race (fleet, agent)" go test -race -count=1 -timeout 50m ./internal/panel/fleet/ ./internal/node/agent/ ;;
    *) results+=("FAIL  unknown job $job") ;;
  esac
done

echo
printf '%s\n' "${results[@]}"
for r in "${results[@]}"; do [[ $r == FAIL* ]] && exit 1; done
exit 0
