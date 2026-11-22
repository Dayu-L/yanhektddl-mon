package main

// 在浏览器会话内执行的四段 JS:
//   jsCheckLogin    —— aita 登录态探测(登录向导的延河课堂阶段用)
//   jsFetchCourses  —— 延河课堂课程列表(含课程关闭时间 EndAt)
//   jsFetchTodos    —— [快路径] eclass 聚合待办接口 /api/todos,
//                      一次拿到全部课程"未提交且未过期"的任务,再并行补详情
//   jsCollectEclass —— [深扫] 逐课程扫 activities/exams/submitted-exams,
//                      课程级并发 + 详情请求全局信号量
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
