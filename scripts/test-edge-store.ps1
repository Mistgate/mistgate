param([string[]]$TestArgs = @())  # extra arguments for go test, e.g. -v -run SSH
$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot "fakesshd.ps1")
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$previousFakeSshd = Save-FakeSshdEnv
$fakeSshd = $null
$tempDir = $null

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

    # The SSH tests of edge/bridge dial a fake SSH server through a Node stand-in of the Worker's sshConnect.
    $tempDir = Join-Path ([IO.Path]::GetTempPath()) ("mistgate-edge-store-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    $fakeSshd = Start-FakeSshd $repoRoot $tempDir

    $wasmExec = Join-Path $goRoot "lib/wasm/wasm_exec_node.js"
    $trimEnv = Join-Path $repoRoot "edge/d1driver/testdata/trim-env.cjs"
    $fakeD1 = Join-Path $repoRoot "edge/d1driver/testdata/fake-d1.cjs"
    $sshConnect = Join-Path $repoRoot "cmd/mistgate-edge/testdata/sshconnect.cjs"
    $exec = "node --disable-warning=ExperimentalWarning --require $trimEnv --require $fakeD1 --require $sshConnect $wasmExec"

    $env:GOOS = "js"
    $env:GOARCH = "wasm"
    & go test "-exec=$exec" @TestArgs ./edge/d1driver/ ./internal/panel/store/ ./edge/bridge/
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    Restore-FakeSshdEnv $previousFakeSshd
    Stop-FakeSshd $fakeSshd
    if ($tempDir -and (Test-Path -LiteralPath $tempDir)) {
        Remove-Item -LiteralPath $tempDir -Recurse -Force
    }
}
