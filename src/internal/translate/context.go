package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"localization-kit/src/internal/config"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/model"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
)

type groupContext struct{ Content, Terms string }

func groupContexts(segments []document.Segment, entries []rules.Entry) map[string]groupContext {
	groups := make(map[string][]struct{ ID, Src string })
	for _, segment := range segments {
		groups[segment.Group] = append(groups[segment.Group], struct{ ID, Src string }{segment.ID, segment.Src})
	}
	result := make(map[string]groupContext, len(groups))
	for group, values := range groups {
		data, _ := json.Marshal(values)
		hash := sha256.Sum256(data)
		var source []string
		for _, item := range values {
			source = append(source, item.Src)
		}
		joined := strings.Join(source, "\n")
		var relevant []rules.Entry
		for _, entry := range entries {
			forms := append([]string{entry.Src}, entry.Variants...)
			for _, form := range forms {
				if form != "" && strings.Contains(joined, form) {
					relevant = append(relevant, entry)
					break
				}
			}
		}
		termData, _ := json.Marshal(relevant)
		termHash := sha256.Sum256(termData)
		result[group] = groupContext{Content: hex.EncodeToString(hash[:]), Terms: hex.EncodeToString(termHash[:])}
	}
	return result
}

func TermsHashes(segments []document.Segment, entries []rules.Entry) map[string]string {
	result := make(map[string]string)
	for group, context := range groupContexts(segments, entries) {
		result[group] = context.Terms
	}
	return result
}

func signature(segment document.Segment, selected config.Model, context groupContext) string {
	return recordedSignature(segment, selected, context, "")
}

func recordedSignature(segment document.Segment, selected config.Model, context groupContext, legacyRevision string) string {
	value, _ := json.Marshal(struct {
		Src           string       `json:"src"`
		Group         string       `json:"group"`
		Profile       string       `json:"profile"`
		Model         string       `json:"model"`
		URL           string       `json:"url"`
		ModelRevision string       `json:"model_revision"`
		RulesRevision int          `json:"rules_revision"`
		Simplified    string       `json:"simplified"`
		Context       groupContext `json:"context"`
	}{segment.Src, segment.Group, selected.Profile, selected.Model, selected.URL, legacyRevision, 5, rules.SimplifiedDigest(), context})
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// CheckInitialResults verifies initial-stage model, source group and history dependencies.
func CheckInitialResults(job *store.Store, segments []document.Segment, entries []rules.Entry) error {
	ids := make(map[string]bool, len(segments))
	for _, segment := range segments {
		ids[segment.ID] = true
	}
	records, err := job.HistoryFor(ids)
	if err != nil {
		return err
	}
	history := make(map[string]document.Result, len(records))
	for _, record := range records {
		history[document.ResultHash(record)] = record
	}
	contexts := groupContexts(segments, entries)
	for _, segment := range segments {
		result, ok := job.Done[segment.ID]
		if !ok || result.Passthrough {
			continue
		}
		modelResult := result
		seen := map[string]bool{}
		for (modelResult.Model == "" || modelResult.ModelURL == "" || modelResult.Profile == "") && modelResult.PreviousHash != "" && !seen[modelResult.PreviousHash] {
			seen[modelResult.PreviousHash] = true
			previous, ok := history[modelResult.PreviousHash]
			if !ok || previous.ID != result.ID {
				break
			}
			modelResult = previous
		}
		selected := config.Model{URL: modelResult.ModelURL, Model: modelResult.Model, Profile: modelResult.Profile}
		if result.Signature == "" || selected.Model == "" || selected.URL == "" || selected.Profile == "" || result.Signature != recordedSignature(segment, selected, contexts[segment.Group], modelResult.ModelRevision) {
			return fmt.Errorf("初翻条目 %s 的原文、模型或规则依赖已变化，请先重新处理", segment.ID)
		}
		if result.HistoryUsed && (result.HistoryHash == "" || result.HistoryHash != HistoryDigest(job, segment)) {
			return fmt.Errorf("初翻条目 %s 的上文已变化，请先重新处理", segment.ID)
		}
	}
	return nil
}

func historyBefore(job *store.Store, segment document.Segment) []model.Message {
	index, ok := job.SegmentIndex(segment.ID)
	if !ok {
		return nil
	}
	var pairs []document.Result
	for i := index - 1; i >= 0 && len(pairs) < 2; i-- {
		previous := job.Segments[i]
		if previous.Group != segment.Group {
			break
		}
		if result, ok := job.Done[previous.ID]; ok && !result.Passthrough && !result.NeedsReview && result.Dst != "" {
			pairs = append(pairs, result)
		}
	}
	var history []model.Message
	for i := len(pairs) - 1; i >= 0; i-- {
		history = append(history, model.Message{Role: "user", Content: pairs[i].Src}, model.Message{Role: "assistant", Content: pairs[i].Dst})
	}
	return history
}

func HistoryDigest(job *store.Store, segment document.Segment) string {
	value, _ := json.Marshal(historyBefore(job, segment))
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
