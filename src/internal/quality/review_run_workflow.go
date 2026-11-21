package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"localization-kit/src/internal/config"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/model"
	"localization-kit/src/internal/store"
	"strings"
)

func RunReviewModel(ctx context.Context, options ModelRunOptions) (summary map[string]any, resultErr error) {
	if options.JobPath == "" || options.Input == "" || options.RunDir == "" || options.Output == "" || options.ModelName == "" || options.MaxOutput <= 0 {
		return nil, fmt.Errorf("invalid model run options")
	}
	run := func() error {

		if err := fileutil.CheckNewPath(options.Output, options.JobPath, options.Input, options.RunDir, options.ConfigPath); err != nil {
			return err
		}
		runAbs, err := fileutil.Canonical(options.RunDir)
		if err != nil {
			return err
		}
		for _, path := range []string{options.JobPath, options.Input, options.ConfigPath} {
			abs, err := fileutil.Canonical(path)
			if err != nil {
				return err
			}
			if fileutil.Overlaps(runAbs, abs) {
				return fmt.Errorf("批次目录不能与作业、材料或模型配置重叠")
			}
		}
		material, err := ReadReviewMaterial(options.Input)
		if err != nil {
			return err
		}
		if err := InspectReview(options.JobPath, material, nil, nil); err != nil {
			return fmt.Errorf("检查材料无效或已过时: %w", err)
		}
		selected, err := options.Config.SelectModel(options.ModelName)
		if err != nil {
			return err
		}
		client, err := model.New(selected)
		if err != nil {
			return err
		}
		manifest := Manifest{Version: 1, MaterialHash: material.Hash, Model: config.ModelRecord{Model: selected}, Prompt: "review-run-v2", MaxOutputTokens: options.MaxOutput}
		for _, batch := range material.Batches {
			batchHash, err := ReviewBatchHash(material, batch)
			if err != nil {
				return err
			}
			manifest.Batches = append(manifest.Batches, batchHash)
		}
		batches := make([]Batch[ReviewVerdict], 0, len(material.Batches))
		for _, batch := range material.Batches {
			payload, err := json.Marshal(struct {
				Batch    ReviewBatch     `json:"batch"`
				Glossary any             `json:"glossary,omitempty"`
				Cards    json.RawMessage `json:"cards,omitempty"`
			}{batch, material.Glossary, material.Cards})
			if err != nil {
				return err
			}
			prompt := strings.Replace(material.Prompt, "返回 JSON：{material_hash,candidate_hash,items:[{id,verdict,issues:[{code,detail,evidence:[{segment_id,quote}]}]}]}。", "", 1)
			request := model.Request{Messages: []model.Message{
				{Role: "system", Content: prompt + `只返回 JSON 对象 {"items":[{"id":"...","verdict":"pass|issues|uncertain","issues":[{"code":"...","detail":"...","evidence":[{"segment_id":"...","quote":"..."}]}]}]}。无问题时可省略 issues。`},
				{Role: "user", Content: string(payload)},
			}, Temperature: 0.2, TopP: 0.95, MaxTokens: options.MaxOutput}
			batches = append(batches, Batch[ReviewVerdict]{Request: request, Validate: func(items []ReviewVerdict) error {
				return InspectReview(options.JobPath, material, &batch, items)
			}})
		}
		results, err := RunBatches(ctx, options.RunDir, options.Retry, client, manifest, batches, func() error {
			if err := config.VerifyModel(options.ConfigPath, options.ModelName, selected); err != nil {
				return err
			}
			return VerifyReviewInput(options.Input, material)
		}, ParseReviewRunResponse)
		if err != nil {
			return err
		}
		var all []ReviewVerdict
		for _, batch := range results {
			all = append(all, batch...)
		}
		report := ReviewReport{MaterialHash: material.Hash, CandidateHash: material.CandidateHash, Items: all}
		if err := config.VerifyModel(options.ConfigPath, options.ModelName, selected); err != nil {
			return err
		}
		if err := VerifyReviewInput(options.Input, material); err != nil {
			return err
		}
		job, err := store.OpenReadOnly(options.JobPath)
		if err != nil {
			return err
		}
		err = ValidateReview(job, material, report)
		if err == nil {
			err = jsonfile.Publish(options.Output, report)
		}
		if err = errors.Join(err, job.Close()); err != nil {
			return err
		}
		summary = map[string]any{"batches": len(material.Batches), "items": len(all), "report": options.Output}
		return nil
	}
	resultErr = run()
	return summary, resultErr
}
