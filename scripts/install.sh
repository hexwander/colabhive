#!/bin/sh
# ==============================================================================
# ColabHive — 1-Line Cross-Platform Installer for Linux & macOS (Apple Silicon / Intel)
# ==============================================================================
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.sh | sh
#
# Environment overrides:
#   COLABHIVE_REPO        GitHub owner/repo (default: hexwander/colabhive)
#   COLABHIVE_VERSION     Release tag or "latest" (default: latest)
#   COLABHIVE_INSTALL_DIR Installation root (default: ~/.colabhive)
#   COLABHIVE_BIN_DIR     Symlink directory (default: ~/.local/bin)
#   COLABHIVE_APP_DIR     macOS .app destination (default: /Applications or ~/Applications)
#   COLABHIVE_SKIP_APP    Set to 1 on macOS to skip installing ColabHive.app
# ==============================================================================

set -eu

REPO="${COLABHIVE_REPO:-hexwander/colabhive}"
VERSION="${COLABHIVE_VERSION:-latest}"
INSTALL_DIR="${COLABHIVE_INSTALL_DIR:-$HOME/.colabhive}"
BIN_DIR="${COLABHIVE_BIN_DIR:-$HOME/.local/bin}"
APP_DIR_OVERRIDE="${COLABHIVE_APP_DIR:-}"
SKIP_APP="${COLABHIVE_SKIP_APP:-0}"

log_info() {
  printf '[colabhive-install] %s\n' "$*"
}

log_err() {
  printf '[colabhive-install] ERROR: %s\n' "$*" >&2
}

# 1. Detect OS and CPU architecture
UNAME_S="$(uname -s 2>/dev/null || echo unknown)"
case "$UNAME_S" in
  Linux*)
    OS="linux"
    ;;
  Darwin*)
    OS="darwin"
    ;;
  MINGW*|MSYS*|CYGWIN*)
    log_err "Detected Windows shell ($UNAME_S). Run the PowerShell installer instead:"
    log_err "  irm https://raw.githubusercontent.com/${REPO}/main/scripts/install.ps1 | iex"
    exit 1
    ;;
  *)
    log_err "Unsupported operating system: $UNAME_S (supported: Linux, Darwin)."
    exit 1
    ;;
esac

UNAME_M="$(uname -m 2>/dev/null || echo unknown)"
case "$UNAME_M" in
  x86_64|amd64)
    ARCH="amd64"
    ;;
  aarch64|arm64)
    ARCH="arm64"
    ;;
  *)
    log_err "Unsupported CPU architecture: $UNAME_M (supported: x86_64/amd64, aarch64/arm64)."
    exit 1
    ;;
esac

# 2. Check required CLI utilities
FETCH_TOOL=""
if command -v curl >/dev/null 2>&1; then
  FETCH_TOOL="curl"
elif command -v wget >/dev/null 2>&1; then
  FETCH_TOOL="wget"
else
  log_err "Neither 'curl' nor 'wget' was found in PATH."
  exit 1
fi

if ! command -v tar >/dev/null 2>&1; then
  log_err "'tar' is required to extract release archives."
  exit 1
fi

HASH_TOOL=""
if command -v sha256sum >/dev/null 2>&1; then
  HASH_TOOL="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  HASH_TOOL="shasum"
elif command -v openssl >/dev/null 2>&1; then
  HASH_TOOL="openssl"
else
  log_err "No SHA-256 tool found (expected 'sha256sum', 'shasum', or 'openssl')."
  exit 1
fi

http_get_text() {
  url="$1"
  if [ "$FETCH_TOOL" = "curl" ]; then
    if [ -n "${GITHUB_TOKEN:-}" ]; then
      curl -fsSL -H "User-Agent: ColabHive-Unix-Installer/1.0" -H "Accept: application/vnd.github+json" -H "Authorization: Bearer ${GITHUB_TOKEN}" "$url"
    else
      curl -fsSL -H "User-Agent: ColabHive-Unix-Installer/1.0" -H "Accept: application/vnd.github+json" "$url"
    fi
  else
    if [ -n "${GITHUB_TOKEN:-}" ]; then
      wget -qO- --header="User-Agent: ColabHive-Unix-Installer/1.0" --header="Accept: application/vnd.github+json" --header="Authorization: Bearer ${GITHUB_TOKEN}" "$url"
    else
      wget -qO- --header="User-Agent: ColabHive-Unix-Installer/1.0" --header="Accept: application/vnd.github+json" "$url"
    fi
  fi
}

