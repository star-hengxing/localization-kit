package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
)

func TestMain(m *testing.M) {
	if err := rules.LoadSimplified(filepath.Join("..", "..", "..", "data", "simplified.json")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestBatchChangesHistoryDependentLines(t *testing.T) {
	dir, _ := fixture(t, true)
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "editor-model", TTL: time.Hour, MaxLines: 20, Context: 5}
	task, err := Next(options)
	if err != nil || task.Status != "ready" || len(task.Lines) != 2 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	submissions := []Submission{{ID: "1", Dst: "您好", Reason: "语气"}, {ID: "2", Dst: "多谢", Reason: "语气"}}
	if err := Submit(dir, options.Worker, task.TaskID, submissions); err != nil {
		t.Fatal(err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, submissions); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if done, err := Next(options); err != nil || done.Status != "done" {
		t.Fatalf("next=%+v err=%v", done, err)
	}
	job, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if got := job.Done["2"]; got.NeedsReview || got.HistoryHash != translate.HistoryDigest(job, job.Segments[1]) {
		t.Fatalf("history result=%+v", got)
	}
}

func TestBatchProofreadingAfterPolish(t *testing.T) {
	dir, segments := fixture(t, false)
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "polisher", TTL: time.Hour, MaxLines: 20, Context: 5}
	task, err := Next(options)
	if err != nil || len(task.Lines) != 2 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{{ID: "1", Dst: "你好"}, {ID: "2", Dst: "谢谢"}}); err != nil {
		t.Fatal(err)
	}
	options.Stage, options.Model = "proofread", "proofreader"
	task, err = Next(options)
	if err != nil || len(task.Lines) != 2 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{{ID: "1", Dst: "您好", Reason: "称呼", Issues: []document.StageIssue{{Code: "tone", Detail: "语气", Quote: segments[0].Src}}}, {ID: "2", Dst: "谢谢"}}); err != nil {
		t.Fatal(err)
	}
	if done, err := Next(options); err != nil || done.Status != "done" {
		t.Fatalf("proofread=%+v err=%v", done, err)
	}
	options.Stage, options.Model = "polish", "polisher"
	if done, err := Next(options); err != nil || done.Status != "done" {
		t.Fatalf("polish=%+v err=%v", done, err)
	}
}

func TestPartiallyWrittenBatchIsReissued(t *testing.T) {
	dir, _ := fixture(t, false)
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", TTL: time.Hour, MaxLines: 20, Context: 5}
	task, err := Next(options)
	if err != nil || len(task.Lines) != 2 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	submissions := []Submission{{ID: "1", Dst: "你好"}, {ID: "2", Dst: "谢谢"}}
	digest, err := jsonfile.Digest(submissions)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := job.Done["1"]
	result.PolishCompletion = &document.StageCompletion{TaskID: task.TaskID, Worker: options.Worker, SubmissionHash: digest}
	if err := job.Append(result); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, submissions); err == nil {
		t.Fatal("partial write reported as complete")
	}
	other := options
	other.Worker = "another-editor"
	reissued, err := Next(other)
	if err != nil || reissued.Status != "ready" || reissued.TaskID == task.TaskID {
		t.Fatalf("reissued=%+v err=%v", reissued, err)
	}
}

