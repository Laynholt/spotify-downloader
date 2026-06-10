param(
    [switch]$InstallFrontendDeps,
    [switch]$InstallHelperDeps,
    [switch]$NoHelperRebuild,
    [switch]$NoClean
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Assert-Command {
    param([string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command not found: $Name"
    }
}

function Invoke-External {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [string[]]$ArgumentList = @(),
        [string]$WorkingDirectory = $PWD.Path
    )

    Push-Location $WorkingDirectory
    try {
        & $FilePath @ArgumentList
        if ($LASTEXITCODE -ne 0) {
            throw "Command failed ($LASTEXITCODE): $FilePath $($ArgumentList -join ' ')"
        }
    }
    finally {
        Pop-Location
    }
}

function Resolve-Python {
    if (Get-Command "py" -ErrorAction SilentlyContinue) {
        return @("py", "-3")
    }
    if (Get-Command "python" -ErrorAction SilentlyContinue) {
        return @("python")
    }
    throw "Required command not found: Python 3. Install Python or make sure py/python is available in PATH."
}

function Invoke-Python {
    param(
        [string[]]$ArgumentList,
        [string]$WorkingDirectory = $PWD.Path
    )

    $python = Resolve-Python
    $filePath = $python[0]
    $args = @()
    if ($python.Length -gt 1) {
        $args += $python[1..($python.Length - 1)]
    }
    $args += $ArgumentList
    Invoke-External $filePath $args $WorkingDirectory
}

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$frontendDir = Join-Path $scriptRoot "frontend"
$nodeModulesDir = Join-Path $frontendDir "node_modules"
$helperDir = Join-Path $scriptRoot "helper"
$helperScript = Join-Path $helperDir "get_token.py"
$helperExtensionDir = Join-Path $helperDir "uBOLite"
$backendBinDir = Join-Path $scriptRoot "backend\bin"
$embeddedHelperExe = Join-Path $backendBinDir "get_token.exe"
$cacheRoot = Join-Path $scriptRoot ".cache"
$goCacheDir = Join-Path $cacheRoot "go-build"
$goTmpDir = Join-Path $cacheRoot "go-tmp"
$helperVenvDir = Join-Path $cacheRoot "helper-venv"
$helperVenvPython = Join-Path $helperVenvDir "Scripts\python.exe"
$appExe = Join-Path $scriptRoot "build\bin\SpotiDownloader.exe"

Push-Location $scriptRoot

try {
    if (-not $IsWindows) {
        throw "This script is intended for Windows builds (.exe)."
    }

    Assert-Command "go"
    Assert-Command "pnpm"
    Assert-Command "wails"

    Write-Step "Prepare local Go build cache"
    New-Item -ItemType Directory -Force $goCacheDir | Out-Null
    New-Item -ItemType Directory -Force $goTmpDir | Out-Null
    $env:GOCACHE = $goCacheDir
    $env:GOTMPDIR = $goTmpDir

    if (-not (Test-Path $helperScript)) {
        throw "Missing helper script: $helperScript"
    }
    if (-not (Test-Path (Join-Path $helperExtensionDir "manifest.json"))) {
        throw "Missing uBOLite extension: $helperExtensionDir"
    }

    $shouldBuildHelper = (-not $NoHelperRebuild) -or (-not (Test-Path $embeddedHelperExe))
    if ($shouldBuildHelper) {
        Write-Step "Build Python token helper"

        if ($InstallHelperDeps -or -not (Test-Path $helperVenvPython)) {
            Write-Host "Creating/updating local helper venv: $helperVenvDir" -ForegroundColor Yellow
            Invoke-Python @("-m", "venv", $helperVenvDir) $scriptRoot
            Invoke-External $helperVenvPython @("-m", "pip", "install", "--upgrade", "pip", "pyinstaller", "nodriver") $scriptRoot
        } else {
            Write-Host "Using existing helper venv: $helperVenvDir" -ForegroundColor Yellow
        }

        Invoke-External $helperVenvPython @(
            "-m", "PyInstaller",
            "--noconfirm",
            "--onefile",
            "--clean",
            "--add-data", "uBOLite;uBOLite",
            "get_token.py"
        ) $helperDir

        $builtHelperExe = Join-Path $helperDir "dist\get_token.exe"
        if (-not (Test-Path $builtHelperExe)) {
            throw "PyInstaller finished, but expected helper was not found: $builtHelperExe"
        }

        New-Item -ItemType Directory -Force $backendBinDir | Out-Null
        Copy-Item -LiteralPath $builtHelperExe -Destination $embeddedHelperExe -Force
        Write-Host "Embedded helper ready: $embeddedHelperExe" -ForegroundColor Green
    } else {
        Write-Step "Reuse existing Python token helper"
        Write-Host "Using existing embedded helper: $embeddedHelperExe" -ForegroundColor Yellow
    }

    if ($InstallFrontendDeps -or -not (Test-Path $nodeModulesDir)) {
        Write-Step "Install frontend dependencies"
        $env:CI = "true"
        Invoke-External "pnpm" @("install", "--frozen-lockfile") $frontendDir
    } else {
        Write-Step "Reuse existing frontend dependencies"
        Write-Host "Using existing frontend\node_modules" -ForegroundColor Yellow
    }

    Write-Step "Build Wails application"
    Write-Host "Wails will generate bindings and build frontend assets" -ForegroundColor Yellow
    $env:CI = "true"
    $wailsArgs = @("build")
    if (-not $NoClean) {
        $wailsArgs += "-clean"
    }
    $wailsArgs += @("-trimpath", "-ldflags", "-s -w")

    if (Get-Command "upx" -ErrorAction SilentlyContinue) {
        $wailsArgs += "-upx"
        Write-Host "UPX detected: enabling Wails UPX compression" -ForegroundColor Yellow
    } else {
        Write-Host "UPX not found: building without UPX compression" -ForegroundColor Yellow
    }

    Invoke-External "wails" $wailsArgs $scriptRoot

    if (Test-Path $appExe) {
        Write-Host ""
        Write-Host "Build completed: $appExe" -ForegroundColor Green
    } else {
        throw "Wails build finished, but expected file was not found: $appExe"
    }
}
finally {
    Pop-Location
}
