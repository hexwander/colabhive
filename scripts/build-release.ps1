#Requires -Version 5.1
<#
.SYNOPSIS
    ColabHive Cross-Target Release Builder (`scripts/build-release.ps1`)
.DESCRIPTION
    Builds the complete ColabHive local & cloud distribution artifacts:
      1. Frontend Observatory, Workbench & Magpie Tray SPA (`web/dist`)
      2. Unified Control Plane, MCP Server & CLI binary (`bin/colabhive.exe`)
      3. 100% Drop-In Google Colab CLI alias (`bin/colab.exe`)
      4. Windows Native System Tray & WebView2 Desktop App (`bin/colabhive-desktop.exe`
         compiled with `-ldflags "-s -w -H=windowsgui"` — zero console window!)
.EXAMPLE
    .\scripts\build-release.ps1
    .\scripts\build-release.ps1 -Version 1.1.0 -SkipWeb
    .\scripts\build-release.ps1 -CrossMatrix
#>

[CmdletBinding()]
param(
    [string]$Version = $(if ($env:COLABHIVE_VERSION) { $env:COLABHIVE_VERSION } else { "1.0.0" }),
    [string]$OutDir = "bin",
    [switch]$SkipWeb,
    [switch]$SkipDesktop,
    [switch]$RequireDesktop,
    [switch]$CrossMatrix
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $RepoRoot

try {
    # --------------------------------------------------------------------------
    # Resolve Git Metadata & Reproducible Build Flags
    # --------------------------------------------------------------------------
    $Commit = "dev"
    try {
        $gitOut = & git rev-parse --short HEAD 2>$null
        if ($LASTEXITCODE -eq 0 -and $gitOut) {
            $Commit = ($gitOut | Select-Object -First 1).Trim()
        }
    } catch {
        $Commit = "dev"
    }
    $BuildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

    $BaseLdFlags = "-s -w -X main.version=$Version -X main.commit=$Commit -X main.buildDate=$BuildDate"
    $DesktopLdFlags = "-s -w -H=windowsgui -X main.version=$Version -X main.commit=$Commit -X main.buildDate=$BuildDate"

    Write-Host ""
    Write-Host "==================================================================" -ForegroundColor Cyan
    Write-Host " ColabHive Release Build  v$Version ($Commit @ $BuildDate)" -ForegroundColor Cyan
    Write-Host "==================================================================" -ForegroundColor Cyan

    # --------------------------------------------------------------------------
    # Step 1/4: Build Frontend SPA (`web/dist`)
    # --------------------------------------------------------------------------
    if (-not $SkipWeb) {
        Write-Host "`n[1/4] Building Frontend SPA (web/dist)..." -ForegroundColor Yellow
        if (-not (Get-Command pnpm -ErrorAction SilentlyContinue)) {
            throw "pnpm not found in PATH. Enable via `corepack enable` or install pnpm."
        }
        if (-not (Test-Path "web/node_modules")) {
            & pnpm --dir web install --frozen-lockfile
            if ($LASTEXITCODE -ne 0) {
                & pnpm --dir web install
                if ($LASTEXITCODE -ne 0) { throw "pnpm install failed." }
            }
        }
        & pnpm --dir web run build
        if ($LASTEXITCODE -ne 0) {
            throw "Frontend build (`pnpm --dir web run build`) failed."
        }
    } else {
        Write-Host "`n[1/4] Skipping Frontend SPA build (-SkipWeb)." -ForegroundColor DarkGray
    }

    if (-not (Test-Path "web/dist/index.html")) {
        Write-Warning "web/dist/index.html was not found. Run without -SkipWeb before shipping UI bundles."
    }

    # Prepare output directory
    $ResolvedOutDir = if ([System.IO.Path]::IsPathRooted($OutDir)) { $OutDir } else { Join-Path $RepoRoot $OutDir }
    New-Item -ItemType Directory -Force -Path $ResolvedOutDir | Out-Null

    # Bundle `web/dist` beside binaries (`<OutDir>/web/dist`) so `colabhive` and `colabhive-desktop.exe`
    # always resolve SPA assets out-of-the-box even when `<OutDir>` is moved outside the repository.
    $SrcWebDist = Join-Path $RepoRoot "web\dist"
    if (Test-Path (Join-Path $SrcWebDist "index.html")) {
        $ResolvedSrcWeb = (Resolve-Path $SrcWebDist).Path.TrimEnd('\', '/')
        $DestWebDist = (Join-Path $ResolvedOutDir "web\dist").TrimEnd('\', '/')
        if (-not [string]::Equals($ResolvedSrcWeb, $DestWebDist, [System.StringComparison]::OrdinalIgnoreCase)) {
            if (Test-Path $DestWebDist) {
                Remove-Item -Recurse -Force $DestWebDist
            }
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $DestWebDist) | Out-Null
            Copy-Item -Path $ResolvedSrcWeb -Destination $DestWebDist -Recurse -Force
        }
    }

    # Detect host OS/Arch via `go env`
    $HostGOOS = (& go env GOOS).Trim()
    $HostGOARCH = (& go env GOARCH).Trim()
    $ExeSuffix = if ($HostGOOS -eq "windows") { ".exe" } else { "" }

    $OrigCGO = $env:CGO_ENABLED
    $OrigGOOS = $env:GOOS
    $OrigGOARCH = $env:GOARCH

    try {
        $env:CGO_ENABLED = "0"
        $env:GOOS = $HostGOOS
        $env:GOARCH = $HostGOARCH

        # ----------------------------------------------------------------------
        # Step 2/4: Build `bin/colabhive(.exe)`
        # ----------------------------------------------------------------------
        $ColabHiveBin = Join-Path $ResolvedOutDir "colabhive$ExeSuffix"
        Write-Host "[2/4] Building Control Plane & MCP Daemon -> $ColabHiveBin" -ForegroundColor Yellow
        & go build -trimpath -ldflags $BaseLdFlags -o $ColabHiveBin ./cmd/colabhive
        if ($LASTEXITCODE -ne 0) { throw "Failed to build ./cmd/colabhive" }

        # ----------------------------------------------------------------------
        # Step 3/4: Build `bin/colab(.exe)`
        # ----------------------------------------------------------------------
        $ColabAliasBin = Join-Path $ResolvedOutDir "colab$ExeSuffix"
        Write-Host "[3/4] Building Drop-In Colab CLI Alias    -> $ColabAliasBin" -ForegroundColor Yellow
        if (Test-Path "./cmd/colab") {
            & go build -trimpath -ldflags $BaseLdFlags -o $ColabAliasBin ./cmd/colab
            if ($LASTEXITCODE -ne 0) { throw "Failed to build ./cmd/colab" }
        } else {
            Copy-Item -Path $ColabHiveBin -Destination $ColabAliasBin -Force
        }

        # ----------------------------------------------------------------------
        # Step 4/4: Build Windows Native Desktop App (`bin/colabhive-desktop.exe`)
        #           Uses `-ldflags "-s -w -H=windowsgui"` for borderless Tray + WebView2
        # ----------------------------------------------------------------------
        $DesktopBin = Join-Path $ResolvedOutDir "colabhive-desktop.exe"
        if (-not $SkipDesktop) {
            if (Test-Path "./cmd/colabhive-desktop") {
                Write-Host "[4/4] Building Windows Native Desktop App -> $DesktopBin (-H=windowsgui)" -ForegroundColor Yellow
                $env:GOOS = "windows"
                $env:GOARCH = if ($HostGOOS -eq "windows") { $HostGOARCH } else { "amd64" }
                & go build -trimpath -ldflags $DesktopLdFlags -o $DesktopBin ./cmd/colabhive-desktop
                if ($LASTEXITCODE -ne 0) { throw "Failed to build ./cmd/colabhive-desktop" }
            } elseif ($RequireDesktop) {
                throw "./cmd/colabhive-desktop not found, and -RequireDesktop was specified."
            } else {
                Write-Host "[4/4] Skipping colabhive-desktop.exe (./cmd/colabhive-desktop not present yet)." -ForegroundColor DarkGray
            }
        } else {
            Write-Host "[4/4] Skipping colabhive-desktop.exe (-SkipDesktop)." -ForegroundColor DarkGray
        }

        # ----------------------------------------------------------------------
        # Optional Cross-Platform Release Matrix (`-CrossMatrix`)
        # ----------------------------------------------------------------------
        if ($CrossMatrix) {
            Write-Host "`n[+] Building Cross-Platform Server Matrix..." -ForegroundColor Cyan
            $Targets = @(
                @{ GOOS = "linux";   GOARCH = "amd64"; Ext = "" },
                @{ GOOS = "linux";   GOARCH = "arm64"; Ext = "" },
                @{ GOOS = "darwin";  GOARCH = "arm64"; Ext = "" },
                @{ GOOS = "darwin";  GOARCH = "amd64"; Ext = "" },
                @{ GOOS = "windows"; GOARCH = "amd64"; Ext = ".exe" }
            )
            foreach ($t in $Targets) {
                $env:GOOS = $t.GOOS
                $env:GOARCH = $t.GOARCH
                $TargetSubDir = Join-Path $ResolvedOutDir "$($t.GOOS)-$($t.GOARCH)"
                New-Item -ItemType Directory -Force -Path $TargetSubDir | Out-Null

                Write-Host "    -> $($t.GOOS)/$($t.GOARCH)" -ForegroundColor Gray
                & go build -trimpath -ldflags $BaseLdFlags -o (Join-Path $TargetSubDir "colabhive$($t.Ext)") ./cmd/colabhive
                if ($LASTEXITCODE -ne 0) { throw "Cross-build failed for $($t.GOOS)/$($t.GOARCH) colabhive" }

                if (Test-Path "./cmd/colab") {
                    & go build -trimpath -ldflags $BaseLdFlags -o (Join-Path $TargetSubDir "colab$($t.Ext)") ./cmd/colab
                    if ($LASTEXITCODE -ne 0) { throw "Cross-build failed for $($t.GOOS)/$($t.GOARCH) colab" }
                }
                if ($t.GOOS -eq "windows" -and (-not $SkipDesktop) -and (Test-Path "./cmd/colabhive-desktop")) {
                    & go build -trimpath -ldflags $DesktopLdFlags -o (Join-Path $TargetSubDir "colabhive-desktop.exe") ./cmd/colabhive-desktop
                    if ($LASTEXITCODE -ne 0) { throw "Cross-build failed for windows/amd64 colabhive-desktop.exe" }
                }
                if (Test-Path (Join-Path $SrcWebDist "index.html")) {
                    $TargetWebDist = Join-Path $TargetSubDir "web\dist"
                    if (Test-Path $TargetWebDist) {
                        Remove-Item -Recurse -Force $TargetWebDist
                    }
                    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $TargetWebDist) | Out-Null
                    Copy-Item -Path $SrcWebDist -Destination $TargetWebDist -Recurse -Force
                }
            }
        }
    } finally {
        $env:CGO_ENABLED = $OrigCGO
        $env:GOOS = $OrigGOOS
        $env:GOARCH = $OrigGOARCH
    }

    # --------------------------------------------------------------------------
    # Artifact Verification & Checksum Summary
    # --------------------------------------------------------------------------
    Write-Host "`nBuild Complete! Artifacts in ${ResolvedOutDir}:" -ForegroundColor Green
    Get-ChildItem -Path $ResolvedOutDir -File | ForEach-Object {
        $sizeMB = [Math]::Round($_.Length / 1MB, 2)
        $hash = (Get-FileHash -Path $_.FullName -Algorithm SHA256).Hash.Substring(0, 16).ToLower()
        Write-Host ("  • {0,-26} {1,7} MB   sha256:{2}..." -f $_.Name, $sizeMB, $hash) -ForegroundColor White
    }
    Write-Host ""
} finally {
    Pop-Location
}
