package agent

import (
	"fmt"
	"slices"
	"time"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
)

type StageStatus struct {
	State  string       `json:"state"`
	Reason string       `json:"reason,omitempty"`
	Claim  *StatusClaim `json:"claim,omitempty"`
}

type StatusClaim struct {
	Worker  string    `json:"worker"`
	Expires time.Time `json:"expires"`
}

type StatusLine struct {
	ID        string      `json:"id"`
	Group     string      `json:"group"`
	Source    *string     `json:"src,omitempty"`
	Target    *string     `json:"dst,omitempty"`
	Translate StageStatus `json:"translate"`
	Polish    StageStatus `json:"polish"`
	Proofread StageStatus `json:"proofread"`
}

type Status struct {
	Total      int            `json:"total"`
	Translate  map[string]int `json:"translate"`
	Polish     map[string]int `json:"polish"`
	Proofread  map[string]int `json:"proofread"`
	InProgress map[string]int `json:"in_progress"`
	Lines      []StatusLine   `json:"lines,omitempty"`
	SnapshotAt *time.Time     `json:"snapshot_at,omitempty"`
}

func InitialStatus(segments []document.Segment, details bool, id string) (Status, error) {
	status := Status{Total: len(segments), Translate: map[string]int{}, Polish: map[string]int{}, Proofread: map[string]int{}, InProgress: map[string]int{"polish": 0, "proofread": 0}}
	found := false
	for _, segment := range segments {
		status.Translate["pending"]++
		status.Polish["blocked"]++
		status.Proofread["blocked"]++
		line := StatusLine{ID: segment.ID, Group: segment.Group, Translate: StageStatus{State: "pending"}, Polish: StageStatus{State: "blocked", Reason: "初翻未完成"}, Proofread: StageStatus{State: "blocked", Reason: "初翻未完成"}}
		if id == segment.ID {
			found = true
			line.Source = &segment.Src
		}
		if details || id == segment.ID {
			status.Lines = append(status.Lines, line)
		}
	}
	if id != "" && !found {
		return Status{}, fmt.Errorf("作品中没有 ID %q", id)
	}
	return status, nil
}

func PublishLiveStatus(path string, job *store.Store) error {
	value, err := InspectLocked(path, job, true, "")
	if err != nil {
		return err
	}
	for i, segment := range job.Segments {
		value.Lines[i].Source = &job.Segments[i].Src
		if result, ok := job.Done[segment.ID]; ok {
			text := result.Dst
			value.Lines[i].Target = &text
		}
	}
	now := time.Now().UTC()
	value.SnapshotAt = &now
	state, err := readClaims(path)
	if err != nil {
		return err
	}
	state.Live = &value
	return writeClaims(path, state)
}

func ReadLiveStatus(path string, details bool, id string) (Status, error) {
	state, err := readClaims(path)
	if err != nil {
		return Status{}, err
	}
	if state.Live == nil {
		return Status{}, fmt.Errorf("作品正在使用，尚无可读取的进度快照")
	}
	value := *state.Live
	if id != "" {
		for _, line := range value.Lines {
			if line.ID == id {
				value.Lines = []StatusLine{line}
				return value, nil
			}
		}
		return Status{}, fmt.Errorf("作品中没有 ID %q", id)
	}
	if !details {
		value.Lines = nil
	} else {
		for i := range value.Lines {
			value.Lines[i].Source, value.Lines[i].Target = nil, nil
		}
	}
	return value, nil
}

func ClearLiveStatus(path string) error {
	state, err := readClaims(path)
	if err != nil {
		return err
	}
	state.Live = nil
	return writeClaims(path, state)
}

type statusStage struct {
	config stageConfig
	deps   dependencies
	reason string
	claims map[string]*StatusClaim
}

// Inspect only reads the job and uses the same completion digest as Next.
func Inspect(jobPath string, details bool, id string) (status Status, resultErr error) {
	job, err := store.OpenReadOnly(jobPath)
	if err != nil {
		return status, err
	}
	defer func() {
		if closeErr := job.Close(); resultErr == nil {
			resultErr = closeErr
		}
	}()
	return InspectLocked(jobPath, job, details, id)
}

