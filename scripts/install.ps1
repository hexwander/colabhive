#Requires -Version 5.1
<#
.SYNOPSIS
    ColabHive 1-Line Windows Installer (Desktop Tray App + Control Plane + Drop-In `colab` CLI)
.DESCRIPTION
    Downloads the latest verified ColabHive Windows release from GitHub Releases,
    verifies its SHA-256 checksum against SHA256SUMS.txt, extracts `colabhive-desktop.exe`,
    `colabhive.exe`, `colab.exe`, and `web/dist` into `$HOME\.colabhive`, adds `$HOME\.colabhive`
    to your User PATH, and creates a Start Menu shortcut (plus optional Desktop shortcut).

    One-line install:
      irm https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.ps1 | iex

    Environment overrides:
      $env:COLABHIVE_REPO             = "hexwander/colabhive"
      $env:COLABHIVE_VERSION          = "latest"          # or e.g. "v1.0.0"
      $env:COLABHIVE_INSTALL_DIR      = "$HOME\.colabhive"
      $env:COLABHIVE_DESKTOP_SHORTCUT = "1"               # set to "1" or "true" to add Desktop .lnk
      $env:COLABHIVE_SKIP_SHORTCUTS   = "0"               # set to "1" to skip Start Menu / Desktop .lnk
#>

