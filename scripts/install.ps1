[CmdletBinding()]
param(
  [string]$Version = $(if ($env:ATCODE_VERSION) { $env:ATCODE_VERSION } else { $env:SONUT_VERSION }),
  [string]$Repository = $(if ($env:ATCODE_REPO) { $env:ATCODE_REPO } elseif ($env:SONUT_REPO) { $env:SONUT_REPO } else { "aslam-anees/sonut" }),
  [string]$InstallDir = $(if ($env:ATCODE_INSTALL_DIR) { $env:ATCODE_INSTALL_DIR } else { $env:SONUT_INSTALL_DIR }),
  [string]$EngineDir = $(if ($env:ATCODE_ENGINE_DIR) { $env:ATCODE_ENGINE_DIR } else { Join-Path $env:USERPROFILE ".atcode\engine" }),
  [string]$GitHubApi = $(if ($env:ATCODE_GITHUB_API) { $env:ATCODE_GITHUB_API } elseif ($env:SONUT_GITHUB_API) { $env:SONUT_GITHUB_API } else { "https://api.github.com" }),
  [string]$GitHubBaseUrl = $(if ($env:ATCODE_GITHUB_BASE_URL) { $env:ATCODE_GITHUB_BASE_URL } elseif ($env:SONUT_GITHUB_BASE_URL) { $env:SONUT_GITHUB_BASE_URL } else { "https://github.com" })
)

$ErrorActionPreference = "Stop"
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}
$ProgressPreference = "SilentlyContinue"

if ([string]::IsNullOrWhiteSpace($Version)) {
  $Version = "latest"
}

if ([string]::IsNullOrWhiteSpace($InstallDir)) {
  $InstallDir = Join-Path $env:USERPROFILE ".atcode\bin"
}

