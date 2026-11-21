package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/logging"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
	"localization-kit/src/internal/workflow"
	"localization-kit/src/internal/workspace"
)

func translateWorkspace(ctx context.Context, w workspace.Workspace, args []string) error {
	f := flag.NewFlagSet("translate", flag.ContinueOnError)
	modelName := f.String("model", w.Config.Model, "模型配置名称")
	previous := f.String("previous", "", "复用的作品目录")
	retry := f.Bool("retry-failed", false, "重试失败条目")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("translate 不接受额外位置参数")
	}
	cfg, err := w.Config.ValidateModelSettings()
	if err != nil {
		return err
	}
	logging.ConfigureLogger(cfg.LogLevel)
	options := workflow.TranslateOptions{
		ResourceRoot: common.ResourceRoot,
		ConfigPath:   filepath.Join(w.Root, "config.toml"),
		ModelName:    *modelName,
		Adapter:      w.Config.Adapter,
		Input:        w.Input(),
		JobPath:      w.State(),
		Retry:        *retry,
		Glossary:     w.Glossaries(),
	}
	if *previous != "" {
		prior, err := workspace.Load(*previous)
		if err != nil {
			return err
		}
		options.PreviousPath = prior.State()
	}
	summary, runErr := workflow.Translate(ctx, options)
	var incomplete *workflow.IncompleteError
	if runErr != nil && !errors.As(runErr, &incomplete) {
		return runErr
	}
	slog.Info(fmt.Sprintf("初翻：总计=%d 完成=%d 失败=%d 待处理=%d 待复核=%d", summary.Total, summary.Done, summary.Failed, summary.Pending, summary.Review))
	if err := json.NewEncoder(os.Stdout).Encode(summary); err != nil {
		return err
	}
	return runErr
}

func runWorkspaceStage(ctx context.Context, w workspace.Workspace, rest []string) error {
	state := w.State()
	f := flag.NewFlagSet("run", flag.ContinueOnError)
	stage := f.String("stage", "", "polish 或 proofread")
	modelName := f.String("model", "", "质量模型名称")
	maxOutput := f.Int("max-output-tokens", common.DefaultQualityMaxOutput, "每块输出 token 上限")
	ttl := f.Duration("ttl", common.DefaultTaskTTL, "任务领取时限")
	contextLines := f.Int("context", common.DefaultTaskContext, "前文条数")
	maxLines := f.Int("max-lines", common.DefaultTaskMaxLines, "每块最多条数")
	scope := f.String("context-scope", common.DefaultTaskScope, "上下文依赖范围: group 或 task")
	var reviewInputs, reviewReports common.StringList
	f.Var(&reviewInputs, "review-input", "初翻审查材料 JSON，可重复")
	f.Var(&reviewReports, "review-report", "对应的初翻审查报告 JSON，可重复")
	if err := f.Parse(rest); err != nil {
		return err
	}
	if f.NArg() != 0 || *modelName == "" || (*stage != "polish" && *stage != "proofread") || *maxOutput <= 0 || *ttl <= 0 || *contextLines < 0 || *maxLines <= 0 || (*scope != "group" && *scope != "task") {
		return fmt.Errorf("run 需要 --stage polish|proofread、--model 及有效的任务选项")
	}
	if len(reviewInputs) != len(reviewReports) || (*stage != "polish" && len(reviewInputs) != 0) {
		return fmt.Errorf("审查材料和报告须成对提供，且只能用于润色")
	}
	reviews := make([]agent.ReviewFiles, len(reviewInputs))
	for i := range reviewInputs {
		reviews[i] = agent.ReviewFiles{Input: reviewInputs[i], Report: reviewReports[i]}
	}
	cfg, err := w.Config.ValidateModelSettings()
	if err != nil {
		return err
	}
	selected, err := cfg.SelectModel(*modelName)
	if err != nil {
		return err
	}
	logging.ConfigureLogger(cfg.LogLevel)
	if err := rules.LoadSimplified(filepath.Join(common.ResourceRoot, rules.DefaultDataPath)); err != nil {
		return err
	}
	glossaries, cards := w.Glossaries(), w.Cards()
	summary, err := workflow.RunStage(ctx, workflow.StageOptions{
		Task:  agent.Options{Job: state, Stage: *stage, Cards: cards, Glossaries: glossaries, Reviews: reviews, TTL: *ttl, Context: *contextLines, MaxLines: *maxLines, Scope: *scope},
		Model: selected, MaxOutput: *maxOutput,
		Validate: func(job *store.Store) error {
			current, err := validateWorkspaceSource(w, job, true)
			if err != nil {
				return err
			}
			if !slices.Equal(current.Glossaries(), glossaries) || current.Cards() != cards {
				return fmt.Errorf("作品材料配置已变化，请重新执行")
			}
			models, err := current.Config.ValidateModelSettings()
			if err != nil {
				return err
			}
			currentModel, err := models.SelectModel(*modelName)
			if err != nil {
				return err
			}
			if currentModel != selected {
				return fmt.Errorf("模型配置已变化，请重新执行")
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(summary)
}
