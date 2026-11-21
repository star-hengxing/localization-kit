package agent

import (
	"fmt"
	"testing"
	"time"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
)

func scopeFixture(t *testing.T, count int) (string, []document.Segment) {
	t.Helper()
	dir := t.TempDir()
	segments := make([]document.Segment, count)
	for i := range segments {
		segments[i] = document.Segment{ID: fmt.Sprint(i), Group: "chapter", Src: "こんにちは"}
	}
	job, err := store.OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	terms := translate.TermsHashes(segments, nil)
	for _, segment := range segments {
		if err := job.Append(document.Result{ID: segment.ID, Src: segment.Src, Dst: "你好", Status: "done", Signature: "sig", TermsHash: terms[segment.Group], Model: "initial", Profile: "hy-mt2"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, segments
}

func TestTaskScopeCompletesChangedBlocksWithoutWholeGroupRecheck(t *testing.T) {
	dir, _ := scopeFixture(t, 6)
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 2}
	for batch := 0; batch < 3; batch++ {
		task, err := Next(options)
		if err != nil || task.Status != "ready" || len(task.Lines) != 2 || task.Lines[0].ID != fmt.Sprint(batch*2) {
			t.Fatalf("batch %d: %+v err=%v", batch, task, err)
		}
		rows := []Submission{{ID: task.Lines[0].ID, Dst: "您好", Reason: "语气"}, {ID: task.Lines[1].ID, Dst: task.Lines[1].Dst}}
		if err := Submit(dir, options.Worker, task.TaskID, rows); err != nil {
			t.Fatal(err)
		}
	}
	if task, err := Next(options); err != nil || task.Status != "done" {
		t.Fatalf("scope did not converge: %+v %v", task, err)
	}
	status, err := Inspect(dir, true, "")
	if err != nil || status.Polish["done"] != 6 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestTaskScopeDigestMatchesVisibleWindow(t *testing.T) {
	dir, segments := scopeFixture(t, 8)
	job, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	deps, err := dependencyHashes(job, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := job.Done["2"]
	digest := func() string {
		value, err := scopedDigest(deps, segments[2], job.Done["2"], "polish", "m", "v", 1, 2, "task", "2", "3")
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	original := digest()
	if original == "" {
		t.Fatal("empty initial digest")
	}
	other := job.Done["7"]
	other.Dst = "远处"
	job.Done["7"] = other
	if digest() != original {
		t.Fatal("unseen distant translation changed task digest")
	}
	other = job.Done["4"]
	other.Dst = "后文译文"
	job.Done["4"] = other
	if digest() != original {
		t.Fatal("following translation was not provided to worker")
	}
	other = job.Done["1"]
	other.Dst = "修改前文"
	job.Done["1"] = other
	if digest() == original {
		t.Fatal("visible context change was ignored")
	}
	job.Done["1"] = document.Result{ID: "1", Src: segments[1].Src, Dst: "你好", Status: "done"}
	other = job.Done["3"]
	other.Dst = "同块变更"
	job.Done["3"] = other
	if digest() == original {
		t.Fatal("same-block translation change was ignored")
	}
	if got, err := scopedDigest(deps, segments[2], result, "polish", "m", "v", 1, 2, "task", "missing", "3"); err != nil || got != "" {
		t.Fatalf("invalid task bounds: %q %v", got, err)
	}
}

func TestTaskProofreadPreservesGroupPolish(t *testing.T) {
	dir, segments := scopeFixture(t, 2)
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", Scope: "group", TTL: time.Hour, MaxLines: 2}
	polish, err := Next(options)
	if err != nil || polish.Status != "ready" {
		t.Fatalf("polish=%+v err=%v", polish, err)
	}
	if err := Submit(dir, options.Worker, polish.TaskID, []Submission{{ID: "0", Dst: "你好"}, {ID: "1", Dst: "你好"}}); err != nil {
		t.Fatal(err)
	}
	options.Stage, options.Scope = "proofread", "task"
	proof, err := Next(options)
	if err != nil || proof.Status != "ready" {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}
	rows := []Submission{{ID: "0", Dst: "您好", Reason: "语气", Issues: []document.StageIssue{{Code: "tone", Detail: "调整", Quote: segments[0].Src}}}, {ID: "1", Dst: "你好"}}
	if err := Submit(dir, options.Worker, proof.TaskID, rows); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(dir, true, "")
	if err != nil || status.Polish["done"] != 2 || status.Proofread["done"] != 2 {
		t.Fatalf("task-scope proofread invalidated group polish: %+v %v", status, err)
	}
}

func TestTaskScopePreservesInitialTranslationHistoryReviews(t *testing.T) {
	dir, segments := scopeFixture(t, 6)
	job, err := store.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments[1:] {
		record := job.Done[segment.ID]
		record.HistoryUsed = true
		record.HistoryHash = translate.HistoryDigest(job, segment)
		if err := job.Append(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 2}
	task, err := Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{{ID: "0", Dst: "您好", Reason: "语气"}, {ID: "1", Dst: "你好"}}); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if !job.Done["5"].NeedsReview || !job.Done["5"].ContextReview {
		t.Fatal("task scope bypassed history-dependent review")
	}
}
