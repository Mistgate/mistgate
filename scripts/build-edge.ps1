# Builds everything the edge Worker needs in edge/worker/dist and web/dist:
#   1. the admin SPA and the user page (pnpm --dir web build), served to the panel by the Worker's static assets;
#   2. the panel for js/wasm -> edge/worker/dist/panel.wasm;
#   3. wasm_exec.js from the Go toolchain that built the wasm (the two must match) -> edge/worker/dist/wasm_exec.js;
#   4. edge/worker/wrangler.dev.toml (git-ignored): wrangler.example.toml with a dummy D1 id, for `wrangler dev --local`.
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
    Invoke-Native "go build (js/wasm)" { go build -ldflags="-s -w" -trimpath -o $wasm ./cmd/mistgate-edge }
} finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    Pop-Location
}

$goRoot = (& go env GOROOT).Trim()
if ($LASTEXITCODE -ne 0) { throw "go env GOROOT failed." }
Copy-Item -LiteralPath (Join-Path $goRoot "lib/wasm/wasm_exec.js") -Destination (Join-Path $dist "wasm_exec.js") -Force

$template = Get-Content -LiteralPath (Join-Path $worker "wrangler.example.toml") -Raw
$local = $template -replace 'replace-with-your-d1-database-id', '00000000-0000-0000-0000-000000000000'
[IO.File]::WriteAllText((Join-Path $worker "wrangler.dev.toml"), $local, (New-Object Text.UTF8Encoding $false))

$bytes = (Get-Item -LiteralPath $wasm).Length
"edge wasm: {0} bytes ({1:N1} MiB, limit in CI 52428800)" -f $bytes, ($bytes / 1MB)