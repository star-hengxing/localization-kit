package quality

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"strings"
)

type PolishModelItem struct {
	ID     string `json:"id"`
	Dst    string `json:"dst"`
	Reason string `json:"reason"`
}

func ParsePolishRunResponse(content string) ([]PolishModelItem, error) {
	var payload struct {
		Items []PolishModelItem `json:"items"`
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("模型结果不是有效 JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || payload.Items == nil {
		return nil, fmt.Errorf("模型结果含多余内容或缺少 items 数组")
	}
	return payload.Items, nil
}

func PolishBatchItems(batch ReviewBatch, items []PolishModelItem) ([]PolishItem, map[string]ReviewTarget, error) {
	targets := make(map[string]ReviewTarget, len(batch.Targets))
	for _, target := range batch.Targets {
		targets[target.ID] = target
	}
	if len(items) != len(targets) {
		return nil, nil, fmt.Errorf("润色结果未覆盖整个批次")
	}
	seen := map[string]bool{}
	result := make([]PolishItem, 0, len(items))
	for _, item := range items {
		target, ok := targets[item.ID]
		if !ok || seen[item.ID] {
			return nil, nil, fmt.Errorf("润色结果包含未知或重复条目 %q", item.ID)
		}
		seen[item.ID] = true
		result = append(result, PolishItem{ID: item.ID, ExpectedResultHash: target.ExpectedResultHash, Dst: item.Dst, Reason: item.Reason})
	}
	return result, targets, nil
}

func InspectPolishBatch(jobPath string, material PolishMaterial, batch ReviewBatch, items []PolishModelItem, actor string) error {
	converted, expected, err := PolishBatchItems(batch, items)
	if err != nil {
		return err
	}
	job, err := store.OpenReadOnly(jobPath)
	if err != nil {
		return err
	}
	_, err = PolishTargets(job, material)
	if err == nil {
		_, _, err = ValidatePolishItems(job, material, expected, converted, actor)
	}
	return errors.Join(err, job.Close())
}

func VerifyPolishInput(path string, material PolishMaterial) error {
	current, err := ReadPolishMaterial(path)
	if err != nil {
		return err
	}
	same, err := sameJSON(current, material)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("润色材料文件已变化，请重新准备")
	}
	return nil
}

type PolishRunReport struct {
	Kind    string             `json:"kind"`
	Items   []ReviewVerdict    `json:"items"`
	Context []document.Segment `json:"context,omitempty"`
}

func PolishBatchReports(material PolishMaterial, batch ReviewBatch) []PolishRunReport {
	targets := map[string]bool{}
	for _, target := range batch.Targets {
		targets[target.ID] = true
	}
	var reports []PolishRunReport
	appendReport := func(source ReviewMaterial, report ReviewReport) {
		items := []ReviewVerdict{}
		for _, item := range report.Items {
			if targets[item.ID] {
				items = append(items, item)
			}
		}
		var context []document.Segment
		seen := map[string]bool{}
		for _, sourceBatch := range source.Batches {
			used := false
			for _, target := range sourceBatch.Targets {
				if targets[target.ID] {
					used = true
					break
				}
			}
			if used {
				for _, segment := range sourceBatch.Context {
					if !seen[segment.ID] {
						seen[segment.ID] = true
						context = append(context, segment)
					}
				}
			}
		}
		reports = append(reports, PolishRunReport{Kind: source.Kind, Items: items, Context: context})
	}
	if material.Report != nil {
		appendReport(material.Review, *material.Report)
	}
	for _, additional := range material.Additional {
		appendReport(additional.Material, additional.Report)
	}
	return reports
}
