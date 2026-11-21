#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
延河课堂 DDL 桌面应用后端 + 启动器
================================================================================
双击桌面快捷方式运行本文件:
- 若服务未启动 -> 启动本地服务(127.0.0.1:8765)并弹出 Edge 应用窗口
- 若已在运行 -> 直接再弹一个窗口,进程立即退出

页面由 dashboard.html 渲染;数据由 yanhekt_monitor.build_report 计算,
与 CSV/Markdown 报告共用同一套逻辑。POST /api/refresh 会后台调用
yanhekt_monitor.py 重新抓取(需要时自动弹 Edge 登录窗)。
"""

import json
import re
import subprocess
import sys
import threading
import time
import webbrowser
from html import unescape
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

BASE = Path(__file__).resolve().parent
sys.path.insert(0, str(BASE))
from yanhekt_monitor import DATA_JSON, build_report  # noqa: E402

PORT = 8765
DASH = BASE / "dashboard.html"
MONITOR = BASE / "yanhekt_monitor.py"
PYW = Path(sys.executable).with_name("pythonw.exe")
MONITOR_EXE = str(PYW if PYW.exists() else sys.executable)
EDGE = Path(r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe")

_state = {"refreshing": False, "lock": threading.Lock()}


# ---------------------------------------------------------------- 数据

def desc_to_text(html: str) -> str:
    """eclass 的作业说明 HTML -> 可读纯文本(保留段落)。"""
    if not html:
        return ""
    s = re.sub(r"(?i)</(p|div|li)>", "\n", html)
    s = re.sub(r"(?i)<br\s*/?>", "\n", s)
    s = re.sub(r"<[^>]+>", "", s)
    s = unescape(s)
    s = re.sub(r"[ \t\u00a0]+", " ", s)
    s = re.sub(r"\n{3,}", "\n\n", s)
    return s.strip()


def snapshot() -> dict:
    """把 ddl_data.json 经 build_report 变成页面可用的 JSON。"""
    raw = json.loads(DATA_JSON.read_text(encoding="utf-8"))
    now, rows, buckets, closes = build_report(
        raw["courses"], raw["items"], raw.get("submittedExams", {}), 3)

    # 作业说明原文(按 课程zyId + 任务id 匹配)
    desc_map = {}
    for it in raw["items"]:
        desc_map[(str(it["zy"]), str(it["id"]))] = desc_to_text(it.get("desc", ""))

    def jrow(r):
        return {"course": r["course"], "kind": r["kind"], "title": r["title"],
                "end": r["end"].isoformat(), "status": r["status"],
                "url": r["url"], "done": r["done"], "no_need": r["no_need"],
                "desc": desc_map.get((str(r["zy"]), str(r["id"])), "")}

    return {
        "fetched_at": raw.get("fetched_at"),
        "missed": [jrow(r) for r in buckets["missed"]],
        "urgent": [jrow(r) for r in buckets["urgent"]],
        "week": [jrow(r) for r in buckets["week"]],
        "later": [jrow(r) for r in buckets["later"]],
        "unknown": [jrow(r) for r in buckets["unknown"]],
        "done": [jrow(r) for r in buckets["done"]],
        "closes": [{"title": c["title"], "college": c.get("college", ""),
                    "end": c["end_dt"].isoformat(), "left": c["left"],
                    "passed": c["passed"]} for c in closes],
    }


def render_page() -> bytes:
    data = snapshot()
    html = DASH.read_text(encoding="utf-8")
    html = html.replace("/*__DATA__*/ null",
                        json.dumps(data, ensure_ascii=False))
    return html.encode("utf-8")


# ---------------------------------------------------------------- HTTP

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):  # 静默
        pass

    def _send(self, code, ctype, body: bytes):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        try:
            if self.path in ("/", "/index.html", "/dashboard.html"):
                self._send(200, "text/html; charset=utf-8", render_page())
            elif self.path == "/api/data":
                self._send(200, "application/json; charset=utf-8",
                           json.dumps(snapshot(), ensure_ascii=False).encode("utf-8"))
            elif self.path == "/api/status":
                self._send(200, "application/json; charset=utf-8",
                           json.dumps({"running": _state["refreshing"]}).encode("utf-8"))
            else:
                self._send(404, "text/plain; charset=utf-8", b"not found")
        except Exception as e:  # noqa: BLE001
            self._send(500, "text/plain; charset=utf-8", str(e).encode("utf-8"))

    def do_POST(self):
        if self.path != "/api/refresh":
            self._send(404, "text/plain; charset=utf-8", b"not found")
            return
        with _state["lock"]:
            already = _state["refreshing"]
            _state["refreshing"] = True

        if not already:
            def work():
                try:
                    subprocess.run(
                        [MONITOR_EXE, str(MONITOR), "--days", "3"],
                        cwd=str(BASE), timeout=900,
                        creationflags=subprocess.CREATE_NO_WINDOW)
                except Exception:
                    pass
                finally:
                    _state["refreshing"] = False
            threading.Thread(target=work, daemon=True).start()
        self._send(200, "application/json; charset=utf-8", b'{"ok":true}')


# ---------------------------------------------------------------- 启动

def open_window():
    url = f"http://127.0.0.1:{PORT}/"
    if EDGE.exists():
        subprocess.Popen([str(EDGE), f"--app={url}", "--window-size=1180,880"],
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    else:
        webbrowser.open(url)


def probe_ours() -> bool:
    """8765 上运行的是不是我们自己的服务(防止把别的程序误认成自己)。"""
    import urllib.request
    try:
        with urllib.request.urlopen(
                f"http://127.0.0.1:{PORT}/api/status", timeout=1.5) as r:
            return "running" in json.loads(r.read().decode("utf-8"))
    except Exception:
        return False


def main():
    global PORT
    # 是自家服务在跑:只开窗口
    if probe_ours():
        open_window()
        print(">> 服务已在运行,已打开窗口")
        return

    # 端口被其他程序占用时自动换端口(8765-8769),避免"双击没反应"
    srv = None
    for port in range(8765, 8770):
        try:
            srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
            PORT = port
            break
        except OSError:
            continue
    if srv is None:
        print(">> 端口 8765-8769 均被占用,无法启动服务")
        return
    if PORT != 8765:
        print(f">> 8765 被其他程序占用,已改用端口 {PORT}")
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    time.sleep(0.3)
    open_window()
    print(f">> 延河课堂 DDL 服务已启动:http://127.0.0.1:{PORT}/")
    print(">> 关闭本窗口(Ctrl+C)后,桌面快捷方式可随时重新启动")
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        print("\n>> 服务已停止")
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        print("\n>> 服务已停止")


if __name__ == "__main__":
    main()
