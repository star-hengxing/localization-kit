package agent

import (
	"fmt"
	"sort"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
)

type AuditWorker struct {
	Worker     string   `json:"worker"`
	Model      string   `json:"model"`
	Lines      int      `json:"lines"`
	Changed    int      `json:"changed"`
	Unchanged  int      `json:"unchanged"`
	Unknown    int      `json:"unknown"`
	ChangeRate *float64 `json:"change_rate"`
}

type AuditTask struct {
	TaskID       string   `json:"task_id"`
	Worker       string   `json:"worker"`
	Model        string   `json:"model"`
	IDs          []string `json:"ids"`
	OriginalSize int      `json:"original_size"`
	Changed      int      `json:"changed"`
	Unchanged    int      `json:"unchanged"`
	Unknown      int      `json:"unknown"`
	AllUnchanged bool     `json:"all_unchanged"`
}

type AuditWarning struct {
	ID string `json:"id"`
	document.Issue
}

type Audit struct {
	States   map[string]int `json:"states"`
	Workers  []AuditWorker  `json:"workers"`
	Tasks    []AuditTask    `json:"tasks"`
	Warnings []AuditWarning `json:"warnings"`
}

// AuditPolish reports current stage status and the recorded changes for each current task.
func AuditPolish(jobPath string) (audit Audit, resultErr error) {
	job, err := store.OpenReadOnly(jobPath)
	if err != nil {
		return audit, err
	}
	defer func() {
		if closeErr := job.Close(); resultErr == nil {
			resultErr = closeErr
		}
	}()
	status, err := InspectLocked(jobPath, job, true, "")
	if err != nil {
		return audit, err
	}
	history := map[string]document.Result{}
	submissions := map[string]map[string]document.Result{}
	if err := job.VisitHistory(func(result document.Result) {
		history[document.ResultHash(result)] = result
		if result.Origin == "polish" && result.EditMethod == "polish" && result.PolishCompletion != nil {
			taskID := result.PolishCompletion.TaskID
			if submissions[taskID] == nil {
				submissions[taskID] = map[string]document.Result{}
			}
			submissions[taskID][result.ID] = result
		}
	}); err != nil {
		return audit, fmt.Errorf("读取作业历史: %w", err)
	}
	audit = Audit{States: status.Polish, Workers: []AuditWorker{}, Tasks: []AuditTask{}, Warnings: []AuditWarning{}}
	workers := map[string]*AuditWorker{}
	tasks := map[string]*AuditTask{}
	allDone := map[string]bool{}
	for _, line := range status.Lines {
		result, ok := job.Done[line.ID]
		if !ok {
			continue
		}
		for _, issue := range result.Warnings {
			if issue.Code == "traditional" || issue.Code == "latin" {
				audit.Warnings = append(audit.Warnings, AuditWarning{ID: line.ID, Issue: issue})
			}
		}
		completion := result.PolishCompletion
		if completion == nil {
			continue
		}
		size := completion.TaskSize
		if size == 0 && completion.Scope == "task" {
			first, firstOK := job.SegmentIndex(completion.BlockStart)
			last, lastOK := job.SegmentIndex(completion.BlockEnd)
			if firstOK && lastOK && first <= last && job.Segments[first].Group == job.Segments[last].Group {
				size = last - first + 1
			}
		}
		task := tasks[completion.TaskID]
		if task == nil {
			task = &AuditTask{TaskID: completion.TaskID, Worker: completion.Worker, Model: completion.Model, IDs: []string{}, OriginalSize: size}
			tasks[completion.TaskID] = task
			allDone[completion.TaskID] = true
		}
		if task.Worker != completion.Worker || task.Model != completion.Model || task.OriginalSize != size {
			allDone[completion.TaskID] = false
		}
		task.IDs = append(task.IDs, line.ID)
		if line.Polish.State != "done" {
			allDone[completion.TaskID] = false
		}
		key := completion.Worker + "\x00" + completion.Model
		worker := workers[key]
		if worker == nil {
			worker = &AuditWorker{Worker: completion.Worker, Model: completion.Model}
			workers[key] = worker
		}
		worker.Lines++
		submitted, exists := submissions[completion.TaskID][line.ID]
		before, found := history[submitted.PreviousHash]
		if !exists || !found || before.ID != line.ID || submitted.PreviousHash == "" {
			task.Unknown++
			worker.Unknown++
		} else if submitted.Dst == before.Dst {
			task.Unchanged++
			worker.Unchanged++
		} else {
			task.Changed++
			worker.Changed++
		}
	}
	for id, task := range tasks {
		task.AllUnchanged = allDone[id] && task.OriginalSize > 0 && len(task.IDs) == task.OriginalSize && task.Unchanged == task.OriginalSize
		audit.Tasks = append(audit.Tasks, *task)
	}
	for _, worker := range workers {
		if known := worker.Changed + worker.Unchanged; known > 0 {
			rate := float64(worker.Changed) / float64(known)
			worker.ChangeRate = &rate
		}
		audit.Workers = append(audit.Workers, *worker)
	}
	sort.Slice(audit.Tasks, func(i, j int) bool { return audit.Tasks[i].TaskID < audit.Tasks[j].TaskID })
	sort.Slice(audit.Workers, func(i, j int) bool {
		if audit.Workers[i].Worker == audit.Workers[j].Worker {
			return audit.Workers[i].Model < audit.Workers[j].Model
		}
		return audit.Workers[i].Worker < audit.Workers[j].Worker
	})
	return audit, nil
}
