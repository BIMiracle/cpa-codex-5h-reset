param(
    [Parameter(Mandatory = $true)][string]$ReleaseZip,
    [Parameter(Mandatory = $true)][string]$ChecksumsFile,
    [string]$StableRoot = "$env:LOCALAPPDATA\CLIProxyAPI",
    [string]$ExistingPluginsDir = ''
)

$ErrorActionPreference = 'Stop'
$zipPath = (Resolve-Path -LiteralPath $ReleaseZip).Path
$checksumPath = (Resolve-Path -LiteralPath $ChecksumsFile).Path
$name = [IO.Path]::GetFileName($zipPath)
if ($name -notmatch '^cpa-codex-5h-reset_(\d+\.\d+\.\d+)_windows_amd64\.zip$') {
    throw "Unexpected release filename: $name"
}
$version = $Matches[1]
$expected = Get-Content -LiteralPath $checksumPath |
    Where-Object { $_ -match ([regex]::Escape($name) + '$') } |
    Select-Object -First 1
if (-not $expected -or $expected -notmatch '^([a-fA-F0-9]{64})\s+') {
    throw "No SHA256 checksum found for $name"
}
$actual = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash
if ($actual -ine $Matches[1]) {
    throw "SHA256 mismatch for $name"
}

Add-Type -AssemblyName System.IO.Compression
$destination = Join-Path $StableRoot 'plugins\windows\amd64'
New-Item -ItemType Directory -Path $destination -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $StableRoot 'state') -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $StableRoot 'scripts') -Force | Out-Null

# Keep existing CPA plugins usable when plugins.dir is changed to StableRoot.
$oldPlatform = if ($ExistingPluginsDir) { Join-Path $ExistingPluginsDir 'windows\amd64' } else { '' }
if ($ExistingPluginsDir -and (Test-Path -LiteralPath $oldPlatform)) {
    Get-ChildItem -LiteralPath $oldPlatform -Filter '*.dll' -File | ForEach-Object {
        $target = Join-Path $destination $_.Name
        if (-not (Test-Path -LiteralPath $target)) {
            Copy-Item -LiteralPath $_.FullName -Destination $target
        }
    }
}

$archive = [IO.Compression.ZipFile]::OpenRead($zipPath)
try {
    $entry = $archive.Entries |
        Where-Object { $_.FullName -eq 'cpa-codex-5h-reset.dll' } |
        Select-Object -First 1
    if (-not $entry) { throw 'Release ZIP does not contain the expected DLL at its root.' }
    $targetDLL = Join-Path $destination "cpa-codex-5h-reset-v$version.dll"
    $source = $entry.Open()
    try {
        $output = [IO.File]::Create($targetDLL)
        try { $source.CopyTo($output) } finally { $output.Dispose() }
    } finally { $source.Dispose() }
} finally { $archive.Dispose() }

Write-Host "Installed $targetDLL"
Write-Host "Set plugins.dir to $(Join-Path $StableRoot 'plugins') in CPA config.yaml,"
Write-Host 'add the plugin configuration from README.md, then restart CPA.'
