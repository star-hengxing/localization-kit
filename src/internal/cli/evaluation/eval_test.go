package evaluation

import (
	"encoding/json"
	"localization-kit/src/internal/evaluation"
	"localization-kit/src/internal/jsonfile"
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
)

func TestCompareJobsUsesOnlyCompleteReviewedTranslations(t *testing.T) {
	segments := []document.Segment{
		{ID: "same", Src: "同じ"}, {ID: "changed", Src: "別"}, {ID: "pending", Src: "待つ"},
		{ID: "review", Src: "見る"}, {ID: "passthrough", Src: "123"},
	}
	baseline := &store.Store{Segments: segments, Done: map[string]document.Result{
		"same": {Dst: "相同"}, "changed": {Dst: "旧"}, "pending": {Dst: "等待"},
		"review": {Dst: "看", NeedsReview: true}, "passthrough": {Dst: "123", Passthrough: true},
	}, Failed: map[string]document.Result{}}
	candidate := &store.Store{Segments: segments, Done: map[string]document.Result{
		"same": {Dst: "相同"}, "changed": {Dst: "新"}, "review": {Dst: "看"},
		"passthrough": {Dst: "123", Passthrough: true},
	}, Failed: map[string]document.Result{}}
	result, err := evaluation.Compare(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Same != 1 || result.Changed != 1 || result.Unpaired != 3 {
		t.Fatalf("unexpected counts: %+v", result)
	}
	if result.Items[2].Candidate.Status != "pending" || !result.Items[3].Baseline.NeedsReview || !result.Items[4].Candidate.Passthrough {
		t.Fatalf("status information lost: %+v", result.Items)
	}
}

func TestBlindScoringRequiresBothPositions(t *testing.T) {
	dir := t.TempDir()
	dstA, dstB := "基线", "候选"
	different := false
	comparison := evaluation.Comparison{Version: 1, BaselineJob: filepath.Join(dir, "baseline"), CandidateJob: filepath.Join(dir, "candidate"), Items: []evaluation.Item{{ID: "one", Src: "原文", Baseline: evaluation.Result{Status: "done", Dst: &dstA}, Candidate: evaluation.Result{Status: "done", Dst: &dstB}, Equal: &different}}}
	input := filepath.Join(dir, "comparison.json")
	if err := jsonfile.Create(input, comparison); err != nil {
		t.Fatal(err)
	}
	blind := filepath.Join(dir, "blind")
	if err := prepareBlind([]string{"--input", input, "--out", blind}); err != nil {
		t.Fatal(err)
	}
	var key evaluation.BlindKey
	if err := jsonfile.Read(filepath.Join(blind, "key.json"), &key); err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(dir, "reports")
	if err := os.Mkdir(reports, 0700); err != nil {
		t.Fatal(err)
	}
	for name := range key.Batches {
		if name[0] != '1' {
			continue
		}
		var batch evaluation.BlindBatch
		if err := jsonfile.Read(filepath.Join(blind, "batches", name+".json"), &batch); err != nil {
			t.Fatal(err)
		}
		report := evaluation.BlindReport{BatchHash: batch.Hash}
		for _, item := range batch.Items {
			report.Items = append(report.Items, evaluation.BlindVerdict{Token: item.Token, Winner: key.Items[item.Token].Candidate})
		}
		if err := jsonfile.Create(filepath.Join(reports, name+".json"), report); err != nil {
			t.Fatal(err)
		}
		break
	}
	resultPath := filepath.Join(dir, "score.json")
	if err := scoreBlind([]string{"--dir", blind, "--reports", reports, "--out", resultPath}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Complete bool `json:"complete"`
		Scored   int  `json:"scored_pairs"`
		Single   int  `json:"single_pass"`
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Scored != 0 || result.Single != 1 {
		t.Fatalf("one pass counted as a completed pair: %+v", result)
	}
	for name := range key.Batches {
		if name[0] != '2' {
			continue
		}
		var batch evaluation.BlindBatch
		if err := jsonfile.Read(filepath.Join(blind, "batches", name+".json"), &batch); err != nil {
			t.Fatal(err)
		}
		report := evaluation.BlindReport{BatchHash: batch.Hash}
		for _, item := range batch.Items {
			report.Items = append(report.Items, evaluation.BlindVerdict{Token: item.Token, Winner: key.Items[item.Token].Candidate})
		}
		if err := jsonfile.Create(filepath.Join(reports, name+".json"), report); err != nil {
			t.Fatal(err)
		}
	}
	completePath := filepath.Join(dir, "complete.json")
	if err := scoreBlind([]string{"--dir", blind, "--reports", reports, "--out", completePath}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(completePath)
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		Complete bool     `json:"complete"`
		Scored   int      `json:"scored_pairs"`
		Rate     *float64 `json:"candidate_score_rate"`
	}
	if err := json.Unmarshal(data, &full); err != nil {
		t.Fatal(err)
	}
	if !full.Complete || full.Scored != 1 || full.Rate == nil || *full.Rate != 1 {
		t.Fatalf("candidate wins not counted correctly: %+v", full)
	}
}

func TestCompareJobsRejectsChangedSource(t *testing.T) {
	baseline := &store.Store{Segments: []document.Segment{{ID: "1", Group: "a", Src: "原文"}}}
	candidate := &store.Store{Segments: []document.Segment{{ID: "1", Group: "b", Src: "原文"}}}
	if _, err := evaluation.Compare(baseline, candidate); err == nil {
		t.Fatal("mismatched inputs were accepted")
	}
}
