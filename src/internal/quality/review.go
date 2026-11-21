package quality

import (
	"bytes"
	"encoding/json"
	"fmt"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
	"os"
	"reflect"
	"sort"
	"strings"
)

type ReviewCandidate struct {
	ID                 string `json:"id"`
	ExpectedResultHash string `json:"expected_result_hash"`
	Dst                string `json:"dst"`
}

type ReviewTarget struct {
	ID                 string `json:"id"`
	Group              string `json:"group"`
	Src                string `json:"src"`
	Dst                string `json:"dst"`
	ExpectedResultHash string `json:"expected_result_hash"`
}

type ReviewBatch struct {
	Targets        []ReviewTarget     `json:"targets"`
	Context        []document.Segment `json:"context"`
	ContextResults []ReviewCandidate  `json:"context_results"`
}

type ReviewMaterial struct {
	Version       int             `json:"version"`
	Kind          string          `json:"kind"`
	Prompt        string          `json:"prompt"`
	Batches       []ReviewBatch   `json:"batches"`
	Glossary      []rules.Entry   `json:"glossary,omitempty"`
	Cards         json.RawMessage `json:"cards,omitempty"`
	CandidateHash string          `json:"candidate_hash,omitempty"`
	Hash          string          `json:"hash"`
}

type ReviewEvidence struct {
	SegmentID string `json:"segment_id"`
	Quote     string `json:"quote"`
}

type ReviewIssue struct {
	Code     string           `json:"code"`
	Detail   string           `json:"detail"`
	Evidence []ReviewEvidence `json:"evidence"`
}

type ReviewVerdict struct {
	ID      string        `json:"id"`
	Verdict string        `json:"verdict"`
	Issues  []ReviewIssue `json:"issues,omitempty"`
}

type ReviewReport struct {
	MaterialHash  string          `json:"material_hash"`
	CandidateHash string          `json:"candidate_hash,omitempty"`
	Items         []ReviewVerdict `json:"items"`
}