func TestSubmitChangesConfirmsUnchangedAndCanRetry(t *testing.T) {
	dir, _ := fixture(t, true)
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", TTL: time.Hour, MaxLines: 20, Context: 5}
	task, err := Next(options)
	if err != nil || len(task.Lines) != 2 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	first, second := task.Lines[0], task.Lines[1]
	edit := Submission{ID: first.ID, Dst: "您好", Reason: "语气"}
	for name, changes := range map[string][]Submission{
		"null":       nil,
		"foreign ID": {{ID: "foreign", Dst: "您好", Reason: "语气"}},
		"duplicate":  {edit, edit},
		"same text":  {{ID: first.ID, Dst: first.Dst}},
	} {
		if err := SubmitChangesWithValidation(dir, options.Worker, task.TaskID, changes, nil); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if err := SubmitChangesWithValidation(dir, "another", task.TaskID, []Submission{edit}, nil); err == nil {
		t.Fatal("accepted another worker's submission")
	}
	for range 2 {
		if err := SubmitChangesWithValidation(dir, options.Worker, task.TaskID, []Submission{edit}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := SubmitChangesWithValidation(dir, options.Worker, task.TaskID, []Submission{}, nil); err == nil {
		t.Fatal("accepted different changes for completed task")
	}
	job, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	updated, unchanged := job.Done[first.ID], job.Done[second.ID]
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if updated.Dst != edit.Dst || unchanged.Dst != second.Dst || unchanged.PolishCompletion == nil || unchanged.PolishCompletion.TaskID != task.TaskID {
		t.Fatalf("updated=%+v unchanged=%+v", updated, unchanged)
	}
	options.Stage, options.Worker = "proofread", "reader"
	task, err = Next(options)
	if err != nil || len(task.Lines) != 2 {
		t.Fatalf("proofread=%+v err=%v", task, err)
	}
	edit.Dst = "您好呀"
	if err := SubmitChangesWithValidation(dir, options.Worker, task.TaskID, []Submission{edit}, nil); err == nil {
		t.Fatal("accepted proofreading change without evidence")
	}
	for range 2 {
		if err := SubmitChangesWithValidation(dir, options.Worker, task.TaskID, []Submission{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if done, err := Next(options); err != nil || done.Status != "done" {
		t.Fatalf("next=%+v err=%v", done, err)
	}
}

func TestSubmitChangesRejectsChangedUneditedLine(t *testing.T) {
	dir, _ := fixture(t, false)
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", TTL: time.Hour, MaxLines: 20, Context: 5}
	task, err := Next(options)
	if err != nil || len(task.Lines) != 2 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	job, err := store.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := job.Done[task.Lines[1].ID]
	result.Dst = "更换后的底稿"
	if err := job.Append(result); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	changes := []Submission{{ID: task.Lines[0].ID, Dst: "您好", Reason: "语气"}}
	if err := SubmitChangesWithValidation(dir, options.Worker, task.TaskID, changes, nil); err == nil {
		t.Fatal("accepted changes after another line's draft changed")
	}
}

func TestBatchContextAndFollowing(t *testing.T) {
	dir := t.TempDir()
	var segments []document.Segment
	for i := 0; i < 12; i++ {
		segments = append(segments, document.Segment{ID: fmt.Sprint(i), Group: "chapter", Src: fmt.Sprintf("原文%d", i)})
	}
	job, err := store.OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	terms := translate.TermsHashes(segments, nil)
	for _, segment := range segments {
		if err := job.Append(document.Result{ID: segment.ID, Src: segment.Src, Dst: "译文", Status: "done", Signature: "s", TermsHash: terms[segment.Group]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", TTL: time.Hour, MaxLines: 4, Context: 2}
	first, err := Next(options)
	if err != nil || len(first.Lines) != 4 || len(first.Context) != 0 || len(first.Following) != 3 || first.Following[0].ID != "4" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	var submissions []Submission
	for _, line := range first.Lines {
		submissions = append(submissions, Submission{ID: line.ID, Dst: line.Dst})
	}
	if err := Submit(dir, options.Worker, first.TaskID, submissions); err != nil {
		t.Fatal(err)
	}
	second, err := Next(options)
	if err != nil || len(second.Lines) != 4 || len(second.Context) != 2 || second.Context[0].ID != "2" || len(second.Following) != 3 || second.Following[0].ID != "8" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestChangedBatchesFinishInOrder(t *testing.T) {
	dir, _ := scopeFixture(t, 60)
	options := Options{Job: dir, Worker: "editor", Model: "model", TTL: time.Hour, MaxLines: 20, Context: 5}
	for _, stage := range []string{"polish", "proofread"} {
		options.Stage = stage
		for count := 0; count < 3; count++ {
			task, err := Next(options)
			if err != nil || task.Status != "ready" || len(task.Lines) != 20 || task.Lines[0].ID != fmt.Sprint(count*20) {
				t.Fatalf("%s batch %d: task=%+v err=%v", stage, count+1, task, err)
			}
			var submissions []Submission
			for _, line := range task.Lines {
				row := Submission{ID: line.ID, Dst: "您好", Reason: "调整语气"}
				if stage == "proofread" {
					row.Dst = "你好呀"
					row.Issues = []document.StageIssue{{Code: "tone", Detail: "使用日常问候", Quote: line.Src}}
				}
				submissions = append(submissions, row)
			}
			if err := Submit(dir, options.Worker, task.TaskID, submissions); err != nil {
				t.Fatal(err)
			}
		}
		if task, err := Next(options); err != nil || task.Status != "done" {
			t.Fatalf("%s after three batches: task=%+v err=%v", stage, task, err)
		}
	}
}

func fixture(t *testing.T, history bool) (string, []document.Segment) {
	t.Helper()
	dir := t.TempDir()
	segments := []document.Segment{{ID: "1", Group: "a", Src: "こんにちは"}, {ID: "2", Group: "a", Src: "ありがとう"}}
	job, err := store.OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	terms := translate.TermsHashes(segments, nil)
	for i, dst := range []string{"你好", "谢谢"} {
		record := document.Result{ID: segments[i].ID, Src: segments[i].Src, Dst: dst, Status: "done", Signature: "signature", TermsHash: terms["a"], Model: "initial", Profile: "hy-mt2"}
		if history && i > 0 {
			record.HistoryUsed = true
			record.HistoryHash = translate.HistoryDigest(job, segments[i])
		}
		if err := job.Append(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, segments
}

func TestNextSubmitAndReviewChangedContext(t *testing.T) {
	dir, _ := fixture(t, true)
	options := Options{Job: dir, Stage: "polish", Worker: "worker-1", Model: "editor", TTL: time.Hour, MaxLines: 1}
	first, err := Next(options)
	if err != nil || first.Status != "ready" || first.Lines[0].ID != "1" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	again, err := Next(options)
	if err != nil || again.TaskID != first.TaskID {
		t.Fatalf("reclaim=%+v err=%v", again, err)
	}
	other := options
	other.Worker = "worker-2"
	if busy, err := Next(other); err != nil || busy.Status != "busy" {
		t.Fatalf("busy=%+v err=%v", busy, err)
	}
	submit := Submission{ID: "1", Dst: "您好", Reason: "调整称呼"}
	if err := Submit(dir, options.Worker, first.TaskID, []Submission{submit}); err != nil {
		t.Fatal(err)
	}
	if err := Submit(dir, options.Worker, first.TaskID, []Submission{submit}); err != nil {
		t.Fatalf("repeat submit: %v", err)
	}
	if err := Submit(dir, options.Worker, first.TaskID, []Submission{Submission{ID: "1", Dst: "不同"}}); err == nil {
		t.Fatal("different repeated submission accepted")
	}
	second, err := Next(options)
	if err != nil || second.Status != "ready" || second.Lines[0].ID != "2" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if err := Submit(dir, options.Worker, second.TaskID, []Submission{Submission{ID: "2", Dst: "多谢", Reason: "调整语气"}}); err != nil {
		t.Fatal(err)
	}
	if next, err := Next(options); err != nil || next.Status != "done" {
		t.Fatalf("later edits reopened an earlier batch: %+v, %v", next, err)
	}
	if _, err := Redo(dir, "polish", []string{"1"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Next(options)
	if err != nil || reopened.Status != "ready" || reopened.Lines[0].ID != "1" {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
	if err := Submit(dir, options.Worker, reopened.TaskID, []Submission{{ID: "1", Dst: "你好", Reason: "更新前文"}}); err != nil {
		t.Fatal(err)
	}
	review, err := Next(options)
	if err != nil || review.Status != "ready" || review.Lines[0].ID != "2" {
		t.Fatalf("earlier edits did not reopen the dependent batch: %+v, %v", review, err)
	}
	if err := Submit(dir, options.Worker, review.TaskID, []Submission{{ID: "2", Dst: review.Lines[0].Dst}}); err != nil {
		t.Fatal(err)
	}
	if next, err := Next(options); err != nil || next.Status != "done" {
		t.Fatalf("stage did not finish after recheck: %+v, %v", next, err)
	}
}

func TestProofreadRecordsEvidenceAndCredits(t *testing.T) {
	dir, segments := fixture(t, false)
	options := Options{Job: dir, Stage: "proofread", Worker: "worker", Model: "proofreader", TTL: time.Hour, MaxLines: 1}
	task, err := Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{Submission{ID: task.Lines[0].ID, Dst: "您好", Reason: "称呼"}}); err == nil {
		t.Fatal("proofread change without evidence accepted")
	}
	submission := Submission{ID: task.Lines[0].ID, Dst: "您好", Reason: "称呼", Issues: []document.StageIssue{{Code: "tone", Detail: "语气", Quote: segments[0].Src}}}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{submission}); err != nil {
		t.Fatal(err)
	}
	job, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	result := job.Done[task.Lines[0].ID]
	if result.Origin != "proofread" || result.ProofreadCompletion == nil || result.ProofreadCompletion.TaskID != task.TaskID || len(result.ProofreadCompletion.Issues) != 1 {
		t.Fatalf("result=%+v", result)
	}
	credits, err := job.ExportCreditLines()
	if err != nil || len(credits) != 2 || credits[1] != "校对: proofreader" {
		t.Fatalf("credits=%v err=%v", credits, err)
	}
}

func TestStaleTaskIsReplacedAndModelChangeReopensStage(t *testing.T) {
	dir, _ := fixture(t, false)
	options := Options{Job: dir, Stage: "polish", Worker: "worker", Model: "model-a", TTL: time.Hour, MaxLines: 1}
	task, err := Next(options)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	changed := job.Done[task.Lines[0].ID]
	changed.Dst = "您好"
	if err := job.Append(changed); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{Submission{ID: task.Lines[0].ID, Dst: "你好"}}); err == nil {
		t.Fatal("stale submission accepted")
	}
	replacement, err := Next(options)
	if err != nil || replacement.Status != "ready" || replacement.TaskID == task.TaskID || replacement.Lines[0].Dst != "您好" {
		t.Fatalf("replacement=%+v err=%v", replacement, err)
	}
	if err := Submit(dir, options.Worker, replacement.TaskID, []Submission{Submission{ID: replacement.Lines[0].ID, Dst: replacement.Lines[0].Dst}}); err != nil {
		t.Fatal(err)
	}
	options.Model = "model-b"
	next, err := Next(options)
	if err != nil || next.Status != "ready" || next.Lines[0].ID != replacement.Lines[0].ID {
		t.Fatalf("changed model task=%+v err=%v", next, err)
	}
}

func TestProofreadPreservesCompletedPolish(t *testing.T) {
	dir, segments := fixture(t, false)
	options := Options{Job: dir, Stage: "polish", Worker: "worker", Model: "polisher", TTL: time.Hour, MaxLines: 1}
	for i := range segments {
		task, err := Next(options)
		if err != nil || task.Status != "ready" || task.Lines[0].ID != segments[i].ID {
			t.Fatalf("polish task=%+v err=%v", task, err)
		}
		if err := Submit(dir, options.Worker, task.TaskID, []Submission{Submission{ID: task.Lines[0].ID, Dst: task.Lines[0].Dst}}); err != nil {
			t.Fatal(err)
		}
	}
	options.Stage, options.Model = "proofread", "proofreader"
	first, err := Next(options)
	if err != nil || first.Status != "ready" || first.Lines[0].ID != "1" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if err := Submit(dir, options.Worker, first.TaskID, []Submission{Submission{ID: "1", Dst: "您好", Reason: "称呼", Issues: []document.StageIssue{{Code: "tone", Detail: "语气", Quote: segments[0].Src}}}}); err != nil {
		t.Fatal(err)
	}
	if status, err := Inspect(dir, false, ""); err != nil || status.Polish["done"] != 2 || status.Proofread["done"] != 1 {
		t.Fatalf("partial proofread status=%+v err=%v", status, err)
	}
	second, err := Next(options)
	if err != nil || second.Status != "ready" || second.Lines[0].ID != "2" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if err := Submit(dir, options.Worker, second.TaskID, []Submission{Submission{ID: "2", Dst: second.Lines[0].Dst}}); err != nil {
		t.Fatal(err)
	}
	if done, err := Next(options); err != nil || done.Status != "done" {
		t.Fatalf("proofread=%+v err=%v", done, err)
	}
	options.Stage, options.Model = "polish", "polisher"
	if next, err := Next(options); err != nil || next.Status != "done" {
		t.Fatalf("completed polish was reopened: %+v err=%v", next, err)
	}
	if _, err := Redo(dir, "polish", []string{"2"}); err != nil {
		t.Fatal(err)
	}
	if status, err := Inspect(dir, true, "2"); err != nil || status.Polish["done"] != 1 || status.Proofread["done"] != 1 {
		t.Fatalf("redo status=%+v err=%v", status, err)
	}
	next, err := Next(options)
	if err != nil || next.Status != "ready" || next.Lines[0].ID != "2" {
		t.Fatalf("reopened polish was not issued: %+v err=%v", next, err)
	}
	if err := Submit(dir, options.Worker, next.TaskID, []Submission{{ID: "2", Dst: next.Lines[0].Dst}}); err != nil {
		t.Fatal(err)
	}
	if next, err := Next(options); err != nil || next.Status != "done" {
		t.Fatalf("resubmitted polish was reopened: %+v err=%v", next, err)
	}
}

func TestConcurrentStageConfigurationAndInterruptedCleanup(t *testing.T) {
	dir, _ := fixture(t, false)
	options := Options{Job: dir, Stage: "polish", Worker: "worker-1", Model: "model-a", TTL: time.Hour, MaxLines: 1}
	task, err := Next(options)
	if err != nil {
		t.Fatal(err)
	}
	other := options
	other.Worker, other.Model = "worker-2", "model-b"
	if _, err := Next(other); err == nil {
		t.Fatal("concurrent stage model conflict accepted")
	}
	state, err := readClaims(dir)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{ID: task.Lines[0].ID, Dst: task.Lines[0].Dst}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{submission}); err != nil {
		t.Fatal(err)
	}
	// Recreate the claim left when a process exits after appending but before cleanup.
	if err := writeClaims(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{submission}); err != nil {
		t.Fatal(err)
	}
	next, err := Next(options)
	if err != nil || next.Status != "ready" || next.Lines[0].ID != "2" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}
