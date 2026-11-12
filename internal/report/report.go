package report

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"hornedviper/internal/store"
)

type Document struct {
	TaskID   int64
	Filename string
	Content  string
}

// BuildTask produces a self-contained Markdown report for one task.
func BuildTask(ctx context.Context, st *store.Store, taskID int64) (*Document, error) {
	task, err := st.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task #%d not found", taskID)
	}
	goal, _ := st.CurrentGoal(ctx, taskID)
	flags, _ := st.ListTaskFlags(ctx, taskID)
	findings, _ := st.ListFindings(ctx, taskID, 500)
	assets, _ := st.ListTaskAssets(ctx, taskID, 500)
	facts, _ := st.ListFacts(ctx, taskID, 500)
	intents, _ := st.ListIntents(ctx, taskID)
	works, _ := st.ListWorks(ctx, taskID)

	var b strings.Builder
	fmt.Fprintf(&b, "# HornedViper 任务报告\n\n")
	fmt.Fprintf(&b, "- 任务：**#%d %s**\n", task.ID, md(task.Title))
	fmt.Fprintf(&b, "- 模式：`%s`\n", task.Mode)
	fmt.Fprintf(&b, "- 状态：`%s`\n", task.Status)
	fmt.Fprintf(&b, "- 创建时间：%s\n", task.CreatedAt.Format("2006-01-02 15:04:05"))
	if task.StartedAt != nil {
		fmt.Fprintf(&b, "- 开始时间：%s\n", task.StartedAt.Format("2006-01-02 15:04:05"))
	}
	if task.EndedAt != nil {
		fmt.Fprintf(&b, "- 结束时间：%s\n", task.EndedAt.Format("2006-01-02 15:04:05"))
	}
	fmt.Fprintf(&b, "- 报告生成：%s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	fmt.Fprintf(&b, "## 目标\n\n")
	if goal != nil {
		fmt.Fprintf(&b, "%s\n\n", goal.Text)
		if len(goal.Acceptance) > 0 {
			fmt.Fprintf(&b, "验收条件：\n")
			for _, a := range goal.Acceptance {
				fmt.Fprintf(&b, "- %s\n", a)
			}
			b.WriteString("\n")
		}
	} else {
		b.WriteString("无目标记录。\n\n")
	}

	fmt.Fprintf(&b, "## 摘要\n\n")
	fmt.Fprintf(&b, "| 指标 | 数量 |\n|---|---:|\n")
	fmt.Fprintf(&b, "| Flag | %d |\n", len(flags))
	fmt.Fprintf(&b, "| Finding | %d |\n", len(findings))
	fmt.Fprintf(&b, "| Asset | %d |\n", len(assets))
	fmt.Fprintf(&b, "| Fact | %d |\n", len(facts))
	fmt.Fprintf(&b, "| Intent | %d |\n", len(intents))
	fmt.Fprintf(&b, "| Work | %d |\n\n", len(works))

	b.WriteString("## Flag 结果\n\n")
	if len(flags) == 0 {
		b.WriteString("暂无 Flag 记录。\n\n")
	} else {
		b.WriteString("| 题目 | Flag | 状态 | 来源 |\n|---|---|---|---|\n")
		for _, f := range flags {
			fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n", md(f.Challenge), md(f.Flag), md(f.Status), md(f.Source))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Findings\n\n")
	if len(findings) == 0 {
		b.WriteString("暂无 Finding。\n\n")
	} else {
		sort.SliceStable(findings, func(i, j int) bool { return findings[i].ID > findings[j].ID })
		for _, f := range findings {
			fmt.Fprintf(&b, "### #%d [%s] %s\n\n", f.ID, md(f.Severity), f.Title)
			if f.SubjectKey != "" {
				fmt.Fprintf(&b, "- Subject: `%s`\n", md(f.SubjectKey))
			}
			if f.Observation != "" {
				fmt.Fprintf(&b, "- Observation: `%s`\n", md(f.Observation))
			}
			if q := strings.TrimSpace(f.Evidence); q != "" {
				fmt.Fprintf(&b, "\n%s\n", fenced(q))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## Assets\n\n")
	if len(assets) == 0 {
		b.WriteString("暂无 Asset。\n\n")
	} else {
		b.WriteString("| ID | Type | Value | Scope |\n|---:|---|---|---|\n")
		for _, a := range assets {
			fmt.Fprintf(&b, "| %d | %s | %s | %s |\n", a.ID, md(a.Type), md(a.Value), md(a.ScopeStatus))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Facts\n\n")
	if len(facts) == 0 {
		b.WriteString("暂无 Fact。\n\n")
	} else {
		for _, f := range facts {
			fmt.Fprintf(&b, "- **#%d** %s\n", f.ID, f.Summary)
			if strings.TrimSpace(f.Detail) != "" {
				fmt.Fprintf(&b, "  - %s\n", md(f.Detail))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## 执行记录\n\n")
	if len(intents) == 0 {
		b.WriteString("暂无 Intent。\n\n")
	} else {
		b.WriteString("| Intent | 状态 | 目标 | 摘要 |\n|---:|---|---|---|\n")
		for _, in := range intents {
			fmt.Fprintf(&b, "| #%d | %s | %s | %s |\n", in.ID, md(string(in.State)), md(in.TargetKey), md(truncate(in.Summary, 160)))
		}
		b.WriteString("\n")
	}
	if len(works) > 0 {
		b.WriteString("| Work | Intent | Worker | 状态 | 停止原因 |\n|---:|---:|---|---|---|\n")
		for _, w := range works {
			fmt.Fprintf(&b, "| #%d | #%d | %s | %s | %s |\n", w.ID, w.IntentID, md(w.WorkerID), md(string(w.State)), md(w.StopReason))
		}
		b.WriteString("\n")
	}

	return &Document{
		TaskID:   task.ID,
		Filename: fmt.Sprintf("task-%d-report-%s.md", task.ID, time.Now().Format("20060102-150405")),
		Content:  b.String(),
	}, nil
}

func md(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func fenced(s string) string {
	if strings.Contains(s, "```") {
		return s
	}
	return "```\n" + strings.TrimSpace(s) + "\n```"
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
