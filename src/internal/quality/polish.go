package quality

import (
	"fmt"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
	"reflect"
	"strings"
)

type PolishMaterial struct {
	Version     int            `json:"version"`
	Mode        string         `json:"mode"`
	Instruction string         `json:"instruction"`
	Review      ReviewMaterial `json:"review_material"`
	Report      *ReviewReport  `json:"review_report,omitempty"`
	Additional  []PolishReview `json:"additional_reviews,omitempty"`
	Hash        string         `json:"hash"`
}

type PolishReview struct {
	Material ReviewMaterial `json:"material"`
	Report   ReviewReport   `json:"report"`
}

type PolishItem struct {
	ID                 string `json:"id"`
	ExpectedResultHash string `json:"expected_result_hash"`
	Dst                string `json:"dst"`
	Reason             string `json:"reason"`
}

type PolishResponse struct {
	MaterialHash string       `json:"material_hash"`
	Actor        string       `json:"actor"`
	Items        []PolishItem `json:"items"`
}

func PolishDigest(material PolishMaterial) (string, error) {
	material.Hash = ""
	return jsonfile.Digest(material)
}

func ReadPolishMaterial(path string) (PolishMaterial, error) {
	var material PolishMaterial
	if err := jsonfile.Read(path, &material); err != nil {
		return material, fmt.Errorf("读取润色材料: %w", err)
	}
	digest, err := PolishDigest(material)
	if err != nil {
		return material, err
	}
	if material.Version != 1 || (material.Mode != "fix" && material.Mode != "rewrite") || material.Hash == "" || material.Hash != digest {
		return material, fmt.Errorf("润色材料无效或内容已更改")
	}
	return material, nil
}

func ReadPolishResponse(path string) (PolishResponse, error) {
	var response PolishResponse
	if err := jsonfile.Read(path, &response); err != nil {
		return response, fmt.Errorf("读取润色结果: %w", err)
	}
	if strings.TrimSpace(response.Actor) == "" || strings.ContainsAny(response.Actor, "\r\n") || strings.Contains(response.Actor, " / ") || len(response.Items) == 0 {
		return response, fmt.Errorf("润色结果须填写执行者和条目")
	}
	return response, nil
}

