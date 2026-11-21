package translate

import (
	"errors"
	"fmt"
	"strings"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
)

type ManualEdit struct {
	ID               string                    `json:"id"`
	ExpectedHash     string                    `json:"expected_result_hash"`
	Dst              string                    `json:"dst"`
	Method           string                    `json:"-"`
	Actor            string                    `json:"-"`
	Reason           string                    `json:"-"`
	Stage            string                    `json:"-"`
	HistoryHash      string                    `json:"-"`
	Completion       *document.StageCompletion `json:"-"`
	PolishCompletion *document.StageCompletion `json:"-"`
}

func ApplyEdits(jobPath string, edits []ManualEdit, entries []rules.Entry, validate func(*store.Store) error) (resultErr error) {
	if err := validateEdits(edits); err != nil {
		return err
	}
	job, err := store.OpenExisting(jobPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := job.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("关闭作业失败: %w", closeErr))
		}
	}()
	return ApplyEditsLocked(job, edits, entries, validate)
}

func validateEdits(edits []ManualEdit) error {
	seen := make(map[string]bool, len(edits))
	for _, item := range edits {
		if item.ID == "" || item.ExpectedHash == "" || strings.TrimSpace(item.Dst) == "" || seen[item.ID] || item.Stage != "" && item.Stage != "polish" && item.Stage != "proofread" || item.Stage != "" && item.Completion == nil {
			return fmt.Errorf("修订条目无效或 ID 重复")
		}
		seen[item.ID] = true
	}
	return nil
}

