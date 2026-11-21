package translate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"localization-kit/src/internal/config"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/model"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/profile"
	"localization-kit/src/internal/translate/rules"
)

type Summary struct {
	Total   int `json:"total"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	Review  int `json:"review,omitempty"`
}

func Run(ctx context.Context, client *model.Client, selected config.Model, segments []document.Segment, entries []rules.Entry, job *store.Store, retryFailed bool) (summary Summary, resultErr error) {
	policy, ok := profile.BatchPolicyFor(selected.Profile)
	if !ok {
		return Summary{}, fmt.Errorf("翻译模型的 profile 必须为 hy-mt2 或 opensakura，当前为 %q", selected.Profile)
	}
	for _, records := range []map[string]document.Result{job.Done, job.Failed} {
		for _, record := range records {
			if !record.HasProtectedOrigin() && !record.Passthrough && (record.Model != "" && record.Model != selected.Model || record.Profile != "" && record.Profile != selected.Profile) {
				return Summary{}, fmt.Errorf("作业已有其他模型或 profile 的结果，请使用新的作业目录")
			}
		}
	}
	defer func() {
		resultErr = errors.Join(resultErr, job.RefreshPolishReviews())
		summary = status(segments, job)
	}()
	if err := job.RefreshPolishReviews(); err != nil {
		return status(segments, job), err
	}
	var pending []document.Segment
	contexts := groupContexts(segments, entries)
	positions := make(map[string]int, len(segments))
	dirtyGroups := map[string]bool{}
	for index, s := range segments {
		positions[s.ID] = index
		sig := signature(s, selected, contexts[s.Group])
		if r, ok := job.Done[s.ID]; ok && r.HasProtectedOrigin() {
			historyHash := ""
			if r.HistoryUsed {
				historyHash = HistoryDigest(job, s)
			}
			historyChanged := r.HistoryUsed && (r.HistoryHash == "" || r.HistoryHash != historyHash)
			if r.HistoryUsed && (dirtyGroups[s.Group] || historyChanged || r.ContextReview && r.ReviewHistoryHash != historyHash) && (!r.ContextReview || r.ReviewHistoryHash != historyHash || r.ReviewSignature != sig || dirtyGroups[s.Group]) {
				r.NeedsReview = true
				r.ContextReview = true
				r.ContextRevision++
				r.ReviewSignature = sig
				r.ReviewTermsHash = contexts[s.Group].Terms
				r.ReviewHistoryHash = historyHash
				if err := job.Append(r); err != nil {
					return status(segments, job), err
				}
			} else if r.Signature == sig && r.NeedsReview && !r.ContextReview && !r.PolishContextReview {
				r.NeedsReview = false
				r.ReviewSignature = ""
				r.ReviewTermsHash = ""
				r.ReviewHistoryHash = ""
				if err := job.Append(r); err != nil {
					return status(segments, job), err
				}
			} else if r.Signature != sig && (!r.NeedsReview || r.ReviewSignature != sig) {
				r.NeedsReview = true
				r.ReviewSignature = sig
				r.ReviewTermsHash = contexts[s.Group].Terms
				if err := job.Append(r); err != nil {
					return status(segments, job), err
				}
			}
			continue
		}
		if r, ok := job.Done[s.ID]; ok && r.Signature == sig {
			if !r.HistoryUsed || !dirtyGroups[s.Group] && r.HistoryHash != "" && r.HistoryHash == HistoryDigest(job, s) {
				continue
			}
		}
		if r, ok := job.Failed[s.ID]; ok && !retryFailed && (r.Signature == sig || r.HasProtectedOrigin()) {
			continue
		}
		_, wasDone := job.Done[s.ID]
		_, wasFailed := job.Failed[s.ID]
		if wasDone || wasFailed {
			if err := job.Append(document.Result{ID: s.ID, Src: s.Src, Status: "pending", Signature: sig, TermsHash: contexts[s.Group].Terms}); err != nil {
				return status(segments, job), err
			}
		}
		if !rules.Translatable(s.Src) {
			if err := job.Append(document.Result{ID: s.ID, Src: s.Src, Dst: s.Src, Passthrough: true, Status: "done", Signature: sig, TermsHash: contexts[s.Group].Terms}); err != nil {
				return status(segments, job), err
			}
			continue
		}
		dirtyGroups[s.Group] = true
		pending = append(pending, s)
	}
	for i := 0; i < len(pending); {
		end := i + 1
		for end < len(pending) && end-i < policy.MaxLines && pending[end].Group == pending[i].Group && positions[pending[end].ID] == positions[pending[end-1].ID]+1 {
			end++
		}
		if err := translateChunk(ctx, client, selected, pending[i:end], entries, contexts, job, policy.HistoryForSingleLine); err != nil {
			return status(segments, job), err
		}
		i = end
	}
	for _, segment := range segments {
		r, ok := job.Done[segment.ID]
		if !ok || !r.HasProtectedOrigin() || !r.ContextReview {
			continue
		}
		historyHash := HistoryDigest(job, segment)
		if historyHash != r.ReviewHistoryHash {
			r.ReviewHistoryHash = historyHash
			r.ContextRevision++
			if err := job.Append(r); err != nil {
				return status(segments, job), err
			}
		}
	}
	return status(segments, job), nil
}

func translateChunk(ctx context.Context, client *model.Client, selected config.Model, segments []document.Segment, entries []rules.Entry, contexts map[string]groupContext, job *store.Store, historyForSingleLine bool) error {
	remaining := segments
	if len(remaining) > 1 {
		for rung := 0; rung < 3; rung++ {
			response, lines, err := call(ctx, client, selected, remaining, entries, rung, nil)
			if err != nil {
				var contextErr *model.ContextLimitError
				if errors.As(err, &contextErr) {
					break
				}
				var finishErr *completionFinishError
				if errors.As(err, &finishErr) {
					for _, segment := range remaining {
						if err := saveFailure(job, segment, selected, contexts[segment.Group], []document.Issue{{Code: "model_finish", Detail: finishErr.Error()}}, finishErr.Reason); err != nil {
							return err
						}
					}
					return nil
				}
				return err
			}
			if lines == nil {
				continue
			}
			var rejected []document.Segment
			for i, segment := range remaining {
				blocking, warnings := rules.Check(segment.Src, lines[i], entries)
				if len(blocking) > 0 {
					rejected = append(rejected, segment)
					continue
				}
				if err := save(job, segment, lines[i], warnings, response, selected, contexts[segment.Group], false); err != nil {
					return err
				}
			}
			remaining = rejected
			break
		}
	}
	for _, segment := range remaining {
		var last []document.Issue
		var lastFinish string
		var history []model.Message
		if historyForSingleLine {
			history = historyBefore(job, segment)
		}
		for rung := 0; rung < 3; rung++ {
			response, lines, err := call(ctx, client, selected, []document.Segment{segment}, entries, rung, history)
			if err != nil {
				var contextErr *model.ContextLimitError
				if errors.As(err, &contextErr) {
					last = []document.Issue{{Code: "context_budget", Detail: "单条文本超过模型上下文限制"}}
					break
				}
				var finishErr *completionFinishError
				if errors.As(err, &finishErr) {
					last = []document.Issue{{Code: "model_finish", Detail: finishErr.Error()}}
					lastFinish = finishErr.Reason
					break
				}
				return err
			}
			if lines == nil {
				last = []document.Issue{{Code: "line_count", Detail: "模型输出不完整或行数不符"}}
				continue
			}
			blocking, warnings := rules.Check(segment.Src, lines[0], entries)
			if len(blocking) > 0 {
				last = blocking
				continue
			}
			if err := save(job, segment, lines[0], warnings, response, selected, contexts[segment.Group], historyForSingleLine); err != nil {
				return err
			}
			last = nil
			break
		}
		if len(last) > 0 {
			if err := saveFailure(job, segment, selected, contexts[segment.Group], last, lastFinish); err != nil {
				return err
			}
		}
	}
	return nil
}

type completionFinishError struct{ Reason string }

func (e *completionFinishError) Error() string {
	return fmt.Sprintf("模型输出未正常结束: %q", e.Reason)
}

func saveFailure(job *store.Store, segment document.Segment, selected config.Model, context groupContext, issues []document.Issue, finish string) error {
	return job.Append(document.Result{ID: segment.ID, Src: segment.Src, Model: selected.Model, ModelURL: selected.URL, Profile: selected.Profile, Status: "failed", Signature: signature(segment, selected, context), TermsHash: context.Terms, Violations: issues, Finish: finish})
}

func call(ctx context.Context, client *model.Client, selected config.Model, segments []document.Segment, entries []rules.Entry, rung int, history []model.Message) (model.Response, []string, error) {
	request, err := profile.Request(selected.Profile, segments, entries, rung)
	if err != nil {
		return model.Response{}, nil, err
	}
	if len(history) != 0 {
		last := request.Messages[len(request.Messages)-1]
		request.Messages = append(request.Messages[:len(request.Messages)-1], history...)
		request.Messages = append(request.Messages, last)
	}
	request, err = client.Prepare(ctx, request)
	if err != nil {
		return model.Response{}, nil, err
	}
	response, err := client.Complete(ctx, request)
	if err != nil {
		return model.Response{}, nil, err
	}
	choice := response.Choices[0]
	if choice.FinishReason == "length" {
		return response, nil, nil
	}
	if choice.FinishReason != "stop" {
		return response, nil, &completionFinishError{Reason: choice.FinishReason}
	}
	lines, err := profile.Parse(selected.Profile, choice.Message.Content, len(segments))
	if err != nil {
		return response, nil, nil
	}
	return response, lines, nil
}

func save(job *store.Store, segment document.Segment, dst string, warnings []document.Issue, response model.Response, selected config.Model, context groupContext, historyUsed bool) error {
	if strings.TrimSpace(dst) == "" {
		return fmt.Errorf("译文为空: %s", segment.ID)
	}
	historyHash := ""
	if historyUsed {
		historyHash = HistoryDigest(job, segment)
	}
	return job.Append(document.Result{ID: segment.ID, Src: segment.Src, Dst: dst, Warnings: warnings, Model: selected.Model, ModelURL: selected.URL, Profile: selected.Profile, Usage: response.Usage, Finish: response.Choices[0].FinishReason, Status: "done", Signature: signature(segment, selected, context), TermsHash: context.Terms, HistoryUsed: historyUsed, HistoryHash: historyHash})
}

func status(segments []document.Segment, job *store.Store) Summary {
	result := Summary{Total: len(segments), Done: len(job.Done), Failed: len(job.Failed)}
	for _, record := range job.Done {
		if record.NeedsReview {
			result.Review++
		}
	}
	result.Pending = result.Total - result.Done - result.Failed
	return result
}
