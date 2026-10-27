#!/usr/bin/env bash
# Builds every edge entry for js/wasm the way the release does, prints its size and fails when one is over its limit or
# imports a template engine. CI runs it; run it before and after a change that adds a dependency to a role.
#
#   scripts/check-edge-sizes.sh [output dir]
#
# The hard ceiling is Cloudflare's 64 MiB per Worker (design/cloudflare-edition/SPIKE.md). A limit is the size measured when it
# was set plus about 3 MB, in whole MiB. Raising one needs a commit that names the feature and says why it cannot live in
# another Worker; the point of the split is that a feature goes to the Worker that needs it.
set -euo pipefail
cd "$(dirname "$0")/.."

out="${1:-$(mktemp -d)}"
mkdir -p "$out"

# entry:limit in MiB
entries=(
  # The program with every role: no longer in the release bundle (the five entries below are), it is what the Node tests and the e2e
  # bridge run, and the size the roles split. 42 MiB: about 3 MB above its size after the October 2026 diet, so a regression like
  # the 7.9 MB template one fails here. 45 MiB since the external reachability sources (cheburcheck, bsbord): every role's code.
  "mistgate-edge:45"
  # Hot path: subscriptions, the person's page, devices. Measured 24.7 MiB (fleet + access + subs, no auth service).
  "mistgate-edge-public:28"
  # The agent link: wireCore plus the link driver. Measured 26.4 MiB.
  "mistgate-edge-link:30"
  # Scheduled work: wireCore, so Telegram, updates and WARP are in it for the work that will move here. Measured 25.1 MiB.
  "mistgate-edge-jobs:28"
  # The admin API and the SPA: the largest role, every admin service. Measured 37.6 MiB; 40.1 MiB with the external reachability
  # sources (cheburcheck, bsbord), whose owner RPCs (settings, sealed keys, the paid-check preview) need the admin's step-up and
  # cannot live in jobs, where their ticks run.
  "mistgate-edge-admin:43"
  # The Provisioner: wireCore plus the SSH client. Measured 29.4 MiB.
  "mistgate-edge-provision:32"
)

status=0
for entry in "${entries[@]}"; do
  name="${entry%%:*}"
  limit=$(( ${entry##*:} * 1048576 ))
  wasm="$out/$name.wasm"
  GOOS=js GOARCH=wasm go build -tags nethttpomithttp2 -ldflags="-s -w" -trimpath -o "$wasm" "./cmd/$name"
  bytes=$(stat -c%s "$wasm")
  printf '%-26s %9d bytes  %5.1f MiB  limit %9d  headroom %8d bytes (%d KiB)\n' \
    "$name" "$bytes" "$(echo "$bytes" | awk '{print $1/1048576}')" "$limit" $((limit - bytes)) $(( (limit - bytes) / 1024 ))
  if [ "$bytes" -gt "$limit" ]; then
    echo "::error::$name.wasm is $bytes bytes, over its limit of $limit"
    status=1
  fi
  # text/template looks methods up by name, which makes the linker keep every exported method of every type (7.9 MB of the
  # wasm). The install page is Go code (internal/panel/provision/ui_page.go) for that reason.
  if GOOS=js GOARCH=wasm go list -deps "./cmd/$name" | grep -Ex 'text/template|html/template'; then
    echo "::error::cmd/$name imports a template engine; see the comment above this check"
    status=1
  fi
done
exit $status
