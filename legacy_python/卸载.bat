@echo off
setlocal EnableExtensions
cd /d "%~dp0"

echo.
echo  ================================================
echo   延河课堂 DDL 监控 - 卸载程序
echo.
echo   将删除:
echo     - 桌面「延河课堂DDL」快捷方式
echo     - 每日 9:00 自动巡检任务
echo     - 正在运行的速查服务
echo     - 整个程序文件夹(包含登录会话与全部数据)
echo  ================================================
echo.
set /p "ans=  确定卸载?输入 y 回车确认，直接回车取消: "
if /i not "%ans%"=="y" goto :cancel

echo.
echo  [1/4] 正在关闭速查服务...
powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -match 'ddl_server' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }"
echo  [2/4] 正在删除桌面快捷方式...
del /f /q "%USERPROFILE%\Desktop\延河课堂DDL.lnk" 2>nul
echo  [3/4] 正在移除每日巡检任务...
schtasks /delete /tn "YanhektDDLMonitor" /f >nul 2>&1
echo  [4/4] 正在删除程序文件夹(本窗口将自动关闭)...
echo.
echo  卸载完成，正在清理残留...
start "" cmd /c "timeout /t 2 /nobreak >nul & rmdir /s /q ""%~dp0."""
exit /b

:cancel
echo.
echo  已取消，未做任何更改。
pause
exit /b
