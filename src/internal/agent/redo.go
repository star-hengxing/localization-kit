package agent

import (
	"fmt"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"strings"
	"time"
)

// Redo reopens selected entries while retaining all prior records in the job history.
func Redo(jobPath, stage string, ids []string) (count int, resultErr error) {
	if jobPath == "" || (stage != "translate" && stage != "polish" && stage != "proofread") || len(ids) == 0 {
		return 0, fmt.Errorf("需要作业、阶段 translate|polish|proofread 及至少一个 ID")
	}
	job, err := store.OpenExisting(jobPath)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := job.Close(); resultErr == nil {
			resultErr = closeErr
		}
	}()
	wanted, groups := map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || wanted[id] {
			return 0, fmt.Errorf("ID 为空或重复: %q", id)
		}
		segment, ok := segmentByID(job, id)
		result, done := job.Done[id]
		if !ok || !done || result.Passthrough {
			return 0, fmt.Errorf("ID %q 没有可重做的已完成译文", id)
		}
		wanted[id], groups[segment.Group] = true, true
	}
	state, err := readClaims(jobPath)
	if err != nil {
		return 0, err
	}
	for _, c := range state.Claims {
		if !groups[c.Group] {
			continue
		}
		valid, err := claimValid(job, c, time.Now().UTC())
		if err != nil {
			return 0, fmt.Errorf("无法核验组 %q 的任务: %w", c.Group, err)
		}
		if valid {
			return 0, fmt.Errorf("组 %q 有活动任务，请先等待或释放", c.Group)
		}
	}
	shadow := *job
	shadow.Done = make(map[string]document.Result, len(job.Done))
	for id, result := range job.Done {
		shadow.Done[id] = result
	}
	var records []document.Result
	if stage == "translate" {
		for _, segment := range job.Segments {
			if wanted[segment.ID] {
				delete(shadow.Done, segment.ID)
			}
		}
	}
	dirty := map[string]bool{}
	for _, segment := range job.Segments {
		old, exists := job.Done[segment.ID]
		if wanted[segment.ID] {
			if stage == "translate" {
				dirty[segment.Group] = true
				records = append(records, document.Result{ID: segment.ID, Src: segment.Src, Status: "pending", PreviousHash: document.ResultHash(old), EditMethod: "redo-translate"})
			} else {
				record := old
				record.PreviousHash = document.ResultHash(old)
				record.EditMethod = "redo-" + stage
				if stage == "polish" {
					record.PolishCompletion = nil
				}
				record.ProofreadCompletion = nil
				records = append(records, record)
			}
			continue
		}
		if stage != "translate" || !dirty[segment.Group] || !exists || !old.HistoryUsed {
			continue
		}
		if old.HasProtectedOrigin() {
			record := old
			if !record.NeedsReview {
				record.ReviewSignature, record.ReviewTermsHash = record.Signature, record.TermsHash
			}
			record.NeedsReview, record.ContextReview = true, true
			record.ContextRevision++
			record.ReviewHistoryHash = translate.HistoryDigest(&shadow, segment)
			records = append(records, record)
			shadow.Done[segment.ID] = record
		} else {
			records = append(records, document.Result{ID: segment.ID, Src: segment.Src, Status: "pending", PreviousHash: document.ResultHash(old), EditMethod: "redo-context"})
			delete(shadow.Done, segment.ID)
		}
	}
	if err := job.AppendBatch(records); err != nil {
		return 0, fmt.Errorf("保存重做状态失败，重新打开作业后恢复: %w", err)
	}
	return len(ids), nil
}
