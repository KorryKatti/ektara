$ErrorActionPreference = "Stop"

$Repo = "korrykatti/ektara"
$Branch = "main"

$BinDir = if ($env:BINDIR) {
    $env:BINDIR
} else {
    Join-Path $env:LOCALAPPDATA "Programs\ektara"
}

# Determine project directory and local cache directory
$ProjectDir = (Get-Location).Path
$GoMod = Join-Path $ProjectDir "go.mod"
$IsCheckout = $false

if (Test-Path $GoMod) {
    $ModuleLine = Get-Content $GoMod |
        Where-Object { $_ -match '^module\s+ektara\s*$' }

    if ($ModuleLine) {
        $IsCheckout = $true
    }
}

# Persistent cache directory in the project directory (or temp fallback)
$TempoDir = if ($IsCheckout) {
    Join-Path $ProjectDir "tempo"
} else {
    Join-Path ([System.IO.Path]::GetTempPath()) "ektara-tempo"
}
New-Item -ItemType Directory -Path $TempoDir -Force | Out-Null

$TempBin = $null
$TempDir = $null

function Get-CommandPath {
    param([string]$Name)

    $Command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($Command) {
        return $Command.Source
    }

    return $null
}

function Download-CachedFile {
    param(
        [string]$Uri,
        [string]$DestinationPath,
        [string]$Description
    )

    if (Test-Path $DestinationPath) {
        $FileInfo = Get-Item $DestinationPath
        if ($FileInfo.Length -gt 0) {
            Write-Host "$Description already cached: $DestinationPath"
            return
        }
        Remove-Item $DestinationPath -Force -ErrorAction SilentlyContinue
    }

    Write-Host "Downloading $Description..."
    Invoke-WebRequest `
        -Uri $Uri `
        -OutFile $DestinationPath `
        -Headers @{ "User-Agent" = "EktaraInstaller" } `
        -UseBasicParsing
}