function Get-AtCodeLatestTag {
  param([string]$Repository, [string]$GitHubApi)

  $apiBase = $GitHubApi.TrimEnd([char[]]"/")
  $release = Invoke-RestMethod `
    -Uri "$apiBase/repos/$Repository/releases/latest" `
    -Headers @{ Accept = "application/vnd.github+json" } `
    -TimeoutSec 15

  if ([string]::IsNullOrWhiteSpace($release.tag_name)) {
    throw "GitHub release response did not include tag_name"
  }

  return [string]$release.tag_name
}

function Get-AtCodeArch {
  $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()

  switch ($arch) {
    "X64" { return "x64" }
    "Arm64" { return "arm64" }
    default { throw "Unsupported architecture: $arch" }
  }
}

function Find-AtCodeExtractedFile {
  param(
    [string]$Root,
    [string]$FileName
  )

  $candidate = Join-Path $Root $FileName
  if (Test-Path $candidate -PathType Leaf) {
    return $candidate
  }

  $matches = @(Get-ChildItem -Path $Root -Filter $FileName -File -Recurse)
  if ($matches.Count -eq 1) {
    return $matches[0].FullName
  }

  throw "Release archive did not contain exactly one $FileName"
}

function Find-AtCodeOptionalExtractedDirectory {
  param(
    [string]$Root,
    [string]$DirectoryName
  )

  $candidate = Join-Path $Root $DirectoryName
  if (Test-Path $candidate -PathType Container) {
    return $candidate
  }

  $matches = @(
    Get-ChildItem -Path $Root -Directory | ForEach-Object {
      $child = Join-Path $_.FullName $DirectoryName
      if (Test-Path $child -PathType Container) {
        Get-Item $child
      }
    }
  )
  if ($matches.Count -eq 0) {
    return $null
  }
  if ($matches.Count -eq 1) {
    return $matches[0].FullName
  }

  throw "Release archive contained multiple $DirectoryName directories"
}

function Find-AtCodeOptionalExtractedFile {
  param(
    [string]$Root,
    [string]$FileName
  )

  $candidate = Join-Path $Root $FileName
  if (Test-Path $candidate -PathType Leaf) {
    return $candidate
  }
  $matches = @(Get-ChildItem -Path $Root -Filter $FileName -File -Recurse)
  if ($matches.Count -gt 0) {
    return $matches[0].FullName
  }
  return $null
}

function Move-AtCodeLegacyEngine {
  $legacyRoot = Join-Path $env:USERPROFILE ".atcode\prism-llama.cpp"
  if (Test-Path $legacyRoot -PathType Container) {
    $migratedRoot = $EngineDir
    if (Test-Path $migratedRoot) {
      $migratedRoot = Join-Path $EngineDir "compat-runtime"
      $index = 2
      while (Test-Path $migratedRoot) {
        $migratedRoot = Join-Path $EngineDir "compat-runtime-$index"
        $index++
      }
    }
    New-Item -ItemType Directory -Path (Split-Path $EngineDir -Parent) -Force | Out-Null
    Move-Item -Path $legacyRoot -Destination $migratedRoot
  }

  $legacyServers = @((Join-Path $InstallDir "llama-server.exe"))
  if (Test-Path $EngineDir -PathType Container) {
    $legacyServers += @(
      Get-ChildItem -Path $EngineDir -Recurse -Filter "llama-server.exe" -File |
        ForEach-Object { $_.FullName }
    )
  }
  foreach ($oldPath in $legacyServers) {
    if (Test-Path $oldPath -PathType Leaf) {
      $newPath = Join-Path (Split-Path $oldPath -Parent) "atcode-server.exe"
      $index = 2
      while (Test-Path $newPath) {
        $newPath = Join-Path (Split-Path $oldPath -Parent) "atcode-server-compat-$index.exe"
        $index++
      }
      Move-Item -Path $oldPath -Destination $newPath
    }
  }
}

function Install-AtCodeEngine {
  param(
    [string]$ReleaseExtractDir,
    [string]$Architecture
  )

  $targetServer = Join-Path $EngineDir "atcode-server.exe"
  if (Test-Path $targetServer -PathType Leaf) {
    Write-Host "Verified $targetServer"
    return
  }

  $packagedServer = Find-AtCodeOptionalExtractedFile -Root $ReleaseExtractDir -FileName "atcode-server.exe"
  if ($null -ne $packagedServer) {
    New-Item -ItemType Directory -Path $EngineDir -Force | Out-Null
    Copy-Item -Path $packagedServer -Destination $targetServer -Force
    Write-Host "Installed $targetServer"
    return
  }

  $engineRepository = if ($env:ATCODE_ENGINE_REPO) { $env:ATCODE_ENGINE_REPO } else { "ggml-org/llama.cpp" }
  $release = Invoke-RestMethod `
    -Uri "$($GitHubApi.TrimEnd([char[]]"/"))/repos/$engineRepository/releases/latest" `
    -Headers @{ Accept = "application/vnd.github+json" } `
    -TimeoutSec 30
  $assetPattern = if ($Architecture -eq "arm64") {
    "bin-win-cpu-arm64\.zip$"
  } else {
    "bin-win-cpu-x64\.zip$"
  }
  $asset = $release.assets | Where-Object { $_.name -match $assetPattern } | Select-Object -First 1
  if ($null -eq $asset) {
    throw "Could not resolve an @code engine build for windows-$Architecture"
  }

  $engineArchive = Join-Path $tempDir "atcode-engine.zip"
  $engineExtract = Join-Path $tempDir "engine-extract"
  Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $engineArchive -UseBasicParsing -TimeoutSec 600
  Expand-Archive -Path $engineArchive -DestinationPath $engineExtract -Force
  $sourceServer = Get-ChildItem -Path $engineExtract -Recurse -Filter "llama-server.exe" -File |
    Select-Object -First 1
  if ($null -eq $sourceServer) {
    throw "The downloaded @code engine package was incomplete"
  }

  New-Item -ItemType Directory -Path $EngineDir -Force | Out-Null
  Copy-Item -Path $sourceServer.FullName -Destination $targetServer -Force
  Get-ChildItem -Path $sourceServer.DirectoryName -Filter "*.dll" -File |
    Copy-Item -Destination $EngineDir -Force
  Write-Host "Installed $targetServer"
}

if ($Version -eq "latest") {
  $tag = Get-AtCodeLatestTag -Repository $Repository -GitHubApi $GitHubApi
} elseif ($Version.StartsWith("v")) {
  $tag = $Version
} else {
  $tag = "v$Version"
}

