param([string[]]$TestArgs = @())  # extra arguments for go test, e.g. -v -run SSH
$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$previousFakeSshd = @{}
foreach ($name in "ADDR", "SILENT", "FP", "USER", "PASS") { $previousFakeSshd[$name] = [Environment]::GetEnvironmentVariable("MG_FAKESSHD_$name") }
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

    # The SSH tests of cmd/mistgate-edge dial a fake SSH server through a Node stand-in of the Worker's sshConnect. The
    # server is a native program: build it before GOOS=js is set, start it, and tell the tests where it listens.
    $tempDir = Join-Path ([IO.Path]::GetTempPath()) ("mistgate-edge-store-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    $fakeSshdExe = Join-Path $tempDir "fakesshd.exe"
    & go build -o $fakeSshdExe ./cmd/mistgate-edge/testdata/fakesshd
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
    $fakeSshdOut = Join-Path $tempDir "fakesshd.out"
    $fakeSshd = Start-Process -FilePath $fakeSshdExe -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $fakeSshdOut -RedirectStandardError (Join-Path $tempDir "fakesshd.err")
    $ready = $null
    for ($i = 0; $i -lt 100 -and -not $ready; $i++) {
        Start-Sleep -Milliseconds 100
        if (Test-Path $fakeSshdOut) { $ready = Get-Content -LiteralPath $fakeSshdOut | Where-Object { $_ -like "READY *" } | Select-Object -First 1 }
    }
    if (-not $ready) {
        throw "The fake SSH server did not report READY."
    }
    $fields = $ready -split ' '
    $env:MG_FAKESSHD_ADDR = $fields[1]
    $env:MG_FAKESSHD_SILENT = $fields[2]
    $env:MG_FAKESSHD_FP = $fields[3]
    $env:MG_FAKESSHD_USER = $fields[4]
    $env:MG_FAKESSHD_PASS = $fields[5]

    $wasmExec = Join-Path $goRoot "lib/wasm/wasm_exec_node.js"
    $trimEnv = Join-Path $repoRoot "edge/d1driver/testdata/trim-env.cjs"
    $fakeD1 = Join-Path $repoRoot "edge/d1driver/testdata/fake-d1.cjs"
    $sshConnect = Join-Path $repoRoot "cmd/mistgate-edge/testdata/sshconnect.cjs"
    $exec = "node --disable-warning=ExperimentalWarning --require $trimEnv --require $fakeD1 --require $sshConnect $wasmExec"

    $env:GOOS = "js"
    $env:GOARCH = "wasm"
    & go test "-exec=$exec" @TestArgs ./edge/d1driver/ ./internal/panel/store/ ./cmd/mistgate-edge/
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    foreach ($name in $previousFakeSshd.Keys) { [Environment]::SetEnvironmentVariable("MG_FAKESSHD_$name", $previousFakeSshd[$name]) }
    if ($fakeSshd -and -not $fakeSshd.HasExited) {
        Stop-Process -Id $fakeSshd.Id -Force
        $fakeSshd.WaitForExit()
    }
    if ($tempDir -and (Test-Path -LiteralPath $tempDir)) {
        Remove-Item -LiteralPath $tempDir -Recurse -Force
    }
}
