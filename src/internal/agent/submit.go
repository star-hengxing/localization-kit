package agent

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
)

type Submission struct {
	ID     string                `json:"id"`
	Dst    string                `json:"dst"`
	Reason string                `json:"reason,omitempty"`
	Issues []document.StageIssue `json:"issues,omitempty"`
}

func Submit(jobPath, worker, taskID string, submissions []Submission) error {
	return SubmitWithValidation(jobPath, worker, taskID, submissions, nil)
}

func SubmitWithValidation(jobPath, worker, taskID string, submissions []Submission, validate func(*store.Store) error) error {
	return submit(jobPath, worker, taskID, submissions, false, validate)
}

// SubmitChangesWithValidation confirms the entire claimed task. Omitted lines
// keep their claimed translations and still undergo staleness checks. An empty
// slice confirms every line. A nil slice is invalid. A matching saved submission is
// recognized even after the claim is cleared, regardless of change order.
func SubmitChangesWithValidation(jobPath, worker, taskID string, changes []Submission, validate func(*store.Store) error) error {
	if changes == nil {
		return fmt.Errorf("改动文件必须包含 JSON 数组")
	}
	changes = slices.Clone(changes)
	slices.SortFunc(changes, func(a, b Submission) int { return strings.Compare(a.ID, b.ID) })
	return submit(jobPath, worker, taskID, changes, true, validate)
}

