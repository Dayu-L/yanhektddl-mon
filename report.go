package main

import (
	"encoding/csv"
	"fmt"
	"html"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	reTag      = regexp.MustCompile(`<[^>]+>`)
	reBlockEnd = regexp.MustCompile(`(?i)</(p|div|li)>`)
	reBr       = regexp.MustCompile(`(?i)<br\s*/?>`)
	reSpaces   = regexp.MustCompile(`[ \t\xa0]+`)
	reNewlines = regexp.MustCompile(`\n{3,}`)
	reWS       = regexp.MustCompile(`\s+`)

	noNeedWords = []string{"不需要提交", "无需提交", "仅供练习"}
	kindNames   = map[string]string{
		"homework":      "作业",
		"exam":          "测验/习题",
		"questionnaire": "问卷",
	}
)

// ---------------------------------------------------------------- 文本处理

// stripHTML 报告用:去标签、压缩空白(对应 python strip_html)
func stripHTML(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	s = reWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// descToText 速查页详情用:保留段落(对应 python desc_to_text)
func descToText(s string) string {
	if s == "" {
		return ""
	}
	s = reBlockEnd.ReplaceAllString(s, "\n")
	s = reBr.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = reNewlines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 时间

// parseEnd 解析 eclass 的 end_time(兼容 ISO8601 带时区/无时区等写法)
func parseEnd(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(TZ)
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, TZ); err == nil {
			return t
		}
	}
	return time.Time{}
}

