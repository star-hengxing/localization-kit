package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
)

type Options struct {
	Job, Stage, Worker, Model, Revision, Cards, Scope string
	Glossaries                                        []string
	Reviews                                           []ReviewFiles
	TTL                                               time.Duration
	Context, MaxLines                                 int
}

type Line struct {
	ID           string       `json:"id"`
	Src          string       `json:"src"`
	Dst          string       `json:"dst"`
	ExpectedHash string       `json:"expected_result_hash,omitempty"`
	Reviews      []ReviewNote `json:"reviews,omitempty"`
}

type Task struct {
	Status        string             `json:"status"`
	TaskID        string             `json:"task_id,omitempty"`
	Stage         string             `json:"stage,omitempty"`
	Scope         string             `json:"scope,omitempty"`
	Worker        string             `json:"worker,omitempty"`
	Model         string             `json:"model,omitempty"`
	Revision      string             `json:"revision,omitempty"`
	Expires       *time.Time         `json:"expires,omitempty"`
	Lines         []Line             `json:"lines,omitempty"`
	Context       []Line             `json:"context,omitempty"`
	Following     []document.Segment `json:"following,omitempty"`
	Glossary      []rules.Entry      `json:"glossary,omitempty"`
	Cards         json.RawMessage    `json:"cards,omitempty"`
	Rules         []string           `json:"rules,omitempty"`
	Blocked       int                `json:"blocked,omitempty"`
	BlockedReason string             `json:"blocked_reason,omitempty"`
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validIdentity(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n") && !strings.Contains(value, " / ")
}

func segmentByID(job *store.Store, id string) (document.Segment, bool) {
	index, ok := job.SegmentIndex(id)
	if !ok {
		return document.Segment{}, false
	}
	return job.Segments[index], true
}

func taskFor(job *store.Store, c claim, entries []rules.Entry, cards json.RawMessage, deps dependencies) (Task, error) {
	if len(c.Items) == 0 || c.MaxLines < 1 || c.Context < 0 || len(c.Items) > c.MaxLines || (scopeName(c.Scope) != "group" && scopeName(c.Scope) != "task") {
		return Task{}, fmt.Errorf("领取记录没有条目")
	}
	task := Task{Status: "ready", TaskID: c.TaskID, Stage: c.Stage, Scope: scopeName(c.Scope), Worker: c.Worker, Model: c.Model, Revision: c.Revision, Expires: &c.Expires, Cards: cards, Rules: []string{"对照原文与上下文，保持语义、语气和术语一致。", "提交本块每一条，保持非空且不增加换行；无需修改时原样提交译文。", "修改译文时填写 reason；无法确认或无法修复时释放任务并向主 agent 报告。"}}
	if scopeName(c.Scope) == "task" {
		task.Rules = append(task.Rules, "本块须在独立执行者会话处理；只使用本次领取返回的材料，不读取或接收其他作品上下文。")
	}
	if len(c.Reviews) > 0 {
		task.Rules = append(task.Rules, "审查意见针对所附底稿；stale 为 true 时底稿或上下文已变化，须重新对照当前材料判断，不能直接套用。")
	}
	var source []string
	firstID, lastID := c.Items[0].SegmentID, c.Items[len(c.Items)-1].SegmentID
	firstIndex, firstFound := job.SegmentIndex(firstID)
	for i, item := range c.Items {
		segment, ok := segmentByID(job, item.SegmentID)
		index, indexOK := job.SegmentIndex(item.SegmentID)
		current, done := job.Done[item.SegmentID]
		result := deps.job.Done[item.SegmentID]
		digest, err := scopedDigest(deps, segment, result, c.Stage, c.Model, c.Revision, c.Context, c.MaxLines, scopeName(c.Scope), firstID, lastID)
		if err != nil {
			return Task{}, err
		}
		if digest != "" && digest != item.InputDigest && scopeName(c.Scope) == "group" {
			digest, err = scopedDigest(deps, segment, result, c.Stage, c.Model, c.Revision, c.Context, c.MaxLines, c.Scope, "", "")
			if err != nil {
				return Task{}, err
			}
		}
		if !firstFound || !indexOK || index != firstIndex+i || !ok || !done || digest == "" || segment.Group != c.Group || document.ResultHash(current) != item.ExpectedHash || digest != item.InputDigest {
			return Task{}, fmt.Errorf("领取的底稿、术语或上下文已变化")
		}
		task.Lines = append(task.Lines, Line{ID: segment.ID, Src: segment.Src, Dst: result.Dst, ExpectedHash: item.ExpectedHash, Reviews: deps.reviews[segment.ID]})
		source = append(source, segment.Src)
	}
	if c.Stage == "proofread" {
		task.Rules = append(task.Rules, "对照原文审查并修复问题；修改时在 issues 中记录 code、detail 和逐字原文 quote。")
	}
	before, following := taskContext(deps.job, firstIndex, firstIndex+len(c.Items)-1, c.Context)
	task.Context = before
	for _, other := range following {
		task.Following = append(task.Following, document.Segment{ID: other.ID, Group: other.Group, Src: other.Src})
	}
	for _, hit := range rules.Hits(strings.Join(source, "\n"), entries) {
		task.Glossary = append(task.Glossary, hit.Entry)
	}
	return task, nil
}

func Next(options Options) (Task, error) {
	return NextWithValidation(options, nil)
}

func NextWithValidation(options Options, validate func(*store.Store) error) (task Task, resultErr error) {
	if options.Job == "" || (options.Stage != "polish" && options.Stage != "proofread") || (scopeName(options.Scope) != "group" && scopeName(options.Scope) != "task") || !validIdentity(options.Worker) || !validIdentity(options.Model) || options.TTL <= 0 || options.Context < 0 || options.MaxLines < 1 {
		return Task{}, fmt.Errorf("需要作业、阶段、worker、模型、正数任务时限和块大小，前文数量不能为负")
	}
	if options.Stage != "polish" && len(options.Reviews) > 0 {
		return Task{}, fmt.Errorf("初翻审查报告只能用于润色")
	}
	for i := range options.Reviews {
		var err error
		options.Reviews[i].Input, err = filepath.Abs(options.Reviews[i].Input)
		if err != nil {
			return Task{}, err
		}
		options.Reviews[i].Report, err = filepath.Abs(options.Reviews[i].Report)
		if err != nil {
			return Task{}, err
		}
	}
	for i, path := range options.Glossaries {
		abs, err := filepath.Abs(path)
		if err != nil {
			return Task{}, err
		}
		options.Glossaries[i] = abs
	}
	if options.Cards != "" {
		abs, err := filepath.Abs(options.Cards)
		if err != nil {
			return Task{}, err
		}
		options.Cards = abs
	}
	job, err := store.OpenExisting(options.Job)
	if err != nil {
		return Task{}, err
	}
	defer func() {
		if closeErr := job.Close(); resultErr == nil {
			if closeErr != nil && task.TaskID != "" {
				// The claim is durable and its ID must reach the worker.
				slog.Warn("任务已领取，关闭作业时出错", "task_id", task.TaskID, "error", closeErr)
				return
			}
			resultErr = closeErr
		}
	}()
	if validate != nil {
		if err := validate(job); err != nil {
			return Task{}, err
		}
	}
	entries, cards, err := materials(options.Glossaries, options.Cards, job.Segments)
	if err != nil {
		return Task{}, err
	}
	if err := translate.RefreshContextReviews(job); err != nil {
		return Task{}, err
	}
	if err := job.RefreshPolishReviews(); err != nil {
		return Task{}, err
	}
	state, err := readClaims(options.Job)
	if err != nil {
		return Task{}, err
	}
	// Reuse the saved execution version when resuming a stage with the same model.
	if saved := state.Stages[options.Stage]; options.Revision == "" && saved.Model == options.Model {
		options.Revision = saved.Revision
	}
	now := time.Now().UTC()
	previousCount := len(state.Claims)
	active := state.Claims[:0]
	for _, c := range state.Claims {
		valid, err := claimValid(job, c, now)
		if err != nil {
			return Task{}, fmt.Errorf("无法核验活动任务 %s 的材料: %w", c.TaskID, err)
		}
		if valid {
			active = append(active, c)
		}
	}
	state.Claims = active
	if len(active) != previousCount {
		if err := writeClaims(options.Job, state); err != nil {
			return Task{}, err
		}
	}
	deps, err := stageDependencies(job, options.Stage, entries, cards, options.Reviews)
	if err != nil {
		return Task{}, err
	}
	for _, c := range active {
		if c.Stage == options.Stage && (c.Model != options.Model || c.Revision != options.Revision || c.Cards != options.Cards || scopeName(c.Scope) != scopeName(options.Scope) || c.Context != options.Context || c.MaxLines != options.MaxLines || !slices.Equal(c.Glossaries, options.Glossaries) || !slices.Equal(c.Reviews, options.Reviews)) {
			return Task{}, fmt.Errorf("同阶段已有使用不同模型、执行设置或材料的活动任务")
		}
	}
	if state.Stages == nil {
		state.Stages = map[string]stageConfig{}
	}
	if config, ok := state.Stages[options.Stage]; !ok || !config.matches(options) {
		state.Stages[options.Stage] = stageConfig{Model: options.Model, Revision: options.Revision, Cards: options.Cards, Scope: scopeName(options.Scope), Glossaries: options.Glossaries, Reviews: options.Reviews, Context: options.Context, MaxLines: options.MaxLines}
		if err := writeClaims(options.Job, state); err != nil {
			return Task{}, err
		}
	}
	for index, c := range active {
		if c.Worker == options.Worker && c.Stage == options.Stage {
			if c.Model != options.Model || c.Revision != options.Revision || c.Cards != options.Cards || scopeName(c.Scope) != scopeName(options.Scope) || c.Context != options.Context || c.MaxLines != options.MaxLines || !slices.Equal(c.Glossaries, options.Glossaries) || !slices.Equal(c.Reviews, options.Reviews) {
				return Task{}, fmt.Errorf("worker 已领取其他配置的任务")
			}
			task, err := taskFor(job, c, entries, cards, deps)
			if err != nil {
				state.Claims = append(state.Claims[:index], state.Claims[index+1:]...)
				active = state.Claims
				if err := writeClaims(options.Job, state); err != nil {
					return Task{}, err
				}
				break
			}
			c.Expires = now.Add(options.TTL)
			state.Claims[index] = c
			task.Expires = &c.Expires
			if err := writeClaims(options.Job, state); err != nil {
				return Task{}, err
			}
			return task, nil
		}
	}
	busyGroups := map[string]bool{}
	for _, c := range active {
		busyGroups[c.Group] = true
	}
	blocked := 0
	busy := false
	blockedGroups := map[string]bool{}
	termsHashes := deps.terms
	for segmentIndex, segment := range job.Segments {
		result, ok := job.Done[segment.ID]
		if ok && result.Passthrough {
			continue
		}
		expectedTerms := result.TermsHash
		if result.NeedsReview {
			expectedTerms = result.ReviewTermsHash
		}
		if !ok || expectedTerms == "" || expectedTerms != termsHashes[segment.Group] {
			blocked++
			blockedGroups[segment.Group] = true
			continue
		}
		if blockedGroups[segment.Group] {
			blocked++
			continue
		}
		if stageComplete(deps, segment, result, options.Stage, state.Stages[options.Stage]) {
			continue
		}
		if busyGroups[segment.Group] {
			busy = true
			continue
		}
		id, err := newID()
		if err != nil {
			return Task{}, err
		}
		c := claim{TaskID: id, Stage: options.Stage, Worker: options.Worker, Model: options.Model, Revision: options.Revision, Group: segment.Group, Cards: options.Cards, Scope: scopeName(options.Scope), Glossaries: options.Glossaries, Reviews: options.Reviews, Expires: now.Add(options.TTL), Context: options.Context, MaxLines: options.MaxLines}
		for _, candidate := range job.Segments[segmentIndex:] {
			if candidate.Group != segment.Group || len(c.Items) >= options.MaxLines {
				break
			}
			current, exists := job.Done[candidate.ID]
			if !exists || current.Passthrough {
				break
			}
			terms := current.TermsHash
			if current.NeedsReview {
				terms = current.ReviewTermsHash
			}
			if terms == "" || terms != termsHashes[candidate.Group] {
				break
			}
			if stageComplete(deps, candidate, current, options.Stage, state.Stages[options.Stage]) {
				break
			}
			c.Items = append(c.Items, claimItem{SegmentID: candidate.ID, ExpectedHash: document.ResultHash(current)})
		}
		firstID, lastID := c.Items[0].SegmentID, c.Items[len(c.Items)-1].SegmentID
		for i := range c.Items {
			item := &c.Items[i]
			currentSegment, _ := segmentByID(job, item.SegmentID)
			item.InputDigest, err = scopedDigest(deps, currentSegment, deps.job.Done[item.SegmentID], c.Stage, c.Model, c.Revision, c.Context, c.MaxLines, c.Scope, firstID, lastID)
			if err != nil {
				return Task{}, err
			}
		}
		task, err := taskFor(job, c, entries, cards, deps)
		if err != nil {
			return Task{}, err
		}
		state.Claims = append(state.Claims, c)
		if err := writeClaims(options.Job, state); err != nil {
			return Task{}, err
		}
		return task, nil
	}
	if busy {
		reason := "待处理的组已被其他执行者领取"
		if blocked > 0 {
			reason += "；部分条目未完成初翻或术语表与当前作业不一致"
		}
		return Task{Status: "busy", Blocked: blocked, BlockedReason: reason}, nil
	}
	if blocked > 0 {
		return Task{Status: "blocked", Blocked: blocked, BlockedReason: "未完成初翻或术语表与当前作业不一致，请先运行翻译同步作业"}, nil
	}
	return Task{Status: "done"}, nil
}
