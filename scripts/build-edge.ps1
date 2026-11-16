# Builds everything the edge Worker needs in edge/worker/dist and web/dist:
#   1. the admin SPA and the user page (pnpm --dir web build), served to the panel by the Worker's static assets;
#   2. the panel for js/wasm -> edge/worker/dist/panel.wasm, and the five role entries of the split edition
#      (cmd/mistgate-edge-{public,link,jobs,admin,provision}) -> dist/<role>.wasm;
#   3. wasm_exec.js from the Go toolchain that built the wasm (the two must match) -> edge/worker/dist/wasm_exec.js;
#   4. edge/worker/wrangler.<role>.dev.toml for the six scripts (front, link, public, admin, jobs, provision; git-ignored): the
#      configs rendered from release.NewEdgeConfig (scripts/edgeconfig) with a dummy D1 id, for `wrangler dev --local` with all
#      six (pnpm dev; FRONT is the first and listens).
#      dist/panel.wasm is the all-roles build of the Node tests and the e2e bridge, not of a Worker of the release.
# wasm_exec.js is a generated copy, so dist/ is git-ignored and this script is the only way it is produced.
param([switch]$SkipWeb)
$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$worker = Join-Path $repoRoot "edge/worker"
$dist = Join-Path $worker "dist"

function Invoke-Native([string]$What, [scriptblock]$Run) {
    & $Run
    if ($LASTEXITCODE -ne 0) { throw "$What failed with exit code $LASTEXITCODE." }
}

if (-not $SkipWeb) {
    Invoke-Native "pnpm install (web)" { pnpm --dir (Join-Path $repoRoot "web") install --frozen-lockfile }
    Invoke-Native "pnpm build (web)" { pnpm --dir (Join-Path $repoRoot "web") build }
}

New-Item -ItemType Directory -Force -Path $dist | Out-Null
$wasm = Join-Path $dist "panel.wasm"
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
Push-Location $repoRoot
try {
    $env:GOOS = "js"
    $env:GOARCH = "wasm"
    Invoke-Native "go build (js/wasm)" { go build -tags nethttpomithttp2 -ldflags="-s -w" -trimpath -o $wasm ./cmd/mistgate-edge }
    # The split edition's Workers (edge/worker/src/roles/*.ts) import dist/<role>.wasm.
    foreach ($role in "public", "link", "jobs", "admin", "provision") {
        Invoke-Native "go build $role (js/wasm)" { go build -tags nethttpomithttp2 -ldflags="-s -w" -trimpath -o (Join-Path $dist "$role.wasm") "./cmd/mistgate-edge-$role" }
    }
} finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    Pop-Location
}

$goRoot = (& go env GOROOT).Trim()
if ($LASTEXITCODE -ne 0) { throw "go env GOROOT failed." }
Copy-Item -LiteralPath (Join-Path $goRoot "lib/wasm/wasm_exec.js") -Destination (Join-Path $dist "wasm_exec.js") -Force

Push-Location $repoRoot
try {
    Invoke-Native "edge dev configs" { go run ./scripts/edgeconfig -out $worker -dev }
} finally {
    Pop-Location
}

$bytes = (Get-Item -LiteralPath $wasm).Length
"edge wasm: {0} bytes ({1:N1} MiB, limit in CI 44040192, headroom {2} bytes = {3:N0} KiB)" -f $bytes, ($bytes / 1MB), (44040192 - $bytes), ((44040192 - $bytes) / 1KB)
foreach ($role in "public", "link", "jobs", "admin", "provision") {
    "edge {0}.wasm: {1} bytes" -f $role, (Get-Item -LiteralPath (Join-Path $dist "$role.wasm")).Length
}
