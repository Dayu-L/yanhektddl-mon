@echo off
chcp 65001 >nul
title 延河课堂 DDL - 创建桌面快捷方式
powershell -NoProfile -Command ^
  "$ws = New-Object -ComObject WScript.Shell;" ^
  "$lnk = $ws.CreateShortcut([IO.Path]::Combine($env:USERPROFILE, 'Desktop', '延河课堂DDL.lnk'));" ^
  "$lnk.TargetPath = '%~dp0yanhektddl.exe';" ^
  "$lnk.WorkingDirectory = '%~dp0';" ^
  "$lnk.IconLocation = '%~dp0yanhektddl.exe, 0';" ^
  "$lnk.Save()"
if exist "%USERPROFILE%\Desktop\延河课堂DDL.lnk" (
  echo 已创建桌面快捷方式:延河课堂DDL
) else (
  echo 创建失败,请手动为 yanhektddl.exe 创建快捷方式
)
pause
