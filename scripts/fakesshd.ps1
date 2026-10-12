# Dot-sourced by scripts/test-edge-store.ps1 and scripts/test-edge-entry.ps1: starts the fake SSH server of the edge js
# tests (cmd/mistgate-edge/testdata/fakesshd) and tells the tests where it listens through MG_FAKESSHD_* variables.
# Build it with the native toolchain, so call Start-FakeSshd before GOOS=js is set.

$script:FakeSshdNames = "ADDR", "SILENT", "FP", "USER", "PASS"

function Save-FakeSshdEnv {
    $saved = @{}
    foreach ($name in $script:FakeSshdNames) { $saved[$name] = [Environment]::GetEnvironmentVariable("MG_FAKESSHD_$name") }
    return $saved
}

function Restore-FakeSshdEnv([hashtable]$Saved) {
    foreach ($name in $Saved.Keys) { [Environment]::SetEnvironmentVariable("MG_FAKESSHD_$name", $Saved[$name]) }
}

# Returns the server process. TempDir must exist; the binary and its output go there.
function Start-FakeSshd([string]$RepoRoot, [string]$TempDir) {
    $exe = Join-Path $TempDir "fakesshd.exe"
    Push-Location $RepoRoot
    try {
        & go build -o $exe ./cmd/mistgate-edge/testdata/fakesshd
        if ($LASTEXITCODE -ne 0) { throw "go build of the fake SSH server failed." }
    } finally {
        Pop-Location
    }
    $out = Join-Path $TempDir "fakesshd.out"
    $process = Start-Process -FilePath $exe -PassThru -WindowStyle Hidden -RedirectStandardOutput $out -RedirectStandardError (Join-Path $TempDir "fakesshd.err")
    $ready = $null
    for ($i = 0; $i -lt 100 -and -not $ready; $i++) {
        Start-Sleep -Milliseconds 100
        if (Test-Path $out) { $ready = Get-Content -LiteralPath $out | Where-Object { $_ -like "READY *" } | Select-Object -First 1 }
    }
    if (-not $ready) {
        Stop-FakeSshd $process
        throw "The fake SSH server did not report READY."
    }
    $fields = $ready -split ' '
    for ($i = 0; $i -lt $script:FakeSshdNames.Count; $i++) {
        [Environment]::SetEnvironmentVariable("MG_FAKESSHD_" + $script:FakeSshdNames[$i], $fields[$i + 1])
    }
    return $process
}

function Stop-FakeSshd($Process) {
    if ($Process -and -not $Process.HasExited) {
        Stop-Process -Id $Process.Id -Force
        $Process.WaitForExit()
    }
}