func ReviewCandidateHash(items []ReviewCandidate) (string, error) {
	ordered := append([]ReviewCandidate(nil), items...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	return jsonfile.Digest(ordered)
}

func ReviewMaterialHash(material ReviewMaterial) (string, error) {
	material.Hash = ""
	return jsonfile.Digest(material)
}

func ReviewContext(job *store.Store, targets []ReviewTarget, candidates map[string]string) ([]document.Segment, []ReviewCandidate) {
	indexes := map[int]bool{}
	for _, target := range targets {
		index, ok := job.SegmentIndex(target.ID)
		if !ok {
			continue
		}
		for i := index - 3; i <= index+3; i++ {
			if i >= 0 && i < len(job.Segments) && job.Segments[i].Group == target.Group {
				indexes[i] = true
			}
		}
	}
	var context []document.Segment
	var results []ReviewCandidate
	for i, segment := range job.Segments {
		if !indexes[i] {
			continue
		}
		context = append(context, segment)
		if result, ok := job.Done[segment.ID]; ok {
			dst := result.Dst
			if candidate, ok := candidates[segment.ID]; ok {
				dst = candidate
			}
			results = append(results, ReviewCandidate{ID: segment.ID, ExpectedResultHash: document.ResultHash(result), Dst: dst})
		}
	}
	return context, results
}

func ReadReviewMaterial(path string) (ReviewMaterial, error) {
	var value ReviewMaterial
	err := jsonfile.Read(path, &value)
	return value, err
}

func ReadReviewReport(path string) (ReviewReport, error) {
	var value ReviewReport
	err := jsonfile.Read(path, &value)
	return value, err
}

func ReadReviewCandidates(path string) ([]ReviewCandidate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	type candidateInput struct {
		ID                 string `json:"id"`
		ExpectedResultHash string `json:"expected_result_hash"`
		Dst                string `json:"dst"`
		Reason             string `json:"reason,omitempty"`
	}
	var rawItems []candidateInput
	if len(data) > 0 && data[0] == '[' {
		err = jsonfile.Read(path, &rawItems)
	} else {
		var envelope struct {
			MaterialHash string           `json:"material_hash,omitempty"`
			Actor        string           `json:"actor,omitempty"`
			Items        []candidateInput `json:"items"`
		}
		err = jsonfile.Read(path, &envelope)
		rawItems = envelope.Items
	}
	if err != nil {
		return nil, err
	}
	if len(rawItems) == 0 {
		return nil, fmt.Errorf("candidate file is empty")
	}
	items := make([]ReviewCandidate, 0, len(rawItems))
	for _, item := range rawItems {
		items = append(items, ReviewCandidate{ID: item.ID, ExpectedResultHash: item.ExpectedResultHash, Dst: item.Dst})
	}
	return items, nil
}

func ValidateReviewCards(job *store.Store, material ReviewMaterial) error {
	if len(material.Cards) > 0 {
		var cards struct {
			SourceHash string   `json:"source_hash"`
			Groups     []string `json:"groups"`
		}
		if err := json.Unmarshal(material.Cards, &cards); err != nil || cards.SourceHash == "" || len(cards.Groups) == 0 {
			return fmt.Errorf("角色卡快照无效")
		}
		groups := map[string]bool{}
		for _, group := range cards.Groups {
			groups[group] = true
		}
		rows := []TermSource{}
		for _, segment := range job.Segments {
			if groups[segment.Group] {
				rows = append(rows, TermSource{Segment: segment})
			}
		}
		sourceHash, err := jsonfile.Digest(rows)
		if err != nil {
			return err
		}
		if sourceHash != cards.SourceHash {
			return fmt.Errorf("角色卡对应的原文已变化，请重新准备")
		}
	}
	return nil
}

func ValidateReview(job *store.Store, material ReviewMaterial, report ReviewReport) error {
	return validateReview(job, material, report, true)
}

// ValidateHistoricalReview checks a review against the immutable source while
// allowing the translation and its surrounding context to have changed.
func ValidateHistoricalReview(job *store.Store, material ReviewMaterial, report ReviewReport) error {
	if material.CandidateHash != "" {
		return fmt.Errorf("不能将候选译文报告用于初翻润色")
	}
	return validateReview(job, material, report, false)
}

func validateReview(job *store.Store, material ReviewMaterial, report ReviewReport, current bool) error {
	materialHash, err := ReviewMaterialHash(material)
	if err != nil {
		return err
	}
	if material.Version != 1 || (material.Kind != "fidelity" && material.Kind != "subject") || material.Hash == "" || material.Hash != materialHash || report.MaterialHash != material.Hash || report.CandidateHash != material.CandidateHash {
		return fmt.Errorf("review material or report digest mismatch")
	}
	if err := ValidateReviewCards(job, material); err != nil {
		return err
	}
	sources := map[string]document.Segment{}
	for _, segment := range job.Segments {
		sources[segment.ID] = segment
	}
	targets := map[string]ReviewTarget{}
	evidenceSources := map[string]map[string]string{}
	var candidates []ReviewCandidate
	candidateContext := map[string]string{}
	if material.CandidateHash != "" {
		for _, batch := range material.Batches {
			for _, target := range batch.Targets {
				candidateContext[target.ID] = target.Dst
			}
		}
	}
	for _, batch := range material.Batches {
		if len(batch.Targets) == 0 {
			return fmt.Errorf("empty review batch")
		}
		expectedContext, expectedResults := ReviewContext(job, batch.Targets, candidateContext)
		if !reflect.DeepEqual(batch.Context, expectedContext) || current && !reflect.DeepEqual(batch.ContextResults, expectedResults) {
			return fmt.Errorf("review context has changed")
		}
		contextSources := map[string]string{}
		for _, segment := range batch.Context {
			actual, ok := sources[segment.ID]
			if !ok || !reflect.DeepEqual(actual, segment) {
				return fmt.Errorf("source context changed: %s", segment.ID)
			}
			if _, duplicate := contextSources[segment.ID]; duplicate {
				return fmt.Errorf("duplicate source context %s", segment.ID)
			}
			contextSources[segment.ID] = segment.Src
		}
		for _, target := range batch.Targets {
			segment, exists := sources[target.ID]
			result, done := job.Done[target.ID]
			_, duplicate := targets[target.ID]
			if !exists || !done || duplicate || segment.Src != target.Src || segment.Group != target.Group || target.Group != batch.Targets[0].Group || target.ExpectedResultHash == "" || current && document.ResultHash(result) != target.ExpectedResultHash || strings.TrimSpace(target.Dst) == "" {
				return fmt.Errorf("invalid, stale or duplicate review target %s", target.ID)
			}
			if current && material.CandidateHash == "" && target.Dst != result.Dst {
				return fmt.Errorf("translation changed for %s", target.ID)
			}
			contextSources[target.ID] = target.Src
			targets[target.ID], evidenceSources[target.ID] = target, contextSources
			candidates = append(candidates, ReviewCandidate{ID: target.ID, ExpectedResultHash: target.ExpectedResultHash, Dst: target.Dst})
		}
	}
	if len(targets) == 0 || len(report.Items) != len(targets) {
		return fmt.Errorf("review report does not cover every target")
	}
	if material.CandidateHash != "" {
		candidateHash, err := ReviewCandidateHash(candidates)
		if err != nil {
			return err
		}
		if material.CandidateHash != candidateHash {
			return fmt.Errorf("candidate digest mismatch")
		}
	}
	seen := map[string]bool{}
	for _, item := range report.Items {
		if _, ok := targets[item.ID]; !ok || seen[item.ID] {
			return fmt.Errorf("unknown or duplicate review verdict %s", item.ID)
		}
		seen[item.ID] = true
		if item.Verdict != "pass" && item.Verdict != "issues" && item.Verdict != "uncertain" {
			return fmt.Errorf("invalid verdict for %s", item.ID)
		}
		if item.Verdict == "pass" && len(item.Issues) != 0 || item.Verdict != "pass" && len(item.Issues) == 0 {
			return fmt.Errorf("verdict and issues disagree for %s", item.ID)
		}
		for _, issue := range item.Issues {
			if strings.TrimSpace(issue.Code) == "" || strings.TrimSpace(issue.Detail) == "" || len(issue.Evidence) == 0 {
				return fmt.Errorf("issue lacks details or evidence for %s", item.ID)
			}
			for _, evidence := range issue.Evidence {
				source, exists := evidenceSources[item.ID][evidence.SegmentID]
				if !exists || strings.TrimSpace(evidence.Quote) == "" || !strings.Contains(source, evidence.Quote) {
					return fmt.Errorf("invalid verbatim evidence for %s", item.ID)
				}
			}
		}
	}
	return nil
}
