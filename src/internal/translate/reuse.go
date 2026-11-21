package translate

import (
	"errors"
	"fmt"
	"regexp"
	"slices"

	"localization-kit/src/internal/config"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
)

var creditLineCount = regexp.MustCompile(`\([0-9]+ 行\)`)

// SeedPrevious copies compatible completed results into a separate new job.
func SeedPrevious(previous, current *store.Store, selected config.Model, entries []rules.Entry) (count int, resultErr error) {
	mutated := false
	defer func() {
		if mutated {
			resultErr = errors.Join(resultErr, current.RefreshPolishReviews())
		}
	}()
	initialModels, err := previous.InitialModels()
	if err != nil {
		return 0, err
	}
	polishActors, err := previous.PolishActorsByID()
	if err != nil {
		return 0, err
	}
	oldSegments := make(map[string]document.Segment, len(previous.Segments))
	contexts := groupContexts(current.Segments, entries)
	for _, segment := range previous.Segments {
		oldSegments[segment.ID] = segment
	}
	for _, segment := range current.Segments {
		old, exists := oldSegments[segment.ID]
		previousResult, done := previous.Done[segment.ID]
		if !exists || old.Src != segment.Src || !done || !previousResult.HasProtectedOrigin() {
			continue
		}
		if result, ok := current.Done[segment.ID]; ok && !result.HasProtectedOrigin() {
			return 0, fmt.Errorf("原作品中受保护的译文 %s 与当前机器译文冲突，请使用新作品目录", segment.ID)
		}
		if _, ok := current.Failed[segment.ID]; ok {
			return 0, fmt.Errorf("原作品中受保护的译文 %s 与当前失败记录冲突，请使用新作品目录", segment.ID)
		}
	}
	shadow := *current
	shadow.Done = make(map[string]document.Result, len(current.Done))
	for id, result := range current.Done {
		shadow.Done[id] = result
	}
	var candidates []document.Result
	for _, segment := range current.Segments {
		if _, ok := current.Done[segment.ID]; ok {
			continue
		}
		if _, ok := current.Failed[segment.ID]; ok {
			continue
		}
		old, ok := oldSegments[segment.ID]
		if !ok || old.Src != segment.Src {
			continue
		}
		result, ok := previous.Done[segment.ID]
		if ok && len(polishActors[segment.ID]) > 0 {
			result.PolishActors = polishActors[segment.ID]
		}
		if ok && result.IsRevision() && result.InitialModel == "" {
			result.InitialModel = initialModels[result.ID]
		}
		sig := signature(segment, selected, contexts[segment.Group])
		if ok && result.HasProtectedOrigin() && result.Signature != sig {
			result.NeedsReview = true
			result.ReviewSignature = sig
			result.ReviewTermsHash = contexts[segment.Group].Terms
		}
		if !ok || !result.HasProtectedOrigin() && result.Signature != sig {
			continue
		}
		if !result.HasProtectedOrigin() && !result.Passthrough && (result.Model != selected.Model || result.Profile != selected.Profile) {
			continue
		}
		if result.HistoryUsed {
			historyHash := HistoryDigest(&shadow, segment)
			if result.HistoryHash == "" || result.HistoryHash != historyHash || result.ContextReview && result.ReviewHistoryHash != historyHash {
				if !result.HasProtectedOrigin() {
					continue
				}
				result.NeedsReview = true
				result.ContextReview = true
				result.ContextRevision++
				result.ReviewSignature = sig
				result.ReviewTermsHash = contexts[segment.Group].Terms
				result.ReviewHistoryHash = historyHash
			}
		}
		candidates = append(candidates, result)
		shadow.Done[result.ID] = result
	}
	for _, candidate := range candidates {
		position, _ := current.SegmentIndex(candidate.ID)
		for _, segment := range current.Segments[position+1:] {
			if segment.Group != current.Segments[position].Group {
				break
			}
			if existing, ok := current.Done[segment.ID]; ok && existing.HistoryUsed {
				return 0, fmt.Errorf("cannot seed %s before existing history-dependent result %s; use a new job directory", candidate.ID, segment.ID)
			}
		}
	}
	if len(candidates) > 0 {
		oldCredits, err := previous.CreditLines()
		if err != nil {
			return 0, err
		}
		newCredits, err := current.CreditLines()
		if err != nil {
			return 0, err
		}
		if oldCredits != nil {
			counted := false
			for _, line := range oldCredits {
				if creditLineCount.MatchString(line) {
					counted = true
					break
				}
			}
			if counted {
				same := len(previous.Segments) == len(current.Segments)
				if same {
					for i, old := range previous.Segments {
						now := current.Segments[i]
						if old.ID != now.ID || old.Src != now.Src || old.Group != now.Group {
							same = false
							break
						}
					}
				}
				covered := make(map[string]bool, len(candidates))
				for _, result := range candidates {
					covered[result.ID] = true
				}
				for id, prior := range previous.Done {
					if now, ok := current.Done[id]; ok && (now.HasProtectedOrigin() || document.ResultHash(now) == document.ResultHash(prior)) {
						covered[id] = true
					}
					if !covered[id] {
						same = false
						break
					}
				}
				if !same {
					return 0, fmt.Errorf("旧作业署名包含行数，部分复用无法可靠保留署名，请先人工核对")
				}
			}
			if newCredits == nil {
				if err := current.SetInitialCreditLines(oldCredits); err != nil {
					return 0, err
				}
			} else if !slices.Equal(oldCredits, newCredits) {
				return 0, fmt.Errorf("旧作业与当前作业的译制署名不一致，请使用新作业目录")
			}
		}
	}
	if len(candidates) > 0 {
		ids := make(map[string]bool, len(candidates))
		for _, candidate := range candidates {
			ids[candidate.ID] = true
		}
		history, err := previous.HistoryFor(ids)
		if err != nil {
			return 0, err
		}
		mutated = true
		if err := current.AppendBatch(append(history, candidates...)); err != nil {
			return 0, err
		}
	}
	return len(candidates), nil
}
