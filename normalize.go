package main

import (
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- 数据归一
//
// 三个数据源(乐学 eclass / 智慧树共享课 / 智慧树智慧课程)的原始形态差异不小:
//   · end:乐学是 RFC3339 UTC(2026-12-13T15:59:00Z),智慧树是北京时间字符串
//   · id:乐学是数字,智慧树是字符串
//   · 完成状态:乐学靠「提交数 / 已提交考试列表」侧写,智慧树接口直接给
//   · kind:乐学 homework/exam/questionnaire,智慧树 ai_homework/ai_exam/ai_topic
//   · url:智慧树直接给,乐学要按 eclass 规则拼
//
// 这些都收敛到 normalizeItem 里,统一成:
//   end    -> "2006-01-02 15:04:05"(北京时间)
//   id     -> string
//   kind   -> homework / exam / questionnaire / topic
//   status -> 未完成 / 已完成 / 未知(永远显式写在 Item 上)
//   url    -> 永远是可直接打开的最终地址
//
// 归一放在两处:采集出口(tryCollect,让快照本身干净)+ 报告入口(buildReport,
// 保证 --from-json / 旧快照也一致)。函数幂等,重复调用结果不变。

const (
	// endLayout 归一后的截止时间格式(北京时间,精确到秒)
	endLayout = "2006-01-02 15:04:05"

	// 规范化的任务类型
	kindHomework      = "homework"
	kindExam          = "exam"
	kindQuestionnaire = "questionnaire"
	kindTopic         = "topic"

	// 规范化的提交状态
	statusUndone  = "未完成"
	statusDone    = "已完成"
	statusUnknown = "未知"

	// maxTitleRunes 标题上限:智慧树的话题讨论把整段问题当标题,
	// 不截会在报告表格里撑出好几屏。
	maxTitleRunes = 120
)

// canonicalKind 把各平台的类型名收敛到规范值。
func canonicalKind(kind string) string {
	switch kind {
	case kindHomework, kindExam, kindQuestionnaire, kindTopic:
		return kind
	case kindAIHomework:
		return kindHomework
	case kindAIExam:
		return kindExam
	case kindAITopic:
		return kindTopic
	}
	return kind
}

// normalizeTitle 压缩空白并限长(emoji/中文按字符截,不会切坏 UTF-8)。
func normalizeTitle(s string) string {
	t := strings.TrimSpace(reWS.ReplaceAllString(s, " "))
	if t == "" {
		return ""
	}
	r := []rune(t)
	if len(r) > maxTitleRunes {
		return string(r[:maxTitleRunes]) + "…"
	}
	return t
}

// eclassURL 乐学任务的跳转地址:
// 作业/问卷 -> learning-activity#/{id},考试 -> learning-activity#/exam/{id}
func eclassURL(zy, kind, id string) string {
	url := eclassBase + "/course/" + zy + "/learning-activity#/"
	if kind == kindExam {
		url += "exam/"
	}
	return url + id
}

// inferStatus 没有显式状态时,按乐学的侧写推断(与历史行为保持一致):
//   · 考试:出现在「已提交考试」列表里才算完成,否则未完成
//   · 其它:提交数 >0 已完成,=0 未完成,拿不到(-1)算未知
func inferStatus(it Item, submitted map[string][]interface{}) string {
	if it.Kind == kindExam {
		for _, idv := range submitted[it.Zy] {
			if idString(idv) == idString(it.ID) {
				return statusDone
			}
		}
		return statusUndone
	}
	switch {
	case it.SC > 0:
		return statusDone
	case it.SC == 0:
		return statusUndone
	}
	return statusUnknown
}

// normalizeItem 把一个任务归一。幂等,可在采集出口与报告入口都调用。
func normalizeItem(it Item, submitted map[string][]interface{}) Item {
	if it.End != "" {
		if t := parseEnd(it.End); !t.IsZero() {
			it.End = t.Format(endLayout)
		}
	}
	it.ID = idString(it.ID)
	it.Kind = canonicalKind(it.Kind)
	it.Title = normalizeTitle(it.Title)
	if it.URL == "" {
		it.URL = eclassURL(it.Zy, it.Kind, idString(it.ID))
	}
	switch it.Status {
	case statusDone, statusUndone, statusUnknown:
		// 数据源已经给了规范值,直接用
	default:
		// 读 nil map 是安全的,调用方不必先判空
		it.Status = inferStatus(it, submitted)
	}
	return it
}

// normalizeCourse 给课程补上来源标识;旧快照没有该字段时兜底为延河课堂
// (历史快照里的课程只可能来自延河课堂的课程列表)。
func normalizeCourse(c Course) Course {
	if c.Platform == "" {
		c.Platform = platformAita
	}
	return c
}

// endAtFromMillis 智慧树共享课给的是毫秒时间戳。
func endAtFromMillis(v string) int64 {
	ms, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return ms / 1000
}

// endAtFromTime 智慧树智慧课程给的是 "2026-09-20 19:18:00"(北京时间)。
func endAtFromTime(v string) int64 {
	if t := parseEnd(strings.TrimSpace(v)); !t.IsZero() {
		return t.Unix()
	}
	return 0
}
