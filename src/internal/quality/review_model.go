package quality

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
	"strings"
)

func ReviewBatchHash(material ReviewMaterial, batch ReviewBatch) (string, error) {
	return jsonfile.Digest(struct {
		Material string      `json:"material"`
		Batch    ReviewBatch `json:"batch"`
	}{material.Hash, batch})
}

func ReviewWithItems(material ReviewMaterial, items []ReviewVerdict) ReviewReport {
	selected := make(map[string]ReviewVerdict, len(items))
	for _, item := range items {
		selected[item.ID] = item
	}
	report := ReviewReport{MaterialHash: material.Hash, CandidateHash: material.CandidateHash}
	for _, batch := range material.Batches {
		for _, target := range batch.Targets {
			item, ok := selected[target.ID]
			if !ok {
				item = ReviewVerdict{ID: target.ID, Verdict: "pass"}
			}
			report.Items = append(report.Items, item)
		}
	}
	return report
}

func ValidateReviewBatch(job *store.Store, material ReviewMaterial, batch ReviewBatch, items []ReviewVerdict) error {
	if len(items) != len(batch.Targets) {
		return fmt.Errorf("模型结果未覆盖整个批次")
	}
	expected := map[string]bool{}
	for _, target := range batch.Targets {
		expected[target.ID] = true
	}
	for _, item := range items {
		if !expected[item.ID] {
			return fmt.Errorf("模型结果包含未知或重复条目 %q", item.ID)
		}
		delete(expected, item.ID)
	}
	return ValidateReview(job, material, ReviewWithItems(material, items))
}

func InspectReview(jobPath string, material ReviewMaterial, batch *ReviewBatch, items []ReviewVerdict) error {
	job, err := store.OpenReadOnly(jobPath)
	if err != nil {
		return err
	}
	if batch == nil {
		err = ValidateReview(job, material, ReviewWithItems(material, nil))
	} else {
		err = ValidateReviewBatch(job, material, *batch, items)
	}
	return errors.Join(err, job.Close())
}

func ParseReviewRunResponse(content string) ([]ReviewVerdict, error) {
	var payload struct {
		Items []ReviewVerdict `json:"items"`
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("模型结果不是有效 JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("模型结果含多余内容")
	}
	if payload.Items == nil {
		return nil, fmt.Errorf("模型结果缺少 items 数组")
	}
	return payload.Items, nil
}

func VerifyReviewInput(path string, original ReviewMaterial) error {
	current, err := ReadReviewMaterial(path)
	if err != nil {
		return err
	}
	same, err := sameJSON(current, original)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("检查材料文件已变化，请重新准备")
	}
	return nil
}

func sameJSON(a, b any) (bool, error) {
	left, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(left, right), nil
}
