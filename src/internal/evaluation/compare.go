package evaluation

import (
	"encoding/json"
	"fmt"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"reflect"
)

type Result struct {
	Status      string           `json:"status"`
	Dst         *string          `json:"dst,omitempty"`
	ResultHash  string           `json:"result_hash,omitempty"`
	Warnings    []document.Issue `json:"warnings,omitempty"`
	Violations  []document.Issue `json:"violations,omitempty"`
	NeedsReview bool             `json:"needs_review,omitempty"`
	Passthrough bool             `json:"passthrough,omitempty"`
	Model       string           `json:"model,omitempty"`
	ModelURL    string           `json:"model_url,omitempty"`
	Revision    string           `json:"revision,omitempty"`
	Profile     string           `json:"profile,omitempty"`
	Finish      string           `json:"finish_reason,omitempty"`
	Usage       json.RawMessage  `json:"usage,omitempty"`
}

type Item struct {
	ID        string `json:"id"`
	Group     string `json:"group"`
	Src       string `json:"src"`
	Baseline  Result `json:"baseline"`
	Candidate Result `json:"candidate"`
	Equal     *bool  `json:"equal,omitempty"`
}

type Comparison struct {
	Version      int     `json:"version"`
	BaselineJob  string  `json:"baseline_job"`
	CandidateJob string  `json:"candidate_job"`
	Baseline     Summary `json:"baseline"`
	Candidate    Summary `json:"candidate"`
	Same         int     `json:"same"`
	Changed      int     `json:"changed"`
	Unpaired     int     `json:"unpaired"`
	Items        []Item  `json:"items"`
}

type Summary struct {
	Total      int            `json:"total"`
	Done       int            `json:"done"`
	Failed     int            `json:"failed"`
	Pending    int            `json:"pending"`
	Review     int            `json:"needs_review"`
	Warnings   map[string]int `json:"warning_counts"`
	Violations map[string]int `json:"violation_counts"`
}

func evaluationResult(job *store.Store, id string) Result {
	if result, ok := job.Done[id]; ok {
		dst := result.Dst
		return Result{Status: "done", Dst: &dst, ResultHash: document.ResultHash(result), Warnings: result.Warnings, NeedsReview: result.NeedsReview, Passthrough: result.Passthrough, Model: result.Model, ModelURL: result.ModelURL, Revision: result.ModelRevision, Profile: result.Profile, Finish: result.Finish, Usage: result.Usage}
	}
	if result, ok := job.Failed[id]; ok {
		return Result{Status: "failed", Violations: result.Violations, Model: result.Model, ModelURL: result.ModelURL, Revision: result.ModelRevision, Profile: result.Profile, Finish: result.Finish, Usage: result.Usage}
	}
	return Result{Status: "pending"}
}

func evaluationSummary(job *store.Store) Summary {
	s := translate.Summarize(job)
	result := Summary{Total: s.Total, Done: s.Done, Failed: s.Failed, Pending: s.Pending, Review: s.Review, Warnings: map[string]int{}, Violations: map[string]int{}}
	for _, entry := range job.Done {
		for _, warning := range entry.Warnings {
			result.Warnings[warning.Code]++
		}
	}
	for _, entry := range job.Failed {
		for _, violation := range entry.Violations {
			result.Violations[violation.Code]++
		}
	}
	return result
}

func Compare(baseline, candidate *store.Store) (Comparison, error) {
	if !reflect.DeepEqual(baseline.Segments, candidate.Segments) {
		return Comparison{}, fmt.Errorf("两个作业的输入条目不同，无法按相同原文比较")
	}
	comparison := Comparison{Version: 1, Baseline: evaluationSummary(baseline), Candidate: evaluationSummary(candidate), Items: make([]Item, 0, len(baseline.Segments))}
	for _, segment := range baseline.Segments {
		item := Item{ID: segment.ID, Group: segment.Group, Src: segment.Src, Baseline: evaluationResult(baseline, segment.ID), Candidate: evaluationResult(candidate, segment.ID)}
		if item.Baseline.Dst != nil && item.Candidate.Dst != nil && !item.Baseline.NeedsReview && !item.Candidate.NeedsReview && !item.Baseline.Passthrough && !item.Candidate.Passthrough {
			equal := *item.Baseline.Dst == *item.Candidate.Dst
			item.Equal = &equal
			if equal {
				comparison.Same++
			} else {
				comparison.Changed++
			}
		} else {
			comparison.Unpaired++
		}
		comparison.Items = append(comparison.Items, item)
	}
	return comparison, nil
}
