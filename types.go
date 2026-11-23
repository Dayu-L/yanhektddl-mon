package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------- 抓取结果类型
// 与 legacy_python/yanhekt_monitor.py 中 JS 返回并写入 ddl_data.json 的结构一致。

type Course struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Teacher string `json:"teacher"`
	College string `json:"college"`
	ZyID    string `json:"zyId"`
	EndAt   int64  `json:"endAt"`
	Term    string `json:"term"`
	// Platform 课程来源:延河课堂 / 智慧树(旧快照没有该字段,归一时兜底为延河课堂)
	Platform string `json:"platform,omitempty"`
}

type Item struct {
	Zy    string      `json:"zy"`
	Kind  string      `json:"kind"` // homework / exam / questionnaire(延河);ai_homework / ai_exam / ai_topic(智慧树)
	ID    interface{} `json:"id"`
	Title string      `json:"title"`
	End   string      `json:"end"`
	SC    int         `json:"sc"` // user_submit_count,-1 = 未知
	Desc  string      `json:"desc"`
	// 以下两个字段在采集出口由 normalizeItem 统一填好(各平台一致):
	Status string `json:"status,omitempty"` // 提交状态,归一后为 未完成/已完成/未知(normalize.go 统一填)
	URL    string `json:"url,omitempty"`    // 任务跳转地址(空 = 按延河 eclass 规则拼接)
}

type Snapshot struct {
	FetchedAt      string                   `json:"fetched_at"`
	DeepAt         string                   `json:"deepAt,omitempty"` // 最近一次深度扫描(RFC3339;每天首轮自动深扫)
	Courses        []Course                 `json:"courses"`
	Items          []Item                   `json:"items"`
	SubmittedExams map[string][]interface{} `json:"submittedExams"`
}

// ---------------------------------------------------------------- 报告类型

type Row struct {
	Course      string
	Kind        string // 作业 / 测验/习题 / 问卷 / 考试 / 话题讨论
	Platform    string // 乐学 / 智慧树(数据来源,速查页用于标识与筛选)
	Title       string
	End         time.Time
	Status      string
	Done        bool
	NoNeed      bool
	Ignored     bool // 用户在速查页标记“忽略”(不再提醒/统计)
	AutoIgnored bool // 已过期超过 48 小时,自动忽略(计算得出,不落盘)
	ID          string
	Desc        string // strip_html 后、截断 60 字(报告用)
	Full        string // desc_to_text 后全文(速查页详情用)
	URL         string
	Zy          string
}

type CloseInfo struct {
	Course
	End    time.Time
	Left   string
	Passed bool
}

type Buckets struct {
	Urgent, Week, Later, Missed, Done, Unknown, Ignored []*Row
}

type Report struct {
	Now    time.Time
	Rows   []*Row
	Bucket Buckets
	Closes []CloseInfo
}

// idString 把 JS 传来的 id(可能是数字或字符串)规范成字符串
// platformOf 由任务的 zy 前缀判定数据来源。
// 乐学(zy-eclass)的任务 zy 是纯数字课程号;智慧树的带平台前缀(见 zhihuishu.go)。
func platformOf(zy string) string {
	if strings.HasPrefix(zy, "zhs:") {
		return platformZhs
	}
	return platformEclass
}

func idString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case nil:
		return ""
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return strings.Trim(string(b), `"`)
	}
}
