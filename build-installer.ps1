# 构建 Windows 安装包(dist\yanhektddl-setup-<版本>.exe)
# 用法:pwsh build-installer.ps1 [-Version 1.0.0]
param([string]$Version = "0.2.0")

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
Set-Location $root

# 1) 找 ISCC(Inno Setup 编译器)
$iscc = @(
    "C:\Program Files (x86)\Inno Setup 6\ISCC.exe",
    "C:\Program Files\Inno Setup 6\ISCC.exe",
    "C:\Program Files\Inno Setup 7\ISCC.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $iscc) {
    $cmd = Get-Command iscc -ErrorAction SilentlyContinue
    if ($cmd) { $iscc = $cmd.Source }
}
if (-not $iscc) {
    Write-Host "未找到 Inno Setup,正在经 winget 安装 Inno Setup 6 …" -ForegroundColor Yellow
    winget install --id JRSoftware.InnoSetup -e --accept-source-agreements --accept-package-agreements
    $iscc = @(
        "C:\Program Files (x86)\Inno Setup 6\ISCC.exe",
        "C:\Program Files\Inno Setup 6\ISCC.exe"
    ) | Where-Object { Test-Path $_ } | Select-Object -First 1
    if (-not $iscc) { throw "Inno Setup 安装失败,请手动安装后重试" }
}

# 2) 构建 Go 主程序
New-Item -ItemType Directory -Force -Path (Join-Path $root "build") | Out-Null
Write-Host ">> go build yanhektddl.exe" -ForegroundColor Cyan
go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $root "build\yanhektddl.exe") .
if ($LASTEXITCODE -ne 0) { throw "go build 失败" }

# 3) 编译安装包
Write-Host ">> ISCC installer.iss (v$Version)" -ForegroundColor Cyan
& $iscc "/DMyAppVersion=$Version" (Join-Path $root "installer.iss") | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Inno Setup 编译失败" }

$out = Join-Path $root "dist\yanhektddl-setup-$Version.exe"
Write-Host "完成:$out" -ForegroundColor Green