http_download_file() {
  url="$1"
  dest="$2"
  if [ "$FETCH_TOOL" = "curl" ]; then
    curl -fsSL -H "User-Agent: ColabHive-Unix-Installer/1.0" -o "$dest" "$url"
  else
    wget -q --header="User-Agent: ColabHive-Unix-Installer/1.0" -O "$dest" "$url"
  fi
}

compute_sha256() {
  file="$1"
  case "$HASH_TOOL" in
    sha256sum)
      sha256sum "$file" | awk '{print tolower($1)}'
      ;;
    shasum)
      shasum -a 256 "$file" | awk '{print tolower($1)}'
      ;;
    openssl)
      openssl dgst -sha256 "$file" | awk '{print tolower($NF)}'
      ;;
  esac
}

lookup_expected_sha256() {
  sums_file="$1"
  asset_name="$2"
  awk -v target="$asset_name" '
    NF >= 2 {
      fname = $2
      sub(/^\*/, "", fname)
      sub(/^.*\//, "", fname)
      if (fname == target) {
        print tolower($1)
        exit
      }
    }
  ' "$sums_file"
}

# 3. Query GitHub Releases API
if [ "$VERSION" = "latest" ] || [ -z "$VERSION" ]; then
  API_URL="https://api.github.com/repos/${REPO}/releases/latest"
else
  case "$VERSION" in
    v*) TAG_QUERY="$VERSION" ;;
    *)  TAG_QUERY="v$VERSION" ;;
  esac
  API_URL="https://api.github.com/repos/${REPO}/releases/tags/${TAG_QUERY}"
fi

log_info "Resolving release metadata from ${REPO} (${VERSION}) for ${OS}/${ARCH}..."
RELEASE_JSON="$(http_get_text "$API_URL")" || {
  log_err "Failed to query GitHub Releases API: $API_URL"
  exit 1
}

TAG_NAME="$(printf '%s\n' "$RELEASE_JSON" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
if [ -z "$TAG_NAME" ]; then
  log_err "Could not parse tag_name from GitHub Releases response ($API_URL)."
  exit 1
fi

ARCHIVE_URL="$(printf '%s\n' "$RELEASE_JSON" | grep -o "https://[^\"]*colabhive_[^/\"]*_${OS}_${ARCH}\\.tar\\.gz" | head -n 1 || true)"
if [ -z "$ARCHIVE_URL" ]; then
  log_err "Could not find release asset matching 'colabhive_*_${OS}_${ARCH}.tar.gz' in ${TAG_NAME}."
  exit 1
fi
ARCHIVE_NAME="${ARCHIVE_URL##*/}"

SUMS_URL="$(printf '%s\n' "$RELEASE_JSON" | grep -o 'https://[^"]*/SHA256SUMS\.txt' | head -n 1 || true)"
if [ -z "$SUMS_URL" ]; then
  log_err "Release ${TAG_NAME} is missing mandatory SHA256SUMS.txt checksum manifest."
  exit 1
fi

TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t colabhive-install)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

ARCHIVE_PATH="${TMP_DIR}/${ARCHIVE_NAME}"
SUMS_PATH="${TMP_DIR}/SHA256SUMS.txt"

# 4. Download tarball and SHA256SUMS.txt
log_info "Downloading ${ARCHIVE_NAME} (${TAG_NAME})..."
http_download_file "$ARCHIVE_URL" "$ARCHIVE_PATH"
http_download_file "$SUMS_URL" "$SUMS_PATH"

# 5. Verify SHA-256 checksum
log_info "Verifying SHA-256 checksum via ${HASH_TOOL}..."
EXPECTED_HASH="$(lookup_expected_sha256 "$SUMS_PATH" "$ARCHIVE_NAME")"

if [ -z "$EXPECTED_HASH" ]; then
  log_err "Checksum entry for '${ARCHIVE_NAME}' not found in SHA256SUMS.txt. Aborting."
  exit 1
fi

ACTUAL_HASH="$(compute_sha256 "$ARCHIVE_PATH")"
if [ "$ACTUAL_HASH" != "$EXPECTED_HASH" ]; then
  log_err "SHA-256 verification failed for ${ARCHIVE_NAME}!"
  log_err "  Expected: ${EXPECTED_HASH}"
  log_err "  Actual  : ${ACTUAL_HASH}"
  exit 1
fi
log_info "SHA-256 verified (${ACTUAL_HASH})"