// ApplyEditsLocked requires a writable Store held by the caller.
func ApplyEditsLocked(job *store.Store, edits []ManualEdit, entries []rules.Entry, validate func(*store.Store) error) (resultErr error) {
	if err := validateEdits(edits); err != nil {
		return err
	}
	seen := make(map[string]bool, len(edits))
	agentBatch := true
	for _, item := range edits {
		seen[item.ID] = true
		agentBatch = agentBatch && item.Stage != ""
	}
	mutationStarted := false
	defer func() {
		if mutationStarted {
			resultErr = errors.Join(resultErr, job.RefreshPolishReviews())
		}
	}()
	if validate != nil {
		if err := validate(job); err != nil {
			return err
		}
	}
	// Reconcile interrupted writes before accepting a version-bound external edit.
	if err := job.RefreshPolishReviews(); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(job); err != nil {
			return err
		}
	}
	termsHashes := TermsHashes(job.Segments, entries)
	initialModels, err := job.InitialModels()
	if err != nil {
		return err
	}
	polishActors, err := job.PolishActorsByID()
	if err != nil {
		return err
	}
	groups := make(map[string]string, len(job.Segments))
	segmentsByID := make(map[string]document.Segment, len(job.Segments))
	for _, segment := range job.Segments {
		groups[segment.ID] = segment.Group
		segmentsByID[segment.ID] = segment
	}
	var records []document.Result
	changed := map[string]bool{}
	for _, item := range edits {
		old, ok := job.Done[item.ID]
		if !ok {
			return fmt.Errorf("%s 没有可修订的已完成译文", item.ID)
		}
		if document.ResultHash(old) != item.ExpectedHash {
			return fmt.Errorf("%s 的译文已改变，请重新导出后修订", item.ID)
		}
		if old.ContextReview {
			segment := segmentsByID[item.ID]
			if old.ReviewHistoryHash == "" || old.ReviewHistoryHash != HistoryDigest(job, segment) {
				return fmt.Errorf("%s 的参考上文已改变，请先运行翻译更新复核状态", item.ID)
			}
			for _, previous := range job.Segments {
				if previous.ID == item.ID {
					break
				}
				if previous.Group != segment.Group || !rules.Translatable(previous.Src) {
					continue
				}
				result, ok := job.Done[previous.ID]
				if !ok || result.NeedsReview && !(agentBatch && seen[previous.ID]) {
					return fmt.Errorf("%s 的参考上文尚未完成或复核", item.ID)
				}
			}
		}
		termsHash := termsHashes[groups[item.ID]]
		expectedTerms := old.TermsHash
		if old.NeedsReview {
			expectedTerms = old.ReviewTermsHash
		}
		if expectedTerms == "" || termsHash != expectedTerms {
			return fmt.Errorf("%s 的术语表与翻译或待复核规则不一致；请提供相同术语表，必要时先运行翻译同步规则", item.ID)
		}
		blocking, warnings := rules.Check(old.Src, item.Dst, entries)
		if len(blocking) != 0 {
			return fmt.Errorf("%s 的修订未通过检查: %v", item.ID, blocking)
		}
		reviewedSignature := old.Signature
		historyHash := old.HistoryHash
		if old.NeedsReview && old.ReviewSignature != "" {
			reviewedSignature = old.ReviewSignature
		}
		if old.ContextReview {
			historyHash = old.ReviewHistoryHash
		}
		origin := "manual"
		if item.Method != "" {
			origin = "polish"
		}
		if item.Stage == "proofread" {
			origin = "proofread"
		}
		actors := append([]string(nil), polishActors[item.ID]...)
		if item.Actor != "" && origin == "polish" {
			found := false
			for _, actor := range actors {
				if actor == item.Actor {
					found = true
				}
			}
			if !found {
				actors = append(actors, item.Actor)
			}
		}
		proofreadActors := append([]string(nil), old.ProofreadActors...)
		if origin == "proofread" && item.Actor != "" && !containsActor(proofreadActors, item.Actor) {
			proofreadActors = append(proofreadActors, item.Actor)
		}
		stage := item.Stage
		if stage == "" {
			switch {
			case origin == "polish":
				stage = "polish"
			case old.ProofreadCompletion != nil:
				stage = "proofread"
			case old.PolishCompletion != nil || old.Origin == "polish":
				stage = "polish"
			default:
				stage = "translate"
			}
		}
		record := document.Result{ID: old.ID, Stage: stage, Src: old.Src, Dst: item.Dst, Warnings: warnings, Status: "done", Signature: reviewedSignature, TermsHash: termsHash, Origin: origin, InitialModel: initialModels[item.ID], PreviousHash: item.ExpectedHash, HistoryUsed: old.HistoryUsed, HistoryHash: historyHash, ContextRevision: old.ContextRevision, EditMethod: item.Method, EditActor: item.Actor, EditReason: item.Reason, PolishActors: actors, ProofreadActors: proofreadActors, PolishContextHash: old.PolishContextHash}
		if item.Stage == "polish" {
			record.PolishCompletion = item.Completion
		} else if item.Stage == "proofread" {
			record.PolishCompletion = item.PolishCompletion
			record.ProofreadCompletion = item.Completion
		}
		records = append(records, record)
		if item.Dst != old.Dst || old.NeedsReview {
			changed[item.ID] = true
		}
	}
	changedGroups := map[string]bool{}
	for _, segment := range job.Segments {
		if !agentBatch && changedGroups[segment.Group] && seen[segment.ID] && job.Done[segment.ID].HistoryUsed {
			return fmt.Errorf("同批修订包含变化的前文和依赖该前文的后文 %s，请先提交前文再复核后文", segment.ID)
		}
		if changed[segment.ID] {
			changedGroups[segment.Group] = true
		}
	}
	mutationStarted = true
	agentEdit := false
	for _, item := range edits {
		if item.Stage != "" {
			agentEdit = true
			break
		}
	}
	dirtyGroups := map[string]bool{}
	for _, segment := range job.Segments {
		if dirtyGroups[segment.Group] {
			old, ok := job.Done[segment.ID]
			if ok && old.HistoryUsed {
				if old.HasProtectedOrigin() || agentEdit {
					if !old.NeedsReview {
						old.ReviewSignature, old.ReviewTermsHash = old.Signature, old.TermsHash
					}
					old.NeedsReview = true
					old.ContextReview = true
					old.ContextRevision++
					old.ReviewHistoryHash = HistoryDigest(job, segment)
					if err := job.Append(old); err != nil {
						return fmt.Errorf("保存修订前标记后续人工译文失败: %w", err)
					}
				} else {
					pending := document.Result{ID: old.ID, Src: old.Src, Status: "pending", Signature: old.Signature, TermsHash: old.TermsHash}
					if err := job.Append(pending); err != nil {
						return fmt.Errorf("保存修订前标记后续译文失败: %w", err)
					}
				}
			}
		}
		if changed[segment.ID] {
			dirtyGroups[segment.Group] = true
		}
	}
	shadow := *job
	shadow.Done = make(map[string]document.Result, len(job.Done))
	for id, result := range job.Done {
		shadow.Done[id] = result
	}
	for _, record := range records {
		shadow.Done[record.ID] = record
	}
	if agentBatch {
		for i := range records {
			if records[i].HistoryUsed {
				records[i].HistoryHash = edits[i].HistoryHash
				if records[i].HistoryHash == "" {
					records[i].HistoryHash = HistoryDigest(&shadow, segmentsByID[records[i].ID])
				}
				shadow.Done[records[i].ID] = records[i]
			}
		}
	}
	polishDigests := shadow.PolishContextDigests()
	for i, record := range records {
		if record.PolishCompletion != nil && (record.Origin == "polish" || record.Origin == "proofread") {
			record.PolishContextHash = ""
		} else if record.Origin == "polish" || record.PolishContextHash != "" || len(record.PolishActors) > 0 {
			record.PolishContextHash = polishDigests[groups[record.ID]]
		}
		records[i] = record
	}
	if agentBatch {
		if err := job.AppendBatch(records); err != nil {
			return fmt.Errorf("保存批次修订失败，重新打开作业后恢复: %w", err)
		}
	} else {
		for i, record := range records {
			if err := job.Append(record); err != nil {
				return fmt.Errorf("已写入 %d 条修订，剩余未完成: %w", i, err)
			}
		}
	}
	if err := RefreshContextReviews(job); err != nil {
		return fmt.Errorf("修订已保存，但刷新后续复核状态失败: %w", err)
	}
	if err := job.RefreshPolishReviews(); err != nil {
		return err
	}
	return nil
}

func RefreshContextReviews(job *store.Store) error {
	for _, segment := range job.Segments {
		result, ok := job.Done[segment.ID]
		if !ok || !result.ContextReview {
			continue
		}
		historyHash := HistoryDigest(job, segment)
		if historyHash != result.ReviewHistoryHash {
			result.ReviewHistoryHash = historyHash
			result.ContextRevision++
			if err := job.Append(result); err != nil {
				return err
			}
		}
	}
	return nil
}

func containsActor(actors []string, actor string) bool {
	for _, existing := range actors {
		if existing == actor {
			return true
		}
	}
	return false
}
