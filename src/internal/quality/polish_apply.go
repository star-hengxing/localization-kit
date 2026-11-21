package quality

import (
	"encoding/json"
	"fmt"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
	"path/filepath"
	"reflect"
)

type ApplyPolishOptions struct {
	ResourceRoot, JobPath, Input, Output, ReviewPath, ReportPath string
	Apply                                                        bool
	Validate                                                     func(*store.Store, ReviewMaterial) error
}

func ValidateCurrentMaterials(material ReviewMaterial, entries []rules.Entry, cardsPath string, segments []document.Segment) error {
	if !reflect.DeepEqual(material.Glossary, entries) && !(len(material.Glossary) == 0 && len(entries) == 0) {
		return fmt.Errorf("作品术语表与润色材料不同")
	}
	if len(material.Cards) == 0 && cardsPath == "" {
		return nil
	}
	if len(material.Cards) == 0 || cardsPath == "" {
		return fmt.Errorf("作品角色卡与润色材料不同")
	}
	var used CharacterCards
	if err := json.Unmarshal(material.Cards, &used); err != nil {
		return err
	}
	current, err := LoadCharacterCards(cardsPath, segments)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(used, current) {
		return fmt.Errorf("作品角色卡与润色材料不同")
	}
	return nil
}

func ProcessPolish(options ApplyPolishOptions) (report map[string]any, resultErr error) {
	if options.JobPath == "" || options.Input == "" || options.Output == "" || options.Apply && (options.ReviewPath == "" || options.ReportPath == "") {
		return nil, fmt.Errorf("invalid polish result options")
	}
	jobPath, input, output, reviewPath, reportPath := &options.JobPath, &options.Input, &options.Output, &options.ReviewPath, &options.ReportPath
	apply := options.Apply
	run := func() error {

		if err := rules.LoadSimplified(filepath.Join(options.ResourceRoot, rules.DefaultDataPath)); err != nil {
			return err
		}
		material, err := ReadPolishMaterial(*input)
		if err != nil {
			return err
		}
		response, err := ReadPolishResponse(*output)
		if err != nil {
			return err
		}
		if !apply {
			job, err := store.OpenReadOnly(*jobPath)
			if err != nil {
				return err
			}
			defer job.Close()
			_, _, err = ValidatePolish(job, material, response)
			if err != nil {
				return err
			}
			report = map[string]any{"valid": true, "candidates": len(response.Items), "post_review_required": true}
			return nil
		}
		postMaterial, err := ReadReviewMaterial(*reviewPath)
		if err != nil {
			return err
		}
		postReport, err := ReadReviewReport(*reportPath)
		if err != nil {
			return err
		}
		if postMaterial.Kind != "fidelity" {
			return fmt.Errorf("润色候选需要忠实度审查")
		}
		sameCards, err := sameJSON(postMaterial.Cards, material.Review.Cards)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(postMaterial.Glossary, material.Review.Glossary) || !sameCards {
			return fmt.Errorf("润色候选审查使用的术语表或角色卡与润色材料不同")
		}
		original := map[string]string{}
		for _, batch := range material.Review.Batches {
			for _, target := range batch.Targets {
				original[target.ID] = target.Dst
			}
		}
		var edits []translate.ManualEdit
		for _, item := range response.Items {
			if item.Dst != original[item.ID] {
				edits = append(edits, translate.ManualEdit{ID: item.ID, ExpectedHash: item.ExpectedResultHash, Dst: item.Dst, Method: "polish-" + material.Mode, Actor: response.Actor, Reason: item.Reason})
			}
		}
		err = translate.ApplyEdits(*jobPath, edits, material.Review.Glossary, func(job *store.Store) error {
			if options.Validate != nil {
				if err := options.Validate(job, material.Review); err != nil {
					return err
				}
			}
			_, candidates, err := ValidatePolish(job, material, response)
			if err != nil {
				return err
			}
			candidateHash, err := ReviewCandidateHash(candidates)
			if err != nil {
				return err
			}
			if postMaterial.CandidateHash == "" || postMaterial.CandidateHash != candidateHash {
				return fmt.Errorf("审查材料与润色候选不一致")
			}
			if err := ValidateReview(job, postMaterial, postReport); err != nil {
				return err
			}
			for _, item := range postReport.Items {
				if item.Verdict != "pass" {
					return fmt.Errorf("润色候选审查 %s 尚未通过", item.ID)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		report = map[string]any{"edited": len(edits), "unchanged": len(response.Items) - len(edits)}
		return nil
	}
	resultErr = run()
	return report, resultErr
}
