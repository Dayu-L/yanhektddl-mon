package main

// 在浏览器会话内执行的三段 JS,与 legacy_python/yanhekt_monitor.py 中的
// CHECK_LOGIN_JS / FETCH_COURSES_JS / COLLECT_ECLASS_JS 逻辑一一对应,
// 仅把返回值改为 JSON.stringify 字符串,便于 Go 解析。

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

// 抓取延河课堂全部课程(在 aita.yanhekt.cn 域内执行)
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

// 在 zy-eclass 域内抓取每门课的作业/考试/问卷 + 提交状态
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
  return JSON.stringify({items: items, submittedExams: submittedExams});
}`
