#!/usr/bin/env bash
# ==============================================================================
# ColabHive — Cross-Platform Release Builder (`scripts/build-release.sh`)
# ==============================================================================
# Builds:
#   1. Frontend Observatory, Workbench & Magpie Tray SPA (`web/dist`)
#   2. Host-native `bin/colabhive` + `bin/colab` static binaries (`CGO_ENABLED=0`)
#   3. Windows Frameless Native Desktop App `bin/colabhive-desktop.exe`
#      (`-ldflags "-s -w -H=windowsgui"`, pure-Go cross-compile, zero MinGW!)
#   4. macOS Apple Silicon (`arm64`) & Intel (`amd64`) `ColabHive.app` bundle
#      (Cocoa + `WKWebView` + Menu Bar `NSPopover` + `AppIcon.icns`)
#
# Usage:
#   ./scripts/build-release.sh
#   ./scripts/build-release.sh --version 1.1.0 --skip-web
#   ./scripts/build-release.sh --all-platforms
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

VERSION="${COLABHIVE_VERSION:-1.0.0}"
OUT_DIR="bin"
SKIP_WEB=0
SKIP_DESKTOP=0
REQUIRE_DESKTOP=0
ALL_PLATFORMS=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      VERSION="$2"
      shift 2
      ;;
    --out-dir)
      OUT_DIR="$2"
      shift 2
      ;;
    --skip-web)
      SKIP_WEB=1
      shift
      ;;
    --skip-desktop)
      SKIP_DESKTOP=1
      shift
      ;;
    --require-desktop)
      REQUIRE_DESKTOP=1
      shift
      ;;
    --all-platforms)
      ALL_PLATFORMS=1
      shift
      ;;
    -h|--help)
      sed -n '2,18p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "[build-release] Unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo "dev")"
BUILD_DATE="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

BASE_LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"
DESKTOP_LDFLAGS="-s -w -H=windowsgui -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"

CLEAN_PLIST_VER="${VERSION#v}"
case "${CLEAN_PLIST_VER}" in
  [0-9]*) ;;
  *) CLEAN_PLIST_VER="1.0.0" ;;
esac

assemble_macos_app_bundle() {
  local target_arch="$1"
  local dest_parent="$2"
  local app_dir="${dest_parent}/ColabHive.app"

  rm -rf "${app_dir}"
  mkdir -p "${app_dir}/Contents/MacOS" "${app_dir}/Contents/Resources/web"

  if [[ "$(uname -s 2>/dev/null || echo unknown)" == "Darwin" ]] && command -v clang >/dev/null 2>&1; then
    CGO_ENABLED=1 GOOS=darwin GOARCH="${target_arch}" \
      go build -trimpath -ldflags "${BASE_LDFLAGS}" \
      -o "${app_dir}/Contents/MacOS/colabhive-desktop" ./cmd/colabhive-desktop
  else
    CGO_ENABLED=0 GOOS=darwin GOARCH="${target_arch}" \
      go build -trimpath -ldflags "${BASE_LDFLAGS}" \
      -o "${app_dir}/Contents/MacOS/colabhive-desktop" ./cmd/colabhive-desktop
  fi

  CGO_ENABLED=0 GOOS=darwin GOARCH="${target_arch}" \
    go build -trimpath -ldflags "${BASE_LDFLAGS}" \
    -o "${app_dir}/Contents/MacOS/colabhive" ./cmd/colabhive

  if [[ -d "./cmd/colab" ]]; then
    CGO_ENABLED=0 GOOS=darwin GOARCH="${target_arch}" \
      go build -trimpath -ldflags "${BASE_LDFLAGS}" \
      -o "${app_dir}/Contents/MacOS/colab" ./cmd/colab
  else
    cp -f "${app_dir}/Contents/MacOS/colabhive" "${app_dir}/Contents/MacOS/colab"
  fi

  CGO_ENABLED=0 go run ./cmd/colabhive-desktop \
    --write-icns "${app_dir}/Contents/Resources/AppIcon.icns"

  if [[ -f "web/dist/index.html" ]]; then
    cp -R "web/dist" "${app_dir}/Contents/Resources/web/dist"
  fi

  cat > "${app_dir}/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>ColabHive</string>
  <key>CFBundleDisplayName</key>
  <string>ColabHive</string>
  <key>CFBundleIdentifier</key>
  <string>com.colabhive.desktop</string>
  <key>CFBundleVersion</key>
  <string>${CLEAN_PLIST_VER}</string>
  <key>CFBundleShortVersionString</key>
  <string>${CLEAN_PLIST_VER}</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleExecutable</key>
  <string>colabhive-desktop</string>
  <key>CFBundleIconFile</key>
  <string>AppIcon</string>
  <key>LSMinimumSystemVersion</key>
  <string>11.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>NSSupportsAutomaticGraphicsSwitching</key>
  <true/>
  <key>NSAppTransportSecurity</key>
  <dict>
    <key>NSAllowsLocalNetworking</key>
    <true/>
  </dict>
</dict>
</plist>
EOF

  if command -v codesign >/dev/null 2>&1; then
    codesign --force --deep --sign - "${app_dir}" >/dev/null 2>&1 || true
  fi
}

