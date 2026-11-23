# 智慧树（在线学堂）待办/作业数据接口调研

调研对象：`https://onlineweb.zhihuishu.com/onlinestuh5`（学生首页）及其关联的作业、考试、智慧课程页面。
调研方式：在**已登录的真实 Edge 会话**中逐个接口实测（页面内 `fetch` 携带 Cookie），并对比页面自身发出的请求与响应。
调研环境：Edge 158 / Windows；已登录的学生账号（本人）；2026-11-23。
标注约定：**【实测】**＝本次真实调用并记录了响应；**【推断】**＝由前端代码或密文解密得出，未做独立调用验证；**【待补】**＝当前账号无对应数据，拿不到响应样例。
脱敏约定：账号侧标识一律写作 `<uuid>`、`<schoolId>`、`<recruitId>`、`<courseId>`、`<classId>`、`<mapUid>`；课程名、教师名、成绩等个人相关内容不落文档。

---

## 0. 结论

1. 鉴权只依赖 Cookie（CAS 单点登录），没有 Bearer token；页面内 `fetch` 直接可用。
2. 业务网关 `/gateway/t/*` 一律要求参数 `secretStr`，其算法为 **AES-128-CBC + PKCS7 + Base64**，key/iv 为编译进前端 bundle 的**常量**。两套常量已破解并复现（见 §2.1），可脱离浏览器直接构造请求。
3. 作业/考试列表用 `flag` 区分未提交（1）与已提交（2），返回项含完整 `endDate`（精确到秒），可直接作为 DDL。
4. 智慧课程（kg-* 域名）使用另一套**运行时混合加密**（RSA 包住会话密钥 + AES），密钥由页面全局 `window.labc(n)` 提供，只能在页面主世界里调用 `window.yxyz(payload, key)` 生成，无法在 Go 侧离线复现。

---

## 1. 会话与凭据

| 项 | 内容 |
| --- | --- |
| 认证形态 | CAS 登录态 Cookie，域 `.zhihuishu.com`，无 token/签名 cookie |
| 主要 Cookie | `CASTGC`、`CASLOGC`、`jt-cas`、`SERVERID`、`__snaker__id`、`gdxidpyhxdE`、`exitRecod_<uuid>` |
| 智慧课程页面额外 Cookie | `o_session_id`、`acw_tc` |
| 用户标识 | `CASLOGC` 是 JSON，其 `.uuid` 即业务 `uuid`，部分接口把它作为查询参数 `uuid=` 传递 |
| 个性化信息 | `GET https://onlineservice-api.zhihuishu.com/gateway/f/v1/login/getLoginUserInfo?time=<ms>` → `{result:{realName, headPicUrl, uuid, username}}`【实测】 |

结论：沿用现有 Edge 专用 profile 登录一次，之后所有接口都能在页面内以 `fetch(url, {credentials:'include'})` 调通；Go 侧无需处理登录流程，只需保证 profile 已登录、会话过期时提示重登。

---

## 2. 统一签名参数 `secretStr`

### 2.1 加密方案【实测，密文完全一致】

```
secretStr = base64( AES-128-CBC( key, iv, JSON.stringify(payload) ) )   // PKCS7 填充
```

| 接口族（域名） | key（16 字节 ASCII） | iv（16 字节 ASCII） |
| --- | --- | --- |
| `onlineservice-api.zhihuishu.com`（学生首页网关） | `7q9oko0vqb3la20r` | `1g3qqdh4jvbskb9x` |
| `studentexam-api.zhihuishu.com`、`taurusexam-api.zhihuishu.com`（作业、考试） | `onbfhdyvz8x7otrp` | `1g3qqdh4jvbskb9x` |
| `kg-ai-run.zhihuishu.com`、`kg-run-student.zhihuishu.com`（智慧课程） | 运行时密钥，见 §2.4 | — |

校验证据（左为明文，右为密钥算法复现结果，均与站点实际发出的密文逐字节相同）：

