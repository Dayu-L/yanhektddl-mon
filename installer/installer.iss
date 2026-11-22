; 延河课堂 DDL —— Inno Setup 安装包脚本
; 构建:.\\build-installer.ps1(内部调用 ISCC 编译本文件)
;
; 设计要点:
;   - PrivilegesRequired=lowest:无需 UAC/管理员,普通用户双击即装
;   - 安装到 {localappdata}\Programs\YanhektDDL(Chrome 式用户目录,
;     用户可写 -> 运行数据(browser_profile/ 报告)可直接放在 exe 旁)
;   - 注册标准"应用与功能"卸载入口;卸载时自动停进程、删计划任务
;   - 卸载后保留登录数据与报告,由用户确认后手动删除(隐私自决)

#define MyAppName "延河课堂 DDL"
; 版本号可由 build-installer.ps1 经 /D 传入,缺省 0.4.0
#ifndef MyAppVersion
#define MyAppVersion "0.4.1"
#endif
#define MyAppPublisher "Dayu-L"
#define MyAppExeName "yanhektddl.exe"

[Setup]
AppId={{7F2A8B1C-9D3E-4A55-BC07-ED41A9C2D6E4}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
UninstallDisplayName={#MyAppName}
DefaultDirName={localappdata}\Programs\YanhektDDL
PrivilegesRequired=lowest
DisableProgramGroupPage=yes
WizardStyle=modern
OutputDir=..\dist
OutputBaseFilename=yanhektddl-setup-{#MyAppVersion}
Compression=lzma2/max
SolidCompression=yes
ArchitecturesInstallIn64BitMode=x64compatible
LicenseFile=..\LICENSE
SetupIconFile=..\assets\app.ico
UninstallDisplayIcon={app}\app.ico

[Languages]
Name: "chinese"; MessagesFile: "ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式(&D)"; Flags: checkedonce
Name: "dailytask"; Description: "开启每天 9:00 自动巡检提醒(&M)"; Flags: checkedonce

[Files]
Source: "..\build\yanhektddl.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\assets\app.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\app.ico"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\app.ico"; Tasks: desktopicon

[Run]
; 注册每日巡检(静默执行,不弹黑窗)
Filename: "{app}\{#MyAppExeName}"; Parameters: "--install-task"; Tasks: dailytask; \
  Flags: runhidden; StatusMsg: "正在注册每日巡检任务…"
; 安装完成后启动(复选框)
Filename: "{app}\{#MyAppExeName}"; Description: "启动 {#MyAppName}"; \
  Flags: nowait postinstall skipifsilent

[UninstallRun]
; 顺序:先停运行中的服务,再删计划任务,最后由 Inno 删除程序文件
Filename: "taskkill"; Parameters: "/IM {#MyAppExeName} /F"; Flags: runhidden; RunOnceId: "KillApp"
Filename: "{app}\{#MyAppExeName}"; Parameters: "--uninstall-task"; Flags: runhidden; RunOnceId: "DelTask"

[UninstallDelete]
; 登录数据(下述 [Code] 会询问)除外,只自动删程序日志
Type: files; Name: "{app}\server.log"

[Code]
// 停掉主程序 + 占用本程序 browser_profile 的抓取浏览器(强杀主程序后会遗留孤儿 Edge,
// 不处理则数据删除/目录移除会因文件锁失败)
procedure KillAppAndStaleBrowsers;
var
  rc: Integer;
begin
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM yanhektddl.exe /F',
    '', SW_HIDE, ewWaitUntilTerminated, rc);
  Exec('powershell.exe',
    '-NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { ($_.Name -eq ''msedge.exe'' -or $_.Name -eq ''chrome.exe'') -and $_.CommandLine -like ''*' + ExpandConstant('{app}') + '\browser_profile*'' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }"',
    '', SW_HIDE, ewWaitUntilTerminated, rc);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep <> usUninstall then exit;
  KillAppAndStaleBrowsers;
  // 静默卸载(/VERYSILENT)不弹窗,默认保留数据
  if UninstallSilent then exit;
  if MsgBox('是否保留用户数据?'
      + #13#10 + '保留:browser_profile(登录会话)、ddl_data.json、ddl_report.*(推荐)'
      + #13#10 + '删除:登录会话与报告一并清除,卸载后不残留任何数据',
      mbConfirmation, MB_YESNO) = IDNO then
  begin
    DelTree(ExpandConstant('{app}\browser_profile'), True, True, True);
    DeleteFile(ExpandConstant('{app}\ddl_data.json'));
    DeleteFile(ExpandConstant('{app}\ddl_report.md'));
    DeleteFile(ExpandConstant('{app}\ddl_report.csv'));
  end;
end;
