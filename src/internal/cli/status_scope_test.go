package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/format/jsonl"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
)

func TestExportReportsStaleTaskScopedPolish(t *testing.T) {
	data := "{\"id\":\"1\",\"group\":\"g\",\"src\":\"こんにちは\"}\n{\"id\":\"2\",\"group\":\"g\",\"src\":\"ありがとう\"}\n"
	root := initJSONLWorkspace(t, data)
	input, jobPath := filepath.Join(root, "source", "input.jsonl"), filepath.Join(root, "state")
	segments, err := jsonl.LoadJSONL(input)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.OpenOrCreate(jobPath, segments)
	if err != nil {
		t.Fatal(err)
	}
	terms := translate.TermsHashes(segments, nil)
	for i, dst := range []string{"你好", "谢谢"} {
		if err := job.Append(document.Result{ID: segments[i].ID, Src: segments[i].Src, Dst: dst, Status: "done", Signature: "sig", TermsHash: terms["g"], Model: "test", Profile: "hy-mt2"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	options := agent.Options{Job: jobPath, Stage: "polish", Worker: "editor", Model: "test", Scope: "task", TTL: time.Hour, MaxLines: 2, Glossaries: []string{filepath.Join(root, "materials", "glossary.json")}}
	task, err := agent.Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if err := agent.Submit(jobPath, "editor", task.TaskID, []agent.Submission{{ID: "1", Dst: "你好"}, {ID: "2", Dst: "谢谢"}}); err != nil {
		t.Fatal(err)
	}
	export := func(name string) error {
		return run(context.Background(), []string{"export", root, "--stage", "polish", "--out", filepath.Join(root, name)})
	}
	if err := export("valid.jsonl"); err != nil {
		t.Fatalf("valid polish export: %v", err)
	}
	assertRefused := func(name, reason string) {
		t.Helper()
		if err := export(name); err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("expected %s export to fail with %q: %v", name, reason, err)
		}
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("invalid export was published: %v", err)
		}
	}
	job, err = store.OpenReadOnly(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	hash := document.ResultHash(job.Done["2"])
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if err := translate.ApplyEdits(jobPath, []translate.ManualEdit{{ID: "2", ExpectedHash: hash, Dst: "多谢"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	status, err := agent.Inspect(jobPath, true, "")
	if err != nil || status.Lines[0].Polish.State != "review" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	assertRefused("stale.jsonl", "review")
	claimsPath := filepath.Join(jobPath, "status.json")
	claimsBefore, err := os.ReadFile(claimsPath)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsBefore, &claims); err != nil {
		t.Fatal(err)
	}
	delete(claims["stages"].(map[string]any), "polish")
	missingConfig, _ := json.Marshal(claims)
	if err := os.WriteFile(claimsPath, missingConfig, 0600); err != nil {
		t.Fatal(err)
	}
	assertRefused("unknown.jsonl", "unknown")
	if err := os.WriteFile(claimsPath, claimsBefore, 0600); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenExisting(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	blocked := job.Done["1"]
	blocked.TermsHash = "different-terms"
	if err := job.Append(blocked); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	status, err = agent.Inspect(jobPath, true, "")
	if err != nil || status.Lines[0].Polish.State != "blocked" {
		t.Fatalf("blocked status=%+v err=%v", status, err)
	}
	assertRefused("blocked.jsonl", "blocked")
}
