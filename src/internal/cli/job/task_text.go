package job

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"localization-kit/src/internal/agent"
)

func writeTaskText(w io.Writer, task agent.Task) error {
	var out strings.Builder
	fmt.Fprintln(&out, "状态", task.Status)
	if task.Blocked > 0 {
		fmt.Fprintln(&out, "受阻条目", task.Blocked)
	}
	if task.BlockedReason != "" {
		fmt.Fprintln(&out, "原因", taskText(task.BlockedReason))
	}
	if task.Status == "ready" {
		fmt.Fprintln(&out, "任务", task.TaskID)
		stage := "润色"
		if task.Stage == "proofread" {
			stage = "校对"
		}
		fmt.Fprintf(&out, "阶段 %s (%d 条)\n", stage, len(task.Lines))
		if task.Expires != nil {
			fmt.Fprintln(&out, "截止", task.Expires.Format(time.RFC3339))
		}
		writeTaskLines(&out, "前文", task.Context)
		writeTaskLines(&out, "待处理", task.Lines)
		if len(task.Following) != 0 {
			fmt.Fprintln(&out, "\n后文")
			for _, line := range task.Following {
				fmt.Fprintf(&out, "%s  %s\n", taskText(line.ID), taskText(line.Src))
			}
		}
		if len(task.Glossary) != 0 {
			fmt.Fprintln(&out, "\n术语")
			for _, term := range task.Glossary {
				fmt.Fprintf(&out, "%s -> %s\n", taskText(term.Src), taskText(term.Dst))
				if term.Kind != "" {
					fmt.Fprintln(&out, "  类别", taskText(term.Kind))
				}
				if term.Constraint != "" {
					fmt.Fprintln(&out, "  约束", taskText(term.Constraint))
				}
				if len(term.Variants) != 0 {
					fmt.Fprintln(&out, "  变体", taskText(strings.Join(term.Variants, ", ")))
				}
				if term.Note != "" {
					fmt.Fprintln(&out, "  备注", taskText(term.Note))
				}
			}
		}
		if len(task.Cards) != 0 {
			fmt.Fprintf(&out, "\n角色卡\n%s\n", task.Cards)
		}
		if len(task.Rules) != 0 {
			fmt.Fprintln(&out, "\n规则")
			for _, rule := range task.Rules {
				fmt.Fprintln(&out, taskText(rule))
			}
		}
	}
	_, err := io.WriteString(w, out.String())
	return err
}

func writeTaskLines(out *strings.Builder, title string, lines []agent.Line) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s\n", title)
	for _, line := range lines {
		fmt.Fprintf(out, "%s  %s -> %s\n", taskText(line.ID), taskText(line.Src), taskText(line.Dst))
		for _, review := range line.Reviews {
			fmt.Fprintf(out, "  审查 %s %s\n", taskText(review.Kind), taskText(review.Verdict))
			if review.Stale {
				fmt.Fprintln(out, "  审查已过期")
			}
			fmt.Fprintln(out, "  审查底稿", taskText(review.Baseline))
			for _, issue := range review.Issues {
				fmt.Fprintf(out, "    %s  %s\n", taskText(issue.Code), taskText(issue.Detail))
				for _, evidence := range issue.Evidence {
					fmt.Fprintf(out, "    依据 %s  %s\n", taskText(evidence.SegmentID), taskText(evidence.Quote))
				}
			}
		}
	}
}

func taskText(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsGraphic(r) {
			out.WriteRune(r)
		} else {
			quoted := strconv.QuoteRuneToGraphic(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		}
	}
	return out.String()
}
