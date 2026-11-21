package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
)

func TestAuditReportsUnchangedBlocksAndReopenedLines(t *testing.T) {
	job, _ := scopeFixture(t, 3)
	options := Options{Job: job, Stage: "polish", Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 2}
	first, err := Next(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := Submit(job, options.Worker, first.TaskID, []Submission{{ID: "0", Dst: "你好"}, {ID: "1", Dst: "你好"}}); err != nil {
		t.Fatal(err)
	}
	second, err := Next(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := Submit(job, options.Worker, second.TaskID, []Submission{{ID: "2", Dst: "您好", Reason: "语气"}}); err != nil {
		t.Fatal(err)
	}
	report, err := AuditPolish(job)
	if err != nil || len(report.Workers) != 1 || report.Workers[0].Lines != 3 || report.Workers[0].Changed != 1 || report.Workers[0].Unchanged != 2 || report.Workers[0].ChangeRate == nil {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	unchanged := 0
	for _, task := range report.Tasks {
		if task.AllUnchanged {
			unchanged++
		}
	}
	if unchanged != 1 {
		t.Fatalf("unchanged blocks=%d: %+v", unchanged, report.Tasks)
	}
	if _, err := Redo(job, "polish", []string{"0"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(job, "results.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var retained [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var record document.Result
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.ID == "0" && record.Origin == "polish" && record.PolishCompletion != nil && record.PolishCompletion.TaskID == first.TaskID {
			continue
		}
		retained = append(retained, line)
	}
	if err := os.WriteFile(path, append(bytes.Join(retained, []byte{'\n'}), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = AuditPolish(job)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range report.Tasks {
		if task.TaskID == first.TaskID && (task.AllUnchanged || task.OriginalSize != 2 || len(task.IDs) != 1) {
			t.Fatalf("partial task misreported: %+v", task)
		}
	}
}

func TestAuditOlderTaskSize(t *testing.T) {
	for _, scope := range []string{"task", "group"} {
		t.Run(scope, func(t *testing.T) {
			jobPath, _ := scopeFixture(t, 1)
			options := Options{Job: jobPath, Stage: "polish", Worker: "editor", Model: "model", Scope: scope, TTL: time.Hour, MaxLines: 1}
			task, err := Next(options)
			if err != nil {
				t.Fatal(err)
			}
			if err := Submit(jobPath, options.Worker, task.TaskID, []Submission{{ID: "0", Dst: "你好"}}); err != nil {
				t.Fatal(err)
			}
			job, err := store.OpenExisting(jobPath)
			if err != nil {
				t.Fatal(err)
			}
			result := job.Done["0"]
			completion := *result.PolishCompletion
			completion.TaskSize = 0
			result.PolishCompletion = &completion
			if err := job.Append(result); err != nil {
				t.Fatal(err)
			}
			if err := job.Close(); err != nil {
				t.Fatal(err)
			}
			report, err := AuditPolish(jobPath)
			if err != nil || len(report.Tasks) != 1 || report.Tasks[0].AllUnchanged != (scope == "task") {
				t.Fatalf("audit=%+v err=%v", report, err)
			}
		})
	}
}

func TestAuditUsesPolishSubmissionAfterProofread(t *testing.T) {
	job, segments := scopeFixture(t, 1)
	options := Options{Job: job, Stage: "polish", Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 1}
	task, err := Next(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := Submit(job, options.Worker, task.TaskID, []Submission{{ID: "0", Dst: "你好"}}); err != nil {
		t.Fatal(err)
	}
	options.Stage, options.Worker = "proofread", "checker"
	task, err = Next(options)
	if err != nil {
		t.Fatal(err)
	}
	rows := []Submission{{ID: "0", Dst: "您好", Reason: "语气", Issues: []document.StageIssue{{Code: "tone", Detail: "语气", Quote: segments[0].Src}}}}
	if err := Submit(job, options.Worker, task.TaskID, rows); err != nil {
		t.Fatal(err)
	}
	report, err := AuditPolish(job)
	if err != nil || len(report.Workers) != 1 || report.Workers[0].Unchanged != 1 || report.Workers[0].Changed != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
