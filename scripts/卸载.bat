@echo off
rem NOTE: keep ANSI(GBK)+CRLF; do NOT re-save as UTF-8.
title 延河课堂 DDL - 卸载
echo ============================================================
echo  将执行以下清理:
echo    1. 结束运行中的 yanhektddl.exe
echo    2. 删除每日 9:00 巡检计划任务
echo    3. 删除桌面快捷方式
echo ============================================================
set /p CONFIRM=确认卸载请输入 y 回车:
if /i not "%CONFIRM%"=="y" (
  echo 已取消,未做任何更改。
  pause
  exit /b 0
)
taskkill /IM yanhektddl.exe /F >nul 2>&1
schtasks /Delete /TN YanhektDDLMonitor /F >nul 2>&1
del "%USERPROFILE%\Desktop\延河课堂DDL.lnk" >nul 2>&1
echo.
echo 清理完成。登录会话与报告数据都在本目录,确认无需保留后
echo 手动删除整个文件夹即可彻底清除:
echo   %~dp0
echo.
pause
