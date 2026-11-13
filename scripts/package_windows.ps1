#Requires -Version 5.1
<#
.SYNOPSIS
mcode Windows 个人分发打包脚本

.DESCRIPTION
构建 Web 前端与 Windows 安装程序，默认静默覆盖安装并重启 MCode。

每次运行都会先构建 web 前端（npm run build）；默认沿用 mygo.json 的 version。
-Clean 构建前清空输出目录；-Bump 时把 patch 加 1（0.1.0 -> 0.1.1）。
构建产物在 build.noindex\personal\windows-amd64，安装程序默认静默覆盖安装到
%LOCALAPPDATA%\Programs\MCode，然后重新启动应用。
安装与重启由一个脱离当前会话的后台 PowerShell 完成，因此在 MCode 内置终端或
“运行脚本”菜单里运行时也能完成；日志写到 %TEMP%\mcode-relaunch.log。

.EXAMPLE
powershell -ExecutionPolicy Bypass -File scripts\package_windows.ps1
#>
[CmdletBinding()]
param(
    [switch]$Clean,
    [switch]$Bump,
    [switch]$NoRelaunch,
    [Alias('h')]
    [switch]$Help
)

$ErrorActionPreference = 'Stop'

function Write-Step([string]$Message) {
    Write-Output ''
    Write-Output "==> $Message"
}

function Fail([string]$Message) {
    [Console]::Error.WriteLine($Message)
    exit 1
}

if ($Help) {
    @'
用法: powershell -ExecutionPolicy Bypass -File scripts\package_windows.ps1 [选项]

选项:
  -Clean        构建前清空输出目录
  -Bump         打包前把 mygo.json 的 version patch 加 1（默认不递增）
  -NoRelaunch   覆盖安装后不重新启动应用
  -Help         显示此帮助信息

说明: 每次运行都会先构建 web 前端（npm run build）再打包，默认沿用 mygo.json 的 version；
      构建产物在 build.noindex\personal\windows-amd64，安装程序默认静默覆盖安装到
      %LOCALAPPDATA%\Programs\MCode，然后重新启动应用。
      安装与重启由一个脱离当前会话的后台 PowerShell 完成，因此在 MCode 内置终端或
      “运行脚本”菜单里运行时也能完成；日志写到 %TEMP%\mcode-relaunch.log。
      Windows 产物默认不签名；首次运行安装程序如遇 SmartScreen 提示，请点「更多信息」→「仍要运行」。
      使用 -Bump 时，构建完成前若失败，会把 mygo.json 的版本号还原到运行前的值。
'@
    exit 0
}

if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    Fail 'Windows 打包需要 Windows'
}

$Root = Split-Path -Parent $PSScriptRoot
$Build = Join-Path $Root 'build.noindex\personal'
$Target = Join-Path $Build 'windows-amd64'
$ConfigPath = Join-Path $Root 'mygo.json'

if (-not (Test-Path $ConfigPath)) {
    Fail "找不到 $ConfigPath"
}
$Helper = Join-Path $PSScriptRoot 'relaunch_app.ps1'
if (-not (Test-Path $Helper)) {
    Fail "缺少 $Helper"
}

$ConfigText = [IO.File]::ReadAllText($ConfigPath)
$Config = $ConfigText | ConvertFrom-Json
$Version = $Config.version
if ([string]::IsNullOrWhiteSpace($Version)) {
    Fail '无法从 mygo.json 读取 version'
}

$Identifier = $Config.identifier
if ([string]::IsNullOrWhiteSpace($Identifier)) {
    Fail '无法从 mygo.json 读取 identifier'
}

$InstallDirName = $Config.name
if ($Config.windows -and -not [string]::IsNullOrWhiteSpace($Config.windows.installDirName)) {
    $InstallDirName = $Config.windows.installDirName
}
if ([string]::IsNullOrWhiteSpace($InstallDirName)) {
    Fail '无法从 mygo.json 读取应用名'
}
$DefaultInstallDir = Join-Path $env:LOCALAPPDATA "Programs\$InstallDirName"

$OriginalConfigText = $ConfigText
$Bumped = $false

