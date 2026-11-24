package main

// 在浏览器会话内执行的四段 JS:
//   jsCheckLogin    —— aita 登录态探测(登录向导的延河课堂阶段用)
//   jsFetchCourses  —— 延河课堂课程列表(含课程关闭时间 EndAt)
//   jsFetchTodos    —— [快路径] eclass 聚合待办接口 /api/todos,
//                      一次拿到全部课程"未提交且未过期"的任务,再并行补详情
//   jsCollectEclass —— [深扫] 逐课程扫 activities/exams/submitted-exams,
//                      课程级并发 + 详情请求全局信号量
//   jsZhsAICourses  —— 智慧树:智慧课程列表(secretStr 已由 Go 侧算好)
//   jsZhsAITasks    —— 智慧树:某门课程的作业/考试/话题讨论任务列表
// 返回值统一为 JSON.stringify 字符串,便于 Go 解析。

// 在 aita.yanhekt.cn 域内探测登录状态
const jsCheckLogin = `async () => {
  try {
    const r = await fetch('` + courseAPI + `', {credentials: 'include',
      headers: {'accept-content-type': 'application/json', 'x-requested-with': 'XMLHttpRequest'}});
    if (!r.ok) return JSON.stringify({ok: false});
    const j = await r.json();
    return JSON.stringify({ok: !!(j.params && j.params.result && j.params.result.data)});
  } catch (e) { return JSON.stringify({ok: false}); }
}`

// 抓取延河课堂全部课程(在 aita.yanhekt.cn 域内执行)。
// 会话失效返回 error 字段(HTTP 401),Go 侧据此走"沿用缓存"分支。
const jsFetchCourses = `async () => {
  const r = await fetch('` + courseAPI + `', {credentials: 'include',
    headers: {'accept-content-type': 'application/json', 'x-requested-with': 'XMLHttpRequest'}});
  if (!r.ok) return JSON.stringify({error: 'HTTP ' + r.status});
  const j = await r.json();
  const res = j.params && j.params.result;
  if (!res || !res.data) return JSON.stringify({error: 'no data'});
  const courses = res.data.map(c => ({
    id: c.Id, title: c.Title, teacher: c.Teacher || '',
    college: c.KkxyName || '', zyId: c.zhiyuan_id ? String(c.zhiyuan_id) : '',
    endAt: c.EndAt || 0, term: c.TermName || ''
  }));
  return JSON.stringify({total: res.total, courses: courses});
}`

// eclass 待办就绪探测:登录向导轮询用(在 zy-eclass 域内执行)
const jsTodosReady = `async () => {
  try {
    const r = await fetch('/api/todos?no-intercept=true', {credentials: 'include'});
    if (!r.ok) return JSON.stringify({ok: false});
    const ct = r.headers.get('content-type') || '';
    if (ct.indexOf('json') < 0) return JSON.stringify({ok: false});
    const j = await r.json();
    return JSON.stringify({ok: Array.isArray(j.todo_list)});
  } catch (e) { return JSON.stringify({ok: false}); }
}`

// [快路径] eclass 聚合待办(在 zy-eclass 域内执行)。
// /api/todos 一次返回全部课程"未提交且未过期"的任务,字段齐全
// (course_id 即延河课堂 zhiyuan_id);再一轮并行 fetch 补作业详情
// (user_submit_count / 描述)。考试本就未提交,无需提交状态字段。
const jsFetchTodos = `async () => {
  const r = await fetch('/api/todos?no-intercept=true', {credentials: 'include'});
  if (!r.ok) return JSON.stringify({ok: false, why: 'HTTP ' + r.status});
  const ct = r.headers.get('content-type') || '';
  if (ct.indexOf('json') < 0) return JSON.stringify({ok: false, why: 'not-json'});
  const j = await r.json();
  if (!Array.isArray(j.todo_list)) return JSON.stringify({ok: false, why: 'no-list'});
  const items = j.todo_list.map(x => ({
    zy: String(x.course_id), kind: x.type, id: x.id, title: x.title,
    end: x.end_time || '', sc: -1, desc: ''
  }));
  const courses = [], seen = {};
  for (const x of j.todo_list) {
    if (x.course_id && !seen[x.course_id]) {
      seen[x.course_id] = 1;
      courses.push({zyId: String(x.course_id), title: x.course_name || ''});
    }
  }
  await Promise.all(items.map(async (it) => {
    try {
      const d = await fetch('/api/activities/' + it.id, {credentials: 'include'});
      if (!d.ok) return;
      const j = await d.json();
      if (it.kind !== 'exam') it.sc = (j.user_submit_count == null) ? 0 : j.user_submit_count;
      it.desc = (j.data && j.data.description) || j.description || '';
    } catch (e) {}
  }));
  return JSON.stringify({ok: true, items: items, courses: courses});
}`

