# install.ps1 — one-line installer for crunchy (Windows).
#
#   irm https://raw.githubusercontent.com/byescaleira/crunchy/master/install.ps1 | iex
#
# Downloads the matching prebuilt crunchy.exe from the latest GitHub release, installs
# it to $env:LOCALAPPDATA\Programs\crunchy and adds that dir to the user PATH, tries to
# install ffmpeg via winget/choco, and checks for a Widevine .wvd in the data-dir.
# Pure Go binary, so no Go toolchain is needed.

$ErrorActionPreference = 'Stop'

$Repo = 'byescaleira/crunchy'
$GitHub = 'https://github.com'

function Info($m) { Write-Host "crunchy install: $m" -ForegroundColor White }
function Warn($m) { Write-Host "crunchy install: $m" -ForegroundColor Yellow }
function Fail($m) { Write-Host "crunchy install: $m" -ForegroundColor Red; exit 1 }

# --- arch detection ----------------------------------------------------------
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'ARM64' { 'arm64' }
    'AMD64' { 'amd64' }
    default { Fail "unsupported arch: $env:PROCESSOR_ARCHITECTURE" }
}

$asset = "crunchy-windows-$arch.exe"
$url = if ($env:CRUNCHY_DOWNLOAD_URL) { $env:CRUNCHY_DOWNLOAD_URL } else { "$GitHub/$Repo/releases/latest/download/$asset" }

# --- install dir -------------------------------------------------------------
$installDir = if ($env:CRUNCHY_INSTALL_DIR) { $env:CRUNCHY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\crunchy' }
if (-not (Test-Path $installDir)) { New-Item -ItemType Directory -Path $installDir -Force | Out-Null }
$dest = Join-Path $installDir 'crunchy.exe'

# --- download ----------------------------------------------------------------
Info "downloading $url"
try {
    Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing
} catch {
    Fail "download failed: $($_.Exception.Message) — release $asset may not exist yet (publish a release first)"
}
if ((Get-Item $dest).Length -eq 0) { Fail "downloaded file is empty" }

# --- add to user PATH (idempotent) ------------------------------------------
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$installDir*") {
    Info "adding $installDir to user PATH"
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    Warn "PATH updated — open a new terminal for 'crunchy' to be found."
}

Info "installed crunchy to $dest"

# --- ffmpeg ------------------------------------------------------------------
if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue)) {
    if (Get-Command winget -ErrorAction SilentlyContinue) {
        Info "installing ffmpeg via winget"
        winget install Gyan.FFmpeg --accept-source-agreements --accept-package-agreements
    } elseif (Get-Command choco -ErrorAction SilentlyContinue) {
        Info "installing ffmpeg via chocolatey"
        choco install ffmpeg -y
    } else {
        Warn "ffmpeg not found and no winget/choco. Install ffmpeg (https://ffmpeg.org/download.html) — muxing needs it."
    }
    if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue)) {
        Warn "ffmpeg still not on PATH — open a new terminal, or muxing will fail."
    }
} else {
    Info "ffmpeg already present"
}

# --- Widevine .wvd -----------------------------------------------------------
$dataDir = Join-Path $env:LOCALAPPDATA 'crunchy-data'
$hasWvd = (Test-Path $dataDir) -and ((Get-ChildItem -Path $dataDir -Filter *.wvd -ErrorAction SilentlyContinue) -or
          (Test-Path (Join-Path $dataDir 'client_id.bin') -PathType Leaf) -and (Test-Path (Join-Path $dataDir 'private_key.pem') -PathType Leaf))
if ($hasWvd) {
    Info "Widevine device found in $dataDir"
} else {
    Warn "no Widevine .wvd (or client_id.bin + private_key.pem) in $dataDir."
    Warn "  Crunchy needs a CDM for DRM downloads. Place a .wvd in $dataDir\"
    Warn "  (or run crunchy from a folder that contains one). Search 'ready to use cdms' on Google."
}

# --- done --------------------------------------------------------------------
Write-Host ""
Write-Host "Done." -ForegroundColor Green
Write-Host "  Open a new terminal and run: crunchy"
Write-Host "  The panel opens in your browser at http://<your-LAN-IP>:8080 (binds all interfaces by default)."
Write-Host "  Restrict to localhost with: crunchy -addr 127.0.0.1:8080"