try {
    # ------------------------------------------------------------
    # Check Go
    # ------------------------------------------------------------

    $Go = Get-CommandPath "go"

    if (-not $Go) {
        throw "Go is required. Install it from https://go.dev/dl/"
    }

    $GoOutput = (& $Go version 2>&1 | Out-String).Trim()

    if ($GoOutput -notmatch 'go version go(\d+\.\d+(?:\.\d+)?)') {
        throw "Could not determine the installed Go version: $GoOutput"
    }

    $GoVersion = [version]$Matches[1]

    Write-Host "Go version: $GoVersion"

    if ($GoVersion -lt [version]"1.27") {
        throw "Ektara requires Go 1.27 or newer. Installed: $GoVersion"
    }

    # ------------------------------------------------------------
    # Check C compiler
    # ------------------------------------------------------------

    $Compiler = $null

    foreach ($Name in @("gcc", "clang", "cc")) {
        $Candidate = Get-CommandPath $Name

        if ($Candidate) {
            $Compiler = $Candidate
            break
        }
    }

    if (-not $Compiler) {
        throw @"
A C compiler is required for CGO.
Install MinGW-w64 or LLVM/Clang and ensure it is on PATH.
MinGW-w64: https://www.mingw-w64.org/
LLVM: https://releases.llvm.org/
"@
    }

    Write-Host "C compiler: $Compiler"

    # ------------------------------------------------------------
    # Prepare installation directory
    # ------------------------------------------------------------

    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null

    # ------------------------------------------------------------
    # Find local checkout or download source
    # ------------------------------------------------------------

    if ($IsCheckout) {
        $Src = $ProjectDir
    } else {
        $Archive = Join-Path $TempoDir "source-$Branch.tar.gz"
        $SourceUrl = "https://github.com/$Repo/archive/refs/heads/$Branch.tar.gz"

        Download-CachedFile `
            -Uri $SourceUrl `
            -DestinationPath $Archive `
            -Description "Ektara source"

        $TempDir = Join-Path $TempoDir "ektara-source-$Branch"
        if (-not (Test-Path (Join-Path $TempDir "go.mod"))) {
            New-Item -ItemType Directory -Path $TempDir -Force | Out-Null
            & tar -xzf $Archive -C $TempDir --strip-components 1
            if ($LASTEXITCODE -ne 0) {
                throw "Failed to extract Ektara source."
            }
        }

        $Src = $TempDir

        if (-not (Test-Path (Join-Path $Src "go.mod"))) {
            throw "Downloaded source archive has an unexpected structure."
        }
    }

    # ------------------------------------------------------------
    # Find 7-Zip
    # ------------------------------------------------------------

    $SevenZip = Get-CommandPath "7z"

    if (-not $SevenZip) {
        $Candidates = @(
            "$env:ProgramFiles\7-Zip\7z.exe",
            "${env:ProgramFiles(x86)}\7-Zip\7z.exe"
        )

        $SevenZip = $Candidates |
            Where-Object { $_ -and (Test-Path $_) } |
            Select-Object -First 1
    }

    if (-not $SevenZip) {
        throw @"
7-Zip is required to extract dependencies.
Install it from https://www.7-zip.org/
Then rerun this script.
"@
    }

    # ------------------------------------------------------------
    # Download mpv and ffmpeg release information
    # ------------------------------------------------------------

    Write-Host "Fetching mpv and ffmpeg release information..."

    $Release = Invoke-RestMethod `
        -Uri "https://api.github.com/repos/shinchiro/mpv-winbuild-cmake/releases/latest" `
        -Headers @{ "User-Agent" = "EktaraInstaller" }

    # Select x64 mpv runtime, dev, and ffmpeg archives
    $RuntimeAsset = $Release.assets |
        Where-Object {
            $_.name -match '^mpv-x86_64-\d+.*\.7z$'
        } |
        Select-Object -First 1

    $DevAsset = $Release.assets |
        Where-Object {
            $_.name -match '^mpv-dev-x86_64-\d+.*\.7z$'
        } |
        Select-Object -First 1

    $FFmpegAsset = $Release.assets |
        Where-Object {
            $_.name -match '^ffmpeg-x86_64-git-.*\.7z$'
        } |
        Select-Object -First 1

    if (-not $RuntimeAsset -or -not $DevAsset) {
        Write-Host "Available release assets:"
        $Release.assets | Select-Object -ExpandProperty name
        throw "Could not find mpv runtime and development archives."
    }

    # Download mpv runtime
    $RuntimeArchive = Join-Path $TempoDir $RuntimeAsset.name
    Download-CachedFile `
        -Uri $RuntimeAsset.browser_download_url `
        -DestinationPath $RuntimeArchive `
        -Description $RuntimeAsset.name

    # Download mpv dev
    $DevArchive = Join-Path $TempoDir $DevAsset.name
    Download-CachedFile `
        -Uri $DevAsset.browser_download_url `
        -DestinationPath $DevArchive `
        -Description $DevAsset.name

    # Download ffmpeg if available from the same release
    $FFmpegArchive = $null
    if ($FFmpegAsset) {
        $FFmpegArchive = Join-Path $TempoDir $FFmpegAsset.name
        Download-CachedFile `
            -Uri $FFmpegAsset.browser_download_url `
            -DestinationPath $FFmpegArchive `
            -Description $FFmpegAsset.name
    }

    # ------------------------------------------------------------
    # Extract archives
    # ------------------------------------------------------------

    $RuntimeDir = Join-Path $TempoDir ([System.IO.Path]::GetFileNameWithoutExtension($RuntimeAsset.name))
    $DevDir = Join-Path $TempoDir ([System.IO.Path]::GetFileNameWithoutExtension($DevAsset.name))

    if (-not (Test-Path $RuntimeDir)) {
        New-Item -ItemType Directory -Path $RuntimeDir -Force | Out-Null
        Write-Host "Extracting mpv runtime..."
        & $SevenZip x $RuntimeArchive "-o$RuntimeDir" -y | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to extract the mpv runtime archive."
        }
    } else {
        Write-Host "mpv runtime files already extracted in tempo."
    }

    if (-not (Test-Path $DevDir)) {
        New-Item -ItemType Directory -Path $DevDir -Force | Out-Null
        Write-Host "Extracting mpv development files..."
        & $SevenZip x $DevArchive "-o$DevDir" -y | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to extract the mpv development archive."
        }
    } else {
        Write-Host "mpv development files already extracted in tempo."
    }

    if ($FFmpegArchive) {
        $FFmpegDir = Join-Path $TempoDir ([System.IO.Path]::GetFileNameWithoutExtension($FFmpegAsset.name))
        if (-not (Test-Path $FFmpegDir)) {
            New-Item -ItemType Directory -Path $FFmpegDir -Force | Out-Null
            Write-Host "Extracting FFmpeg..."
            & $SevenZip x $FFmpegArchive "-o$FFmpegDir" -y | Out-Null
            if ($LASTEXITCODE -ne 0) {
                throw "Failed to extract FFmpeg archive."
            }
        } else {
            Write-Host "FFmpeg files already extracted in tempo."
        }

        # Copy ffmpeg.exe to BinDir
        $FFmpegExe = Get-ChildItem $FFmpegDir -Filter "ffmpeg.exe" -Recurse -File | Select-Object -First 1
        if ($FFmpegExe) {
            Copy-Item $FFmpegExe.FullName (Join-Path $BinDir "ffmpeg.exe") -Force
            Write-Host "ffmpeg.exe copied to $BinDir"
        }
    }

    # ------------------------------------------------------------
    # Locate the main mpv DLL
    # ------------------------------------------------------------

    $DllNames = @(
        "libmpv-2.dll",
        "mpv-2.dll",
        "libmpv.dll",
        "mpv.dll"
    )

    $MpvDll = $null

    foreach ($Name in $DllNames) {
        $MpvDll = Get-ChildItem $DevDir, $RuntimeDir `
            -Filter $Name `
            -Recurse `
            -File `
            -ErrorAction SilentlyContinue |
            Select-Object -First 1

        if ($MpvDll) {
            break
        }
    }

    if (-not $MpvDll) {
        Write-Host "DLL files found in runtime archive:"
        Get-ChildItem $RuntimeDir -Filter "*.dll" -Recurse -File |
            Select-Object -ExpandProperty FullName

        Write-Host "DLL files found in development archive:"
        Get-ChildItem $DevDir -Filter "*.dll" -Recurse -File |
            Select-Object -ExpandProperty FullName

        throw "No compatible mpv DLL was found."
    }

    Write-Host "Found mpv DLL: $($MpvDll.FullName)"

    # ------------------------------------------------------------
    # Copy mpv runtime DLLs into the installation directory
    # ------------------------------------------------------------

    $AllDlls = @(
        Get-ChildItem $RuntimeDir -Filter "*.dll" -Recurse -File
        Get-ChildItem $DevDir -Filter "*.dll" -Recurse -File
    )

    foreach ($Dll in $AllDlls) {
        $Destination = Join-Path $BinDir $Dll.Name

        # Prefer the runtime archive if the same DLL exists in both.
        if (
            $Dll.FullName.StartsWith(
                $RuntimeDir,
                [System.StringComparison]::OrdinalIgnoreCase
            ) -or
            -not (Test-Path $Destination)
        ) {
            Copy-Item $Dll.FullName $Destination -Force
        }
    }

    Write-Host "mpv DLLs copied to $BinDir"

    # ------------------------------------------------------------
    # Version metadata
    # ------------------------------------------------------------

    $LdFlags = "-s -w"
    $Git = Get-CommandPath "git"

    if (
        $IsCheckout -and
        (Test-Path (Join-Path $Src ".git")) -and
        $Git
    ) {
        $Version = & $Git -C $Src -c safe.directory="$Src" `
            describe --tags --always --dirty 2>$null

        $Commit = & $Git -C $Src -c safe.directory="$Src" `
            rev-parse --short HEAD 2>$null

        if ($LASTEXITCODE -eq 0 -and $Version -and $Commit) {
            $Version = ($Version | Out-String).Trim()
            $Commit = ($Commit | Out-String).Trim()

            $LdFlags = "-s -w -X main.version=$Version -X main.commit=$Commit"
        } else {
            Write-Warning "Git metadata unavailable; building without version metadata."
        }
    }

    # ------------------------------------------------------------
    # Build Ektara
    # ------------------------------------------------------------

    $TempBin = Join-Path $BinDir (
        ".ektara-" + [guid]::NewGuid().ToString("N") + ".exe"
    )

    Write-Host "Building Ektara..."

    Push-Location $Src

    try {
        $env:CGO_ENABLED = "1"

        & $Go build `
            -tags nocgo `
            -trimpath `
            -ldflags $LdFlags `
            -o $TempBin `
            .

        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed with exit code $LASTEXITCODE."
        }
    }
    finally {
        Pop-Location
    }

    # ------------------------------------------------------------
    # Install executable
    # ------------------------------------------------------------

    $InstalledExe = Join-Path $BinDir "ektara.exe"

    Move-Item -Path $TempBin -Destination $InstalledExe -Force
    $TempBin = $null

    Write-Host ""
    Write-Host "Installed: $InstalledExe" -ForegroundColor Green

    # ------------------------------------------------------------
    # Add installation directory to user PATH
    # ------------------------------------------------------------

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")

    $PathEntries = @(
        $UserPath -split ';' |
            Where-Object { $_.Trim() }
    )

    $AlreadyOnPath = $false

    foreach ($Entry in $PathEntries) {
        if ($Entry.TrimEnd('\') -ieq $BinDir.TrimEnd('\')) {
            $AlreadyOnPath = $true
            break
        }
    }

    if (-not $AlreadyOnPath) {
        $NewUserPath = if ([string]::IsNullOrWhiteSpace($UserPath)) {
            $BinDir
        } else {
            "$UserPath;$BinDir"
        }

        [Environment]::SetEnvironmentVariable(
            "Path",
            $NewUserPath,
            "User"
        )

        $env:Path = "$env:Path;$BinDir"

        Write-Host "Added installation directory to your user PATH."
    }

    # ------------------------------------------------------------
    # Verify installation
    # ------------------------------------------------------------

    Write-Host ""
    Write-Host "Checking installed version..."

    & $InstalledExe --version

    if ($LASTEXITCODE -ne 0) {
        Write-Warning "Ektara was installed, but its version check failed."
    }

    Write-Host ""
    Write-Host "Installation complete." -ForegroundColor Green
    Write-Host "Run 'ektara' in a new terminal."
}
catch {
    Write-Host ""
    Write-Host "Installation failed: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
finally {
    # Remove any incomplete executable.
    if ($TempBin -and (Test-Path $TempBin)) {
        Remove-Item $TempBin -Force -ErrorAction SilentlyContinue
    }
}
