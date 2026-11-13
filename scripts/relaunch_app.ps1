#Requires -Version 5.1
<#
.SYNOPSIS
在调用方退出后静默覆盖安装 MCode 并重新启动应用

.DESCRIPTION
调用方可能是 MCode 应用自己（内置终端或“运行脚本”菜单）：应用退出时会关闭
终端会话，甚至杀掉子进程。所以打包脚本先把自己脱离成后台 PowerShell，再由本脚本
依次完成：等调用方退出 -> 静默运行安装程序（安装程序会先关闭旧实例）-> 启动新应用。
日志写到 -LogPath，默认 %TEMP%\mcode-relaunch.log。
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Installer,
    [Parameter(Mandatory = $true)]
    [string]$Identifier,
    [Parameter(Mandatory = $true)]
    [string]$DefaultInstallDir,
    [int]$WaitPid = 0,
    [switch]$NoRelaunch,
    [string]$LogPath = (Join-Path $env:TEMP 'mcode-relaunch.log')
)

$ErrorActionPreference = 'Stop'

function Write-Log([string]$Message) {
    $line = '{0} {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Message
    Add-Content -Path $LogPath -Value $line -Encoding UTF8
}

function Fail([string]$Message) {
    Write-Log "错误: $Message"
    exit 1
}

$LogDir = Split-Path -Parent $LogPath
if ($LogDir -and -not (Test-Path $LogDir)) {
    New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
}

if (-not (Test-Path $Installer)) {
    Fail "安装程序不存在: $Installer"
}

Write-Log "开始覆盖安装 $Installer（后台 PID $PID）"

# 调用方此时只剩退出这一步，没有必要久等；安装程序随后会关闭正在运行的应用。
if ($WaitPid -gt 0) {
    $Deadline = (Get-Date).AddSeconds(2)
    while ((Get-Date) -lt $Deadline) {
        if (-not (Get-Process -Id $WaitPid -ErrorAction SilentlyContinue)) {
            break
        }
        Start-Sleep -Milliseconds 200
    }
}

# 静默安装；NSIS 安装程序会先关闭安装目录里正在运行的 MCode。
try {
    $Install = Start-Process -FilePath $Installer -ArgumentList '/S' -WindowStyle Hidden -PassThru
} catch {
    Fail "无法运行安装程序: $($_.Exception.Message)"
}
if (-not $Install.WaitForExit(300000)) {
    try {
        if (-not $Install.HasExited) {
            $Install.Kill()
        }
    } catch {
    }
    Fail '安装程序超时（300 秒）'
}
if ($Install.ExitCode -ne 0) {
    Fail "安装程序退出码 $($Install.ExitCode)"
}

# 安装位置以卸载注册表为准，用户曾自选目录时也能找到。
$InstallDir = $null
$UninstallKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\$Identifier"
if (Test-Path $UninstallKey) {
    $InstallDir = (Get-ItemProperty -Path $UninstallKey -Name InstallLocation -ErrorAction SilentlyContinue).InstallLocation
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = $DefaultInstallDir
}
$InstallDir = $InstallDir.Trim().Trim('"')

if ($NoRelaunch) {
    Write-Log "安装完成: $InstallDir（未重启）"
    exit 0
}

$Exe = Join-Path $InstallDir 'MCode.exe'
if (-not (Test-Path $Exe)) {
    Fail "安装后找不到 $Exe"
}

# 等旧进程完全退出，避免新进程只激活旧实例。
$Running = @()
$Deadline = (Get-Date).AddSeconds(10)
while ((Get-Date) -lt $Deadline) {
    $Running = @(Get-CimInstance Win32_Process -Filter "Name='MCode.exe'" -ErrorAction SilentlyContinue | Where-Object {
            $_.ExecutablePath -and $_.ExecutablePath.Equals($Exe, [StringComparison]::OrdinalIgnoreCase)
        })
    if ($Running.Count -eq 0) {
        break
    }
    Start-Sleep -Milliseconds 200
}
if ($Running.Count -gt 0) {
    Fail "旧进程仍未退出: $($Running.ProcessId -join ', ')"
}

try {
    Start-Process -FilePath $Exe -WorkingDirectory $InstallDir | Out-Null
} catch {
    Fail "启动 MCode 失败: $($_.Exception.Message)"
}

# 启动后确认新进程确实起来了；单实例应用在旧实例还在时只会激活旧实例。
$Running = @()
$Deadline = (Get-Date).AddSeconds(30)
while ((Get-Date) -lt $Deadline) {
    $Running = @(Get-CimInstance Win32_Process -Filter "Name='MCode.exe'" -ErrorAction SilentlyContinue | Where-Object {
            $_.ExecutablePath -and $_.ExecutablePath.Equals($Exe, [StringComparison]::OrdinalIgnoreCase)
        })
    if ($Running.Count -gt 0) {
        break
    }
    Start-Sleep -Milliseconds 200
}
if ($Running.Count -eq 0) {
    Fail "MCode 没有启动: $Exe"
}

Write-Log "已重新启动 MCode: $Exe"