if ($Bump) {
    if ($Version -match '^(\d+)\.(\d+)\.(\d+)$') {
        $NextVersion = '{0}.{1}.{2}' -f $Matches[1], $Matches[2], ([int]$Matches[3] + 1)
    } else {
        $NextVersion = "$Version.1"
    }
    $ConfigText = [regex]::Replace($ConfigText, '(?m)^(\s*"version"\s*:\s*")([^"]+)(")', {
            param($match)
            $match.Groups[1].Value + $NextVersion + $match.Groups[3].Value
        }, 1)
    [IO.File]::WriteAllText($ConfigPath, $ConfigText, (New-Object System.Text.UTF8Encoding($false)))
    $Version = $NextVersion
    $Bumped = $true
}

function Restore-Version {
    if (-not $Bumped) {
        return
    }
    [IO.File]::WriteAllText($ConfigPath, $OriginalConfigText, (New-Object System.Text.UTF8Encoding($false)))
    [Console]::Error.WriteLine('打包未成功，已还原 mygo.json 的版本号')
}

try {
    if ($Clean) {
        Write-Step '清理构建目录（-Clean）'
        if (Test-Path $Build) {
            Remove-Item -Recurse -Force $Build
        }
    }
    New-Item -ItemType Directory -Force -Path $Build | Out-Null

    Write-Step '使用已提交的 models.dev 能力快照'

    Write-Step '构建 Web 前端（web/ -> internal/webassets/dist）'
    $Npm = (Get-Command npm.cmd -ErrorAction SilentlyContinue).Source
    if (-not $Npm) {
        $Npm = (Get-Command npm -ErrorAction SilentlyContinue).Source
    }
    if (-not $Npm) {
        throw '需要 npm 才能构建 web 前端'
    }
    $Web = Join-Path $Root 'web'
    if (-not (Test-Path (Join-Path $Web 'node_modules'))) {
        Push-Location $Web
        try {
            & $Npm ci
            if ($LASTEXITCODE -ne 0) {
                throw 'npm ci 失败'
            }
        } finally {
            Pop-Location
        }
    }
    Push-Location $Web
    try {
        & $Npm run build
        if ($LASTEXITCODE -ne 0) {
            throw 'npm run build 失败'
        }
    } finally {
        Pop-Location
    }

    Write-Step "构建 MCode $Version（windows/amd64）"
    $Go = (Get-Command go.exe -ErrorAction SilentlyContinue).Source
    if (-not $Go) {
        $Go = (Get-Command go -ErrorAction SilentlyContinue).Source
    }
    if (-not $Go) {
        throw '需要 Go 才能打包'
    }
    Push-Location $Root
    try {
        & $Go tool mygo build -platform windows/amd64 -o $Build -skip-notarize
        if ($LASTEXITCODE -ne 0) {
            throw 'go tool mygo build 失败'
        }
    } finally {
        Pop-Location
    }

    $Exe = Join-Path $Target 'MCode.exe'
    if (-not (Test-Path $Exe)) {
        throw "未生成 $Exe"
    }
    $Installers = @(Get-ChildItem -Path $Target -Filter '*-setup-*-amd64.exe' -File)
    if ($Installers.Count -ne 1) {
        throw "应在 $Target 生成一个 Windows 安装程序，实际 $($Installers.Count) 个"
    }
    $Installer = $Installers[0].FullName
    if ($Installer -notmatch [regex]::Escape("-$Version-amd64.exe")) {
        throw "安装程序版本与 mygo.json 不一致: $Installer"
    }
} catch {
    Restore-Version
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}

$Action = if ($NoRelaunch) { '覆盖安装' } else { '覆盖安装并重启' }
Write-Step "$Action MCode"
$PowerShell = Join-Path $PSHOME 'powershell.exe'
if (-not (Test-Path $PowerShell)) {
    $PowerShell = 'powershell.exe'
}
$LogPath = Join-Path $env:TEMP 'mcode-relaunch.log'
$HelperArgs = @(
    '-NoProfile'
    '-ExecutionPolicy', 'Bypass'
    '-File', "`"$Helper`""
    '-Installer', "`"$Installer`""
    '-Identifier', "`"$Identifier`""
    '-DefaultInstallDir', "`"$DefaultInstallDir`""
    '-WaitPid', $PID
    '-LogPath', "`"$LogPath`""
)
if ($NoRelaunch) {
    $HelperArgs += '-NoRelaunch'
}
try {
    Start-Process -FilePath $PowerShell -ArgumentList $HelperArgs -WindowStyle Hidden | Out-Null
} catch {
    Fail "无法安排后台安装: $($_.Exception.Message)"
}

Write-Output "已在后台安排$Action，日志: $LogPath"
