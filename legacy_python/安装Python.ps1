# Install Python for Yanhekt DDL (auto, per-user, no admin needed)
# Called by 启动向导.bat. Progress bar shown here + official installer window.

$ErrorActionPreference = 'SilentlyContinue'
$here = $PSScriptRoot

$inst = Get-ChildItem -Path $here -Filter 'python-*-amd64.exe' | Select-Object -First 1
if (-not $inst) {
    Write-Host '  [X] 没有找到 Python 安装程序(安装包不完整)'
    exit 1
}

function Find-PyW {
    return (Get-ChildItem -Path (Join-Path $env:LOCALAPPDATA 'Programs\Python') -Filter 'Python3*' -Directory |
        ForEach-Object { Join-Path $_.FullName 'pythonw.exe' } |
        Where-Object { Test-Path $_ } |
        Select-Object -First 1)
}

function Run-Installer($extraArgs, $label) {
    Write-Host "  · $label"
    $p = Start-Process -FilePath $inst.FullName -ArgumentList $extraArgs -PassThru
    $start = Get-Date
    $maxWait = 900
    while ($true) {
        if ($p -and $p.HasExited) { break }
        $elapsed = ((Get-Date) - $start).TotalSeconds
        if ($elapsed -gt $maxWait) { break }
        $pct = [Math]::Min(95, [int]($elapsed / 90 * 100))
        $remain = [Math]::Max(0, [int](90 - $elapsed))
        Write-Progress -Activity '正在安装 Python — 请勿关闭安装窗口' `
            -Status "预计还需约 $remain 秒(实际进度见 Python 安装窗口)" `
            -PercentComplete $pct
        Start-Sleep -Milliseconds 1000
    }
    Write-Progress -Activity '正在安装 Python' -Completed
    # 等待安装器子进程落盘(官方安装器分好几层,父进程可能先退出)
    for ($i = 0; $i -lt 30; $i++) {
        if (Find-PyW) { break }
        Start-Sleep -Seconds 1
    }
    $code = $null
    if ($p) { $code = $p.ExitCode }
    return $code
}

$ok = $false

if (Find-PyW) {
    $ok = $true
} else {
    Write-Host ''
    Write-Host '  =================================================='
    Write-Host '   正在安装 Python(仅安装给当前用户,无需管理员)'
    Write-Host '   · 屏幕上会出现 Python 官方安装窗口,自带进度显示'
    Write-Host '   · 下方的进度条为预计进度,以官方窗口为准'
    Write-Host '   · 请不要关闭任何窗口,约 1-2 分钟'
    Write-Host '  =================================================='
    Write-Host ''

    # 注意:Include_launcher=0 —— 不装 py 启动器(它默认要装给全机、需提权,
    # /passive 下提权失败会静默退出;本程序直接用 pythonw.exe,不需要启动器)
    $code = Run-Installer @(
        '/passive', 'InstallAllUsers=0', 'PrependPath=1',
        'Include_tcltk=1', 'Include_launcher=0'
    ) '第一次安装'

    if (Find-PyW) {
        $ok = $true
    } else {
        # 常见残留:之前装过又删了文件夹 → 注册表仍认为已安装 → 安装器直接退出 0
        Write-Host '  · 检测到旧版本残留,正在清理后重装(多等 1-2 分钟)…'
        $null = Run-Installer @('/uninstall', '/quiet') '清理旧版本'
        Start-Sleep -Seconds 3
        $code = Run-Installer @(
            '/passive', 'InstallAllUsers=0', 'PrependPath=1',
            'Include_tcltk=1', 'Include_launcher=0'
        ) '重新安装'
        if (Find-PyW) { $ok = $true }
    }
}

if ($ok) {
    Write-Host ''
    Write-Host '  [OK] Python 安装完成,正在继续打开安装向导…'
    Write-Host ''
    exit 0
}

Write-Host ''
Write-Host '  [X] Python 没有装完。'
if ($null -ne $code) {
    Write-Host "    安装器退出码: $code"
    if ($code -eq 1603) {
        Write-Host '    (1603 = 系统里有过损坏的旧版 Python 安装)'
        Write-Host '    处理方法:设置 -> 应用 -> 找到 Python 卸载;'
        Write-Host '    若卸载也报错,请重启电脑后再试;仍失败请联系分享给你的人。'
    }
}
$log = Get-ChildItem "$env:TEMP" -Filter 'Python*.log' | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($log) {
    Write-Host "    详细日志: $($log.FullName)"
    Write-Host ''
    Write-Host '  ---- 安装器日志最后 40 行(排查用,截图给技术支持) ----'
    Get-Content $log.FullName -Tail 40 | ForEach-Object { Write-Host ("  | " + $_) }
    Write-Host '  ----------------------------------------------------'
}
Write-Host '    兜底办法:手动双击本文件夹里的 python-3.14.10-amd64.exe 安装'
Write-Host '    (安装界面第一步勾选 Add Python to PATH)'
Write-Host ''
exit 1
