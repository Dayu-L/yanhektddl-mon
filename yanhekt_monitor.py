#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
延河课堂 (aita.yanhekt.cn) + 北理工乐学 eclass (zy-eclass.bit.edu.cn) DDL 监控
================================================================================

用法:
    python yanhekt_monitor.py              # 单次抓取,生成 ddl_report.md / ddl_report.csv
    python yanhekt_monitor.py --watch 60   # 每 60 分钟自动巡检一次(可 Ctrl+C 停止)
    python yanhekt_monitor.py --notify     # 发现临期/漏交任务时弹 Windows 通知
    python yanhekt_monitor.py --days 3     # 自定义"临期"天数阈值(默认 3)
    python yanhekt_monitor.py --from-json  # 不开浏览器,直接用 ddl_data.json 重新生成报告

首次运行会弹出 Edge 窗口要求登录延河课堂(账号+密码+验证码,人工输入一次)。
会话保存在 ./browser_profile,之后长期免登录;会话失效时脚本会自动再弹窗。

数据来源(全部为官方接口,在浏览器会话内调用):
  1. 延河课堂课程列表        -> 每门课的 zhiyuan_id / 课程关闭时间 EndAt
  2. eclass /api/...          -> 每门课的作业 homework / 考试 exam / 问卷 questionnaire
  3. user_submit_count        -> 作业是否已提交
  4. submitted-exams          -> 考试是否已提交
