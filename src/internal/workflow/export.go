package workflow

import (
	"fmt"
	"path/filepath"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/exporter"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/format"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
)

type ExportOptions struct {
	ResourceRoot, JobPath, Adapter, Input, Output string
	Stage                                         string
	// Validate runs under the store lock before and after publication.
	// An error from the final check leaves the published output at Output.
	Validate  func(*store.Store) error
	Bilingual bool
	Glossary  []string
}

func Export(options ExportOptions) (summary translate.Summary, resultErr error) {
	if options.Input == "" || options.Output == "" || options.JobPath == "" || options.Stage == "" {
		return summary, fmt.Errorf("导出需要作品状态、原文、阶段和输出路径")
	}

	if err := rules.LoadSimplified(filepath.Join(options.ResourceRoot, rules.DefaultDataPath)); err != nil {
		return summary, err
	}
	if err := fileutil.CheckPaths(options.Input, options.JobPath, options.Output); err != nil {
		return summary, err
	}
	if err := fileutil.CheckOutput(options.Output); err != nil {
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
	job, err := store.OpenReadOnly(options.JobPath)
	if err != nil {
		return summary, err
	}
	defer job.Close()
	if err := job.VerifySegments(source.Segments); err != nil {
		return summary, err
	}
	if options.Validate != nil {
		if err := options.Validate(job); err != nil {
			return summary, err
		}
	}
	selected, err := job.StageResults(options.Stage)
	if err != nil {
		return summary, err
	}
	shadow := *job
	shadow.Done = selected
	shadow.Failed = map[string]document.Result{}
	if options.Stage != "translate" {
		// Check the selected version against its own context. Live completion IDs still detect redos.
		status, err := agent.InspectLocked(options.JobPath, &shadow, true, "")
		if err != nil {
			return summary, err
		}
		for _, line := range status.Lines {
			item := selected[line.ID]
			if item.Passthrough && item.Status == "done" {
				continue
			}
			current := job.Done[line.ID]
			stage := line.Polish
			completion, liveCompletion := item.PolishCompletion, current.PolishCompletion
			if options.Stage == "proofread" {
				stage = line.Proofread
				completion, liveCompletion = item.ProofreadCompletion, current.ProofreadCompletion
			}
			if stage.State != "done" {
				return summary, fmt.Errorf("%s 阶段条目 %s 未完成或需复核: %s", options.Stage, line.ID, stage.State)
			}
			if completion == nil || liveCompletion == nil || liveCompletion.TaskID != completion.TaskID {
				return summary, fmt.Errorf("%s 阶段条目 %s 的底稿已更新或需复核", options.Stage, line.ID)
			}
		}
	}
	termHashes := translate.TermsHashes(source.Segments, entries)
	for _, segment := range source.Segments {
		item, ok := selected[segment.ID]
		if !ok || item.Status != "done" || item.NeedsReview {
			return summary, fmt.Errorf("%s 阶段条目 %s 未完成或需复核", options.Stage, segment.ID)
		}
		current, ok := job.Done[segment.ID]
		if !ok || current.Signature != item.Signature || current.NeedsReview && current.ReviewSignature != item.Signature {
			return summary, fmt.Errorf("%s 阶段条目 %s 的底稿已更新或需复核", options.Stage, segment.ID)
		}
		if item.TermsHash == "" || item.TermsHash != termHashes[segment.Group] {
			return summary, fmt.Errorf("%s 阶段条目 %s 的术语依赖已变化，请先重新处理", options.Stage, segment.ID)
		}
		if options.Stage != "translate" && item.HistoryUsed && item.HistoryHash != translate.HistoryDigest(&shadow, segment) {
			return summary, fmt.Errorf("%s 阶段条目 %s 的上文已变化，请先复核", options.Stage, segment.ID)
		}
	}
	if options.Stage == "translate" {
		if err := translate.CheckInitialResults(&shadow, source.Segments, entries); err != nil {
			return summary, err
		}
	}
	if options.Validate != nil {
		if err := options.Validate(job); err != nil {
			return summary, err
		}
	}
	var credits []string
	switch source.Adapter {
	case "vtt", "artc":
		credits, err = shadow.ExportCreditLines()
		if err != nil {
			return summary, err
		}
	}
	if err := exporter.Publish(source, selected, options.Output, entries, credits, options.Bilingual); err != nil {
		return summary, err
	}
	if options.Validate != nil {
		if err := options.Validate(job); err != nil {
			return summary, fmt.Errorf("输出已生成，但作品配置或原文已变化: %w", err)
		}
	}
	return translate.Summarize(&shadow), nil
}
