# AGENTS.md

## 程序功能(一句话)

延河课堂/北理工乐学 + 智慧树(共享课、智慧课程)的 DDL 监控:单文件 Go exe 驱动系统 Edge 抓取本人课程作业/考试/话题讨论截止日期,本地服务 + 托盘 + 速查窗口,可选每日 9:00 通知。详细说明见 [README.md](README.md),平台接口细节见 [docs/zhihuishu-api.md](docs/zhihuishu-api.md)。

## 项目结构与调试

```
yanhektddl-mon/
├─ main.go               # 入口:参数分发;go:embed web/ 页面
├─ browser.go / js.go    # 抓取:chromedp 驱动系统 Edge;平台接口 JS 片段
├─ zhihuishu.go          # 智慧树:secretStr 加密、登录探测、智慧课程(作业/考试/话题讨论)
├─ zhihuishu_share.go    # 智慧树共享课:recruitId 编码、Go 侧 HTTP、网关预热
├─ normalize.go          # 数据归一:三路来源的 end/id/kind/status/url 抹平成同一套
├─ report.go / types.go  # 数据模型、临期分组规则、Markdown/CSV 报告
├─ server.go / desktop.go# 本地 HTTP 服务(127.0.0.1:8765)、桌面模式、窗口开关
├─ tray.go / login.go    # 系统托盘、首次登录向导
├─ platform_windows.go   # Win32 适配:控制台/DPI/单实例/窗口(纯 syscall,禁止引入 CGO)
├─ platform_other.go     # 非 Windows 占位
├─ web/                  # dashboard.html / login.html / settings.html / app.css(三页共用样式,构建时嵌入 exe)
├─ assets/               # app.ico(托盘与 exe 图标)
├─ installer/            # installer.iss + build-installer.ps1(输出 dist/ 安装包)
├─ scripts/              # 便携版 bat(GBK 编码!勿转 UTF-8)
├─ tools/                # make_icon.py 等离线工具
├─ legacy_python/        # 旧 Python 实现存档,不维护
├─ docs/                 # 平台接口调研笔记(zhihuishu-api.md:签名算法、接口清单、实测样例)
└─ build/ dist/          # 构建产物,已 gitignore,不入库
```

调试方法(按需选用):

```powershell
go build -trimpath -ldflags "-s -w -X main.version=0.3.1" -o yanhektddl.exe .
go vet ./...                                   # 静态检查,提交前必过
go test -run "TestBrowserSmoke|TestReopenSequence" -v .   # 真实起停 headless Edge,无需登录
go test -short .                              # 单元测试(跳过联网冒烟)
go test -run "TestAesSecretVectors|TestZhs" -v .   # 智慧树签名/映射/未登录判定

go build -trimpath -ldflags "-s -w -X main.version=<版本>" -o build/yanhektddl.exe .   # 调试时别污染仓库根目录的 exe
./build/yanhektddl.exe --fetch        # 全链路验收(注意 build/ 下是另一份 browser_profile)

./yanhektddl.exe --fetch --visible    # 可见浏览器窗口,排查登录/抓取问题
./yanhektddl.exe --from-json           # 不联网,用 ddl_data.json 离线复算报告
./yanhektddl.exe                       # 桌面模式;异常看同目录 server.log
curl http://127.0.0.1:8765/api/status  # 服务是否存活
./installer/build-installer.ps1        # 产出 dist/yanhektddl-setup-<版本>.exe
```

常见排错:抓取失败先看 `server.log`;"chrome failed to start" 多为 profile 锁残留,程序已内置清锁重试,仍失败就杀掉命令行含 `browser_profile` 的 msedge 进程后重试。日志里出现 "[智慧树] 未登录或会话失效" 说明该数据源没登记:`--fetch --visible` 或登录向导会依次给乐学(必需)→延河(可选)→智慧树(可选)三次登录机会,已登录的会自动跳过提示;不登录只是少一个数据源。

## Git 操作规范

**何时 commit**:完成一个完整、可验证的功能或修复(构建通过、相关测试通过、涉及安装包/托盘/服务的改动已在实机验证)后立即提交;一次 commit 只做一件事,不要把无关改动混进来。提交信息用中文,首行为标题,正文说明动机与验证结果。

