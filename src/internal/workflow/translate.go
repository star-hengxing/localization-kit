package workflow

import (
	"context"
	"errors"
	"fmt"
	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/config"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/format"
	"localization-kit/src/internal/model"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
	"log/slog"
	"path/filepath"
)

type TranslateOptions struct {
	ResourceRoot, ConfigPath, ModelName, Adapter, Input, JobPath, PreviousPath string
	Retry                                                                      bool
	Glossary                                                                   []string
}

type IncompleteError struct{ Summary translate.Summary }

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("翻译需处理：失败 %d 条，待处理 %d 条，人工修订待复核 %d 条", e.Summary.Failed, e.Summary.Pending, e.Summary.Review)
}

func Translate(ctx context.Context, options TranslateOptions) (summary translate.Summary, resultErr error) {
	if options.Input == "" || options.JobPath == "" {
		return summary, fmt.Errorf("初翻需要原文和作品状态目录")
	}

	input, err := fileutil.Canonical(options.Input)
	if err != nil {
		return summary, err
	}
	state, err := fileutil.Canonical(options.JobPath)
	if err != nil {
		return summary, err
	}
	if fileutil.Overlaps(input, state) {
		return summary, fmt.Errorf("输入与作品目录不能互相重叠")
	}
	if options.PreviousPath != "" {
		previousAbs, err := fileutil.Canonical(options.PreviousPath)
		if err != nil {
			return summary, err
		}
		if fileutil.Overlaps(previousAbs, input) || fileutil.Overlaps(previousAbs, state) {
			return summary, fmt.Errorf("原作业与输入或新作业路径不能重叠")
		}
	}
	if err := rules.LoadSimplified(filepath.Join(options.ResourceRoot, rules.DefaultDataPath)); err != nil {
		return summary, err
	}
	cfg, err := config.Load(options.ConfigPath)
	if err != nil {
		return summary, err
	}
	selected, err := cfg.SelectModel(options.ModelName)
	if err != nil {
		return summary, err
	}
	client, err := model.New(selected)
	if err != nil {
		return summary, err
	}
	entries, err := rules.LoadGlossaries(options.Glossary)
	if err != nil {
		return summary, err
	}
	source, err := format.LoadSource(options.Adapter, options.Input)
	if err != nil {
		return summary, err
	}
	job, err := store.OpenOrCreate(options.JobPath, source.Segments)
	if err != nil {
		return summary, err
	}
	slog.Info("开始翻译", "model", selected.Model, "profile", selected.Profile, "条目", len(source.Segments))
	defer func() {
		resultErr = errors.Join(resultErr, job.Close())
		if resultErr != nil {
			slog.Info("翻译结束")
		} else {
			slog.Info("翻译完成")
		}
	}()
	job.BeforeUnlock = func() error { return agent.ClearLiveStatus(options.JobPath) }
	if err := agent.PublishLiveStatus(options.JobPath, job); err != nil {
		return summary, err
	}
	job.AfterCommit = func(job *store.Store) error { return agent.PublishLiveStatus(options.JobPath, job) }
	if options.PreviousPath != "" {
		previous, err := store.OpenReadOnly(options.PreviousPath)
		if err != nil {
			return summary, err
		}
		_, seedErr := translate.SeedPrevious(previous, job, selected, entries)
		closeErr := previous.Close()
		if seedErr != nil {
			return summary, seedErr
		}
		if closeErr != nil {
			return summary, closeErr
		}
	}
	summary, err = translate.Run(ctx, client, selected, source.Segments, entries, job, options.Retry)
	if err != nil {
		return summary, err
	}
	if summary.Failed > 0 || summary.Pending > 0 || summary.Review > 0 {
		return summary, &IncompleteError{Summary: summary}
	}
	return summary, nil
}