| payload | secretStr |
| --- | --- |
| `{}` | `yaVfYIJIdqrHDXsDL3vsYw==` |
| `{"status":0,"pageNo":1,"pageSize":5}` | `NEuH3llD9woD4DQgu0k6Uvp5yyo8WT84Dq5lU2iPwvgKNatePzJ+vu/4PRaNYy+K` |
| `{"status":1,"pageNo":1,"pageSize":5}` | `WfBsR9nN4rmJr8WYuVEa7gS3yO+TJHUNeLBjW3EEIZiRPdqZbDjacXj0jGbYkFAq` |

含课程参数的 payload（如 `{"recruitId":"<recruitId>"}`、`{"courseId":<courseId>,"recruitId":"<recruitId>","flag":2}`）同样验证为逐字节一致，因其取值即本人课程/开课编号，此处不列具体明文与密文。

实现见附录 A（30 行，仅用标准库 `crypto/aes`、`crypto/cipher`）。

### 2.2 请求体形态【实测两种都可用】

- 表单式（`onlineservice-api`、`studentexam-api`、`taurusexam-api`）：
  `Content-Type: application/x-www-form-urlencoded`，body = `secretStr=<urlencode(base64)>&date=<毫秒时间戳>`；`date` **可省略**，服务端不校验。
- JSON 式（智慧课程 kg-*）：`Content-Type: application/json;charset=UTF-8`，body = `{"secretStr":"<base64>","date":<毫秒时间戳>}`。

### 2.3 网关前缀 `f` 与 `t`

观察到的规律：`/gateway/f/...` 为免签（如 `getLoginUserInfo`），`/gateway/t/...` 为需 `secretStr`（缺失时服务端返回
`java.lang.RuntimeException: aes加密参数异常 … AesAbstractService.checkDecryptParam`）。**【实测】**

### 2.4 智慧课程（kg-*）的混合加密【实测】

前端取密钥与加密：

```js
const key = await window.labc(6);      // 172 字符的会话密钥串（同一会话内稳定）
const secretStr = window.yxyz(payload, key);
```

`window.yxyz` 的实现（bundle 内可见）为：用 `rsaUtil` 以 RSA 公钥加密该 key，再把结果作为 AES 密钥加密 payload。
两个函数都挂在**页面主世界的 window 上**（`window.labc`、`window.yxyz`、`window.aesUtil`、`window.rsaUtil`），
因此无法离线复现，但可在页面内直接调用。实测以 `{"courseId":"<courseId>","classId":<classId>}` 等 payload
经此路径调用 kg 接口返回 `code:200`。

页面请求另带自定义头 `XQJZXHIZ`，实测**不带也返回 200**，判定为统计/防重放用途，非必需。

### 2.5 可重放性【实测】

用上一次请求的 `secretStr`（对应 payload `{"status":1,...}`）重放：原样 `date`、换新 `date`、完全不带 `date` 三种都返回
`code:200`。即 `secretStr` **不绑定时间、不做一次性校验**，是否可复用以 payload 语义为准（同一密文只能解出同一 payload）。

---

## 3. 接口清单

统一响应包装：`onlineservice-api` 用 `{result|rt, code, message}`；作业/考试服务用 `{rt, msg, status, currentTime}`；
kg-* 用 `{code, message, data, traceId}`。

### 3.1 共享课（学生首页「共享课」卡片）

#### 课程列表【实测】

```
POST https://onlineservice-api.zhihuishu.com/gateway/t/v1/student/course/share/queryShareCourseInfo
payload: {"status":0,"pageNo":1,"pageSize":5}          # status: 0=进行中, 1=已完成
```

响应：`result.totalCount`、`result.courseOpenDtos[]`，关键字段：

| 字段 | 说明 |
| --- | --- |
| `courseId` | 课程 id（数字） |
| `recruitId` | 开课 id（数字），作业/考试接口的主键 |
| `courseName` / `teacherName` / `schoolName` | 课程名 / 教师 / 学校 |
| `progress` | 形如 `"100.0%"` |
| `courseStartTime` / `courseEndTime` | 毫秒时间戳 |
| `secret` | 40 位十六进制串，用途【待补】（推测为课程导流/跳转签名） |

实测结果：`status:0` 正常返回在学课程；`status:1` 返回 `totalCount:0`（无已完成课程）。

#### 作业列表【实测】

