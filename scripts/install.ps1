[CmdletBinding()]
param(
  [ValidateSet('Install', 'Doctor', 'Uninstall', 'Purge')]
  [string]$Action = 'Install',
  [string]$Source = '.\releaseguard.exe',
  [string]$Checksums = '',
  [string]$Prefix = "$env:LOCALAPPDATA\ReleaseGuard",
  [string]$ConfirmPurge = ''
)

$ErrorActionPreference = 'Stop'
$root = [System.IO.Path]::GetFullPath($Prefix)
$binDir = Join-Path $root 'bin'
$dataDir = Join-Path $root 'data'
$binary = Join-Path $binDir 'releaseguard.exe'

switch ($Action) {
  'Install' {
    if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) { throw "Binary not found: $Source" }
    if ($Checksums) {
      if (-not (Test-Path -LiteralPath $Checksums -PathType Leaf)) { throw "Checksum file not found: $Checksums" }
      $name = Split-Path -Leaf $Source
      $entry = Get-Content -LiteralPath $Checksums | Where-Object { $_ -match ('^[0-9a-fA-F]{64}\s+\*?' + [regex]::Escape($name) + '$') } | Select-Object -First 1
      if (-not $entry) { throw "Checksum entry not found for $name" }
      $expected = ($entry -split '\s+')[0].ToLowerInvariant()
      $actual = (Get-FileHash -LiteralPath $Source -Algorithm SHA256).Hash.ToLowerInvariant()
      if ($actual -ne $expected) { throw "SHA-256 verification failed for $Source" }
      Write-Host "Verified SHA-256 for $name"
    }
    New-Item -ItemType Directory -Force -Path $binDir, $dataDir | Out-Null
    Copy-Item -LiteralPath $Source -Destination $binary -Force
    & $binary version
    Write-Host "Installed $binary"
    Write-Host "Data directory: $dataDir"
  }
  'Doctor' {
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw "ReleaseGuard is not installed at $binary" }
    $report = Join-Path $dataDir 'release-report.json'
    if (Test-Path -LiteralPath $report -PathType Leaf) {
      & $binary doctor --report $report --state (Join-Path $dataDir 'releaseguard-runs.db')
      if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    } else {
      & $binary version
      Write-Warning 'Create a release report before running the full doctor.'
    }
  }
  'Uninstall' {
    Remove-Item -LiteralPath $binary -Force -ErrorAction SilentlyContinue
    Write-Host "Removed binaries; preserved $dataDir"
  }
  'Purge' {
    if ($ConfirmPurge -ne $root) { throw "Pass -ConfirmPurge with the exact path: $root" }
    if ($root -notlike '*\ReleaseGuard') { throw "Refusing to purge unexpected path: $root" }
    Remove-Item -LiteralPath $root -Recurse -Force
    Write-Host "Purged $root"
  }
}