"""

import argparse
import csv
import json
import re
import subprocess
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

try:
    sys.stdout.reconfigure(encoding="utf-8")
except Exception:
    pass

BASE = Path(__file__).resolve().parent
PROFILE_DIR = BASE / "browser_profile"
REPORT_MD = BASE / "ddl_report.md"
REPORT_CSV = BASE / "ddl_report.csv"
DATA_JSON = BASE / "ddl_data.json"

PORTAL_URL = "https://aita.yanhekt.cn/education/?tenant_code=21#/home"
COURSE_API = ("https://aita.yanhekt.cn/personal/courseapi/vlabpassportapi/"
              "v1/account-profile/course?nowpage=1&per-page=100")
ECLASS_BASE = "https://zy-eclass.bit.edu.cn"

TZ = timezone(timedelta(hours=8))  # 北京时间
LOGIN_WAIT_S = 600  # 等待人工登录的最长时间(秒)


# ---------------------------------------------------------------- 浏览器会话

def launch_browser(headless: bool = True):
    """启动持久化 Edge(独立 profile,不影响日常使用的 Edge)。"""
    from playwright.sync_api import sync_playwright

    pw = sync_playwright().start()
    try:
        ctx = pw.chromium.launch_persistent_context(
            user_data_dir=str(PROFILE_DIR),
            channel="msedge",
            headless=headless,
            args=["--no-first-run", "--no-default-browser-check",
                  "--disable-blink-features=AutomationControlled"],
        )
    except Exception:
        # Edge 通道失败时退回 Playwright 自带 Chromium / 本机 Chrome
        try:
            ctx = pw.chromium.launch_persistent_context(
                user_data_dir=str(PROFILE_DIR), headless=headless,
                args=["--no-first-run", "--no-default-browser-check"])
        except Exception:
            ctx = pw.chromium.launch_persistent_context(
                user_data_dir=str(PROFILE_DIR), channel="chrome", headless=headless,
                args=["--no-first-run", "--no-default-browser-check"])
    return pw, ctx


def new_page(ctx):
    return ctx.pages[0] if ctx.pages else ctx.new_page()


# 在 aita.yanhekt.cn 域内探测登录状态
CHECK_LOGIN_JS = """
async () => {
  try {
    const r = await fetch('%s', {credentials: 'include',
      headers: {'accept-content-type': 'application/json', 'x-requested-with': 'XMLHttpRequest'}});
    if (!r.ok) return {ok: false};
    const j = await r.json();
    return {ok: !!(j.params && j.params.result && j.params.result.data)};
  } catch (e) { return {ok: false}; }
}
""" % COURSE_API

# 抓取延河课堂全部课程(在 aita.yanhekt.cn 域内执行)
FETCH_COURSES_JS = """
async () => {
  const r = await fetch('%s', {credentials: 'include',
    headers: {'accept-content-type': 'application/json', 'x-requested-with': 'XMLHttpRequest'}});
  if (!r.ok) return {error: 'HTTP ' + r.status};
  const j = await r.json();
  const res = j.params && j.params.result;
  if (!res || !res.data) return {error: 'no data'};
  const courses = res.data.map(c => ({
    id: c.Id, title: c.Title, teacher: c.Teacher || '',
    college: c.KkxyName || '', zyId: c.zhiyuan_id ? String(c.zhiyuan_id) : '',
    endAt: c.EndAt || 0, term: c.TermName || ''
  }));
  return {total: res.total, courses};
}
""" % COURSE_API

# 在 zy-eclass 域内抓取每门课的作业/考试/问卷 + 提交状态
COLLECT_ECLASS_JS = """
async (zyIds) => {
  const get = async (u) => {
    try {
      const r = await fetch(u, {credentials: 'include'});
      if (!r.ok) return null;
      const ct = (r.headers.get('content-type') || '');
      if (ct.indexOf('json') < 0) return null;   // 会话失效时会返回登录页 HTML
      return await r.json();
    } catch (e) { return null; }
  };
  const items = [];
  const submittedExams = {};
  for (const id of zyIds) {
    const [a, e, se] = await Promise.all([
      get('/api/courses/' + id + '/activities?sub_course_id=0'),
      get('/api/courses/' + id + '/exams'),
      get('/api/courses/' + id + '/submitted-exams?no-intercept=true')
    ]);
    submittedExams[id] = (se && se.exam_ids) || [];
    const details = [];
    if (a && a.activities) for (const x of a.activities) {
      if ((x.type === 'homework' || x.type === 'questionnaire') && x.end_time) {
        items.push({zy: id, kind: x.type, id: x.id, title: x.title, end: x.end_time,
                    sc: -1, desc: ''});
        details.push(x.id);
      }
    }
    if (e && e.exams) for (const x of e.exams) {
      if (x.end_time) items.push({zy: id, kind: 'exam', id: x.id, title: x.title,
                                  end: x.end_time, sc: -1, desc: x.description || ''});
    }
    // 并发拉取作业详情(user_submit_count / 描述),10 个一批
    for (let i = 0; i < details.length; i += 10) {
      const batch = details.slice(i, i + 10);
      const res = await Promise.all(batch.map(d => get('/api/activities/' + d)));
      res.forEach((d, k) => {
        const it = items.find(t => t.zy === id && t.id === batch[k] && t.kind !== 'exam');
        if (d && it) {
          it.sc = (d.user_submit_count == null) ? -1 : d.user_submit_count;
          it.desc = (d.data && d.data.description) || '';
        }
      });
    }
  }
  return {items, submittedExams};
}
"""


def wait_for_login(ctx, page, timeout_s: int = LOGIN_WAIT_S) -> bool:
    """弹出可见窗口等待用户完成登录(含验证码),轮询直到会话生效。"""
    page.goto(PORTAL_URL, wait_until="domcontentloaded", timeout=60000)
    try:
        page.bring_to_front()
    except Exception:
        pass
    print("\n" + "=" * 60)
    print(">>> 需要登录延河课堂:请在刚弹出的 Edge 窗口中输入 <<<")
    print("    账号 + 密码 + 图形验证码,然后点击[登录]")
    print(f"    (最长等待 {timeout_s // 60} 分钟,登录成功后会自动继续)")
    print("=" * 60 + "\n")
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        if page.is_closed():          # 用户不小心关掉了窗口 -> 自动重开
            page = ctx.new_page()
            page.goto(PORTAL_URL, wait_until="domcontentloaded", timeout=60000)
        try:
            res = page.evaluate(CHECK_LOGIN_JS)
            if res and res.get("ok"):
                print(">> 登录成功,会话已保存到 browser_profile (下次免登录)")
                return True
        except Exception:
            pass
        time.sleep(3)
    return False


def try_collect(page):
    """阶段1(延河课堂课程) + 阶段2(eclass 任务)。
    未登录 -> 返回 None;成功 -> (courses, items, submitted)。"""
    # ---- 阶段 1:延河课堂(课程列表 + 课程关闭时间) ----
    page.goto(PORTAL_URL, wait_until="domcontentloaded", timeout=60000)
    time.sleep(2)
    res = page.evaluate(CHECK_LOGIN_JS)
    if not (res and res.get("ok")):
        return None
    res = page.evaluate(FETCH_COURSES_JS)
    if not res or res.get("error"):
        raise RuntimeError(f"课程列表获取失败: {res}")
    courses = res["courses"]
    print(f">> 延河课堂:共 {res['total']} 门课")

    # ---- 阶段 2:eclass(作业 / 考试 / 问卷 + 提交状态) ----
    zy_ids = [c["zyId"] for c in courses if c["zyId"]]
    items, submitted = [], {}
    if zy_ids:
        # 任意课程页触发 CAS->eclass SSO,自动建立 eclass 会话
        page.goto(f"{ECLASS_BASE}/course/{zy_ids[0]}/content",
                  wait_until="domcontentloaded", timeout=90000)
        time.sleep(3)
        data = page.evaluate(COLLECT_ECLASS_JS, zy_ids)
        items = (data or {}).get("items", [])
        submitted = (data or {}).get("submittedExams", {})
        print(f">> 乐学 eclass:已扫描 {len(zy_ids)} 门课,带截止时间的任务 {len(items)} 条")

    return courses, items, submitted


# ---------------------------------------------------------------- 报告生成

def strip_html(s: str) -> str:
    s = re.sub(r"<[^>]+>", " ", s or "")
    return re.sub(r"\s+", " ", s).strip()


def fmt_remaining(deadline: datetime, now: datetime) -> str:
    d = deadline - now
    total_min = int(d.total_seconds() // 60)
    if total_min < 0:
        return f"已过期 {-total_min // 1440}天{(-total_min % 1440) // 60}小时"
    days, rem = divmod(total_min, 1440)
    hours = rem // 60
    if days >= 1:
        return f"{days}天{hours}小时"
    return f"{hours}小时" if hours > 0 else f"{total_min}分钟"


def build_report(courses, items, submitted, urgent_days: int):
    now = datetime.now(TZ)
    course_by_zy = {c["zyId"]: c for c in courses if c["zyId"]}

    rows = []
    for it in items:
        c = course_by_zy.get(it["zy"], {})
        end = datetime.fromisoformat(it["end"]).astimezone(TZ)
        desc = strip_html(it.get("desc", ""))
        no_need = any(k in desc for k in ("不需要提交", "无需提交", "仅供练习"))
        if it["kind"] == "exam":
            done = it["id"] in (submitted.get(it["zy"]) or [])
            status = "已完成" if done else "未完成"
        else:
            done = (it.get("sc", -1) > 0)
            status = "已完成" if done else ("未知" if it.get("sc", -1) < 0 else "未完成")
        if no_need:
            status = "已完成(无需提交)" if done else "无需提交(练习)"
        rows.append({
            "course": c.get("title", f"课程{it['zy']}"),
            "kind": {"homework": "作业", "exam": "测验/习题", "questionnaire": "问卷"}[it["kind"]],
            "title": it["title"], "end": end, "done": done, "status": status,
            "no_need": no_need, "id": it["id"],
            "desc": (desc[:60] + "…") if len(desc) > 60 else desc,
            # 一键跳转:作业/问卷 -> learning-activity#/{id},考试 -> learning-activity#/exam/{id}
            "url": (f"{ECLASS_BASE}/course/{it['zy']}/learning-activity#/exam/{it['id']}"
                    if it["kind"] == "exam" else
                    f"{ECLASS_BASE}/course/{it['zy']}/learning-activity#/{it['id']}"),
            "zy": it["zy"],
        })

    rows.sort(key=lambda r: r["end"])

    pending = [r for r in rows if not r["done"] and not r["no_need"]]
    urgent = [r for r in pending if r["end"] >= now and (r["end"] - now) <= timedelta(days=urgent_days)]
    week = [r for r in pending if r["end"] >= now
            and urgent_days * 86400 < (r["end"] - now).total_seconds() <= 7 * 86400]
    later = [r for r in pending if (r["end"] - now).total_seconds() > 7 * 86400]
    missed = [r for r in pending if r["end"] < now]
    done_rows = [r for r in rows if r["done"]]
    unknown = [r for r in pending if r["status"] == "未知"]

    closes = []
    for c in courses:
        if c["endAt"]:
            end = datetime.fromtimestamp(c["endAt"], TZ)
            closes.append({**c, "end_dt": end,
                           "left": fmt_remaining(end, now),
                           "passed": end < now})
    closes.sort(key=lambda c: c["end_dt"])

    return now, rows, dict(urgent=urgent, week=week, later=later, missed=missed,
                           done=done_rows, unknown=unknown), closes


def table(rows):
    if not rows:
        return "_(无)_\n"
    lines = ["| 课程 | 任务 | 类型 | 截止时间(北京) | 剩余 | 状态 |", "|---|---|---|---|---|---|"]
    now = datetime.now(TZ)
    for r in rows:
        title_md = f"[{r['title']}]({r['url']})" if r.get("url") else r["title"]
        lines.append(f"| {r['course']} | {title_md} | {r['kind']} | "
                     f"{r['end'].strftime('%m-%d %H:%M')} | {fmt_remaining(r['end'], now)} | {r['status']} |")
    return "\n".join(lines) + "\n"


def write_reports(now, rows, buckets, closes, urgent_days):
    # ---------- Markdown ----------
    L = []
    L.append("# 📌 延河课堂 / 乐学 DDL 监控报告\n")
    L.append(f"> 生成时间:{now.strftime('%Y-%m-%d %H:%M:%S')} (北京时间)  ")
    L.append(f"> 任务总数:{len(rows)} | "
             f"未完成:{sum(1 for r in rows if r['status'] in ('未完成', '未知'))} | "
             f"已完成:{sum(1 for r in rows if r['done'])} | "
             f"已过期未提交:{len(buckets['missed'])}\n")

    if buckets["urgent"]:
        L.append(f"\n## 🔥 {urgent_days} 天内截止且未完成(尽快处理!)\n")
        L.append(table(buckets["urgent"]))
    if buckets["week"]:
        L.append("\n## ⏰ 7 天内截止且未完成\n")
        L.append(table(buckets["week"]))
    if buckets["later"]:
        L.append("\n## 📅 更远期的任务(未完成)\n")
        L.append(table(buckets["later"]))
    if buckets["missed"]:
        L.append("\n## ❗ 已过期且未提交(尽快联系老师补交)\n")
        L.append(table(buckets["missed"]))
    if buckets["unknown"]:
        L.append("\n## ❓ 提交状态未知(建议打开链接人工确认)\n")
        L.append(table(buckets["unknown"]))

    L.append("\n## 🏁 课程开放截止时间(延河课堂)\n")
    if closes:
        L.append("| 课程 | 开课学院 | 关闭时间(北京) | 状态 |")
        L.append("|---|---|---|---|")
        for c in closes:
            state = "已关闭" if c["passed"] else f"剩 {c['left']}"
            L.append(f"| {c['title']} | {c['college'] or '-'} | "
                     f"{c['end_dt'].strftime('%Y-%m-%d %H:%M')} | {state} |")
        L.append("")

    done = buckets["done"]
    if done:
        L.append(f"\n## ✅ 已完成({len(done)} 项)\n")
        L.append("<details><summary>点击展开</summary>\n")
        L.append(table(done))
        L.append("</details>\n")

    REPORT_MD.write_text("\n".join(L), encoding="utf-8")

    # ---------- CSV ----------
    with REPORT_CSV.open("w", newline="", encoding="utf-8-sig") as f:
        w = csv.writer(f)
        w.writerow(["类别", "课程", "任务", "类型", "截止时间(北京时间)",
                    "剩余/状态", "提交状态", "备注", "链接"])
        for r in rows:
            w.writerow(["任务", r["course"], r["title"], r["kind"],
                        r["end"].strftime("%Y-%m-%d %H:%M:%S"),
                        fmt_remaining(r["end"], now), r["status"],
                        "无需提交(练习)" if r["no_need"] else r["desc"],
                        r["url"]])
        for c in closes:
            w.writerow(["课程关闭", c["title"], "", "",
                        c["end_dt"].strftime("%Y-%m-%d %H:%M:%S"),
                        "已关闭" if c["passed"] else c["left"],
                        "", c["college"],
                        "https://aita.yanhekt.cn/education/?tenant_code=21#/home"])


def notify(title: str, msg: str):
    """Windows 气泡通知(无第三方依赖,失败则忽略)。"""
    ps = (
        "Add-Type -AssemblyName System.Windows.Forms, System.Drawing;"
        "$n = New-Object System.Windows.Forms.NotifyIcon;"
        "$n.Icon = [System.Drawing.SystemIcons]::Warning;"
        "$n.Visible = $true;"
        f"$n.ShowBalloonTip(15000, '{title}', @'\n{msg}\n'@, "
        "[System.Windows.Forms.ToolTipIcon]::Warning);"
        "Start-Sleep -Seconds 16; $n.Dispose()"
    )
    try:
        subprocess.run(["powershell", "-NoProfile", "-Command", ps],
                       timeout=30, capture_output=True)
    except Exception:
        pass


# ---------------------------------------------------------------- 主流程

def print_summary(now, rows, buckets, urgent_days):
    print("\n" + "=" * 62)
    if buckets["urgent"]:
        print(f"🔥 紧急!{urgent_days} 天内截止且未完成的任务 {len(buckets['urgent'])} 项:")
        for r in buckets["urgent"]:
            print(f"   [{r['kind']}] {r['course']} | {r['title']}")
            print(f"           截止 {r['end'].strftime('%m-%d %H:%M')} "
                  f"(剩 {fmt_remaining(r['end'], now)})")
    if buckets["missed"]:
        print(f"❗ 已过期且未提交 {len(buckets['missed'])} 项:")
        for r in buckets["missed"]:
            print(f"   [{r['kind']}] {r['course']} | {r['title']} "
                  f"(截止 {r['end'].strftime('%m-%d %H:%M')})")
    if not buckets["urgent"] and not buckets["missed"]:
        print(f"✅ 无临期/漏交任务。未完成且未过期共 "
              f"{len(buckets['week']) + len(buckets['later'])} 项,详见报告。")
    print("=" * 62)
    print(f"报告已生成:\n  {REPORT_MD}\n  {REPORT_CSV}")


def report_from_snapshot(path: str, urgent_days: int) -> bool:
    """不打开浏览器,直接用已保存的数据快照生成报告。"""
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    now, rows, buckets, closes = build_report(
        data["courses"], data["items"], data.get("submittedExams", {}), urgent_days)
    write_reports(now, rows, buckets, closes, urgent_days)
    print_summary(now, rows, buckets, urgent_days)
    return bool(buckets["urgent"] or buckets["missed"])


def run_once(args) -> bool:
    """完整抓取一次;返回是否有紧急事项。"""
    pw, ctx = launch_browser(headless=not args.visible)
    try:
        page = new_page(ctx)
        result = try_collect(page)
        if result is None:                       # 需要人工登录
            ctx.close()
            pw.stop()
            pw, ctx = launch_browser(headless=False)
            page = new_page(ctx)
            if not wait_for_login(ctx, page):
                raise RuntimeError("等待登录超时,未获取到有效会话")
            result = try_collect(page)
            if result is None:
                raise RuntimeError("登录后仍未获取到有效会话,请检查账号")
        courses, items, submitted = result
    finally:
        try:
            ctx.close()
        except Exception:
            pass
        try:
            pw.stop()
        except Exception:
            pass

    now = datetime.now(TZ)
    now, rows, buckets, closes = build_report(courses, items, submitted, args.days)
    write_reports(now, rows, buckets, closes, args.days)

    DATA_JSON.write_text(json.dumps(
        {"fetched_at": now.isoformat(), "courses": courses,
         "items": items, "submittedExams": submitted},
        ensure_ascii=False, indent=1), encoding="utf-8")

    print_summary(now, rows, buckets, args.days)

    if args.notify and (buckets["urgent"] or buckets["missed"]):
        lines = [f"{r['course']} | {r['title']} 截止 {r['end'].strftime('%m-%d %H:%M')}"
                 for r in (buckets["missed"] + buckets["urgent"])[:5]]
        notify("延河课堂 DDL 提醒", "\n".join(lines))
    return bool(buckets["urgent"] or buckets["missed"])


def main():
    ap = argparse.ArgumentParser(description="延河课堂/乐学 DDL 监控")
    ap.add_argument("--watch", type=int, metavar="分钟",
                    help="循环监控模式:每 N 分钟重新抓取一次")
    ap.add_argument("--days", type=int, default=3,
                    help="临期阈值天数(默认 3)")
    ap.add_argument("--notify", action="store_true",
                    help="发现紧急/漏交任务时弹 Windows 通知")
    ap.add_argument("--visible", action="store_true",
                    help="强制以可见窗口运行浏览器(调试用)")
    ap.add_argument("--from-json", metavar="快照路径", nargs="?", const=str(DATA_JSON),
                    help="不打开浏览器,用已保存的数据快照直接生成报告")
    args = ap.parse_args()

    if args.from_json:
        report_from_snapshot(args.from_json, args.days)
        return

    if not args.watch:
        run_once(args)
        return

    print(f"循环监控已启动:每 {args.watch} 分钟检查一次,Ctrl+C 退出。")
    while True:
        try:
            run_once(args)
        except KeyboardInterrupt:
            raise
        except Exception as e:
            print(f"[!] 本轮抓取失败:{e}")
        try:
            time.sleep(args.watch * 60)
        except KeyboardInterrupt:
            print("\n监控已停止。")
            return


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\n已退出。")
    except RuntimeError as e:
        print(f"\n[错误] {e}")
        sys.exit(2)