// InspectLocked requires the caller to hold the job lock.
func InspectLocked(jobPath string, job *store.Store, details bool, id string) (status Status, resultErr error) {
	state, err := readClaims(jobPath)
	if err != nil {
		return status, err
	}
	pendingReviews, err := job.PendingPolishReviews()
	if err != nil {
		return status, err
	}
	stages := map[string]*statusStage{}
	for _, name := range []string{"polish", "proofread"} {
		stage := &statusStage{claims: map[string]*StatusClaim{}}
		config, ok := state.Stages[name]
		if !ok {
			stage.reason = "阶段配置未保存，无法核验"
		} else if scopeName(config.Scope) != "group" && scopeName(config.Scope) != "task" {
			stage.reason = "阶段上下文范围无效，无法核验"
		} else if entries, cards, loadErr := materials(config.Glossaries, config.Cards, job.Segments); loadErr != nil {
			stage.reason = fmt.Sprintf("阶段材料不可读取或无效: %v", loadErr)
		} else {
			stage.config = config
			var reviewErr error
			stage.deps, reviewErr = stageDependencies(job, name, entries, cards, config.Reviews)
			if reviewErr != nil {
				stage.reason = fmt.Sprintf("审查材料不可读取或无效: %v", reviewErr)
				stages[name] = stage
				continue
			}
			for _, c := range state.Claims {
				if c.Stage != name || !c.Expires.After(time.Now().UTC()) || c.Model != config.Model || c.Revision != config.Revision || c.Cards != config.Cards || scopeName(c.Scope) != scopeName(config.Scope) || c.Context != config.Context || c.MaxLines != config.MaxLines || !slices.Equal(c.Glossaries, config.Glossaries) || !slices.Equal(c.Reviews, config.Reviews) {
					continue
				}
				valid, _ := claimValid(job, c, time.Now().UTC())
				for _, item := range c.Items {
					_, refreshed := pendingReviews[item.SegmentID]
					segment, found := segmentByID(job, item.SegmentID)
					result := job.Done[item.SegmentID]
					contextChanged := found && result.ContextReview && translate.HistoryDigest(job, segment) != result.ReviewHistoryHash
					if refreshed || contextChanged {
						valid = false
					}
				}
				if valid {
					for _, item := range c.Items {
						stage.claims[item.SegmentID] = &StatusClaim{Worker: c.Worker, Expires: c.Expires}
					}
				}
			}
		}
		stages[name] = stage
	}
	status = Status{Total: len(job.Segments), Translate: map[string]int{}, Polish: map[string]int{}, Proofread: map[string]int{}, InProgress: map[string]int{"polish": 0, "proofread": 0}}
	blocked := map[string]map[string]bool{"polish": {}, "proofread": {}}
	found := false
	for _, segment := range job.Segments {
		result, done := job.Done[segment.ID]
		_, failed := job.Failed[segment.ID]
		_, refresh := pendingReviews[segment.ID]
		line := StatusLine{ID: segment.ID, Group: segment.Group}
		switch {
		case done && result.Passthrough:
			line.Translate.State = "passthrough"
		case done && (result.NeedsReview || refresh):
			line.Translate.State = "review"
		case done:
			line.Translate.State = "done"
		case failed:
			line.Translate.State = "failed"
		default:
			line.Translate.State = "pending"
		}
		line.Polish = inspectStage("polish", segment, result, done, refresh, stages["polish"], blocked["polish"])
		line.Proofread = inspectStage("proofread", segment, result, done, refresh, stages["proofread"], blocked["proofread"])
		status.Translate[line.Translate.State]++
		status.Polish[line.Polish.State]++
		status.Proofread[line.Proofread.State]++
		if line.Polish.Claim != nil {
			status.InProgress["polish"]++
		}
		if line.Proofread.Claim != nil {
			status.InProgress["proofread"]++
		}
		if details || id == segment.ID {
			if id == segment.ID {
				found = true
				line.Source = &segment.Src
				if done {
					line.Target = &result.Dst
				}
			}
			status.Lines = append(status.Lines, line)
		}
	}
	if id != "" && !found {
		return Status{}, fmt.Errorf("作业中没有 ID %q", id)
	}
	return status, nil
}

func inspectStage(name string, segment document.Segment, result document.Result, done, refresh bool, stage *statusStage, blocked map[string]bool) StageStatus {
	if done && result.Passthrough {
		return StageStatus{State: "not_applicable"}
	}
	if !done {
		blocked[segment.Group] = true
		return StageStatus{State: "blocked", Reason: "初翻未完成"}
	}
	if stage.reason != "" {
		return StageStatus{State: "unknown", Reason: stage.reason}
	}
	terms := result.TermsHash
	if result.NeedsReview {
		terms = result.ReviewTermsHash
	}
	if terms == "" || terms != stage.deps.terms[segment.Group] {
		blocked[segment.Group] = true
		return StageStatus{State: "blocked", Reason: "术语与作业不一致，需同步初翻"}
	}
	if blocked[segment.Group] {
		return StageStatus{State: "blocked", Reason: "同组前文尚未完成或术语不一致"}
	}
	value := StageStatus{Claim: stage.claims[segment.ID]}
	if !refresh && stageComplete(stage.deps, segment, result, name, stage.config) {
		value.State = "done"
		return value
	}
	completion := result.PolishCompletion
	if name == "proofread" {
		completion = result.ProofreadCompletion
	}
	if completion != nil || result.NeedsReview || refresh {
		value.State, value.Reason = "review", "底稿、依赖或阶段配置已变化，需重新核对"
	} else {
		value.State = "pending"
	}
	return value
}
