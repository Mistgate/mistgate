$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$tempRoot = (Resolve-Path ([IO.Path]::GetTempPath())).Path
$tempRootPrefix = $tempRoot.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
$tempDir = Join-Path $tempRoot ("mistgate-edge-entry-" + [guid]::NewGuid().ToString("N"))

try {
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    $resolvedTempDir = (Resolve-Path $tempDir).Path
    if (-not $resolvedTempDir.StartsWith($tempRootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Edge entry test output directory is outside the OS temp directory."
    }

    $wasm = Join-Path $resolvedTempDir "panel.wasm"
    $oracle = Join-Path $resolvedTempDir "vps-oracle.exe"
    $env:GOOS = "js"
    $env:GOARCH = "wasm"
    & go build -tags=edgeentrytest -o $wasm ./cmd/mistgate-edge
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }

    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    & go build -o $oracle ./cmd/mistgate-edge/testdata/vps-oracle
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }

    if (Get-Command node -ErrorAction SilentlyContinue) {
        $nodeExe = (Get-Command node).Source
    } else {
        throw "Edge entry test requires Node 22."
    }
    $nodeVersion = (& $nodeExe --version).Trim()
    if ($LASTEXITCODE -ne 0 -or $nodeVersion -notmatch '^v22\.') {
        throw "Edge entry test requires Node 22; found '$nodeVersion'."
    }

    $goRoot = (& go env GOROOT).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "go env GOROOT failed."
    }
    $wasmExec = Join-Path $goRoot "lib/wasm/wasm_exec.js"
    $harness = Join-Path $repoRoot "cmd/mistgate-edge/testdata/bridge.cjs"
    & $nodeExe $harness $wasm $wasmExec $oracle $resolvedTempDir
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    if (Test-Path $tempDir) {
        $cleanupTarget = (Resolve-Path -LiteralPath $tempDir).Path
        if (-not $cleanupTarget.StartsWith($tempRootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Edge entry test cleanup target is outside the OS temp directory."
        }
        Remove-Item -LiteralPath $cleanupTarget -Recurse -Force
    }
}