**commit 前检查**:

```powershell
git status --short   # 逐条确认,重点排查不该入库的文件
```

- 二进制与产物不入库:`*.exe`、`build/`、`dist/`、`rsrc.syso`、`browser_profile/`、`ddl_data.json`、`ddl_report.*`、`server.log`(应被 .gitignore 挡住;出现即说明 ignore 规则有漏洞,先修 .gitignore)
- 尤其警惕 **MSVC/MinGW 编译器误产的 .exe/.dll/.obj/.pdb**、编辑器临时文件——这类"无关二进制"一旦进了历史就很难清掉
- `scripts/*.bat` 是 GBK+CRLF 编码敏感文件,若 status 里出现异常改动,先确认不是编辑器擅自转码
- `git add` 后再看一次 `git status --short`,确认暂存区没有夹带

**commit 后 push**:本地提交后立即 `git push` 到 origin(main 分支),不要攒多个提交;推送失败先解决冲突/权限,不要绕过。

## 发布 Release(gh CLI)

前提:三处版本号已统一(见上文),且 `git status` 干净、代码已推送到 main。

```powershell
# 0) 用 PowerShell 7(pwsh)执行,不要用 Windows PowerShell 5.1:
#    build-installer.ps1 是无 BOM 的 UTF-8,5.1 按 ANSI 读取,会在中文字符串处
#    报「字符串缺少终止符 / 缺少结束的 }」而编译失败(不是脚本坏了)。
# 1) 构建(安装包 + 便携版两个资产)
pwsh -NoProfile -ExecutionPolicy Bypass -File installer/build-installer.ps1 -Version 0.4.1
copy-item build\yanhektddl.exe dist\yanhektddl-0.4.1.exe

# 2) 写发布说明(临时 md,用完删除),面向用户可读:
#    下载对照表(安装包推荐/便携版)、本版亮点、环境要求

# 3) 创建 release(gh 会自动在 main 上建同名标签)
gh release create v0.4.1 dist\yanhektddl-setup-0.4.1.exe dist\yanhektddl-0.4.1.exe `
  -R Dayu-L/yanhektddl-mon --target main `
  --title "v0.4.1 —— <一句话标题>" --notes-file 临时.md

# 4) 发布后发现遗漏:重新构建后覆盖资产(不改标签)
gh release upload v0.4.1 dist\yanhektddl-setup-0.4.1.exe -R Dayu-L/yanhektddl-mon --clobber
```

验证:`gh release view v0.4.1 -R Dayu-L/yanhektddl-mon` 资产齐全、非 draft、非 prerelease。
注意:gh 需对该仓库有 release 写权限,报 403 时先 `gh auth status` 检查账号/token。

## 其他约定

- 版本号三处同源:`main.go` 的 `version`、`installer/installer.iss`、`installer/build-installer.ps1`;发布时统一改动,或用 `./installer/build-installer.ps1 -Version x.y.z` 一次注入
- 新增 Windows 能力优先写进 `platform_windows.go`(纯 syscall);**禁止引入需要 CGO 的依赖**(本机无 gcc,且会破坏单文件分发)
- 抓取逻辑改动的验收标准:`--fetch` 全链路真实跑通,不是"编译通过"
- 新增数据源一律「尽力而为」:未登录/接口异常只能记日志跳过(参考 `browser.go` 阶段 4 + `logZhsSkip`),不得让延河/乐学的核心流程失败
- 新增数据源必须在采集出口经过 `normalize.go` 的 `normalizeItem`/`normalizeCourse`
  (时间统一北京时间、id 字符串、kind 规范值、status 显式、url 最终地址),不得把平台特有格式写进快照;
  数据源自身的类型名(如 `ai_homework`)在 `canonicalKind` 里映射到规范值即可
- 新增数据源时必须给 `Row.Platform` / `Course.Platform` 赋值(速查页据此打标签与筛选),并在 `docs/` 同步实测结论
- 平台密钥、签名算法等平台常量集中在对应源文件头部(如 `zhihuishu.go` 的 `zhsSiteAESKey`),并在 `docs/` 记下实测依据;平台换包失效时先查这类常量