```
POST https://studentexam-api.zhihuishu.com/studentExam/gateway/t/v1/student/getStudentHomework
payload: {"recruitId":"<recruitId>","courseId":<courseId>,"flag":1,"pageSize":100,"pageNum":0}
```

- `flag=1`：未提交（待办）；`flag=2`：已提交。
- `flag=2` 响应：`rt.studentHomeworkList[]`，字段含
  `id, examId, examName, cpOrderNumber`(章节名), `endDate`(截止时间，`YYYY-MM-DD HH:MM:SS`), `startTime`,
  `state`(4=已完成), `score, totalScore, oneHundredPont, problemNum, chapterId, courseId, courseName, examUserName`。
- `flag=1` 且无待交时响应仅 `rt:{next:-1}`，**不带** `studentHomeworkList`（列表字段何时出现【待补】）。

#### 考试列表【实测】

```
POST https://taurusexam-api.zhihuishu.com/taurusExam/gateway/t/v1/student/getStudentFinalExam
payload: {"courseId":<courseId>,"recruitId":"<recruitId>","flag":1}     # flag 语义同上
```

响应：`rt[]`，字段含 `id, examId, examName, startTime, endDate, studentStartTime, studentEndTime, submitTime`
`state, score, totalScore, oneHundredPont, problemNum, limitTime, courseId, courseName, examUserName`。
实测 `flag=1` 返回空列表；`flag=2` 返回已完成的考试项，`endDate` 为完整到秒的时间串。

注意：该接口的 payload 必须含 `recruitId`，缺参会抛
`java.lang.NullPointerException … GatewayStudentController.getStudentFinalExam`【实测】。

#### 未提交考试（免签备用）【实测】

```
GET https://onlineservice-api.zhihuishu.com/gateway/t/v1/student/getStudentNoCommitExam
```

响应 `rt.data[]`（当前账号为空）。免 `secretStr` 也可访问，可作为考试待办的兜底来源。

### 3.2 智慧课程（学生首页「智慧课程」卡片）

#### 课程列表【实测】

```
POST https://onlineservice-api.zhihuishu.com/gateway/t/v1/student/queryStudentAICourseList
payload: {}
```

响应 `rt[]`：`courseId`(字符串)、`classId`(数字)、`courseName`、`teacherUserName`、`className`、
`startTime`/`endTime`（`YYYY-MM-DD HH:MM:SS`）、`linkUrl`（课程页地址）、`mastery`。
实测正常返回在学智慧课程（`linkUrl` 指向 `ai-smart-course-student-pro` 域名下的课程页）。

#### 任务列表(taskList,已完整验证)【实测】

```
POST https://kg-run-student.zhihuishu.com/student/gateway/t/task/taskList
payload: {"courseId":"<courseId>","taskType":1,"taskName":"","status":0}
```

- 参数取自前端真实请求(hook `window.yxyz` 获得):`courseId`、`taskType`、`taskName`(空串)、`status`。
  **不需要** `classId`,也没有分页参数 —— 一门课一次全量返回。
- `taskType`:`1`=作业测试、`2`=考试、`3`=话题讨论。
- `status`:**`0`=未完成/进行中,`1`=已完成/已结束**(与页面 tab 一一对应)。
  `taskType=2` 实测返回 200,但当前账号无考试,列表为空(见 §5)。
- 返回 `data[]`,元素关键字段:

| 字段 | 说明 |
| --- | --- |
| `id` | 任务 id |
| `taskName` | 标题 |
| `taskDesc` | 任务描述(整段 HTML,实测单条可达 4.5 KB;`taskDescribe` 多为 `null`) |
| `taskType` / `status` | 任务类型;元素级 `status` `1`=进行中、`2`=已结束 |
| `startTime` / `endTime` | `YYYY-MM-DD HH:MM:SS`,**精确到秒**(页面上只显示「剩余 6天13小时」) |
| `examId` / `examTestId` / `examPaperId` | 作业/考试关联的试卷 id(话题讨论为 `null`) |
| `score` / `totalScore` | 成绩(未评分 `null`) |
| `taskDiscussionVO.isReply` / `isWatch` | 仅话题讨论:我是否已回复 / 已围观 |
| `courseId` / `classId` | 课程、班级 id |