func submit(jobPath, worker, taskID string, submissions []Submission, changesOnly bool, validate func(*store.Store) error) (resultErr error) {
	if jobPath == "" || !validIdentity(worker) || taskID == "" || (!changesOnly && len(submissions) == 0) {
		return fmt.Errorf("需要作业、worker、任务 ID 和提交内容")
	}
	job, err := store.OpenExisting(jobPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := job.Close(); resultErr == nil {
			resultErr = closeErr
		}
	}()
	if validate != nil {
		if err := validate(job); err != nil {
			return err
		}
	}
	if err := translate.RefreshContextReviews(job); err != nil {
		return err
	}
	if err := job.RefreshPolishReviews(); err != nil {
		return err
	}
	state, err := readClaims(jobPath)
	if err != nil {
		return err
	}
	var payload any = submissions
	if changesOnly {
		payload = struct {
			Changes []Submission `json:"changes"`
		}{submissions}
	}
	digest, err := jsonfile.Digest(payload)
	if err != nil {
		return err
	}
	if changesOnly {
		if saved, err := job.CheckAgentSubmission(taskID, worker, digest); saved || err != nil {
			return err
		}
	} else {
		completed := 0
		for _, submission := range submissions {
			previous, found := job.FindAgentTask(taskID, worker, submission.ID)
			if found {
				if previous != digest {
					return fmt.Errorf("任务已经以不同结果提交")
				}
				completed++
			}
		}
		if completed == len(submissions) {
			return nil
		}
		if completed != 0 {
			return fmt.Errorf("任务部分结果已保存，请重新领取剩余条目")
		}
	}
	index := -1
	var c claim
	for i, candidate := range state.Claims {
		if candidate.TaskID == taskID {
			index, c = i, candidate
			break
		}
	}
	if index < 0 || c.Worker != worker || !c.Expires.After(time.Now().UTC()) || (!changesOnly && len(c.Items) != len(submissions)) {
		return fmt.Errorf("任务不存在、已过期、条目不完整或不属于该 worker")
	}
	entries, cards, err := materials(c.Glossaries, c.Cards, job.Segments)
	if err != nil {
		return err
	}
	beforeDeps, err := stageDependencies(job, c.Stage, entries, cards, c.Reviews)
	if err != nil {
		return err
	}
	task, err := taskFor(job, c, entries, cards, beforeDeps)
	if err != nil {
		return err
	}
	if changesOnly {
		submissions, err = completeSubmission(task.Lines, submissions)
		if err != nil {
			return err
		}
	}
	shadow := *beforeDeps.job
	shadow.Done = maps.Clone(beforeDeps.job.Done)
	for i, submission := range submissions {
		item := c.Items[i]
		if submission.ID != item.SegmentID || strings.TrimSpace(submission.Dst) == "" {
			return fmt.Errorf("提交条目须与领取的顺序一致且译文非空")
		}
		segment, _ := segmentByID(job, item.SegmentID)
		old := job.Done[item.SegmentID]
		if submission.Dst != beforeDeps.job.Done[item.SegmentID].Dst && strings.TrimSpace(submission.Reason) == "" {
			return fmt.Errorf("修改译文需要填写原因")
		}
		if c.Stage == "proofread" {
			if submission.Dst != old.Dst && len(submission.Issues) == 0 {
				return fmt.Errorf("校对修改需要提供问题及原文依据")
			}
			if submission.Dst == old.Dst && len(submission.Issues) != 0 {
				return fmt.Errorf("存在未修复的问题，不能提交为完成")
			}
			for _, issue := range submission.Issues {
				if strings.TrimSpace(issue.Code) == "" || strings.TrimSpace(issue.Detail) == "" || strings.TrimSpace(issue.Quote) == "" || !strings.Contains(segment.Src, issue.Quote) {
					return fmt.Errorf("校对问题需要代码、说明和逐字原文依据")
				}
			}
		}
		updated := old
		updated.Dst = submission.Dst
		if old.NeedsReview && old.ReviewSignature != "" {
			updated.Signature = old.ReviewSignature
		}
		updated.NeedsReview = false
		updated.ContextReview = false
		shadow.Done[item.SegmentID] = updated
	}
	deps, err := dependencyHashes(&shadow, entries, cards)
	if err != nil {
		return err
	}
	deps.reviews, deps.reviewsHash, err = loadReviews(&shadow, c.Reviews, entries, cards)
	if err != nil {
		return err
	}
	var edits []translate.ManualEdit
	firstID, lastID := c.Items[0].SegmentID, c.Items[len(c.Items)-1].SegmentID
	for i, submission := range submissions {
		item := c.Items[i]
		segment, _ := segmentByID(job, item.SegmentID)
		old, updated := job.Done[item.SegmentID], shadow.Done[item.SegmentID]
		completionHash, err := scopedDigest(deps, segment, updated, c.Stage, c.Model, c.Revision, c.Context, c.MaxLines, c.Scope, firstID, lastID)
		if err != nil {
			return err
		}
		basis, err := taskBasis(deps, segment, updated, c.Stage, c.Model, c.Revision, c.Context, c.MaxLines)
		if err != nil {
			return err
		}
		completion := &document.StageCompletion{TaskID: taskID, TaskSize: len(c.Items), Worker: worker, Model: c.Model, Revision: c.Revision, Context: c.Context, MaxLines: c.MaxLines, Scope: scopeName(c.Scope), BlockStart: firstID, BlockEnd: lastID, Digest: completionHash, Basis: basis, SubmissionHash: digest, Issues: submission.Issues}
		var polish *document.StageCompletion
		var historyHash string
		if c.Stage == "proofread" {
			polish = old.PolishCompletion
		} else if old.HistoryUsed {
			historyHash = translate.HistoryDigest(&shadow, segment)
		}
		edits = append(edits, translate.ManualEdit{ID: segment.ID, ExpectedHash: item.ExpectedHash, Dst: submission.Dst, Method: c.Stage, Actor: c.Model, Reason: submission.Reason, Stage: c.Stage, HistoryHash: historyHash, Completion: completion, PolishCompletion: polish})
	}
	if validate != nil {
		if err := validate(job); err != nil {
			return err
		}
	}
	if err := translate.ApplyEditsLocked(job, edits, entries, nil); err != nil {
		return err
	}
	state.Claims = append(state.Claims[:index], state.Claims[index+1:]...)
	if err := writeClaims(jobPath, state); err != nil {
		return fmt.Errorf("结果已保存，但清理任务领取记录失败: %w", err)
	}
	return nil
}

func completeSubmission(lines []Line, changes []Submission) ([]Submission, error) {
	byID := make(map[string]Submission, len(changes))
	for _, change := range changes {
		if _, duplicate := byID[change.ID]; duplicate {
			return nil, fmt.Errorf("改动含重复条目 %q", change.ID)
		}
		byID[change.ID] = change
	}
	submissions := make([]Submission, 0, len(lines))
	for _, line := range lines {
		change, edited := byID[line.ID]
		if !edited {
			submissions = append(submissions, Submission{ID: line.ID, Dst: line.Dst})
			continue
		}
		if change.Dst == line.Dst {
			return nil, fmt.Errorf("改动条目 %q 的译文与底稿相同", line.ID)
		}
		submissions = append(submissions, change)
		delete(byID, line.ID)
	}
	for _, change := range changes {
		if _, unclaimed := byID[change.ID]; unclaimed {
			return nil, fmt.Errorf("改动条目 %q 不在本次领取中", change.ID)
		}
	}
	return submissions, nil
}