function Install-ColabHive {
    [CmdletBinding()]
    param(
        [string]$Repo = $(if ($env:COLABHIVE_REPO) { $env:COLABHIVE_REPO } else { "hexwander/colabhive" }),
        [string]$Version = $(if ($env:COLABHIVE_VERSION) { $env:COLABHIVE_VERSION } else { "latest" }),
        [string]$InstallDir = $(if ($env:COLABHIVE_INSTALL_DIR) { $env:COLABHIVE_INSTALL_DIR } else { Join-Path $HOME ".colabhive" }),
        [switch]$DesktopShortcut,
        [switch]$SkipShortcuts
    )

    Set-StrictMode -Version Latest
    $ErrorActionPreference = "Stop"
    $ProgressPreference = "SilentlyContinue"

    # Ensure TLS 1.2+ on Windows PowerShell 5.1
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch {
        # Ignore on runtimes where SecurityProtocol is managed automatically
    }

    $CreateDesktopShortcut = $DesktopShortcut.IsPresent -or ($env:COLABHIVE_DESKTOP_SHORTCUT -in @("1", "true", "TRUE", "yes", "YES"))
    $NoShortcuts = $SkipShortcuts.IsPresent -or ($env:COLABHIVE_SKIP_SHORTCUTS -in @("1", "true", "TRUE", "yes", "YES"))

    Write-Host ""
    Write-Host "==================================================================" -ForegroundColor Cyan
    Write-Host "  ColabHive Installer for Windows (Desktop Tray + CLI + MCP)" -ForegroundColor Cyan
    Write-Host "==================================================================" -ForegroundColor Cyan

    # 1. Detect Windows CPU architecture (amd64 / arm64)
    $RawArch = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) {
        $RawArch = $env:PROCESSOR_ARCHITEW6432
    }
    $Arch = switch -Regex ($RawArch) {
        "ARM64"         { "arm64"; break }
        "AMD64|x86_64"  { "amd64"; break }
        default         { throw "Unsupported Windows architecture '$RawArch'. ColabHive requires 64-bit Windows (amd64 or arm64)." }
    }

    # 2. Query GitHub Releases API
    $ApiUrl = if ($Version -eq "latest" -or [string]::IsNullOrWhiteSpace($Version)) {
        "https://api.github.com/repos/$Repo/releases/latest"
    } else {
        $NormalizedTag = if ($Version.StartsWith("v")) { $Version } else { "v$Version" }
        "https://api.github.com/repos/$Repo/releases/tags/$NormalizedTag"
    }

    Write-Host "[1/5] Resolving release metadata from $Repo ($Version)..." -ForegroundColor Yellow
    $Headers = @{
        "User-Agent" = "ColabHive-Windows-Installer/1.0"
        "Accept"     = "application/vnd.github+json"
    }
    if ($env:GITHUB_TOKEN) {
        $Headers["Authorization"] = "Bearer $($env:GITHUB_TOKEN)"
    }

    try {
        $Release = Invoke-RestMethod -Uri $ApiUrl -Headers $Headers -Method Get
    } catch {
        throw "Failed to query GitHub Releases API ($ApiUrl): $($_.Exception.Message)"
    }

    if (-not $Release -or -not $Release.assets) {
        throw "No release assets found at $ApiUrl. Ensure a tagged release has been published."
    }

    $TagName = [string]$Release.tag_name
    $Assets = @($Release.assets)

    $ZipAsset = $Assets | Where-Object { $_.name -like "colabhive_*_windows_${Arch}.zip" } | Select-Object -First 1
    if (-not $ZipAsset -and $Arch -eq "arm64") {
        Write-Host "      No native arm64 archive found; falling back to windows_amd64..." -ForegroundColor DarkYellow
        $Arch = "amd64"
        $ZipAsset = $Assets | Where-Object { $_.name -like "colabhive_*_windows_amd64.zip" } | Select-Object -First 1
    }
    if (-not $ZipAsset) {
        throw "Could not find asset matching 'colabhive_*_windows_${Arch}.zip' in release $TagName."
    }

    $SumsAsset = $Assets | Where-Object { $_.name -eq "SHA256SUMS.txt" } | Select-Object -First 1
    if (-not $SumsAsset) {
        throw "Release $TagName is missing mandatory 'SHA256SUMS.txt' checksum manifest."
    }

    $TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("colabhive-install-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Force -Path $TempDir | Out-Null

    try {
        $ZipFileName = [string]$ZipAsset.name
        $ZipPath = Join-Path $TempDir $ZipFileName
        $SumsPath = Join-Path $TempDir "SHA256SUMS.txt"

        # 3. Download release archive & SHA256SUMS.txt
        Write-Host "[2/5] Downloading $ZipFileName ($TagName)..." -ForegroundColor Yellow
        Invoke-WebRequest -Uri $ZipAsset.browser_download_url -Headers @{ "User-Agent" = "ColabHive-Windows-Installer/1.0" } -OutFile $ZipPath -UseBasicParsing
        Invoke-WebRequest -Uri $SumsAsset.browser_download_url -Headers @{ "User-Agent" = "ColabHive-Windows-Installer/1.0" } -OutFile $SumsPath -UseBasicParsing

        # 4. Verify SHA-256 checksum before extraction
        Write-Host "[3/5] Verifying SHA-256 cryptographic signature..." -ForegroundColor Yellow
        $ExpectedHash = $null
        foreach ($line in (Get-Content -Path $SumsPath)) {
            $trimmed = $line.Trim()
            if (-not $trimmed) { continue }
            if ($trimmed -match '^([0-9a-fA-F]{64})\s+\*?(.+)$') {
                $candidateHash = $Matches[1].ToLowerInvariant()
                $candidateName = ([System.IO.Path]::GetFileName($Matches[2].Trim()))
                if ([string]::Equals($candidateName, $ZipFileName, [System.StringComparison]::OrdinalIgnoreCase)) {
                    $ExpectedHash = $candidateHash
                    break
                }
            }
        }
        if (-not $ExpectedHash) {
            throw "Entry for '$ZipFileName' was not found in SHA256SUMS.txt. Aborting install."
        }

        $ActualHash = (Get-FileHash -Path $ZipPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($ActualHash -ne $ExpectedHash) {
            throw "SHA-256 mismatch for '$ZipFileName'! Expected $ExpectedHash, got $ActualHash. Aborting install."
        }
        Write-Host "      Verified SHA-256: $($ActualHash.Substring(0, 16))..." -ForegroundColor Green

        # 5. Extract and install into $InstallDir (preserving existing data/ vault!)
        Write-Host "[4/5] Installing binaries and Web Observatory into $InstallDir..." -ForegroundColor Yellow
        $ExtractDir = Join-Path $TempDir "extracted"
        Expand-Archive -Path $ZipPath -DestinationPath $ExtractDir -Force

        $SourceRoot = $ExtractDir
        $NestedCandidate = Join-Path $ExtractDir "colabhive_windows_${Arch}"
        if (Test-Path (Join-Path $NestedCandidate "colabhive.exe")) {
            $SourceRoot = $NestedCandidate
        } else {
            $FoundExe = Get-ChildItem -Path $ExtractDir -Filter "colabhive.exe" -Recurse -File | Select-Object -First 1
            if ($FoundExe) {
                $SourceRoot = $FoundExe.Directory.FullName
            }
        }

        $SrcColabHive = Join-Path $SourceRoot "colabhive.exe"
        if (-not (Test-Path $SrcColabHive)) {
            throw "Archive '$ZipFileName' did not contain colabhive.exe."
        }

        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
        New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir "data") | Out-Null

        Copy-Item -Path $SrcColabHive -Destination (Join-Path $InstallDir "colabhive.exe") -Force

        $SrcColabAlias = Join-Path $SourceRoot "colab.exe"
        if (Test-Path $SrcColabAlias) {
            Copy-Item -Path $SrcColabAlias -Destination (Join-Path $InstallDir "colab.exe") -Force
        } else {
            Copy-Item -Path $SrcColabHive -Destination (Join-Path $InstallDir "colab.exe") -Force
        }

        $HasDesktopExe = $false
        $SrcDesktop = Join-Path $SourceRoot "colabhive-desktop.exe"
        if (Test-Path $SrcDesktop) {
            Copy-Item -Path $SrcDesktop -Destination (Join-Path $InstallDir "colabhive-desktop.exe") -Force
            $HasDesktopExe = $true
        }

        # Support both `web/dist` (standard) and legacy `web-dist` archive layouts
        $SrcWebDist = Join-Path $SourceRoot "web\dist"
        if (-not (Test-Path (Join-Path $SrcWebDist "index.html"))) {
            $LegacyWebDist = Join-Path $SourceRoot "web-dist"
            if (Test-Path (Join-Path $LegacyWebDist "index.html")) {
                $SrcWebDist = $LegacyWebDist
            }
        }
        if (Test-Path (Join-Path $SrcWebDist "index.html")) {
            $DestWebParent = Join-Path $InstallDir "web"
            $DestWebDist = Join-Path $DestWebParent "dist"
            if (Test-Path $DestWebDist) {
                Remove-Item -Recurse -Force $DestWebDist
            }
            New-Item -ItemType Directory -Force -Path $DestWebParent | Out-Null
            Copy-Item -Path $SrcWebDist -Destination $DestWebDist -Recurse -Force
        }

        foreach ($doc in @("LICENSE", "README.md", "README.zh-CN.md")) {
            $docPath = Join-Path $SourceRoot $doc
            if (Test-Path $docPath) {
                Copy-Item -Path $docPath -Destination (Join-Path $InstallDir $doc) -Force
            }
        }

        # 6. Update User PATH & create Start Menu / optional Desktop shortcuts
        Write-Host "[5/5] Configuring User PATH and Windows shortcuts..." -ForegroundColor Yellow
        $NormalizedInstall = [System.IO.Path]::GetFullPath($InstallDir).TrimEnd('\')
        $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
        if (-not $UserPath) { $UserPath = "" }

        $PathEntries = @($UserPath -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        $AlreadyInUserPath = $false
        foreach ($entry in $PathEntries) {
            $cleanEntry = $entry.Trim().TrimEnd('\')
            if ([string]::Equals($cleanEntry, $NormalizedInstall, [System.StringComparison]::OrdinalIgnoreCase)) {
                $AlreadyInUserPath = $true
                break
            }
        }

        if (-not $AlreadyInUserPath) {
            $NewUserPath = if ($UserPath.Trim()) { "$($UserPath.TrimEnd(';'));$NormalizedInstall" } else { $NormalizedInstall }
            [Environment]::SetEnvironmentVariable("Path", $NewUserPath, "User")
            Write-Host "      Added '$NormalizedInstall' to User PATH." -ForegroundColor Green
        } else {
            Write-Host "      '$NormalizedInstall' is already in User PATH." -ForegroundColor DarkGray
        }

        # Ensure current PowerShell session can run `colabhive` and `colab` immediately
        $SessionEntries = @($env:PATH -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        $InSessionPath = $false
        foreach ($entry in $SessionEntries) {
            if ([string]::Equals($entry.Trim().TrimEnd('\'), $NormalizedInstall, [System.StringComparison]::OrdinalIgnoreCase)) {
                $InSessionPath = $true
                break
            }
        }
        if (-not $InSessionPath) {
            $env:PATH = "$NormalizedInstall;$env:PATH"
        }

        if ($HasDesktopExe -and (-not $NoShortcuts)) {
            try {
                $WshShell = New-Object -ComObject WScript.Shell
                $DesktopExePath = Join-Path $NormalizedInstall "colabhive-desktop.exe"

                $StartMenuDir = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs"
                if (Test-Path $StartMenuDir) {
                    $StartMenuLnk = Join-Path $StartMenuDir "ColabHive Desktop.lnk"
                    $Sc = $WshShell.CreateShortcut($StartMenuLnk)
                    $Sc.TargetPath = $DesktopExePath
                    $Sc.WorkingDirectory = $NormalizedInstall
                    $Sc.IconLocation = "$DesktopExePath,0"
                    $Sc.Description = "ColabHive - Multi-Account Google Colab GPU Pool & MCP Router"
                    $Sc.Save()
                    Write-Host "      Created Start Menu shortcut: ColabHive Desktop" -ForegroundColor Green
                }

                if ($CreateDesktopShortcut) {
                    $UserDesktopDir = [Environment]::GetFolderPath("Desktop")
                    if ($UserDesktopDir -and (Test-Path $UserDesktopDir)) {
                        $DesktopLnk = Join-Path $UserDesktopDir "ColabHive Desktop.lnk"
                        $DSc = $WshShell.CreateShortcut($DesktopLnk)
                        $DSc.TargetPath = $DesktopExePath
                        $DSc.WorkingDirectory = $NormalizedInstall
                        $DSc.IconLocation = "$DesktopExePath,0"
                        $DSc.Description = "ColabHive - Multi-Account Google Colab GPU Pool & MCP Router"
                        $DSc.Save()
                        Write-Host "      Created Desktop shortcut: ColabHive Desktop" -ForegroundColor Green
                    }
                }
            } catch {
                Write-Warning "Could not create .lnk shortcut ($($_.Exception.Message)). Binaries were installed cleanly."
            }
        }

        Write-Host ""
        Write-Host "==================================================================" -ForegroundColor Green
        Write-Host "  ColabHive $TagName installed successfully!" -ForegroundColor Green
        Write-Host "==================================================================" -ForegroundColor Green
        Write-Host "  Install Dir : $NormalizedInstall"
        Write-Host "  Vault Dir   : $(Join-Path $NormalizedInstall 'data') (AES-256-GCM encrypted)"
        Write-Host ""
        Write-Host "  Next Steps:" -ForegroundColor Cyan
        if ($HasDesktopExe) {
            Write-Host "    1. Launch Native Tray + Desktop App (Start Menu -> 'ColabHive Desktop'):"
            Write-Host "       Start-Process `"$(Join-Path $NormalizedInstall 'colabhive-desktop.exe')`"" -ForegroundColor White
        }
        Write-Host "    2. Or start the local Control Plane & Web Observatory in terminal:"
        Write-Host "       colabhive serve --web-dir `"$(Join-Path $NormalizedInstall 'web\dist')`" --data-dir `"$(Join-Path $NormalizedInstall 'data')`"" -ForegroundColor White
        Write-Host "    3. Check pooled GPU quota or try the zero-account Demo Cluster:"
        Write-Host "       colab usage --gpu-hours" -ForegroundColor White
        Write-Host "       colabhive serve --demo" -ForegroundColor White
        Write-Host ""
    } finally {
        if (Test-Path $TempDir) {
            Remove-Item -Recurse -Force $TempDir -ErrorAction SilentlyContinue
        }
    }
}

Install-ColabHive @args