实测样例:话题讨论 `status:0` 返回 19 条(进行中)、`status:1` 返回 2 条(已结束);
作业 `status:1` 返回 2 条已完成、`status:0` 为空。

**完成状态判定**:

- 作业/考试:查询侧 `status`(0/1)就是结论,直接采信。
- 话题讨论:接口的 `status` 表示讨论「进行中/已结束」,与「我是否回复」无关,
  因此用元素级 `taskDiscussionVO.isReply` 判定;已结束但未回复的仍算未完成(属于漏交)。

#### 任务汇总与考试待办(备用)【实测】

```
POST https://kg-run-student.zhihuishu.com/student/gateway/t/home/query-course-task-type
payload: {"courseId":"<courseId>","classId":<classId>}

POST https://kg-ai-run.zhihuishu.com/run/gateway/t/stu/task/v2/get-exam-todo-tasks
payload: {"courseId":"<courseId>","classId":<classId>}

POST https://kg-ai-run.zhihuishu.com/run/gateway/t/stu/task/v2/get-all-exam-todo-tasks
payload: {}   # 实测同样 200
```

- 汇总:`data:[{taskName:"作业测试",taskType:1,count:<任务数>,unfinished:<未完成数>},{taskName:"话题讨论",taskType:3,...}]`(个人数值以占位表示)。
- 考试待办:`data:[]`(当前无考试待办,元素字段【待补】)。因为 `taskList` 的 `taskType=2` 复用同一套 schema,实现里以 `taskList` 为准,这两个接口只作备用。

### 3.3 免签的提醒/通知类接口【实测，均返回空数组】

| 接口 | 用途 |
| --- | --- |
| `GET https://hike-examstu.zhihuishu.com/zhsathome/homework/findImportantReminderList?uuid=<uuid>&date=<ISO>` | 首页「重要提醒」（作业到期提醒），学生首页加载时即调用 |
| `GET https://onlineservice-api.zhihuishu.com/gateway/t/v1/student/message/message/getImportantNoticeList` | 重要通知 |
| `GET https://onlineservice-api.zhihuishu.com/gateway/t/v1/teacher/message/message/listStudentSystemMessage` | 系统消息 |

`findImportantReminderList` 是该 host 下唯一存在的 homework 相关路径；`findHomeworkList`、`queryHomeworkList`
等命名一律 404【实测】。

### 3.4 相关前端页面（DOM 兜底，不推荐）

| 页面 | 说明 |
| --- | --- |
| `https://onlineexamh5new.zhihuishu.com/stuExamWeb.html#/webExamList?recruitId=<urlencode(base64)>` | 共享课「作业考试」H5。该 `recruitId` 是开课 id 的另一套 base64 编码（课程列表接口不直接返回），不宜作为主路径 |
| `https://ai-smart-course-student-pro.zhihuishu.com/singleCourse/taskAndExam/<courseId>/<classId>?mapUid=<mapUid>` | 智慧课程「任务·作业·考试」。作业卡片节点 `.homework-test-content-item`，文本为「标题 / 开始时间 / 截止时间 / 成绩」，但**只有「11月29日 23:59」，无年份**，故接口优于 DOM |

---

## 4. 数据模型映射建议

现有代码用 `Course` + `Item{Kind, Title, End, ...}`（见 `types.go`）：

| 智慧树字段 | 映射 |
| --- | --- |
| `courseName` / `examName` | `Course.Title` / `Item.Title` |
| 共享课作业（`getStudentHomework`） | `Kind="homework"` |
| 共享课考试（`getStudentFinalExam`、`getStudentNoCommitExam`） | `Kind="exam"` |
| 智慧课程任务（`taskList`） | `Kind="homework"`（待区分测验/讨论） |
| `endDate` / `endTime` | `Item.End`（已是本地时间字符串，直接按 `2006-01-02 15:04:05` 解析） |
| `id` + `examId` | `Item.ID` |
| 来源标记 | 现有 `Item` 只有 `Zy` 字段，需要新增平台维度区分「延河 / 智慧树」（如 `Zy="zhihuishu"` 或独立字段） |

---

## 5. 待补与风险

**待补样例**：

