# Builds the signed-release edge bundle (mistgate-edge.tar.gz) the way the release workflow does and proves it:
#   1. two builds into different directories are byte for byte the same;
#   2. the six scripts of edge.json (front.js and the five <role>.js with their <role>.wasm as CompiledWasm modules) load in
#      workerd (Miniflare) from the extracted tarball, and a real request per role goes through FRONT: a subscription, the
#      admin SPA, a link upgrade, a cron tick, an install request, the empty-database first run (edge/worker/test/bundle);
#   3. `wrangler deploy --dry-run` of each script, with the bindings edge.json describes, prints "Total Upload": it must stay
#      under Cloudflare's limit of 64 MiB per Worker (uncompressed; there is no compressed limit since 2026-09-04).
# Needs Go, Node 22, pnpm and tar. No Cloudflare account, no login, no deploy.
param(
    [switch]$SkipWeb,
    [string]$Version = "v0.0.0-test"
)
$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$worker = Join-Path $repoRoot "edge/worker"
$limitKiB = 64 * 1024

function Invoke-Native([string]$What, [scriptblock]$Run) {
    & $Run
    if ($LASTEXITCODE -ne 0) { throw "$What failed with exit code $LASTEXITCODE." }
}

$tempRoot = (Resolve-Path ([IO.Path]::GetTempPath())).Path
$tempDir = Join-Path $tempRoot ("mistgate-edge-bundle-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tempDir | Out-Null
try {
    if (-not $SkipWeb) {
        Invoke-Native "pnpm install (web)" { pnpm --dir (Join-Path $repoRoot "web") install --frozen-lockfile }
        Invoke-Native "pnpm build (web)" { pnpm --dir (Join-Path $repoRoot "web") build }
    }
    Invoke-Native "pnpm install (edge/worker)" { pnpm --dir $worker install --frozen-lockfile }

    # Any 32 bytes are a well-formed key for a build that is only inspected.
    $key = [Convert]::ToBase64String([byte[]](1..32))
    $tarballs = @()
    foreach ($n in 1, 2) {
        $out = Join-Path $tempDir "build$n"
        Push-Location $repoRoot
        try {
            Invoke-Native "release build ($n)" { go run ./cmd/mistgate release build --version $Version --built 1789000000 --key $key --out $out mistgate-edge.tar.gz }
        } finally {
            Pop-Location
        }
        $tarballs += Join-Path $out "mistgate-edge.tar.gz"
    }
    $sums = $tarballs | ForEach-Object { (Get-FileHash -Algorithm SHA256 -LiteralPath $_).Hash.ToLowerInvariant() }
    if ($sums[0] -ne $sums[1]) { throw "two builds of the edge bundle differ: $($sums[0]) $($sums[1])" }
    "edge bundle sha256 (two builds equal): $($sums[0]), $((Get-Item -LiteralPath $tarballs[0]).Length) bytes"

    $bundle = Join-Path $tempDir "bundle"
    New-Item -ItemType Directory -Path $bundle | Out-Null
    # On Windows the system tar (bsdtar): a tar from Git Bash does not take Windows paths.
    $tar = "tar"
    if ($env:SystemRoot -and (Test-Path -LiteralPath (Join-Path $env:SystemRoot "System32/tar.exe"))) { $tar = Join-Path $env:SystemRoot "System32/tar.exe" }
    Invoke-Native "tar -x" { & $tar -xzf $tarballs[0] -C $bundle }
    $edge = Get-Content -LiteralPath (Join-Path $bundle "edge.json") -Raw | ConvertFrom-Json

    $env:MISTGATE_EDGE_BUNDLE = $bundle
    try {
        Invoke-Native "vitest (bundle)" { pnpm --dir $worker exec vitest run --project bundle }
    } finally {
        Remove-Item Env:MISTGATE_EDGE_BUNDLE -ErrorAction SilentlyContinue
    }

    # wrangler.toml per script from edge.json, next to copies of that script's modules (so that no other file is picked up as a module).
    foreach ($script in $edge.scripts) {
        $dir = Join-Path $tempDir ("upload-" + $script.role)
        New-Item -ItemType Directory -Path $dir | Out-Null
        $toml = New-Object System.Collections.Generic.List[string]
        $toml.Add("name = `"$($script.default_name)`"")
        $toml.Add("main = `"$($script.main_module)`"")
        $toml.Add("compatibility_date = `"$($edge.compatibility_date)`"")
        $toml.Add("workers_dev = false")
        $toml.Add("no_bundle = true")
        $toml.Add("find_additional_modules = true")
        # wrangler's default rules already type *.js as ESModule and *.wasm as CompiledWasm, as edge.json says.
        foreach ($m in $script.modules) { Copy-Item -LiteralPath (Join-Path $bundle $m.name) -Destination $dir }
        if ($script.cron) { $toml.Add("[triggers]"); $toml.Add("crons = [" + (($script.cron | ForEach-Object { "`"$_`"" }) -join ", ") + "]") }
        foreach ($d1 in $script.d1) { $toml.Add("[[d1_databases]]"); $toml.Add("binding = `"$($d1.binding)`""); $toml.Add("database_name = `"mistgate`""); $toml.Add("database_id = `"00000000-0000-0000-0000-000000000000`"") }
        foreach ($d in $script.durable_objects) { $toml.Add("[[durable_objects.bindings]]"); $toml.Add("name = `"$($d.binding)`""); $toml.Add("class_name = `"$($d.class_name)`"") }
        foreach ($d in $script.durable_objects_external) {
            $owner = ($edge.scripts | Where-Object { $_.role -eq $d.script_role }).default_name
            $toml.Add("[[durable_objects.bindings]]"); $toml.Add("name = `"$($d.binding)`""); $toml.Add("class_name = `"$($d.class_name)`""); $toml.Add("script_name = `"$owner`"")
        }
        foreach ($v in $script.services) {
            $target = ($edge.scripts | Where-Object { $_.role -eq $v.service_role }).default_name
            $toml.Add("[[services]]"); $toml.Add("binding = `"$($v.binding)`""); $toml.Add("service = `"$target`"")
            if ($v.entrypoint) { $toml.Add("entrypoint = `"$($v.entrypoint)`"") }
        }
        foreach ($m in $script.migrations) {
            $toml.Add("[[migrations]]"); $toml.Add("tag = `"$($m.tag)`"")
            $toml.Add("new_sqlite_classes = [" + (($m.new_sqlite_classes | ForEach-Object { "`"$_`"" }) -join ", ") + "]")
        }
        if ($script.assets) {
            $toml.Add("[assets]"); $toml.Add("directory = `"" + (Join-Path $bundle $script.assets.directory).Replace('\', '/') + "`""); $toml.Add("binding = `"$($script.assets.binding)`"")
            $toml.Add("run_worker_first = true"); $toml.Add("html_handling = `"none`""); $toml.Add("not_found_handling = `"none`"")
        }
        $config = Join-Path $dir "wrangler.toml"
        [IO.File]::WriteAllLines($config, $toml, (New-Object Text.UTF8Encoding $false))
        $env:CI = "1"; $env:WRANGLER_SEND_METRICS = "false"
        # wrangler writes warnings to stderr; they are not failures (Windows PowerShell 5.1 turns them into errors under Stop).
        $ErrorActionPreference = "Continue"
        $log = & pnpm --dir $worker exec wrangler deploy --dry-run --outdir (Join-Path $tempDir ("dry-" + $script.role)) -c $config 2>&1 | Out-String
        $exit = $LASTEXITCODE
        $ErrorActionPreference = "Stop"
        if ($exit -ne 0) { throw "wrangler deploy --dry-run ($($script.role)) failed:`n$log" }
        if ($log -notmatch 'Total Upload:\s*([\d.]+) KiB\s*/\s*gzip:\s*([\d.]+) KiB') { throw "no Total Upload in the wrangler output ($($script.role)):`n$log" }
        $total = [double]::Parse($Matches[1], [Globalization.CultureInfo]::InvariantCulture)
        $gzip = $Matches[2]
        "{0,-9} Total Upload {1:N2} KiB ({2:N2} MiB), gzip {3} KiB; limit 64 MiB, headroom {4:N2} MiB" -f $script.role, $total, ($total / 1024), $gzip, (($limitKiB - $total) / 1024)
        if ($total -gt $limitKiB) { throw "the $($script.role) script uploads $total KiB, over Cloudflare's 64 MiB limit." }
    }
} finally {
    Remove-Item Env:CI, Env:WRANGLER_SEND_METRICS -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $tempDir) { Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue }
}
