#!/usr/bin/env bash
# install.sh — one-line installer for crunchy (macOS + Linux).
#
#   curl -fsSL https://raw.githubusercontent.com/byescaleira/crunchy/master/install.sh | bash
#
# Downloads the matching prebuilt `crunchy` binary from the latest GitHub release,
# installs it on PATH, auto-installs ffmpeg (if a package manager is available),
# and checks for a Widevine .wvd in the data-dir. Pure Go binary, so no Go toolchain
# is needed on the user's machine.
#
# Env overrides (mainly for testing):
#   CRUNCHY_INSTALL_DIR   install the binary here instead of auto-detecting a PATH dir
#   CRUNCHY_DOWNLOAD_URL  download this URL instead of releases/latest/download/<asset>
set -euo pipefail

REPO="byescaleira/crunchy"
GITHUB="https://github.com"

err()  { printf '\033[31mcrunchy install: %s\033[0m\n' "$*" >&2; }
info() { printf '\033[1mcrunchy install:\033[0m %s\n' "$*"; }
warn() { printf '\033[33mcrunchy install: %s\033[0m\n' "$*"; }

# --- OS / arch detection ------------------------------------------------------
case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux)  os="linux" ;;
    *) err "unsupported OS: $(uname -s) (this installer covers macOS + Linux; Windows uses install.ps1)"; exit 1 ;;
esac

case "$(uname -m)" in
    arm64|aarch64) arch="arm64" ;;
    x86_64|amd64)  arch="amd64" ;;
    *) err "unsupported arch: $(uname -m)"; exit 1 ;;
esac

asset="crunchy-${os}-${arch}"
url="${CRUNCHY_DOWNLOAD_URL:-${GITHUB}/${REPO}/releases/latest/download/${asset}}"

# --- Download ----------------------------------------------------------------
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
bin="${tmpdir}/${asset}"

info "downloading ${url}"
if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$bin"
elif command -v wget >/dev/null 2>&1; then
    wget -qO "$bin" "$url"
else
    err "need curl or wget to download the binary"; exit 1
fi

if [ ! -s "$bin" ]; then
    err "downloaded file is empty — release ${asset} may not exist yet (publish a release first)"; exit 1
fi
chmod +x "$bin"

# --- Install location --------------------------------------------------------
install_dir="${CRUNCHY_INSTALL_DIR:-}"
if [ -z "$install_dir" ]; then
    if [ -w "/usr/local/bin" ]; then
        install_dir="/usr/local/bin"
    elif sudo -n true 2>/dev/null; then
        install_dir="/usr/local/bin"
        SUDO="sudo"
    else
        install_dir="$HOME/.local/bin"
    fi
fi
mkdir -p "$install_dir"

if [ "${SUDO:-}" = "sudo" ]; then
    sudo install -m 0755 "$bin" "${install_dir}/crunchy"
else
    install -m 0755 "$bin" "${install_dir}/crunchy"
fi

# Warn if the install dir isn't on PATH (only for the no-sudo fallback).
case ":${PATH}:" in
    *":${install_dir}:"*) ;;
    *) warn "$install_dir is not on your PATH. Add it: export PATH=\"${install_dir}:\$PATH\" (in your shell rc)";;
esac

info "installed crunchy to ${install_dir}/crunchy"

# --- ffmpeg / ffprobe ---------------------------------------------------------
ensure_ffmpeg() {
    if command -v ffmpeg >/dev/null 2>&1 && command -v ffprobe >/dev/null 2>&1; then
        info "ffmpeg already present"; return 0
    fi
    case "$os" in
        darwin)
            if command -v brew >/dev/null 2>&1; then
                info "installing ffmpeg via Homebrew"
                brew install ffmpeg || warn "brew install ffmpeg failed — install ffmpeg manually"
            else
                warn "ffmpeg not found and Homebrew is not installed. Install Homebrew (https://brew.sh) then: brew install ffmpeg"
            fi
            ;;
        linux)
            if command -v apt-get >/dev/null 2>&1; then
                info "installing ffmpeg via apt"
                sudo apt-get update -y && sudo apt-get install -y ffmpeg
            elif command -v dnf >/dev/null 2>&1; then
                info "installing ffmpeg via dnf"
                sudo dnf install -y ffmpeg
            elif command -v yum >/dev/null 2>&1; then
                info "installing ffmpeg via yum"
                sudo yum install -y ffmpeg
            elif command -v pacman >/dev/null 2>&1; then
                info "installing ffmpeg via pacman"
                sudo pacman -S --noconfirm ffmpeg
            else
                warn "ffmpeg not found and no known package manager. Install ffmpeg with your system's package manager."
            fi
            ;;
    esac
    if ! command -v ffmpeg >/dev/null 2>&1; then
        warn "ffmpeg still not on PATH — muxing will fail until it is installed"
    fi
}
ensure_ffmpeg

# --- Widevine .wvd ------------------------------------------------------------
data_dir="$HOME/.crunchy-data"
if ls "$data_dir"/*.wvd >/dev/null 2>&1 || { [ -f "$data_dir/client_id.bin" ] && [ -f "$data_dir/private_key.pem" ]; }; then
    info "Widevine device found in ${data_dir}"
else
    warn "no Widevine .wvd (or client_id.bin + private_key.pem) in ${data_dir}."
    warn "  Crunchy needs a CDM for DRM downloads. Place a .wvd in ${data_dir}/"
    warn "  (or run crunchy from a dir that contains one). Search 'ready to use cdms' on Google."
fi

# --- Done ---------------------------------------------------------------------
printf '\n\033[32mDone.\033[0m Run \033[1mcrunchy\033[0m to start — the panel opens in your browser\n'
echo "  at http://<your-LAN-IP>:8080 (binds all interfaces by default)."
echo "  Restrict to localhost with: crunchy -addr 127.0.0.1:8080"