func PolishTargets(job *store.Store, material PolishMaterial) (map[string]ReviewTarget, error) {
	reviewHash, err := ReviewMaterialHash(material.Review)
	if err != nil {
		return nil, err
	}
	if material.Review.Version != 1 || (material.Review.Kind != "fidelity" && material.Review.Kind != "subject") || material.Review.Hash == "" || material.Review.Hash != reviewHash || material.Review.CandidateHash != "" {
		return nil, fmt.Errorf("润色底稿材料无效")
	}
	if err := ValidateReviewCards(job, material.Review); err != nil {
		return nil, err
	}
	if material.Report != nil {
		if err := ValidateReview(job, material.Review, *material.Report); err != nil {
			return nil, fmt.Errorf("初翻检查报告无效: %w", err)
		}
	}
	targets := map[string]ReviewTarget{}
	segments := make(map[string]document.Segment, len(job.Segments))
	for _, segment := range job.Segments {
		segments[segment.ID] = segment
	}
	for _, batch := range material.Review.Batches {
		if len(batch.Targets) == 0 {
			return nil, fmt.Errorf("润色材料存在空批次")
		}
		context, results := ReviewContext(job, batch.Targets, nil)
		if !reflect.DeepEqual(batch.Context, context) || !reflect.DeepEqual(batch.ContextResults, results) {
			return nil, fmt.Errorf("润色材料上下文已变化，请重新准备")
		}
		for _, target := range batch.Targets {
			segment, found := segments[target.ID]
			result, done := job.Done[target.ID]
			if _, duplicate := targets[target.ID]; duplicate || !found || !done || result.NeedsReview || result.Passthrough || segment.Src != target.Src || segment.Group != target.Group || target.Group != batch.Targets[0].Group || result.Dst != target.Dst || document.ResultHash(result) != target.ExpectedResultHash {
				return nil, fmt.Errorf("润色目标 %q 不存在、重复或译文已变化", target.ID)
			}
			targets[target.ID] = target
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("润色材料没有可用目标")
	}
	for _, additional := range material.Additional {
		sameCards, err := sameJSON(additional.Material.Cards, material.Review.Cards)
		if err != nil {
			return nil, err
		}
		if additional.Material.CandidateHash != "" || !reflect.DeepEqual(additional.Material.Glossary, material.Review.Glossary) || !sameCards {
			return nil, fmt.Errorf("附加检查使用的底稿、术语表或角色卡不一致")
		}
		if err := ValidateReview(job, additional.Material, additional.Report); err != nil {
			return nil, fmt.Errorf("附加检查无效: %w", err)
		}
		count := 0
		for _, batch := range additional.Material.Batches {
			for _, target := range batch.Targets {
				if old, ok := targets[target.ID]; !ok || old != target {
					return nil, fmt.Errorf("附加检查的目标与润色底稿不一致")
				}
				count++
			}
		}
		if count != len(targets) {
			return nil, fmt.Errorf("附加检查未覆盖全部润色目标")
		}
	}
	return targets, nil
}

func ValidatePolish(job *store.Store, material PolishMaterial, response PolishResponse) ([]translate.ManualEdit, []ReviewCandidate, error) {
	if response.MaterialHash != material.Hash {
		return nil, nil, fmt.Errorf("润色结果不属于当前材料")
	}
	targets, err := PolishTargets(job, material)
	if err != nil {
		return nil, nil, err
	}
	if len(response.Items) != len(targets) {
		return nil, nil, fmt.Errorf("润色结果未覆盖所有目标")
	}
	return ValidatePolishItems(job, material, targets, response.Items, response.Actor)
}

func ValidatePolishItems(job *store.Store, material PolishMaterial, targets map[string]ReviewTarget, items []PolishItem, actor string) ([]translate.ManualEdit, []ReviewCandidate, error) {
	if strings.TrimSpace(actor) == "" || strings.ContainsAny(actor, "\r\n") || strings.Contains(actor, " / ") || len(items) != len(targets) {
		return nil, nil, fmt.Errorf("润色执行者或目标数量无效")
	}
	termsHashes := translate.TermsHashes(job.Segments, material.Review.Glossary)
	seen := map[string]bool{}
	var edits []translate.ManualEdit
	var candidates []ReviewCandidate
	for _, item := range items {
		target, ok := targets[item.ID]
		if !ok || seen[item.ID] || item.ExpectedResultHash != target.ExpectedResultHash || strings.TrimSpace(item.Dst) == "" {
			return nil, nil, fmt.Errorf("润色结果 %q 无效、重复或版本不匹配", item.ID)
		}
		seen[item.ID] = true
		if item.Dst != target.Dst && strings.TrimSpace(item.Reason) == "" {
			return nil, nil, fmt.Errorf("润色结果 %q 缺少改动理由", item.ID)
		}
		if job.Done[item.ID].TermsHash != termsHashes[target.Group] {
			return nil, nil, fmt.Errorf("%s 的术语表与底稿不一致", item.ID)
		}
		blocking, _ := rules.Check(target.Src, item.Dst, material.Review.Glossary)
		if len(blocking) != 0 {
			return nil, nil, fmt.Errorf("%s 的润色未通过检查: %v", item.ID, blocking)
		}
		edits = append(edits, translate.ManualEdit{ID: item.ID, ExpectedHash: item.ExpectedResultHash, Dst: item.Dst, Method: "polish-" + material.Mode, Actor: actor, Reason: item.Reason})
		candidates = append(candidates, ReviewCandidate{ID: item.ID, ExpectedResultHash: item.ExpectedResultHash, Dst: item.Dst})
	}
	return edits, candidates, nil
}
