# 构建 Windows 安装包(dist\yanhektddl-setup-<版本>.exe)
# 用法:pwsh installer\build-installer.ps1 [-Version 0.4.0]
param([string]$Version = "0.7.0")

$ErrorActionPreference = "Stop"
# 脚本位于 installer\ 子目录,项目根目录是其父目录
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root

# 1) 找 ISCC(Inno Setup 编译器)
$iscc = @(
    "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe",
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

# 2) 生成 exe 图标资源(rsrc -> rsrc.syso,go build 自动打入)
$ico = Join-Path $root "assets\app.ico"
if (Test-Path $ico) {
    $rsrc = Join-Path $env:USERPROFILE "go\bin\rsrc.exe"
    if (-not (Test-Path $rsrc)) {
        Write-Host ">> go install rsrc" -ForegroundColor Cyan
        go install github.com/akavel/rsrc@latest
    }
    if (Test-Path $rsrc) {
        & $rsrc -ico $ico -o (Join-Path $root "rsrc.syso")
        Write-Host ">> 已生成 rsrc.syso(应用图标)" -ForegroundColor Cyan
    }
}

# 3) 构建 Go 主程序
New-Item -ItemType Directory -Force -Path (Join-Path $root "build") | Out-Null
Write-Host ">> go build yanhektddl.exe" -ForegroundColor Cyan
go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $root "build\yanhektddl.exe") .
if ($LASTEXITCODE -ne 0) { throw "go build 失败" }

# 4) 编译安装包
Write-Host ">> ISCC installer.iss (v$Version)" -ForegroundColor Cyan
& $iscc "/DMyAppVersion=$Version" (Join-Path $PSScriptRoot "installer.iss") | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Inno Setup 编译失败" }

$out = Join-Path $root "dist\yanhektddl-setup-$Version.exe"
Write-Host "完成:$out" -ForegroundColor Green
