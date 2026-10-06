$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH

try {
    Set-Location $repoRoot
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        throw "Edge storage tests require Node 22; node was not found on PATH."
    }
    $nodeVersion = (& node --version).Trim()
    if ($LASTEXITCODE -ne 0 -or $nodeVersion -notmatch '^v22\.') {
        throw "Edge storage tests require Node 22; found '$nodeVersion'."
    }

    $goRoot = (& go env GOROOT).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "go env GOROOT failed."
    }
    $wasmExec = Join-Path $goRoot "lib/wasm/wasm_exec_node.js"
    $trimEnv = Join-Path $repoRoot "edge/d1driver/testdata/trim-env.cjs"
    $fakeD1 = Join-Path $repoRoot "edge/d1driver/testdata/fake-d1.cjs"
    $exec = "node --require $trimEnv --require $fakeD1 $wasmExec"

    $env:GOOS = "js"
    $env:GOARCH = "wasm"
    & go test "-exec=$exec" ./edge/d1driver/ ./internal/panel/store/
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
}
