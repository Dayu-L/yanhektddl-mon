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
}

type Item struct {
	Zy    string      `json:"zy"`
	Kind  string      `json:"kind"` // homework / exam / questionnaire
	ID    interface{} `json:"id"`
	Title string      `json:"title"`
	End   string      `json:"end"`
	SC    int         `json:"sc"` // user_submit_count,-1 = 未知
	Desc  string      `json:"desc"`
}

type Snapshot struct {
	FetchedAt      string                   `json:"fetched_at"`
	Courses        []Course                 `json:"courses"`
	Items          []Item                   `json:"items"`
	SubmittedExams map[string][]interface{} `json:"submittedExams"`
}

// ---------------------------------------------------------------- 报告类型

type Row struct {
	Course string
	Kind   string // 作业 / 测验/习题 / 问卷
	Title  string
	End    time.Time
	Status string
	Done   bool
	NoNeed bool
	ID     string
	Desc   string // strip_html 后、截断 60 字(报告用)
	Full   string // desc_to_text 后全文(速查页详情用)
	URL    string
	Zy     string
}

type CloseInfo struct {
	Course
	End    time.Time
	Left   string
	Passed bool
}

type Buckets struct {
	Urgent, Week, Later, Missed, Done, Unknown []*Row
}

type Report struct {
	Now    time.Time
	Rows   []*Row
	Bucket Buckets
	Closes []CloseInfo
}

// idString 把 JS 传来的 id(可能是数字或字符串)规范成字符串
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
