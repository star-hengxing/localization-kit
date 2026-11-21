package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/quality"
	"localization-kit/src/internal/store"
)

func reviewFixture(t *testing.T) (Options, string) {
	t.Helper()
	job, segments := scopeFixture(t, 2)
	root := t.TempDir()
	input, output := filepath.Join(root, "material.json"), filepath.Join(root, "report.json")
	if _, err := quality.PrepareReview(quality.ReviewOptions{JobPath: job, Kind: "fidelity", Output: input, BatchSize: 2}); err != nil {
		t.Fatal(err)
	}
	material, err := quality.ReadReviewMaterial(input)
	if err != nil {
		t.Fatal(err)
	}
	report := quality.ReviewReport{MaterialHash: material.Hash, Items: []quality.ReviewVerdict{
		{ID: segments[0].ID, Verdict: "issues", Issues: []quality.ReviewIssue{{Code: "omission", Detail: "核对语气", Evidence: []quality.ReviewEvidence{{SegmentID: segments[0].ID, Quote: segments[0].Src}}}}},
		{ID: segments[1].ID, Verdict: "pass"},
	}}
	if err := jsonfile.Create(output, report); err != nil {
		t.Fatal(err)
	}
	return Options{Job: job, Stage: "polish", Worker: "editor", Model: "model", Scope: "task", TTL: time.Hour, MaxLines: 1, Reviews: []ReviewFiles{{Input: input, Report: output}}}, output
}

func TestReviewNotesCompleteAfterReviewFlagCleared(t *testing.T) {
	options, _ := reviewFixture(t)
	job, err := store.OpenExisting(options.Job)
	if err != nil {
		t.Fatal(err)
	}
	row := job.Done["0"]
	row.NeedsReview, row.ReviewSignature, row.ReviewTermsHash = true, row.Signature, row.TermsHash
	if err := job.Append(row); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	task, err := Next(options)
	if err != nil || task.Status != "ready" || !task.Lines[0].Reviews[0].Stale {
		t.Fatalf("review task=%+v err=%v", task, err)
	}
	if err := Submit(options.Job, options.Worker, task.TaskID, []Submission{{ID: "0", Dst: task.Lines[0].Dst}}); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(options.Job, true, "0")
	if err != nil || status.Lines[0].Polish.State != "done" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestProofreadRetainsPolishWithReviewNotes(t *testing.T) {
	options, _ := reviewFixture(t)
	task, err := Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if err := Submit(options.Job, options.Worker, task.TaskID, []Submission{{ID: "0", Dst: task.Lines[0].Dst}}); err != nil {
		t.Fatal(err)
	}
	options.Stage, options.Worker, options.Reviews = "proofread", "proofreader", nil
	task, err = Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	rows := []Submission{{ID: "0", Dst: "您好", Reason: "语气", Issues: []document.StageIssue{{Code: "tone", Detail: "语气", Quote: task.Lines[0].Src}}}}
	if err := Submit(options.Job, options.Worker, task.TaskID, rows); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(options.Job, true, "0")
	if err != nil || status.Lines[0].Polish.State != "done" || status.Lines[0].Proofread.State != "done" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestReviewRejectsChangedCharacterFacts(t *testing.T) {
	job, _ := scopeFixture(t, 2)
	root := t.TempDir()
	cards, input, output := filepath.Join(root, "cards.json"), filepath.Join(root, "review.json"), filepath.Join(root, "report.json")
	writeTestCards(t, job, cards, "名字")
	if _, err := quality.PrepareReview(quality.ReviewOptions{JobPath: job, Kind: "subject", Output: input, CardsPath: cards, BatchSize: 20}); err != nil {
		t.Fatal(err)
	}
	material, err := quality.ReadReviewMaterial(input)
	if err != nil {
		t.Fatal(err)
	}
	report := quality.ReviewReport{MaterialHash: material.Hash, Items: []quality.ReviewVerdict{{ID: "0", Verdict: "pass"}, {ID: "1", Verdict: "pass"}}}
	if err := jsonfile.Create(output, report); err != nil {
		t.Fatal(err)
	}
	var current quality.CardFile
	if err := jsonfile.Read(cards, &current); err != nil {
		t.Fatal(err)
	}
	current.Characters[0].NameDst = "新译名"
	data, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cards, data, 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{Job: job, Stage: "polish", Worker: "editor", Model: "model", Cards: cards, Scope: "task", TTL: time.Hour, MaxLines: 1, Reviews: []ReviewFiles{{Input: input, Report: output}}}
	if _, err := Next(options); err == nil {
		t.Fatal("review accepted with changed character facts")
	}
}

func TestReviewNotesFollowTaskAndMarkChangedContext(t *testing.T) {
	options, _ := reviewFixture(t)
	task, err := Next(options)
	if err != nil || task.Status != "ready" || len(task.Lines) != 1 || len(task.Lines[0].Reviews) != 1 || task.Lines[0].Reviews[0].Stale || len(task.Lines[0].Reviews[0].Issues) != 1 {
		t.Fatalf("first task: %+v, %v", task, err)
	}
	if err := Submit(options.Job, options.Worker, task.TaskID, []Submission{{ID: task.Lines[0].ID, Dst: "您好", Reason: "语气"}}); err != nil {
		t.Fatal(err)
	}
	task, err = Next(options)
	if err != nil || task.Status != "ready" || task.Lines[0].ID != "1" || len(task.Lines[0].Reviews) != 1 || !task.Lines[0].Reviews[0].Stale {
		t.Fatalf("changed context was not identified: %+v, %v", task, err)
	}
	if err := Submit(options.Job, options.Worker, task.TaskID, []Submission{{ID: task.Lines[0].ID, Dst: task.Lines[0].Dst}}); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(options.Job, true, "")
	if err != nil || status.Polish["done"] != 2 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestReviewChangeInvalidatesClaimAndMissingReportIsUnknown(t *testing.T) {
	options, reportPath := reviewFixture(t)
	task, err := Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	report, err := quality.ReadReviewReport(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	report.Items[0].Issues[0].Detail = "不同意见"
	if err := os.Remove(reportPath); err != nil {
		t.Fatal(err)
	}
	if err := jsonfile.Create(reportPath, report); err != nil {
		t.Fatal(err)
	}
	if err := Submit(options.Job, options.Worker, task.TaskID, []Submission{{ID: task.Lines[0].ID, Dst: task.Lines[0].Dst}}); err == nil {
		t.Fatal("changed review accepted after claim")
	}
	if err := os.Remove(reportPath); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(options.Job, false, "")
	if err != nil || status.Polish["unknown"] != 2 {
		t.Fatalf("missing report not surfaced: %+v, %v", status, err)
	}
}