// fmtRemaining 剩余时间文本(与 python fmt_remaining 语义一致)
func fmtRemaining(deadline, now time.Time) string {
	totalMin := int(math.Floor(deadline.Sub(now).Seconds() / 60))
	if totalMin < 0 {
		t := -totalMin
		return fmt.Sprintf("已过期 %d天%d小时", t/1440, (t%1440)/60)
	}
	days, rem := totalMin/1440, totalMin%1440
	hours := rem / 60
	if days >= 1 {
		return fmt.Sprintf("%d天%d小时", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%d小时", hours)
	}
	return fmt.Sprintf("%d分钟", totalMin)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// ---------------------------------------------------------------- 报告构建

// autoIgnoreAfter 已过期超过该时长且未完成的任务自动忽略:
// 补交早已无望,继续留在“已过期未提交”里只会制造噪音。计算得出、
// 不写入 ignored.json —— 若老师事后延长截止时间,任务会自动回到待办。
const autoIgnoreAfter = 48 * time.Hour

// buildReport 对应 python build_report:统一计算排序、分组、课程关闭时间。
// ignored 为用户标记的忽略清单(key 见 ignoreKey):被忽略且未完成的任务
// 单独归入 Bucket.Ignored,不进入临期/漏交分组,不触发通知。
func buildReport(courses []Course, items []Item, submitted map[string][]interface{}, urgentDays int, ignored map[string]bool) *Report {
	now := time.Now().In(TZ)
	courseByZy := map[string]Course{}
	for _, c := range courses {
		if c.ZyID != "" {
			courseByZy[c.ZyID] = c
		}
	}

	rep := &Report{Now: now}
	for _, it := range items {
		c := courseByZy[it.Zy]
		course := c.Title
		if course == "" {
			course = "课程" + it.Zy
		}
		end := parseEnd(it.End)

		desc := stripHTML(it.Desc)
		noNeed := containsAny(desc, noNeedWords)

		var done bool
		status := "未完成"
		if it.Kind == "exam" {
			for _, idv := range submitted[it.Zy] {
				if idString(idv) == idString(it.ID) {
					done = true
					break
				}
			}
			if done {
				status = "已完成"
			}
		} else {
			done = it.SC > 0
			switch {
			case done:
				status = "已完成"
			case it.SC < 0:
				status = "未知"
			}
		}
		if noNeed {
			if done {
				status = "已完成(无需提交)"
			} else {
				status = "无需提交(练习)"
			}
		}

		// 一键跳转:作业/问卷 -> learning-activity#/{id},考试 -> learning-activity#/exam/{id}
		url := eclassBase + "/course/" + it.Zy + "/learning-activity#/"
		if it.Kind == "exam" {
			url += "exam/"
		}
		url += idString(it.ID)

		rep.Rows = append(rep.Rows, &Row{
			Course: course,
			Kind:   kindNames[it.Kind],
			Title:  it.Title,
			End:    end,
			Done:   done,
			Status: status,
			NoNeed: noNeed,
			Ignored: ignored[ignoreKey(it.Zy, idString(it.ID))],
			ID:     idString(it.ID),
			Desc:   truncate(desc, 60),
			Full:   descToText(it.Desc),
			URL:    url,
			Zy:     it.Zy,
		})
	}
	sort.SliceStable(rep.Rows, func(i, j int) bool {
		return rep.Rows[i].End.Before(rep.Rows[j].End)
	})

	day := 24 * time.Hour
	for _, r := range rep.Rows {
		if r.Done {
			rep.Bucket.Done = append(rep.Bucket.Done, r)
		}
		// 已忽略且未完成:单独成组,不参与临期/漏交/未知统计
		// (已完成的任务“忽略”无意义,照常进已完成组)
		if r.Ignored && !r.Done {
			rep.Bucket.Ignored = append(rep.Bucket.Ignored, r)
			continue
		}
		// 已过期超过 48 小时且未完成:自动忽略,同样移出临期/漏交
		if !r.Done && !r.NoNeed && r.End.Before(now.Add(-autoIgnoreAfter)) {
			r.AutoIgnored = true
			rep.Bucket.Ignored = append(rep.Bucket.Ignored, r)
			continue
		}
		if r.Done || r.NoNeed {
			continue // pending = 未完成且无需提交标记
		}
		switch {
		case r.End.Before(now):
			rep.Bucket.Missed = append(rep.Bucket.Missed, r)
		case r.End.Sub(now) <= time.Duration(urgentDays)*day:
			rep.Bucket.Urgent = append(rep.Bucket.Urgent, r)
		case r.End.Sub(now) <= 7*day:
			rep.Bucket.Week = append(rep.Bucket.Week, r)
		default:
			rep.Bucket.Later = append(rep.Bucket.Later, r)
		}
		if r.Status == "未知" {
			rep.Bucket.Unknown = append(rep.Bucket.Unknown, r)
		}
	}

	for _, c := range courses {
		if c.EndAt == 0 {
			continue
		}
		end := time.Unix(c.EndAt, 0).In(TZ)
		rep.Closes = append(rep.Closes, CloseInfo{
			Course: c, End: end,
			Left:   fmtRemaining(end, now),
			Passed: end.Before(now),
		})
	}
	sort.SliceStable(rep.Closes, func(i, j int) bool {
		return rep.Closes[i].End.Before(rep.Closes[j].End)
	})
	return rep
}

// ---------------------------------------------------------------- Markdown / CSV

func mdTable(rows []*Row, now time.Time) string {
	if len(rows) == 0 {
		return "_(无)_\n"
	}
	var b strings.Builder
	b.WriteString("| 课程 | 任务 | 类型 | 截止时间(北京) | 剩余 | 状态 |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | [%s](%s) | %s | %s | %s | %s |\n",
			r.Course, r.Title, r.URL, r.Kind,
			r.End.Format("01-02 15:04"), fmtRemaining(r.End, now), r.Status)
	}
	return b.String()
}

// writeReports 生成 ddl_report.md 与 ddl_report.csv
func writeReports(rep *Report, urgentDays int) {
	now := rep.Now
	var L []string
	L = append(L, "# 📌 延河课堂 / 乐学 DDL 监控报告\n")
	L = append(L, fmt.Sprintf("> 生成时间:%s (北京时间)  ", now.Format("2006-01-02 15:04:05")))
	unfinished := 0
	ignoredCount := 0
	for _, r := range rep.Rows {
		if r.Status == "未完成" || r.Status == "未知" {
			if r.Ignored {
				ignoredCount++
			} else {
				unfinished++
			}
		}
	}
	doneCount := len(rep.Bucket.Done)
	L = append(L, fmt.Sprintf("> 任务总数:%d | 未完成:%d | 已完成:%d | 已过期未提交:%d\n\n",
		len(rep.Rows), unfinished, doneCount, len(rep.Bucket.Missed)))
	if ignoredCount > 0 {
		L = append(L, fmt.Sprintf("> 已忽略 %d 项(不再提醒,速查页可恢复)\n\n", ignoredCount))
	}

	if len(rep.Bucket.Urgent) > 0 {
		L = append(L, fmt.Sprintf("\n## 🔥 %d 天内截止且未完成(尽快处理!)\n\n", urgentDays))
		L = append(L, mdTable(rep.Bucket.Urgent, now))
	}
	if len(rep.Bucket.Week) > 0 {
		L = append(L, "\n## ⏰ 7 天内截止且未完成\n")
		L = append(L, mdTable(rep.Bucket.Week, now))
	}
	if len(rep.Bucket.Later) > 0 {
		L = append(L, "\n## 📅 更远期的任务(未完成)\n")
		L = append(L, mdTable(rep.Bucket.Later, now))
	}
	if len(rep.Bucket.Missed) > 0 {
		L = append(L, "\n## ❗ 已过期且未提交(尽快联系老师补交)\n")
		L = append(L, mdTable(rep.Bucket.Missed, now))
	}
	if n := autoIgnoredCount(rep.Rows); n > 0 {
		L = append(L, fmt.Sprintf("\n> 另有 %d 项已过期超过 48 小时,已自动忽略,不再列入提醒。\n", n))
	}
	if len(rep.Bucket.Unknown) > 0 {
		L = append(L, "\n## ❓ 提交状态未知(建议打开链接人工确认)\n")
		L = append(L, mdTable(rep.Bucket.Unknown, now))
	}

	L = append(L, "\n## 🏁 课程开放截止时间(延河课堂)\n")
	if len(rep.Closes) > 0 {
		L = append(L, "| 课程 | 开课学院 | 关闭时间(北京) | 状态 |")
		L = append(L, "|---|---|---|---|")
		for _, c := range rep.Closes {
			state := c.Left
			if c.Passed {
				state = "已关闭"
			}
			college := c.College
			if college == "" {
				college = "-"
			}
			fmtS := fmt.Sprintf("| %s | %s | %s | %s |", c.Title, college,
				c.End.Format("2006-01-02 15:04"), state)
			L = append(L, fmtS)
		}
		L = append(L, "")
	}

	if len(rep.Bucket.Done) > 0 {
		L = append(L, fmt.Sprintf("\n## ✅ 已完成(%d 项)\n", len(rep.Bucket.Done)))
		L = append(L, "<details><summary>点击展开</summary>\n\n")
		L = append(L, mdTable(rep.Bucket.Done, now))
		L = append(L, "</details>\n")
	}
	_ = os.WriteFile(reportMD, []byte(strings.Join(L, "\n")), 0o644)

	// ---------- CSV(utf-8-sig) ----------
	f, err := os.Create(reportCSV)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(f)
	w.Write([]string{"类别", "课程", "任务", "类型", "截止时间(北京时间)",
		"剩余/状态", "提交状态", "备注", "链接"})
	for _, r := range rep.Rows {
		note := r.Desc
		switch {
		case r.AutoIgnored:
			note = "已过期超 48 小时,自动忽略"
		case r.NoNeed && r.Ignored:
			note = "无需提交(练习);已忽略"
		case r.NoNeed:
			note = "无需提交(练习)"
		case r.Ignored:
			note = "已忽略(不再提醒)"
		}
		w.Write([]string{"任务", r.Course, r.Title, r.Kind,
			r.End.Format("2006-01-02 15:04:05"),
			fmtRemaining(r.End, now), r.Status, note, r.URL})
	}
	for _, c := range rep.Closes {
		left := c.Left
		if c.Passed {
			left = "已关闭"
		}
		w.Write([]string{"课程关闭", c.Title, "", "",
			c.End.Format("2006-01-02 15:04:05"), left, "", c.College,
			"https://aita.yanhekt.cn/education/?tenant_code=21#/home"})
	}
	w.Flush()
}

// printSummary 控制台摘要(与 python print_summary 一致)
func printSummary(rep *Report, urgentDays int) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 62))
	if len(rep.Bucket.Urgent) > 0 {
		fmt.Printf("🔥 紧急!%d 天内截止且未完成的任务 %d 项:\n",
			urgentDays, len(rep.Bucket.Urgent))
		for _, r := range rep.Bucket.Urgent {
			fmt.Printf("   [%s] %s | %s\n", r.Kind, r.Course, r.Title)
			fmt.Printf("           截止 %s (剩 %s)\n",
				r.End.Format("01-02 15:04"), fmtRemaining(r.End, rep.Now))
		}
	}
	if len(rep.Bucket.Missed) > 0 {
		fmt.Printf("❗ 已过期且未提交 %d 项:\n", len(rep.Bucket.Missed))
		for _, r := range rep.Bucket.Missed {
			fmt.Printf("   [%s] %s | %s (截止 %s)\n", r.Kind, r.Course,
				r.Title, r.End.Format("01-02 15:04"))
		}
	}
	if len(rep.Bucket.Urgent) == 0 && len(rep.Bucket.Missed) == 0 {
		fmt.Printf("✅ 无临期/漏交任务。未完成且未过期共 %d 项,详见报告。\n",
			len(rep.Bucket.Week)+len(rep.Bucket.Later))
	}
	fmt.Println(strings.Repeat("=", 62))
	fmt.Printf("报告已生成:\n  %s\n  %s\n", reportMD, reportCSV)
}

// hasUrgent 是否存在临期或漏交(用于通知判断)
func hasUrgent(rep *Report) bool {
	return len(rep.Bucket.Urgent) > 0 || len(rep.Bucket.Missed) > 0
}

// autoIgnoredCount 自动忽略(已过期超 48 小时)的任务数
func autoIgnoredCount(rows []*Row) int {
	n := 0
	for _, r := range rows {
		if r.AutoIgnored {
			n++
		}
	}
	return n
}
