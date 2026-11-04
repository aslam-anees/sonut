# Compatibility entrypoint for the canonical checksum-verifying @code installer.
[CmdletBinding()]
param(
  [string]$Version,
  [string]$Repository,
  [string]$InstallDir,
  [string]$EngineDir
)

$ErrorActionPreference = "Stop"
$sourceRepository = if ($env:ATCODE_INSTALL_SOURCE_REPO) { $env:ATCODE_INSTALL_SOURCE_REPO } else { "aslam-anees/sonut" }
$sourceRef = if ($env:ATCODE_INSTALL_SOURCE_REF) { $env:ATCODE_INSTALL_SOURCE_REF } else { "src" }
$installerUrl = "https://raw.githubusercontent.com/$sourceRepository/$sourceRef/scripts/install.ps1"
$installerSource = (Invoke-WebRequest -Uri $installerUrl -UseBasicParsing -TimeoutSec 30).Content
$installer = [ScriptBlock]::Create($installerSource)
$forward = @{}
if (-not [string]::IsNullOrWhiteSpace($Version)) { $forward.Version = $Version }
if (-not [string]::IsNullOrWhiteSpace($Repository)) { $forward.Repository = $Repository }
if (-not [string]::IsNullOrWhiteSpace($InstallDir)) { $forward.InstallDir = $InstallDir }
if (-not [string]::IsNullOrWhiteSpace($EngineDir)) { $forward.EngineDir = $EngineDir }
& $installer @forward
