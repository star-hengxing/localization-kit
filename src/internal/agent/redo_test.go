package agent

import (
	"os"
	"testing"
	"time"

	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
)

func TestRedoIgnoresClaimAlreadySubmitted(t *testing.T) {
	jobPath, _ := scopeFixture(t, 1)
	options := Options{Job: jobPath, Stage: "polish", Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 1}
	task, err := Next(options)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(statePath(jobPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := Submit(jobPath, options.Worker, task.TaskID, []Submission{{ID: "0", Dst: "你好"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath(jobPath), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Redo(jobPath, "polish", []string{"0"}); err != nil {
		t.Fatalf("completed claim blocked redo: %v", err)
	}
}

func TestRedoStagesPreservesTranslationAndClearsLaterStage(t *testing.T) {
	jobPath, _ := scopeFixture(t, 1)
	options := Options{Job: jobPath, Stage: "polish", Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 1}
	for _, stage := range []string{"polish", "proofread"} {
		options.Stage = stage
		task, err := Next(options)
		if err != nil || task.Status != "ready" {
			t.Fatalf("task=%+v err=%v", task, err)
		}
		if err := Submit(jobPath, options.Worker, task.TaskID, []Submission{{ID: "0", Dst: "你好"}}); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := Redo(jobPath, "polish", []string{"0"}); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	job, err := store.OpenReadOnly(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	result := job.Done["0"]
	if result.Dst != "你好" || result.PolishCompletion != nil || result.ProofreadCompletion != nil || result.PreviousHash == "" {
		t.Fatalf("result=%+v", result)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	options.Stage = "polish"
	task, err := Next(options)
	if err != nil || task.Status != "ready" || task.Lines[0].ID != "0" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
}

func TestRedoProofreadPreservesPolish(t *testing.T) {
	jobPath, _ := scopeFixture(t, 1)
	options := Options{Job: jobPath, Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 1}
	for _, stage := range []string{"polish", "proofread"} {
		options.Stage = stage
		task, err := Next(options)
		if err != nil {
			t.Fatal(err)
		}
		if err := Submit(jobPath, options.Worker, task.TaskID, []Submission{{ID: "0", Dst: "你好"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Redo(jobPath, "proofread", []string{"0"}); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(jobPath, true, "0")
	if err != nil || status.Lines[0].Polish.State != "done" || status.Lines[0].Proofread.State == "done" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestRedoTranslateProtectsDependentHumanResults(t *testing.T) {
	jobPath, segments := scopeFixture(t, 3)
	job, err := store.OpenExisting(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	for i, segment := range segments {
		if i == 0 {
			continue
		}
		result := job.Done[segment.ID]
		result.HistoryUsed = true
		result.HistoryHash = translate.HistoryDigest(job, segment)
		if i == 1 {
			result.Origin = "manual"
		}
		if err := job.Append(result); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Redo(jobPath, "translate", []string{"0"}); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenReadOnly(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if _, ok := job.Done["0"]; ok {
		t.Fatal("target remained completed")
	}
	if row := job.Done["1"]; row.Dst != "你好" || !row.NeedsReview || !row.ContextReview {
		t.Fatalf("manual result not protected: %+v", row)
	}
	if _, ok := job.Done["2"]; ok {
		t.Fatal("dependent machine result remained completed")
	}
}

func TestRedoRejectsActiveTaskAndInvalidIDs(t *testing.T) {
	jobPath, _ := scopeFixture(t, 2)
	options := Options{Job: jobPath, Stage: "polish", Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 1}
	task, err := Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if _, err := Redo(jobPath, "translate", []string{"1"}); err == nil {
		t.Fatal("redo accepted with active group task")
	}
	if _, err := Release(jobPath, options.Worker); err != nil {
		t.Fatal(err)
	}
	if _, err := Redo(jobPath, "translate", []string{"0", "missing"}); err == nil {
		t.Fatal("unknown ID accepted")
	}
	job, err := store.OpenReadOnly(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if len(job.Done) != 2 {
		t.Fatal("invalid request changed completed results")
	}
}