1. `taskList` 的作业/考试「未完成」侧(`taskType=1|2`, `status=0`)非空样例。
2. 「进行中但已交」的作业出现在哪个列表(元素级 `examStatus` 取值)—— 若它仍落在 `status:0`,
   则作业/考试也不能只看查询侧 `status`,需改看 `examStatus`。
3. 是否真存在 `taskType=2` 的考试数据(当前账号无考试,只能确认接口返回 200)。
4. `get-exam-todo-tasks` / `get-all-exam-todo-tasks` 非空时的字段。
5. 共享课的作业/考试(密钥已破解,尚未实现,见 §2.1)。
6. 课程列表里的 `secret` 字段用途。

**风险**：

1. AES key/iv 取自前端 bundle 常量,平台换包即可能失效;同时需观察是否按学期轮换。
   Go 侧已把两对常量集中在 `zhihuishu.go`,解密失败会体现在「aes加密参数异常」的报错里。
2. 智慧课程的 `window.labc` / `window.yxyz` 依赖页面已加载 SDK;若改版后符号改名,
   需重新定位(`aesUtil`/`rsaUtil`/`labc2` 是同时存在的备选符号)。
3. 未验证会话过期后的行为(CAS 静默续期链是否会影响页面内 `fetch`)。
4. 抓取频率需克制:上述接口均为只读列表,但学生首页本身并发请求较多,
   轮询间隔不宜低于现有延河策略。

---

## 6. 已落地实现(仓库内)

| 关注点 | 位置 |
| --- | --- |
| `secretStr` 加密、课程/任务映射、智慧课程抓取流程 | `zhihuishu.go`(`aesSecret` / `collectZhihuishu` / `zhsItemFromTask`) |
| 两段页面 JS(课程列表、任务列表) | `js.go` 的 `jsZhsAICourses` / `jsZhsAITasks` |
| 接入抓取主流程(阶段性、失败只自动跳过) | `browser.go` 的 `tryCollect` 阶段 4 |
| 非延河平台的任务字段(`status` / `url`) | `types.go` 的 `Item`、`report.go` 的 `buildReport` |
| 单元测试(密钥向量、字段映射、报告行为、未登录判定) | `zhihuishu_test.go` |

---

## 附录 A：`secretStr` 的 Go 复现

```go
func aesSecret(payload string, key, iv string) string {
	pt := []byte(payload)
	pad := aes.BlockSize - len(pt)%aes.BlockSize
	for i := 0; i < pad; i++ {
		pt = append(pt, byte(pad))
	}
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return ""
	}
	ct := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(ct, pt)
	return base64.StdEncoding.EncodeToString(ct)
}

// 学生首页网关
secret := aesSecret(`{"status":0,"pageNo":1,"pageSize":5}`, "7q9oko0vqb3la20r", "1g3qqdh4jvbskb9x")
// 作业/考试
secret = aesSecret(`{"courseId":<courseId>,"recruitId":"<recruitId>","flag":1}`, "onbfhdyvz8x7otrp", "1g3qqdh4jvbskb9x")
```

首次写实现时，用 `{}` → `yaVfYIJIdqrHDXsDL3vsYw==` 这条断言做单元测试即可锁定密钥与填充正确。

## 附录 B：调研手法（可复现）

1. 在已登录页面里用 `fetch` 逐个试探接口；`/gateway/t/` 缺 `secretStr` 时的报错信息可直接定位签名要求。
2. **主世界注入**：从扩展/隔离环境无法访问页面 `window`，通过 `document.createElement('script')` 注入代码即可在页面主世界执行；回传结果写到 `document.documentElement.dataset`，再从外部读取。
3. **钩住 `CryptoJS.AES.encrypt`**：包装该函数记录 `(key, iv, 明文, 密文)`，再点一下页面上的标签（如「已提交」），即可拿到该接口族真实使用的 key/iv 与明文格式。
4. **密文即证据**：拿到 key/iv 后，反向 AES 解密页面实际发出的 `secretStr`，可直接读出完整 payload 结构，无需猜测参数名。
5. 页面自带的埋点（`collector.zhihuishu.com/public/jsonp/collect`）会把请求参数明文写进 URL，可作为参数形态的旁证。
