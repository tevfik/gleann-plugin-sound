# One-liner installer for gleann-plugin-sound (Windows PowerShell)
$ErrorActionPreference = "Stop"

Write-Host "=== Installing gleann-plugin-sound ===" -ForegroundColor Cyan

# Detect Architecture
$Arch = $env:PROCESSOR_ARCHITECTURE
if ($Arch -eq "AMD64") {
    $Arch = "amd64"
} elseif ($Arch -eq "ARM64") {
    $Arch = "arm64"
} else {
    Write-Error "Unsupported architecture: $Arch"
    exit 1
}

# Create install directory
$InstallDir = Join-Path $HOME ".local\bin"
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
}

# Resolve latest release tag
$Repo = "tevfik/gleann-plugin-sound"
Write-Host "Fetching latest release info..." -ForegroundColor Yellow
$ReleaseUrl = "https://api.github.com/repos/$Repo/releases/latest"
$Response = Invoke-RestMethod -Uri $ReleaseUrl -UseBasicParsing
$LatestTag = $Response.tag_name

if (-not $LatestTag) {
    Write-Error "Could not retrieve latest release tag."
    exit 1
}

Write-Host "Found release: $LatestTag" -ForegroundColor Green

# Download URL
$CleanTag = $LatestTag.TrimStart('v')
$ZipFile = "gleann-plugin-sound_${CleanTag}_windows_${Arch}.zip"
$Url = "https://github.com/$Repo/releases/download/$LatestTag/$ZipFile"

Write-Host "Downloading binary from: $Url" -ForegroundColor Yellow
$TempDir = Join-Path $env:TEMP "gleann-sound-install"
if (Test-Path $TempDir) {
    Remove-Item -Recurse -Force $TempDir
}
New-Item -ItemType Directory -Force -Path $TempDir | Out-Null

$DownloadPath = Join-Path $TempDir $ZipFile
Invoke-WebRequest -Uri $Url -OutFile $DownloadPath -UseBasicParsing

# Extract and install
Write-Host "Extracting to $InstallDir..." -ForegroundColor Yellow
Expand-Archive -Path $DownloadPath -DestinationPath $TempDir -Force
Move-Item -Path (Join-Path $TempDir "gleann-plugin-sound.exe") -Destination $InstallDir -Force

# Clean up
Remove-Item -Recurse -Force $TempDir

# Check Path environment variable
$PathEnv = [Environment]::GetEnvironmentVariable("Path", "User")
if ($PathEnv -notlike "*$InstallDir*") {
    Write-Host "Adding $InstallDir to User Path..." -ForegroundColor Yellow
    [Environment]::SetEnvironmentVariable("Path", $PathEnv + ";" + $InstallDir, "User")
    $env:Path += ";" + $InstallDir
}

Write-Host "✓ gleann-plugin-sound installed successfully to $InstallDir\gleann-plugin-sound.exe" -ForegroundColor Green
Write-Host "Starting configuration wizard..." -ForegroundColor Yellow
Start-Process -FilePath (Join-Path $InstallDir "gleann-plugin-sound.exe") -ArgumentList "tui" -NoNewWindow -Wait