// [深扫] 在 zy-eclass 域内抓取每门课的作业/考试/问卷 + 提交状态。
// 相比旧版串行逐课,这里课程级 6 路并发,活动详情走全局 12 路信号量,
// 15 门课约 19 秒 -> 3-4 秒。
const jsCollectEclass = `async (zyIds) => {
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
  // 活动详情全局信号量:课程并发叠加时限制 in-flight 详情请求总数
  let detActive = 0;
  const detQueue = [];
  const pump = () => {
    while (detActive < 12 && detQueue.length) {
      const t = detQueue.shift();
      detActive++;
      get(t.url).then(d => { detActive--; t.res(d); pump(); });
    }
  };
  const getDetail = (id) => new Promise(res => {
    detQueue.push({url: '/api/activities/' + id, res: res}); pump();
  });
  const scanCourse = async (id) => {
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
    await Promise.all(details.map(d => getDetail(d).then(info => {
      if (!info) return;
      const it = items.find(t => t.zy === id && t.id === d && t.kind !== 'exam');
      if (it) {
        it.sc = (info.user_submit_count == null) ? -1 : info.user_submit_count;
        it.desc = (info.data && info.data.description) || '';
      }
    })));
  };
  let idx = 0;
  const workers = [];
  for (let k = 0; k < 6 && k < zyIds.length; k++) {
    workers.push((async () => {
      while (true) {
        const i = idx++;
        if (i >= zyIds.length) return;
        await scanCourse(zyIds[i]);
      }
    })());
  }
  await Promise.all(workers);
  return JSON.stringify({items: items, submittedExams: submittedExams});
}`

// ---------------------------------------------------------------- 智慧树(在线学堂)
//
// 智慧树的 /gateway/t/* 接口都要参数 secretStr(见 docs/zhihuishu-api.md)。
// 学生首页网关的 secretStr 由 Go 侧算好传进来(密钥失效时只改 Go 常量);
// 智慧课程(kg-*)的签名只能在页面主世界现算,故这两个全局函数必须存在:
//   window.labc(n)        -> 172 字符的会话密钥串
//   window.yxyz(data,key) -> RSA 包住 key 后 AES 加密 payload
// 页面主世界即 chromedp Runtime.evaluate 的默认执行环境,可以直接调用。

// 智慧树 · 智慧课程列表(在智慧树自家域的页面内执行)。
// 第一行是登录态兜底:未登录时页面会被弹到 login.zhihuishu.com(统一登录),
// 那里发跨域请求只会拿到 "Failed to fetch",不如直接报 login-page 让 Go 侧
// 归到「未登录」,日志与登录轮询才不会误判。
const jsZhsAICourses = `async (secretStr) => {
  if (!/(^|\.)zhihuishu\.com$/.test(location.hostname)) {
    return JSON.stringify({ok: false, why: 'login-page'});
  }
  try {
    const r = await fetch('` + zhsAICourseAPI + `', {
      method: 'POST', credentials: 'include',
      headers: {'Content-Type': 'application/x-www-form-urlencoded'},
      body: 'secretStr=' + encodeURIComponent(secretStr)
    });
    if (!r.ok) return JSON.stringify({ok: false, why: 'HTTP ' + r.status});
    const txt = await r.text();
    let j = null;
    try { j = JSON.parse(txt); } catch (e) { return JSON.stringify({ok: false, why: 'not-json'}); }
    const rt = j.rt || j.result;
    if (!Array.isArray(rt)) return JSON.stringify({ok: false, why: (j.msg || j.message || 'no-list')});
    return JSON.stringify({ok: true, courses: rt});
  } catch (e) { return JSON.stringify({ok: false, why: String(e)}); }
}`

