package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/format/jsonl"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
)

func TestExportValidatesSelectedStageContext(t *testing.T) {
	root := t.TempDir()
	resources := filepath.Join("..", "..", "..")
	if err := rules.LoadSimplified(filepath.Join(resources, rules.DefaultDataPath)); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "source.jsonl")
	if err := os.WriteFile(input, []byte("{\"id\":\"1\",\"group\":\"chapter\",\"src\":\"こんにちは\"}\n{\"id\":\"2\",\"group\":\"chapter\",\"src\":\"ありがとう\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	segments, err := jsonl.LoadJSONL(input)
	if err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(root, "state")
	job, err := store.OpenOrCreate(jobPath, segments)
	if err != nil {
		t.Fatal(err)
	}
	termsHash := translate.TermsHashes(segments, nil)["chapter"]
	for i, segment := range segments {
		result := document.Result{ID: segment.ID, Src: segment.Src, Dst: []string{"你好", "谢谢"}[i], Status: "done", Signature: "initial", TermsHash: termsHash}
		if i > 0 {
			result.HistoryUsed = true
			result.HistoryHash = translate.HistoryDigest(job, segment)
		}
		if err := job.Append(result); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}

	complete := func(stage, scope string, size int, rows ...agent.Submission) {
		t.Helper()
		options := agent.Options{Job: jobPath, Stage: stage, Worker: "editor", Model: "test", Scope: scope, TTL: time.Hour, MaxLines: size}
		task, err := agent.Next(options)
		if err != nil || task.Status != "ready" || len(task.Lines) != len(rows) {
			t.Fatalf("claim %s: status=%s lines=%d err=%v", stage, task.Status, len(task.Lines), err)
		}
		if err := agent.Submit(jobPath, options.Worker, task.TaskID, rows); err != nil {
			t.Fatal(err)
		}
	}
	complete("polish", "task", 2, agent.Submission{ID: "1", Dst: "你好"}, agent.Submission{ID: "2", Dst: "谢谢"})
	complete("proofread", "task", 1, agent.Submission{ID: "1", Dst: "你好"})
	opts := ExportOptions{ResourceRoot: resources, JobPath: jobPath, Adapter: "jsonl", Input: input, Stage: "proofread", Output: filepath.Join(root, "unfinished.jsonl")}
	if _, err := Export(opts); err == nil {
		t.Fatal("unfinished proofread exported")
	}
	complete("proofread", "task", 1, agent.Submission{ID: "2", Dst: "谢谢"})
	if _, err := agent.Redo(jobPath, "proofread", []string{"1"}); err != nil {
		t.Fatal(err)
	}
	complete("proofread", "task", 1, agent.Submission{ID: "1", Dst: "您好", Reason: "统一问候语气", Issues: []document.StageIssue{{Code: "register", Detail: "使用礼貌称呼", Quote: "こんにちは"}}})
	opts.Output = filepath.Join(root, "stale-proofread.jsonl")
	if _, err := Export(opts); err == nil {
		t.Fatal("proofread exported with stale preceding context")
	}

	opts.Stage, opts.Output = "polish", filepath.Join(root, "polish.jsonl")
	summary, err := Export(opts)
	if err != nil || summary.Done != 2 {
		t.Fatalf("polish export during proofread: %+v, %v", summary, err)
	}
	exported, err := os.Open(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Close()
	decoder := json.NewDecoder(exported)
	for _, want := range []string{"你好", "谢谢"} {
		var row document.ExportLine
		if err := decoder.Decode(&row); err != nil || row.Dst != want {
			t.Fatalf("polish text: got %q, want %q, err=%v", row.Dst, want, err)
		}
	}
	complete("proofread", "task", 1, agent.Submission{ID: "2", Dst: "谢谢"})
	opts.Stage, opts.Output = "proofread", filepath.Join(root, "verified-proofread.jsonl")
	if summary, err := Export(opts); err != nil || summary.Done != 2 {
		t.Fatalf("proofread export after context recheck: %+v, %v", summary, err)
	}
	opts.Stage, opts.Output = "", filepath.Join(root, "current.jsonl")
	if _, err := Export(opts); err == nil {
		t.Fatal("export accepted a missing stage")
	}
	if _, err := agent.Redo(jobPath, "polish", []string{"2"}); err != nil {
		t.Fatal(err)
	}
	opts.Stage, opts.Output = "polish", filepath.Join(root, "reopened.jsonl")
	if _, err := Export(opts); err == nil {
		t.Fatal("reopened polish exported")
	}
	complete("polish", "task", 2, agent.Submission{ID: "2", Dst: "谢谢"})
	opts.Output = filepath.Join(root, "repolished.jsonl")
	if summary, err := Export(opts); err != nil || summary.Done != 2 {
		t.Fatalf("polish export after partial redo: %+v, %v", summary, err)
	}
}
