# 延河课堂 DDL 监控 · yanhektddl-mon

作业/测验截止日期 **一键查看 · 一键直达 · 每日自动提醒**。

自动读取延河课堂(aita.yanhekt.cn)与北理工乐学 eclass 上你自己的课程数据,
汇总成一张按截止时间排序的清单:临期任务高亮、漏交任务报警、点一下直达作业页面。

> 隐私:登录会话、课程和作业数据只保存在本机程序目录,不上传任何服务器。
> 首次安装只需在弹出的 Edge 窗口里登录一次(账号 + 密码 + 验证码),之后长期免登录。

面向使用者的操作步骤见 **[使用说明.md](使用说明.md)**。

---

## 功能

- **一屏总览**:所有课程的作业、测验、问卷按截止时间排序,倒计时按秒跳动
- **临期高亮**:红 = 已过期未提交 / 橙 = 3 天内 / 黄 = 7 天内 / 灰 = 已完成
- **一键直达**:点击任务行或「打开 ↗」直接进入平台对应页面
- **作业详情**:展开老师填写的作业要求原文
- **后台刷新**:点「刷新数据」在后台重新抓取,不阻塞窗口
- **每日巡检**:可选每天早上 9:00 自动检查,临期/漏交弹出 Windows 通知
- **零环境安装**:自带官方 Python 安装包,图形向导自动装好 Python 与抓取组件
- **干净卸载**:一键删除桌面图标、计划任务、运行中的服务和程序目录

## 快速开始

1. 解压项目压缩包到任意目录(建议非系统盘)
2. 双击 **`启动向导.bat`**,按向导提示完成安装
   - 电脑没有 Python 时会自动静默安装(约 1-2 分钟)
   - 会检查并自动安装 `playwright` 抓取组件
   - 在弹出的 Edge 窗口登录一次延河课堂账号
3. 装好后双击桌面 **「延河课堂DDL」** 即可打开速查窗口

环境要求:Windows 10/11、Microsoft Edge(系统自带)、可访问校园网的网络环境。
Python 3.11+ 由安装包自动处理,普通用户无需手动安装。

## 目录结构

```
yanhektddl-mon/
├─ 启动向导.bat           # 用户入口:检测/安装 Python 后打开图形向导
├─ 安装向导.py            # 四步图形安装向导(Tkinter)
├─ 安装Python.ps1        # 静默安装随附的官方 Python(仅当前用户)
├─ 卸载.bat               # 卸载:停服务、删图标、删计划任务、删目录
├─ 使用说明.md            # 面向使用者的安装与日常使用手册
├─ yanhekt_monitor.py     # 核心:浏览器抓取、数据快照、Markdown/CSV 报告、CLI
├─ ddl_server.py          # 本地服务(127.0.0.1:8765)+ 桌面快捷方式启动器
├─ dashboard.html         # 速查窗口前端页面
├─ README.md              # 本文件:项目说明与开发指引
└─ LICENSE                # MIT
```

程序运行后会在同目录生成以下文件(均已加入 `.gitignore`,不要提交):

| 文件/目录 | 内容 |
|---|---|
| `browser_profile/` | 持久化浏览器会话(含登录 Cookie,**属于隐私数据**) |
| `ddl_data.json` | 抓取数据快照(课程、任务、提交状态) |
| `ddl_report.md` / `ddl_report.csv` | 生成的截止日期报告 |
| `python-*-amd64.exe` | 发布用离线 Python 安装包(约 33 MB,不入库) |
| `__pycache__/` | Python 字节码缓存 |

## 工作原理

1. `yanhekt_monitor.py` 用 Playwright 启动一个独立的持久化 Edge 会话,
   会话保存在 `browser_profile/`,不影响日常使用的浏览器。
2. 在已登录的会话内调用平台官方接口:
   - 延河课堂课程列表 → 课程 `zhiyuan_id` 与课程关闭时间
   - eclass 作业 / 考试 / 问卷接口 → 任务与截止时间
   - 提交状态接口 → 是否已完成
3. 数据写入 `ddl_data.json`,`build_report()` 统一计算剩余时间、分组和报告,
   同时供 Markdown/CSV 报告与本地页面使用。
4. `ddl_server.py` 在 `127.0.0.1:8765` 提供页面与 `/api/refresh` 后台刷新接口;
   重复启动时只新开窗口,不会起第二个服务。

只读取使用者本人的课程信息,不抓取、不存储其他同学的数据。

## 开发与调试

```powershell
# 安装依赖(浏览器抓取组件)
python -m pip install playwright

# 直接抓取一次,生成 ddl_report.md / ddl_report.csv
python yanhekt_monitor.py

# 需要观察浏览器行为时(显示 Edge 窗口)
python yanhekt_monitor.py --visible

# 直接用已有快照重新生成报告,不打开浏览器
python yanhekt_monitor.py --from-json

# 启动本地服务与速查页面
python ddl_server.py
```

常用参数:

| 参数 | 说明 |
|---|---|
| `--watch N` | 每 N 分钟自动巡检一次(可 `Ctrl+C` 停止) |
| `--days N` | 「临期」阈值天数,默认 3 |
| `--notify` | 发现临期/漏交任务时弹出 Windows 通知 |
| `--visible` | 显示浏览器窗口,便于排查登录/抓取问题 |
| `--from-json [路径]` | 不联网,直接用 `ddl_data.json`(或指定快照)生成报告 |

## 发布打包

发布压缩包需要保持根目录的扁平结构(脚本之间使用相对路径),
并额外放入官方 Python 安装包:

1. 从 [python.org](https://www.python.org/downloads/windows/) 下载 Windows 64 位安装程序,
   命名为 `python-<版本>-amd64.exe`,放在项目根目录。
2. 确认 `启动向导.bat`、`安装向导.py`、`安装Python.ps1`、`卸载.bat`、
   `yanhekt_monitor.py`、`ddl_server.py`、`dashboard.html`、`使用说明.md` 齐全。
3. 打包时**不要**包含 `browser_profile/`、`ddl_data.json`、`ddl_report.*`、`__pycache__/`
   等本机运行时数据。

安装包体积较大,不纳入 Git;`.gitignore` 已忽略 `python-*-amd64.exe`。

## 卸载

双击 **`卸载.bat`**,按提示输入 `y` 回车,即可自动清理。手动卸载等价于:

1. 删除桌面「延河课堂DDL」快捷方式
2. PowerShell 执行 `Unregister-ScheduledTask -TaskName 'YanhektDDLMonitor'`
3. 删除整个程序文件夹

## 常见问题

见 [使用说明.md · 常见问题](使用说明.md#常见问题):
提示需要登录、抓取失败、数据存放位置、修改巡检频率等。

## 许可证

[MIT](LICENSE) © 2026 Dayu-L