$releaseVersion = $tag -replace "^v", ""
$arch = Get-AtCodeArch
$archiveName = "atcode-v$releaseVersion-windows-$arch.zip"
$checksumName = "$archiveName.sha256"
$releaseBase = $GitHubBaseUrl.TrimEnd([char[]]"/")
$releaseUrl = "$releaseBase/$Repository/releases/download/$tag"
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("atcode-install-" + [System.Guid]::NewGuid().ToString("N"))
$extractDir = Join-Path $tempDir "extract"
$archivePath = Join-Path $tempDir $archiveName
$checksumPath = Join-Path $tempDir $checksumName

try {
  New-Item -ItemType Directory -Path $tempDir, $extractDir -Force | Out-Null

  Write-Host "Installing @code $tag for windows-$arch"
  Invoke-WebRequest -Uri "$releaseUrl/$archiveName" -OutFile $archivePath -UseBasicParsing -TimeoutSec 300
  Invoke-WebRequest -Uri "$releaseUrl/$checksumName" -OutFile $checksumPath -UseBasicParsing -TimeoutSec 300

  $checksumLine = Get-Content -Path $checksumPath -TotalCount 1
  $expectedChecksum = ($checksumLine -split "\s+")[0].ToLowerInvariant()
  $actualChecksum = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()

  if ($expectedChecksum -ne $actualChecksum) {
    throw "Checksum mismatch for $archiveName"
  }

  Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force

  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  $requiredFiles = @(
    "at.exe",
    "atcode-windows-command-runner.exe",
    "atcode-windows-sandbox-setup.exe"
  )
  foreach ($fileName in $requiredFiles) {
    $sourcePath = Find-AtCodeExtractedFile -Root $extractDir -FileName $fileName
    if ($fileName -eq "at.exe") {
      Copy-Item -Path $sourcePath -Destination (Join-Path $InstallDir "at.exe") -Force
      Copy-Item -Path $sourcePath -Destination (Join-Path $InstallDir "atcode.exe") -Force
      Copy-Item -Path $sourcePath -Destination (Join-Path $InstallDir "sonut.exe") -Force
    } else {
      Copy-Item -Path $sourcePath -Destination (Join-Path $InstallDir $fileName) -Force
    }
  }
  $helpersPath = Find-AtCodeOptionalExtractedDirectory -Root $extractDir -DirectoryName "helpers"
  if ($null -ne $helpersPath) {
    $targetHelpersPath = Join-Path $InstallDir "helpers"
    if (Test-Path $targetHelpersPath) {
      Remove-Item -Path $targetHelpersPath -Recurse -Force
    }
    Copy-Item -Path $helpersPath -Destination $targetHelpersPath -Recurse -Force
  }
  if ($env:ATCODE_SKIP_ENGINE -ne "1") {
    Move-AtCodeLegacyEngine
    Install-AtCodeEngine -ReleaseExtractDir $extractDir -Architecture $arch
  }

  $targetPath = Join-Path $InstallDir "at.exe"
  Write-Host "Installed $targetPath"

  $pathEntries = $env:PATH -split [System.IO.Path]::PathSeparator
  if ($pathEntries -notcontains $InstallDir) {
    if ($env:ATCODE_NO_PATH_UPDATE -eq "1") {
      Write-Host "Add $InstallDir to PATH to run @code with 'at' from any directory."
    } else {
      $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
      $userEntries = @($userPath -split [System.IO.Path]::PathSeparator)
      if ($userEntries -notcontains $InstallDir) {
        $updatedUserPath = if ([string]::IsNullOrWhiteSpace($userPath)) {
          $InstallDir
        } else {
          "$InstallDir$([System.IO.Path]::PathSeparator)$userPath"
        }
        [Environment]::SetEnvironmentVariable("Path", $updatedUserPath, "User")
      }
      $env:PATH = "$InstallDir$([System.IO.Path]::PathSeparator)$env:PATH"
      Write-Host "Added $InstallDir to your user PATH"
    }
  }
} finally {
  if (Test-Path $tempDir) {
    Remove-Item -Path $tempDir -Recurse -Force
  }
}
