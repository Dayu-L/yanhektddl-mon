@echo off
setlocal EnableExtensions
cd /d "%~dp0"

rem ============================================================
rem   延河课堂 DDL 监控 - 安装向导启动器
rem   双击本文件开始安装（全程自动，无需懂任何技术）
rem ============================================================

rem ---- 第 1 步:在常见位置找真实的 Python(任意 3.x 版本) ----
set "PYW="
for /d %%d in ("%LOCALAPPDATA%\Programs\Python\Python*") do if exist "%%~d\pythonw.exe" set "PYW=%%~d\pythonw.exe"
for /d %%d in ("C:\Program Files\Python*") do if exist "%%~d\pythonw.exe" set "PYW=%%~d\pythonw.exe"
for /d %%d in ("C:\Program Files (x86)\Python*") do if exist "%%~d\pythonw.exe" set "PYW=%%~d\pythonw.exe"
for /d %%d in ("C:\Python*") do if exist "%%~d\pythonw.exe" set "PYW=%%~d\pythonw.exe"
if defined PYW goto :launch_wizard

rem ---- 第 2 步:py 启动器的窗口版 ----
rem      必须先验证它指向的 Python 真实可用；否则"装过又卸载"时
rem      启动器会残留并直接报错（Unable to create process）中断流程
where pyw >nul 2>nul
if not %errorlevel%==0 goto :try_path_python
py -3 -c "pass" >nul 2>nul
if not %errorlevel%==0 goto :try_path_python
start "" pyw -3 -X utf8 "安装向导.py"
exit /b

rem ---- 第 3 步:PATH 里的真 Python(排除微软商店占位程序) ----
:try_path_python
for /f "delims=" %%p in ('where python 2^>nul') do (
    echo %%p | findstr /i "WindowsApps" >nul
    if errorlevel 1 (
        start "" "%%p" -X utf8 "安装向导.py"
        exit /b
    )
)

rem ---- 第 4 步:没找到 Python → 用本包附带的官方安装程序自动装 ----
if not exist "python-*-amd64.exe" goto :try_winget
powershell -NoProfile -ExecutionPolicy Bypass -File "安装Python.ps1"

rem 装完再找一次(注意:这些行必须在块外,变量才是执行时展开)
set "PYW="
for /d %%d in ("%LOCALAPPDATA%\Programs\Python\Python*") do if exist "%%~d\pythonw.exe" set "PYW=%%~d\pythonw.exe"
for /d %%d in ("C:\Program Files\Python*") do if exist "%%~d\pythonw.exe" set "PYW=%%~d\pythonw.exe"
if defined PYW goto :launch_wizard

echo   安装似乎没有完成。请重启电脑后重新双击本文件。
pause
exit /b

rem ---- 第 5 步:精简包(没带安装程序) → 试试系统自带的 winget ----
:try_winget
where winget >nul 2>nul
if not %errorlevel%==0 goto :manual
echo  正在通过系统组件自动安装 Python,请稍候(约 1-2 分钟)…
winget install -e --id Python.Python.3.14 --silent --accept-package-agreements --accept-source-agreements
set "PYW="
for /d %%d in ("%LOCALAPPDATA%\Programs\Python\Python*") do if exist "%%~d\pythonw.exe" set "PYW=%%~d\pythonw.exe"
if defined PYW goto :launch_wizard

rem ---- 最后:实在装不了,给出指引 ----
:manual
echo.
echo  ================================================
echo   自动安装没有成功。请手动装一下 Python：
echo.
echo   1. 打开浏览器访问 https://www.python.org/downloads/
echo   2. 下载并安装,安装界面第一步务必勾选
echo      "Add Python to PATH"  （这点很重要）
echo.
echo   3. 装好后重新双击本文件
echo  ================================================
echo.
pause
exit /b

rem ---- 启动图形安装向导 ----
:launch_wizard
start "" "%PYW%" -X utf8 "安装向导.py"
exit /b
