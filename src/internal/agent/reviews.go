package agent

import (
	"encoding/json"
	"fmt"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/quality"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
	"reflect"
)

type ReviewFiles struct {
	Input  string `json:"input"`
	Report string `json:"report"`
}

type ReviewNote struct {
	Kind     string                `json:"kind"`
	Verdict  string                `json:"verdict"`
	Issues   []quality.ReviewIssue `json:"issues,omitempty"`
	Baseline string                `json:"baseline"`
	Stale    bool                  `json:"stale"`
}

func loadReviews(job *store.Store, files []ReviewFiles, entries []rules.Entry, cards json.RawMessage) (map[string][]ReviewNote, string, error) {
	notes := map[string][]ReviewNote{}
	if len(files) == 0 {
		return notes, "", nil
	}
	var digest []any
	for _, pair := range files {
		material, err := quality.ReadReviewMaterial(pair.Input)
		if err != nil {
			return nil, "", err
		}
		report, err := quality.ReadReviewReport(pair.Report)
		if err != nil {
			return nil, "", err
		}
		if err := quality.ValidateHistoricalReview(job, material, report); err != nil {
			return nil, "", fmt.Errorf("审查报告无效: %w", err)
		}
		if !reflect.DeepEqual(material.Glossary, entries) && !(len(material.Glossary) == 0 && len(entries) == 0) {
			return nil, "", fmt.Errorf("审查报告使用的术语表与任务不一致")
		}
		if len(material.Cards) > 0 {
			var used, supplied quality.CharacterCards
			if json.Unmarshal(material.Cards, &used) != nil || json.Unmarshal(cards, &supplied) != nil || used.MaterialHash == "" || used.MaterialHash != supplied.MaterialHash || used.SourceHash != supplied.SourceHash || !reflect.DeepEqual(used.Groups, supplied.Groups) || !reflect.DeepEqual(used.Characters, supplied.Characters) {
				return nil, "", fmt.Errorf("审查报告使用的角色卡与任务不一致")
			}
		}
		verdicts := map[string]quality.ReviewVerdict{}
		for _, item := range report.Items {
			verdicts[item.ID] = item
		}
		for _, batch := range material.Batches {
			_, currentContext := quality.ReviewContext(job, batch.Targets, nil)
			contextStale := len(batch.ContextResults) != len(currentContext)
			if !contextStale {
				for i, original := range batch.ContextResults {
					if original.ID != currentContext[i].ID || original.Dst != currentContext[i].Dst {
						contextStale = true
						break
					}
				}
			}
			for _, target := range batch.Targets {
				item := verdicts[target.ID]
				result := job.Done[target.ID]
				notes[target.ID] = append(notes[target.ID], ReviewNote{Kind: material.Kind, Verdict: item.Verdict, Issues: item.Issues, Baseline: target.Dst, Stale: contextStale || result.Dst != target.Dst || result.NeedsReview})
			}
		}
		digest = append(digest, material, report)
	}
	reviewHash, err := jsonfile.Digest(digest)
	return notes, reviewHash, err
}
