package job

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
)

func NextWithValidation(jobPath string, args []string, validate func(*store.Store) error) error {
	f := flag.NewFlagSet("job next", flag.ContinueOnError)
	output := f.String("format", "json", "输出格式: json 或 text")
	stage := f.String("stage", "", "polish 或 proofread")
	worker := f.String("worker", "", "执行者标识")
	model := f.String("model", "", "实际模型 ID")
	cards := f.String("cards", "", "角色卡 JSON")
	ttl := f.Duration("ttl", common.DefaultTaskTTL, "任务领取时限")
	contextLines := f.Int("context", common.DefaultTaskContext, "前文条数")
	scope := f.String("context-scope", common.DefaultTaskScope, "上下文依赖范围: group 或 task")
	maxLines := f.Int("max-lines", common.DefaultTaskMaxLines, "每块最多条数")
	var glossaries, reviewInputs, reviewReports common.StringList
	f.Var(&glossaries, "glossary", "术语表 JSON，可重复")
	f.Var(&reviewInputs, "review-input", "初翻审查材料 JSON，可重复")
	f.Var(&reviewReports, "review-report", "对应的初翻审查报告 JSON，可重复")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("job next 不接受多余位置参数")
	}
	if *output != "json" && *output != "text" {
		return fmt.Errorf("--format 必须为 json 或 text")
	}
	if len(reviewInputs) != len(reviewReports) {
		return fmt.Errorf("--review-input 和 --review-report 必须成对提供")
	}
	reviews := make([]agent.ReviewFiles, len(reviewInputs))
	for i := range reviewInputs {
		reviews[i] = agent.ReviewFiles{Input: reviewInputs[i], Report: reviewReports[i]}
	}
	if err := rules.LoadSimplified(filepath.Join(common.ResourceRoot, rules.DefaultDataPath)); err != nil {
		return err
	}
	task, err := agent.NextWithValidation(agent.Options{Job: jobPath, Stage: *stage, Worker: *worker, Model: *model, Cards: *cards, Scope: *scope, Glossaries: glossaries, Reviews: reviews, TTL: *ttl, Context: *contextLines, MaxLines: *maxLines}, validate)
	if err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("任务：阶段=%s worker=%s model=%s scope=%s 状态=%s ID=%s 条目=%d", *stage, *worker, *model, *scope, task.Status, task.TaskID, len(task.Lines)))
	if *output == "text" {
		return writeTaskText(os.Stdout, task)
	}
	task.Revision = ""
	return json.NewEncoder(os.Stdout).Encode(task)
}

func SubmitWithValidation(jobPath string, args []string, validate func(*store.Store) error) error {
	f := flag.NewFlagSet("job submit", flag.ContinueOnError)
	worker := f.String("worker", "", "执行者标识")
	taskID := f.String("task", "", "任务 ID")
	file := f.String("file", "", "提交结果 JSON")
	changesFile := f.String("changes", "", "改动 JSON，其余条目原样确认")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (*file == "") == (*changesFile == "") {
		return fmt.Errorf("需要 --worker、--task，以及 --file 或 --changes 之一")
	}
	input := *file
	submit := agent.SubmitWithValidation
	if *changesFile != "" {
		input = *changesFile
		submit = agent.SubmitChangesWithValidation
	}
	var submissions []agent.Submission
	if err := jsonfile.Read(input, &submissions); err != nil {
		return err
	}
	if err := rules.LoadSimplified(filepath.Join(common.ResourceRoot, rules.DefaultDataPath)); err != nil {
		return err
	}
	if err := submit(jobPath, *worker, *taskID, submissions, validate); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("提交：worker=%s 任务=%s 条目=%d", *worker, *taskID, len(submissions)))
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "accepted", "task_id": *taskID})
}

func Release(jobPath string, args []string) error {
	f := flag.NewFlagSet("job release", flag.ContinueOnError)
	worker := f.String("worker", "", "执行者标识")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("job release 不接受多余位置参数")
	}
	count, err := agent.Release(jobPath, *worker)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"released": count})
}