echo "=================================================================="
echo " ColabHive Release Build  v${VERSION} (${COMMIT} @ ${BUILD_DATE})"
echo "=================================================================="

# ------------------------------------------------------------------------------
# Step 1/4: Build Frontend SPA (`web/dist`)
# ------------------------------------------------------------------------------
if [[ "${SKIP_WEB}" -eq 0 ]]; then
  echo ""
  echo "[1/4] Building Frontend SPA (web/dist)..."
  if ! command -v pnpm >/dev/null 2>&1; then
    echo "[build-release] Error: pnpm not found in PATH (run 'corepack enable')." >&2
    exit 1
  fi
  if [[ ! -d "web/node_modules" ]]; then
    pnpm --dir web install --frozen-lockfile || pnpm --dir web install
  fi
  pnpm --dir web run build
else
  echo ""
  echo "[1/4] Skipping Frontend SPA build (--skip-web)."
fi

if [[ ! -f "web/dist/index.html" ]]; then
  echo "[build-release] Warning: web/dist/index.html missing; run without --skip-web before packaging UI." >&2
fi

mkdir -p "${OUT_DIR}"

if [[ -f "web/dist/index.html" ]]; then
  SRC_WEB_DIST="$(cd "web/dist" && pwd)"
  mkdir -p "${OUT_DIR}/web/dist"
  DST_WEB_DIST="$(cd "${OUT_DIR}/web/dist" && pwd)"
  if [[ "${SRC_WEB_DIST}" != "${DST_WEB_DIST}" ]]; then
    rm -rf "${OUT_DIR}/web/dist"
    mkdir -p "${OUT_DIR}/web"
    cp -R "web/dist" "${OUT_DIR}/web/dist"
  fi
fi

HOST_GOOS="$(go env GOOS)"
HOST_GOARCH="$(go env GOARCH)"
EXE_EXT=""
if [[ "${HOST_GOOS}" == "windows" ]]; then
  EXE_EXT=".exe"
fi

# ------------------------------------------------------------------------------
# Step 2/4: Build Host `bin/colabhive`
# ------------------------------------------------------------------------------
echo "[2/4] Building Control Plane & MCP Daemon -> ${OUT_DIR}/colabhive${EXE_EXT} (${HOST_GOOS}/${HOST_GOARCH})"
CGO_ENABLED=0 GOOS="${HOST_GOOS}" GOARCH="${HOST_GOARCH}" \
  go build -trimpath -ldflags "${BASE_LDFLAGS}" -o "${OUT_DIR}/colabhive${EXE_EXT}" ./cmd/colabhive

# ------------------------------------------------------------------------------
# Step 3/4: Build Host `bin/colab`
# ------------------------------------------------------------------------------
echo "[3/4] Building Drop-In Colab CLI Alias    -> ${OUT_DIR}/colab${EXE_EXT} (${HOST_GOOS}/${HOST_GOARCH})"
if [[ -d "./cmd/colab" ]]; then
  CGO_ENABLED=0 GOOS="${HOST_GOOS}" GOARCH="${HOST_GOARCH}" \
    go build -trimpath -ldflags "${BASE_LDFLAGS}" -o "${OUT_DIR}/colab${EXE_EXT}" ./cmd/colab
else
  cp -f "${OUT_DIR}/colabhive${EXE_EXT}" "${OUT_DIR}/colab${EXE_EXT}"
fi