// 智慧树 · 登录态探测(免签接口 getLoginUserInfo,任意 zhihuishu.com 页面内可执行)。
// 不能用「课程列表是否为空」判登录:实测未登录时列表接口也可能回 200 + 空数组,
// 登录轮询会因此误判成功、把还没输完密码的窗口关掉。这里只认用户信息。
const jsZhsLoginReady = `async () => {
  if (!/(^|\.)zhihuishu\.com$/.test(location.hostname)) {
    return JSON.stringify({ok: false, why: 'login-page'});
  }
  if (/^(passport|login)\./i.test(location.hostname)) {
    return JSON.stringify({ok: false, why: 'login-page'});
  }
  try {
    const r = await fetch('` + zhsLoginUserAPI + `?time=' + Date.now(), {credentials: 'include'});
    if (!r.ok) return JSON.stringify({ok: false, why: 'HTTP ' + r.status});
    const txt = await r.text();
    let j = null;
    try { j = JSON.parse(txt); } catch (e) { return JSON.stringify({ok: false, why: 'not-json'}); }
    const u = j.result || j.rt;
    if (u && (u.uuid || u.username || u.realName)) return JSON.stringify({ok: true});
    return JSON.stringify({ok: false, why: 'no-user'});
  } catch (e) { return JSON.stringify({ok: false, why: String(e)}); }
}`

// 智慧树 · 某门智慧课程的任务列表(在 ai-smart-course-student-pro 域内执行)。
// lists 形如 [[taskType,status],...];返回 {ok, lists:{"type:status":[任务]}, errs}
// 只保留用得上的字段:接口里的 taskDesc 是整段 HTML,原样传回会把日志和快照撑爆。
const jsZhsAITasks = `async (courseId, lists) => {
  try {
    if (typeof window.yxyz !== 'function' || typeof window.labc !== 'function') {
      return JSON.stringify({ok: false, why: 'no-sdk'});
    }
    const key = await window.labc(6);
    const out = {}, errs = {};
    for (const pair of lists) {
      const taskType = pair[0], status = pair[1];
      const k = taskType + ':' + status;
      try {
        const r = await fetch('` + zhsAITaskAPI + `', {
          method: 'POST', credentials: 'include',
          headers: {'Content-Type': 'application/json;charset=UTF-8'},
          body: JSON.stringify({
            secretStr: window.yxyz({courseId: courseId, taskType: taskType, taskName: '', status: status}, key),
            date: Date.now()
          })
        });
        const txt = await r.text();
        let j = null;
        try { j = JSON.parse(txt); } catch (e) { out[k] = []; errs[k] = 'not-json'; continue; }
        if ((j.code | 0) !== 200) { out[k] = []; errs[k] = (j.message || ('code ' + j.code)); continue; }
        out[k] = (Array.isArray(j.data) ? j.data : []).map(x => ({
          id: x.id, name: x.taskName || '',
          desc: String(x.taskDesc || '').slice(0, 1200),
          type: x.taskType || 0, status: x.status || 0,
          start: x.startTime || '', end: x.endTime || '',
          examId: (x.examId == null ? null : x.examId),
          reply: (x.taskDiscussionVO && typeof x.taskDiscussionVO.isReply === 'boolean') ? x.taskDiscussionVO.isReply : null
        }));
      } catch (e) { out[k] = []; errs[k] = String(e); }
    }
    return JSON.stringify({ok: true, lists: out, errs: errs});
  } catch (e) { return JSON.stringify({ok: false, why: String(e)}); }
}`