# 6. Extract and install under ~/.colabhive (preserving ~/.colabhive/data!)
EXTRACT_DIR="${TMP_DIR}/extract"
mkdir -p "$EXTRACT_DIR"
tar -xzf "$ARCHIVE_PATH" -C "$EXTRACT_DIR"

SRC_ROOT="$EXTRACT_DIR"
if [ -f "${EXTRACT_DIR}/colabhive_${OS}_${ARCH}/colabhive" ]; then
  SRC_ROOT="${EXTRACT_DIR}/colabhive_${OS}_${ARCH}"
else
  FOUND_BIN="$(find "$EXTRACT_DIR" -type f -name colabhive 2>/dev/null | head -n 1 || true)"
  if [ -n "$FOUND_BIN" ]; then
    SRC_ROOT="$(dirname "$FOUND_BIN")"
  fi
fi

if [ ! -f "${SRC_ROOT}/colabhive" ]; then
  log_err "Extracted archive did not contain 'colabhive' binary."
  exit 1
fi

log_info "Installing ColabHive ${TAG_NAME} into ${INSTALL_DIR}..."
mkdir -p "$INSTALL_DIR"
mkdir -p "${INSTALL_DIR}/data"
chmod 700 "${INSTALL_DIR}/data" 2>/dev/null || true

cp -f "${SRC_ROOT}/colabhive" "${INSTALL_DIR}/colabhive"
chmod 755 "${INSTALL_DIR}/colabhive"

if [ -f "${SRC_ROOT}/colab" ] && [ ! -L "${SRC_ROOT}/colab" ]; then
  cp -f "${SRC_ROOT}/colab" "${INSTALL_DIR}/colab"
  chmod 755 "${INSTALL_DIR}/colab"
else
  ln -sf "${INSTALL_DIR}/colabhive" "${INSTALL_DIR}/colab"
fi

if [ -f "${SRC_ROOT}/colabhive-desktop" ]; then
  cp -f "${SRC_ROOT}/colabhive-desktop" "${INSTALL_DIR}/colabhive-desktop"
  chmod 755 "${INSTALL_DIR}/colabhive-desktop"
fi

if [ -f "${SRC_ROOT}/web/dist/index.html" ]; then
  rm -rf "${INSTALL_DIR}/web/dist"
  mkdir -p "${INSTALL_DIR}/web"
  cp -R "${SRC_ROOT}/web/dist" "${INSTALL_DIR}/web/dist"
elif [ -f "${SRC_ROOT}/web-dist/index.html" ]; then
  rm -rf "${INSTALL_DIR}/web/dist"
  mkdir -p "${INSTALL_DIR}/web"
  cp -R "${SRC_ROOT}/web-dist" "${INSTALL_DIR}/web/dist"
fi

for doc in LICENSE README.md README.zh-CN.md; do
  if [ -f "${SRC_ROOT}/${doc}" ]; then
    cp -f "${SRC_ROOT}/${doc}" "${INSTALL_DIR}/${doc}"
  fi
done

# 7. On macOS (`darwin/arm64` & `darwin/amd64`), also install Native `ColabHive.app` when published!
INSTALLED_APP_PATH=""
if [ "$OS" = "darwin" ] && [ "$SKIP_APP" != "1" ]; then
  MAC_APP_URL="$(printf '%s\n' "$RELEASE_JSON" | grep -o "https://[^\"]*ColabHive_macOS_${ARCH}\\.app\\.zip" | head -n 1 || true)"
  if [ -n "$MAC_APP_URL" ] && (command -v ditto >/dev/null 2>&1 || command -v unzip >/dev/null 2>&1); then
    MAC_APP_NAME="${MAC_APP_URL##*/}"
    MAC_APP_ZIP="${TMP_DIR}/${MAC_APP_NAME}"
    log_info "Downloading Native macOS Desktop App ${MAC_APP_NAME}..."
    if http_download_file "$MAC_APP_URL" "$MAC_APP_ZIP"; then
      EXPECTED_APP_HASH="$(lookup_expected_sha256 "$SUMS_PATH" "$MAC_APP_NAME")"
      ACTUAL_APP_HASH="$(compute_sha256 "$MAC_APP_ZIP")"
      if [ -n "$EXPECTED_APP_HASH" ] && [ "$ACTUAL_APP_HASH" = "$EXPECTED_APP_HASH" ]; then
        APP_EXTRACT="${TMP_DIR}/app-extract"
        mkdir -p "$APP_EXTRACT"
        if command -v ditto >/dev/null 2>&1; then
          ditto -x -k "$MAC_APP_ZIP" "$APP_EXTRACT"
        else
          unzip -q "$MAC_APP_ZIP" -d "$APP_EXTRACT"
        fi
        if [ -d "${APP_EXTRACT}/ColabHive.app" ]; then
          TARGET_APPS_DIR="$APP_DIR_OVERRIDE"
          if [ -z "$TARGET_APPS_DIR" ]; then
            if [ -d "/Applications" ] && [ -w "/Applications" ]; then
              TARGET_APPS_DIR="/Applications"
            else
              TARGET_APPS_DIR="$HOME/Applications"
            fi
          fi
          mkdir -p "$TARGET_APPS_DIR"
          rm -rf "${TARGET_APPS_DIR}/ColabHive.app"
          cp -R "${APP_EXTRACT}/ColabHive.app" "${TARGET_APPS_DIR}/ColabHive.app"
          if command -v xattr >/dev/null 2>&1; then
            xattr -dr com.apple.quarantine "${TARGET_APPS_DIR}/ColabHive.app" 2>/dev/null || true
          fi
          INSTALLED_APP_PATH="${TARGET_APPS_DIR}/ColabHive.app"
          log_info "Installed Native macOS App -> ${INSTALLED_APP_PATH}"
        fi
      else
        log_err "Skipping ${MAC_APP_NAME}: SHA-256 checksum mismatch or missing entry."
      fi
    fi
  fi
