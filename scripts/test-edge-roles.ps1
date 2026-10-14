# Builds the five role entries (cmd/mistgate-edge-{public,link,jobs,admin,provision}) for js/wasm and runs the Node smoke test of
# cmd/mistgate-edge/testdata/roles.cjs on them: each role starts on a shared fake D1, has only its own mgPanel members, refuses
# a database nobody initialised (the admin creates the instance) and answers one call of its own.
$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$tempRoot = (Resolve-Path ([IO.Path]::GetTempPath())).Path
$tempRootPrefix = $tempRoot.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
$tempDir = Join-Path $tempRoot ("mistgate-edge-roles-" + [guid]::NewGuid().ToString("N"))

try {
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    $resolvedTempDir = (Resolve-Path $tempDir).Path
    if (-not $resolvedTempDir.StartsWith($tempRootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Edge roles test output directory is outside the OS temp directory."
    }

    $env:GOOS = "js"
    $env:GOARCH = "wasm"
    Push-Location $repoRoot
    try {
        foreach ($role in "public", "link", "jobs", "admin", "provision") {
            & go build "-tags=nethttpomithttp2" -o (Join-Path $resolvedTempDir "$role.wasm") "./cmd/mistgate-edge-$role"
            if ($LASTEXITCODE -ne 0) {
                exit $LASTEXITCODE
            }
        }
    } finally {
        Pop-Location
    }
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch

    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        throw "Edge roles test requires Node 22."
    }
    $nodeExe = (Get-Command node).Source
    $nodeVersion = (& $nodeExe --version).Trim()
    if ($LASTEXITCODE -ne 0 -or $nodeVersion -notmatch '^v22\.') {
        throw "Edge roles test requires Node 22; found '$nodeVersion'."
    }

    $goRoot = (& go env GOROOT).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "go env GOROOT failed."
    }
    $wasmExec = Join-Path $goRoot "lib/wasm/wasm_exec.js"
    $harness = Join-Path $repoRoot "cmd/mistgate-edge/testdata/roles.cjs"
    & $nodeExe --disable-warning=ExperimentalWarning $harness $wasmExec $resolvedTempDir
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    if (Test-Path $tempDir) {
        $cleanupTarget = (Resolve-Path -LiteralPath $tempDir).Path
        if (-not $cleanupTarget.StartsWith($tempRootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Edge roles test cleanup target is outside the OS temp directory."
        }
        Remove-Item -LiteralPath $cleanupTarget -Recurse -Force
    }
}
