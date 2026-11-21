package quality

import (
	"fmt"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
)

type CharacterWarning struct {
	ID         string `json:"id"`
	Group      string `json:"group"`
	ResultHash string `json:"result_hash"`
	Code       string `json:"code"`
	Detail     string `json:"detail"`
	Src        string `json:"src"`
	Dst        string `json:"dst"`
}

func CharacterWarnings(jobPathValue, cardsPathValue, narrationPathValue string) (summary map[string]any, resultErr error) {
	if jobPathValue == "" || cardsPathValue == "" {
		return nil, fmt.Errorf("missing character warning inputs")
	}
	jobPath, cardsPath, narrationPath := &jobPathValue, &cardsPathValue, &narrationPathValue
	run := func() error {

		job, err := store.OpenReadOnly(*jobPath)
		if err != nil {
			return err
		}
		defer job.Close()
		cards, err := LoadCharacterCards(*cardsPath, job.Segments)
		if err != nil {
			return err
		}
		groups := map[string]bool{}
		for _, segment := range job.Segments {
			groups[segment.Group] = true
		}
		narration := map[string]string{}
		if *narrationPath != "" {
			if err := jsonfile.Read(*narrationPath, &narration); err != nil {
				return err
			}
			if narration == nil {
				return fmt.Errorf("narration must be a JSON object")
			}
			for group, person := range narration {
				if !groups[group] || person != "third" && person != "first" && person != "unknown" {
					return fmt.Errorf("unknown group or narration value for %q", group)
				}
			}
		}
		selected := map[string]bool{}
		for _, group := range cards.Groups {
			if !groups[group] {
				return fmt.Errorf("character material selects unknown group %q", group)
			}
			selected[group] = true
		}
		warnings := []CharacterWarning{}
		checked := 0
		for _, segment := range job.Segments {
			result, ok := job.Done[segment.ID]
			if !ok || result.NeedsReview || result.Passthrough || !selected[segment.Group] {
				continue
			}
			checked++
			add := func(code, detail string) {
				warnings = append(warnings, CharacterWarning{ID: segment.ID, Group: segment.Group, ResultHash: document.ResultHash(result), Code: code, Detail: detail, Src: segment.Src, Dst: result.Dst})
			}
			if detail := NamedCharacterWarning(segment.Src, result.Dst, cards.Characters); detail != "" {
				add("pronoun", detail)
			}
			if narration[segment.Group] == "third" && NarrationPersonWarning(segment.Src, result.Dst) {
				add("narration", "Third-person narration contains 我 without a recognized source first-person expression; verify the speaker and subject against the source.")
			}
		}
		summary = map[string]any{"checked": checked, "warnings": warnings, "scope": "Conservative heuristics only; no warnings does not establish semantic correctness."}
		return nil
	}
	resultErr = run()
	return summary, resultErr
}