fi

# 8. Link `colabhive`, `colab`, and `colabhive-desktop` into ~/.local/bin
mkdir -p "$BIN_DIR"
ln -sf "${INSTALL_DIR}/colabhive" "${BIN_DIR}/colabhive"
ln -sf "${INSTALL_DIR}/colab" "${BIN_DIR}/colab"
if [ -n "$INSTALLED_APP_PATH" ] && [ -x "${INSTALLED_APP_PATH}/Contents/MacOS/colabhive-desktop" ]; then
  ln -sf "${INSTALLED_APP_PATH}/Contents/MacOS/colabhive-desktop" "${BIN_DIR}/colabhive-desktop"
elif [ -x "${INSTALL_DIR}/colabhive-desktop" ]; then
  ln -sf "${INSTALL_DIR}/colabhive-desktop" "${BIN_DIR}/colabhive-desktop"
fi

printf '\n'
printf '==================================================================\n'
printf '  ColabHive %s installed successfully!\n' "$TAG_NAME"
printf '==================================================================\n'
printf '  Install Root : %s\n' "$INSTALL_DIR"
printf '  CLI Symlinks : %s/colabhive, %s/colab\n' "$BIN_DIR" "$BIN_DIR"
if [ -n "$INSTALLED_APP_PATH" ]; then
  printf '  macOS App    : %s (Native Menu Bar + Frameless Shell)\n' "$INSTALLED_APP_PATH"
fi
printf '  Vault Dir    : %s/data (AES-256-GCM encrypted)\n' "$INSTALL_DIR"

case ":${PATH}:" in
  *":${BIN_DIR}:"*)
    ;;
  *)
    printf '\n'
    printf '  NOTE: Add %s to your PATH if not already configured:\n' "$BIN_DIR"
    printf '    export PATH="%s:$PATH"\n' "$BIN_DIR"
    ;;
esac

printf '\n'
printf '  Quickstart:\n'
if [ -n "$INSTALLED_APP_PATH" ]; then
  printf '    1. Launch Native macOS Desktop & Menu Bar App:\n'
  printf '       open "%s"\n' "$INSTALLED_APP_PATH"
  printf '    2. Or start headless Control Plane + Web Observatory on http://127.0.0.1:7860:\n'
  printf '       colabhive serve --web-dir "%s/web/dist" --data-dir "%s/data"\n' "$INSTALL_DIR" "$INSTALL_DIR"
else
  printf '    1. Start Control Plane + Web Observatory on http://127.0.0.1:7860:\n'
  printf '       colabhive serve --web-dir "%s/web/dist" --data-dir "%s/data"\n' "$INSTALL_DIR" "$INSTALL_DIR"
  printf '    2. Or explore the interactive 4-Account Demo Cluster immediately:\n'
  printf '       colabhive serve --web-dir "%s/web/dist" --demo\n' "$INSTALL_DIR"
fi
printf '    3. Check pooled GPU quota or connect your AI IDE via MCP:\n'
printf '       colab usage --gpu-hours\n'
printf '       colabhive mcp\n'
printf '\n'
