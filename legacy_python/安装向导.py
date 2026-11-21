#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
延河课堂 DDL 监控 · 图形安装向导(小白友好版)
================================================================================
双击「启动向导.bat」运行。

四步完成:
  1. 这是干什么(欢迎)
  2. 自动检查电脑环境,缺东西自动装(不用懂任何术语)
  3. 弹出 Edge 登录一次你的延河课堂账号(输验证码),自动抓取你的课程
  4. 一键完成:建桌面快捷方式 + 可选每日 9:00 自动提醒

所有数据只保存在你自己电脑的安装目录,不上传任何服务器。
"""

import json
import subprocess
import sys
import threading
import queue
import time
from pathlib import Path

import tkinter as tk
from tkinter import ttk, messagebox

BASE = Path(__file__).resolve().parent
MONITOR = BASE / "yanhekt_monitor.py"
SERVER = BASE / "ddl_server.py"
DATA = BASE / "ddl_data.json"

PY = sys.executable
PYW = Path(PY).with_name("pythonw.exe")
if not PYW.exists():
    PYW = Path(PY)

EDGE_PATHS = [
    Path(r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"),
    Path(r"C:\Program Files\Microsoft\Edge\Application\msedge.exe"),
]
CREATE_NO_WINDOW = 0x08000000 if sys.platform == "win32" else 0

# 配色与仪表盘一致
INK = "#0a0a0a"
MUTED = "#5f6368"
GROUND = "#f2f4f7"
SURFACE = "#ffffff"
BORDER = "#e6e6e6"
BLUE = "#1d5fbf"
GREEN = "#247b50"
RED = "#c4242a"
FONT = "Microsoft YaHei UI"

TOTAL_STEPS = 4


class Wizard(tk.Tk):
    def __init__(self):
        super().__init__()
        self.title("延河课堂 DDL · 安装向导")
        self.geometry("600x560")
        self.minsize(560, 520)
        self.configure(bg=GROUND)
        self.state = {
            "env_ok": False,
            "pull_ok": False,
            "courses": [],
            "daily": True,
        }
        self.q = queue.Queue()

        style = ttk.Style(self)
        try:
            style.theme_use("clam")
        except Exception:
            pass

        # ---- 头部 ----
        head = tk.Frame(self, bg=GROUND)
        head.pack(fill="x", padx=28, pady=(22, 0))
        tk.Label(head, text="延河课堂 DDL 监控", bg=GROUND, fg=INK,
                 font=(FONT, 17, "bold")).pack(side="left")
        self.step_lbl = tk.Label(head, text="", bg=GROUND, fg=MUTED,
                                 font=(FONT, 10))
        self.step_lbl.pack(side="right")

        # ---- 内容区 ----
        self.body = tk.Frame(self, bg=SURFACE, highlightthickness=1,
                             highlightbackground=BORDER)
        self.body.pack(fill="both", expand=True, padx=28, pady=16)

        # ---- 底部 ----
        self.foot = tk.Frame(self, bg=GROUND)
        self.foot.pack(fill="x", padx=28, pady=(0, 20))

        self.hint = tk.Label(self.foot, text="", bg=GROUND, fg=MUTED,
                             font=(FONT, 9), anchor="w")
        self.hint.pack(side="left")
        self.btn_area = tk.Frame(self.foot, bg=GROUND)
        self.btn_area.pack(side="right")

        self.show("welcome")

    # ------------------------------------------------------------ 框架

    def set_step(self, n, name):
        self.step_lbl.config(text=f"第 {n} / {TOTAL_STEPS} 步 · {name}")

    def clear_body(self):
        for w in self.body.winfo_children():
            w.destroy()
        for w in self.btn_area.winfo_children():
            w.destroy()

    def big_button(self, text, cmd, primary=True, state="normal"):
        bg = BLUE if primary else SURFACE
        fg = "#ffffff" if primary else INK
        b = tk.Button(self.btn_area, text=text, command=cmd, state=state,
                      bg=bg, fg=fg, activebackground=bg, activeforeground=fg,
                      relief="flat", padx=22, pady=8,
                      font=(FONT, 11, "bold"), cursor="hand2")
        b.pack(side="right", padx=(8, 0))
        return b

    def body_text(self, text, size=11, color=INK, bold=False,
                  pady=(0, 8), wraplen=530):
        tk.Label(self.body, text=text, bg=SURFACE, fg=color, justify="left",
                 wraplength=wraplen,
                 font=(FONT, size, "bold" if bold else "normal")
                 ).pack(anchor="w", padx=24, pady=pady)
        return None

    # ------------------------------------------------------------ 1 欢迎页

    def show(self, page):
        self.clear_body()
        getattr(self, "page_" + page)()

    def page_welcome(self):
        self.set_step(1, "了解")
        self.body_text("这个程序帮你盯着「作业/测验的截止时间」", 14, INK, True, pady=(28, 10))
        rows = [
            ("看", "打开桌面图标,一张清单按时间排好:什么时候交什么"),
            ("点", "点任意一条,直达那个作业的网页,不用自己翻平台"),
            ("提醒", "每天自动检查,快到期的、漏交的,Windows 弹窗提醒你"),
        ]
        for tag, txt in rows:
            f = tk.Frame(self.body, bg=SURFACE)
            f.pack(anchor="w", padx=24, pady=7)
            tk.Label(f, text=tag, bg=BLUE, fg="#ffffff", width=3,
                     font=(FONT, 10, "bold")).pack(side="left")
            tk.Label(f, text=txt, bg=SURFACE, fg=INK, wraplength=440,
                     justify="left", font=(FONT, 10)).pack(side="left", padx=10)
        self.body_text("接下来需要你做的只有一件事:在弹出的浏览器里\n登录一次你自己的延河课堂账号。",
                       11, MUTED, pady=(22, 4))
        self.body_text("所有数据只存在你自己电脑里,不上传任何服务器。", 10, MUTED)
        self.hint.config(text="点「开始安装」继续")
        self.big_button("开始安装 →", lambda: self.show("env"))

    # ------------------------------------------------------------ 2 环境检查

    def page_env(self):
        self.set_step(2, "环境检查")
        self.body_text("正在检查你的电脑,缺的组件会自动安装…", 14, INK, True, pady=(24, 12))
        self.env_rows = {}
        for key, name in (("python", "Python 运行环境"),
                          ("edge", "Microsoft Edge 浏览器"),
                          ("playwright", "网页抓取组件(约40MB,自动下载)")):
            f = tk.Frame(self.body, bg=SURFACE)
            f.pack(anchor="w", fill="x", padx=24, pady=6)
            tk.Label(f, text="…", bg=SURFACE, fg=MUTED, width=2,
                     font=(FONT, 11)).pack(side="left")
            tk.Label(f, text=name, bg=SURFACE, fg=INK,
                     font=(FONT, 11)).pack(side="left", padx=8)
            self.env_rows[key] = f
        self.env_note = tk.Label(self.body, text="", bg=SURFACE, fg=MUTED,
                                 wraplength=530, justify="left",
                                 font=(FONT, 10))
        self.env_note.pack(anchor="w", padx=24, pady=(14, 0))
        self.env_bar = ttk.Progressbar(self.body, mode="indeterminate",
                                       length=500)
        self.env_bar.pack(anchor="w", padx=24, pady=(10, 0))
        self.env_bar.start(12)
        self.env_btn = self.big_button("下一步 →", lambda: self.show("pull"),
                                       state="disabled")
        self.hint.config(text="正在检查,一般半分钟内完成")
        threading.Thread(target=self._check_env, daemon=True).start()

    def _env_mark(self, key, ok, note=""):
        f = self.env_rows[key]
        lbl = f.winfo_children()[0]
        lbl.config(text="✓" if ok else "✗",
                   fg=GREEN if ok else RED, font=(FONT, 12, "bold"))
        if note:
            f.winfo_children()[1].config(text=note)

    def _check_env(self):
        # Python:向导正在跑,必然 OK
        self.after(0, lambda: self._env_mark("python", True))
        edge = next((p for p in EDGE_PATHS if p.exists()), None)
        self.after(0, lambda: self._env_mark("edge", bool(edge)))
        r = subprocess.run([PY, "-c", "import playwright"],
                            capture_output=True, creationflags=CREATE_NO_WINDOW)
        if r.returncode == 0:
            self.after(0, lambda: self._env_mark("playwright", True))
            self.after(0, self._env_done)
            return
        self.after(0, lambda: self._env_mark(
            "playwright", False, "网页抓取组件(下载中,约1-3分钟)…"))
        try:
            subprocess.run(
                [PY, "-m", "pip", "install", "playwright",
                 "-i", "https://pypi.tuna.tsinghua.edu.cn/simple"],
                capture_output=True, text=True,
                creationflags=CREATE_NO_WINDOW, timeout=900)
        except Exception:
            pass
        r = subprocess.run([PY, "-c", "import playwright"],
                            capture_output=True, creationflags=CREATE_NO_WINDOW)
        ok = r.returncode == 0
        self.after(0, lambda: self._env_mark(
            "playwright", ok, "网页抓取组件" + ("" if ok else " —— 自动安装失败")))
        self.after(0, self._env_done, ok)

    def _env_done(self, pw_ok=True):
        self.state["env_ok"] = pw_ok
        self.env_bar.stop()
        if pw_ok:
            self.env_note.config(text="环境就绪!", fg=GREEN)
            self.env_btn.config(state="normal")
            self.hint.config(text="一切正常,点「下一步」")
        else:
            self.env_note.config(
                text="自动安装失败。解决方法:\n"
                     "  打开命令提示符(开始菜单搜 cmd),粘贴这行后回车:\n"
                     "  pip install playwright -i https://pypi.tuna.tsinghua.edu.cn/simple\n"
                     "  完成后回来点「重新检查」。", fg=RED)
            self.env_btn.config(text="重新检查 →",
                                command=self.show_env_again)

    def show_env_again(self):
        self.show("env")

    # ------------------------------------------------------------ 3 登录抓取

    def show_pull_retry(self):
        self.show("pull")

    def page_pull(self):
        self.set_step(3, "登录并抓取")
        self.body_text("接下来会弹出一个浏览器窗口(Edge)", 14, INK, True, pady=(24, 8))
        self.body_text("请在那个窗口里:", 11, INK)
        steps = [
            "①  输入你的延河课堂账号、密码和图片验证码",
            "②  点[登录]",
            "③  之后就不用管了,等它自动完成(约1-2分钟)",
        ]
        for s in steps:
            self.body_text(s, 11, MUTED, pady=(0, 5))
        self.pull_status = tk.Label(self.body, text="", bg=SURFACE, fg=BLUE,
                                    wraplength=530, justify="left",
                                    font=(FONT, 11, "bold"))
        self.pull_status.pack(anchor="w", padx=24, pady=(18, 0))
        self.pull_log = tk.Label(self.body, text="", bg=SURFACE, fg=MUTED,
                                 wraplength=530, justify="left",
                                 font=(FONT, 9))
        self.pull_log.pack(anchor="w", padx=24, pady=(4, 0))
        self.pull_bar = ttk.Progressbar(self.body, mode="indeterminate",
                                        length=500)
        self.pull_bar.pack(anchor="w", padx=24, pady=(12, 0))
        self.hint.config(text="登录一次以后,基本不用再登录")
        self.pull_btn = self.big_button("弹出浏览器,开始登录 →", self._start_pull)

    def _start_pull(self):
        self.pull_btn.config(state="disabled")
        self.pull_status.config(text="● 正在抓取…请留意弹出的 Edge 窗口", fg=BLUE)
        self.pull_bar.start(12)
        threading.Thread(target=self._pull_work, daemon=True).start()
        self.after(200, self._poll_pull)

    def _pull_work(self):
        try:
            proc = subprocess.Popen(
                [PY, str(MONITOR), "--days", "3"],
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                text=True, encoding="utf-8", errors="replace",
                creationflags=CREATE_NO_WINDOW)
            last = ""
            for line in proc.stdout:
                line = line.strip()
                if line:
                    last = line
            proc.wait(timeout=1200)
        except Exception as e:
            last = "出错:" + str(e)
        self.q.put(last)

    def _poll_pull(self):
        try:
            last = self.q.get_nowait()
        except queue.Empty:
            self.after(200, self._poll_pull)
            return
        self.pull_bar.stop()
        info = self._verify()
        if info:
            n, courses = info
            self.state["pull_ok"] = True
            self.state["courses"] = courses
            self.pull_status.config(
                text=f"✓ 成功!已拉取到你的 {n} 门课程", fg=GREEN)
            self.pull_log.config(text="  " + "、".join(courses[:4]) + ("…" if n > 4 else ""))
            self.hint.config(text="数据已经在你电脑上了")
            self.pull_btn.config(text="下一步 →", state="normal",
                                 command=lambda: self.show("finish"))
        else:
            self.pull_status.config(text="✗ 没有成功", fg=RED)
            self.pull_log.config(
                text=f"  {last}\n"
                     "常见原因:验证码输错、网断了、账号密码不对。\n"
                     "浏览器窗口还开着的话,先去把登录完成。", justify="left")
            self.hint.config(text="没问题的话,点「再试一次」")
            self.pull_btn.config(text="再试一次 →", state="normal",
                                 command=self.show_pull_retry)

    def _verify(self):
        try:
            d = json.loads(DATA.read_text(encoding="utf-8"))
            cs = d.get("courses", [])
            if not cs:
                return None
            return len(cs), [c.get("title", "") for c in cs[:4]]
        except Exception:
            return None

    # ------------------------------------------------------------ 4 完成

    def page_finish(self):
        self.set_step(4, "完成安装")
        self.body_text("最后一步:把入口装到桌面", 14, INK, True, pady=(24, 10))
        f = tk.Frame(self.body, bg=SURFACE)
        f.pack(anchor="w", fill="x", padx=24, pady=6)
        tk.Label(f, text="桌面创建「延河课堂DDL」图标(必装)", bg=SURFACE,
                 fg=INK, font=(FONT, 11)).pack(side="left")
        self.daily_var = tk.BooleanVar(value=True)
        cb = tk.Checkbutton(self.body, variable=self.daily_var, bg=SURFACE,
                            activebackground=SURFACE, fg=INK,
                            selectcolor="#ffffff",
                            font=(FONT, 11), anchor="w", justify="left",
                            text="每天早上 9 点自动检查,快到期的/漏交的\n"
                                 "弹 Windows 通知提醒我(推荐)")
        cb.pack(anchor="w", padx=24, pady=(6, 0))
        self.fin_note = tk.Label(self.body, text="", bg=SURFACE, fg=MUTED,
                                 wraplength=530, justify="left",
                                 font=(FONT, 10))
        self.fin_note.pack(anchor="w", padx=24, pady=(16, 0))
        self.hint.config(text="点「完成安装」就都配好了")
        self.big_button("完成安装", self._do_install)

    def _do_install(self):
        self.state["daily"] = self.daily_var.get()
        ok_link = self._make_shortcut()
        ok_task = self._setup_task() if self.state["daily"] else None
        lines = []
        lines.append("✓ 桌面图标「延河课堂DDL」已创建" if ok_link
                     else "⚠ 桌面图标创建失败(可用文件夹里的程序手动打开)")
        if self.state["daily"]:
            lines.append("✓ 每日 9:00 自动检查已开启" if ok_task
                          else "⚠ 自动检查开启失败(不影响手动使用)")
        else:
            lines.append("— 未开启自动检查(随时可在向导里重新开启)")
        lines.append("")
        lines.append("现在就可以用了:")
        lines.append("  · 双击桌面「延河课堂DDL」打开速查窗口")
        lines.append("  · 点任意一条任务 → 直达作业网页")
        lines.append("  · 点「详情」→ 看老师写的作业要求")
        lines.append("")
        lines.append("想要卸载:删桌面图标,删整个文件夹即可彻底清除。")
        self.fin_note.config(text="\n".join(lines), fg=INK)
        self.hint.config(text="安装完成!")
        self.clear_buttons()
        self.big_button("打开速查窗口体验 →", self._launch_and_exit)

    def clear_buttons(self):
        for w in self.btn_area.winfo_children():
            w.destroy()

    def _launch_and_exit(self):
        try:
            subprocess.Popen([str(PYW), str(SERVER)], cwd=str(BASE),
                             creationflags=CREATE_NO_WINDOW)
        except Exception:
            pass
        self.destroy()

    # -------------------------------------------------- PowerShell 动作

    def _ps(self, script: str) -> str:
        r = subprocess.run(["powershell", "-NoProfile", "-Command", script],
                           capture_output=True, text=True,
                           creationflags=CREATE_NO_WINDOW)
        return (r.stdout or "").strip()

    def _make_shortcut(self) -> bool:
        out = self._ps(
            "$ws = New-Object -ComObject WScript.Shell;"
            "$desktop = [Environment]::GetFolderPath('Desktop');"
            "$lnk = $ws.CreateShortcut((Join-Path $desktop '延河课堂DDL.lnk'));"
            f"$lnk.TargetPath = '{PYW}';"
            f"$lnk.Arguments = '\"{SERVER}\"';"
            f"$lnk.WorkingDirectory = '{BASE}';"
            "$lnk.Description = '延河课堂 DDL 桌面速查';"
            "$lnk.IconLocation = 'C:\\Windows\\System32\\shell32.dll,24';"
            "$lnk.Save();"
            "Test-Path (Join-Path $desktop '延河课堂DDL.lnk')")
        return "True" in out

    def _setup_task(self) -> bool:
        out = self._ps(
            "$action = New-ScheduledTaskAction "
            f"-Execute '{PYW}' "
            f"-Argument '\"{MONITOR}\" --days 3 --notify' "
            f"-WorkingDirectory '{BASE}';"
            "$trigger = New-ScheduledTaskTrigger -Daily -At '09:00';"
            "$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable "
            "-AllowStartIfOnBatteries -DontStopIfGoingOnBatteries "
            "-MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Hours 1);"
            "Register-ScheduledTask -TaskName 'YanhektDDLMonitor' "
            "-Description '延河课堂 DDL 每日巡检' "
            "-Action $action -Trigger $trigger -Settings $settings -Force | Out-Null;"
            "(Get-ScheduledTask -TaskName 'YanhektDDLMonitor').State")
        return ("Ready" in out) or ("就绪" in out)


def main():
    if sys.version_info < (3, 11):
        r = tk.Tk()
        r.withdraw()
        messagebox.showwarning(
            "Python 版本太老",
            "检测到 Python " + sys.version.split()[0] + ",需要 3.11 或更新版本。\n\n"
            "请到 https://www.python.org/downloads/ 安装最新版\n"
            "(安装时勾选 Add Python to PATH),装好后重新双击启动向导。")
        r.destroy()
        return
    try:
        Wizard().mainloop()
    except Exception as e:  # pythonw 下无控制台,用弹窗报错
        r = tk.Tk()
        r.withdraw()
        messagebox.showerror("安装向导启动失败", str(e))
        r.destroy()


if __name__ == "__main__":
    main()