# ------------------------------------------------------------------------------
# Step 4/4: Build Native Desktop App (`ColabHive.app` on macOS + `colabhive-desktop.exe` for Windows)
# ------------------------------------------------------------------------------
if [[ "${SKIP_DESKTOP}" -eq 0 ]]; then
  if [[ -d "./cmd/colabhive-desktop" ]]; then
    if [[ "${HOST_GOOS}" == "darwin" ]]; then
      echo "[4/4] Building Native macOS App Bundle    -> ${OUT_DIR}/ColabHive.app (darwin/${HOST_GOARCH})"
      assemble_macos_app_bundle "${HOST_GOARCH}" "${OUT_DIR}"
      cp -f "${OUT_DIR}/ColabHive.app/Contents/MacOS/colabhive-desktop" "${OUT_DIR}/colabhive-desktop"
    fi
    WIN_ARCH="amd64"
    if [[ "${HOST_GOOS}" == "windows" ]]; then
      WIN_ARCH="${HOST_GOARCH}"
    fi
    echo "[4/4] Building Windows Native Desktop App -> ${OUT_DIR}/colabhive-desktop.exe (windows/${WIN_ARCH}, -H=windowsgui)"
    CGO_ENABLED=0 GOOS=windows GOARCH="${WIN_ARCH}" \
      go build -trimpath -ldflags "${DESKTOP_LDFLAGS}" -o "${OUT_DIR}/colabhive-desktop.exe" ./cmd/colabhive-desktop
  elif [[ "${REQUIRE_DESKTOP}" -eq 1 ]]; then
    echo "[build-release] Error: ./cmd/colabhive-desktop not found (--require-desktop)." >&2
    exit 1
  else
    echo "[4/4] Skipping desktop build (./cmd/colabhive-desktop not present yet)."
  fi
else
  echo "[4/4] Skipping desktop build (--skip-desktop)."
fi

# ------------------------------------------------------------------------------
# Optional Cross-Platform Release Matrix (`--all-platforms`)
# ------------------------------------------------------------------------------
if [[ "${ALL_PLATFORMS}" -eq 1 ]]; then
  echo ""
  echo "[+] Building Cross-Platform Server & Desktop Matrix..."
  PLATFORMS=(
    "linux:amd64:"
    "linux:arm64:"
    "darwin:arm64:"
    "darwin:amd64:"
    "windows:amd64:.exe"
  )
  for entry in "${PLATFORMS[@]}"; do
    IFS=":" read -r t_os t_arch t_ext <<< "${entry}"
    target_dir="${OUT_DIR}/${t_os}-${t_arch}"
    mkdir -p "${target_dir}"
    echo "    -> ${t_os}/${t_arch}"
    CGO_ENABLED=0 GOOS="${t_os}" GOARCH="${t_arch}" \
      go build -trimpath -ldflags "${BASE_LDFLAGS}" -o "${target_dir}/colabhive${t_ext}" ./cmd/colabhive
    if [[ -d "./cmd/colab" ]]; then
      CGO_ENABLED=0 GOOS="${t_os}" GOARCH="${t_arch}" \
        go build -trimpath -ldflags "${BASE_LDFLAGS}" -o "${target_dir}/colab${t_ext}" ./cmd/colab
    fi
    if [[ "${SKIP_DESKTOP}" -eq 0 && -d "./cmd/colabhive-desktop" ]]; then
      if [[ "${t_os}" == "windows" ]]; then
        CGO_ENABLED=0 GOOS=windows GOARCH="${t_arch}" \
          go build -trimpath -ldflags "${DESKTOP_LDFLAGS}" -o "${target_dir}/colabhive-desktop.exe" ./cmd/colabhive-desktop
      elif [[ "${t_os}" == "darwin" ]]; then
        assemble_macos_app_bundle "${t_arch}" "${target_dir}"
        cp -f "${target_dir}/ColabHive.app/Contents/MacOS/colabhive-desktop" "${target_dir}/colabhive-desktop"
      fi
    fi
    if [[ -f "web/dist/index.html" ]]; then
      rm -rf "${target_dir}/web/dist"
      mkdir -p "${target_dir}/web"
      cp -R "web/dist" "${target_dir}/web/dist"
    fi
  done
fi

echo ""
echo "Build Complete! Artifacts in ${OUT_DIR}/:"
ls -lh "${OUT_DIR}"
