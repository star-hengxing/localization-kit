package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/format/jsonl"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/workflow"
)

func TestReusedInitialRejectsChangedSourceGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		lines := 1
		for _, m := range request.Messages {
			if m.Role == "user" {
				lines = strings.Count(m.Content, "\n") + 1
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "test", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": strings.TrimSuffix(strings.Repeat("你好\n", lines), "\n")}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	modelConfig := fmt.Sprintf("[models.test]\nurl=%q\nmodel='test'\nprofile='hy-mt2'\n", server.URL)
	makeWorkspace := func(first string) string {
		t.Helper()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
			t.Fatal(err)
		}
		input := fmt.Sprintf("{\"id\":\"1\",\"group\":\"g\",\"src\":%q}\n{\"id\":\"2\",\"group\":\"g\",\"src\":\"ありがとう\"}\n", first)
		if err := os.WriteFile(filepath.Join(root, "source", "input.jsonl"), []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), []string{"init", root, "--adapter", "jsonl"}); err != nil {
			t.Fatal(err)
		}
		configureWorkspaceModel(t, root, modelConfig)
		return root
	}
	first := makeWorkspace("こんにちは")
	if err := run(context.Background(), []string{"translate", first, "--model", "test"}); err != nil {
		t.Fatal(err)
	}
	opts := agent.Options{Job: filepath.Join(first, "state"), Stage: "polish", Worker: "editor", Model: "editor", TTL: time.Hour, MaxLines: 20, Context: 5}
	task, err := agent.Next(opts)
	if err != nil || task.Status != "ready" {
		t.Fatalf("task: %+v %v", task, err)
	}
	var result []agent.Submission
	for _, line := range task.Lines {
		result = append(result, agent.Submission{ID: line.ID, Dst: "您好", Reason: "润色"})
	}
	if err := agent.Submit(opts.Job, opts.Worker, task.TaskID, result); err != nil {
		t.Fatal(err)
	}
	second := makeWorkspace("こんばんは")
	err = run(context.Background(), []string{"translate", second, "--model", "test", "--previous", first})
	var incomplete *workflow.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected protected result requiring review: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.jsonl")
	if err := run(context.Background(), []string{"export", second, "--stage", "translate", "--out", out}); err == nil {
		t.Fatal("stale initial translation from changed group was exported")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("stale output published: %v", err)
	}
}

func TestPreviouslyImportedWorkspacePreservesHistoryAndDrafts(t *testing.T) {
	ctx := context.Background()
	source := "{\"id\":\"1\",\"group\":\"g1\",\"src\":\"こんにちは\"}\n{\"id\":\"2\",\"group\":\"g2\",\"src\":\"ありがとう\"}\n"
	original := initJSONLWorkspace(t, source)
	originalState := filepath.Join(original, "state")
	segments, err := jsonl.LoadJSONL(filepath.Join(original, "source", "input.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.OpenOrCreate(originalState, segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetInitialCreditLines([]string{"初翻: previous-model"}); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	oldDraft := `{"id":"1","src":"こんにちは","dst":"你好","model":"historical-editor","profile":"","status":"done","origin":"legacy","needs_review":true,"legacy_record":{"provenance":{"driver":"agent","stage":"verify"}}}`
	oldFailure := `{"id":"2","src":"ありがとう","dst":"","model":"","profile":"","status":"failed","origin":"legacy","legacy_record":{"provenance":{"driver":"api"}}}`
	originalHistory := filepath.Join(originalState, "results.jsonl")
	originalRecords := oldDraft + "\n" + oldFailure + "\n"
	if err := os.WriteFile(originalHistory, []byte(originalRecords), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"status", original, "--id", "1"}); err != nil {
		t.Fatal(err)
	}
	state, err = store.OpenReadOnly(originalState)
	if err != nil {
		t.Fatal(err)
	}
	olderHash := document.ResultHash(state.Done["1"])
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte(oldDraft))); olderHash != want {
		t.Fatalf("older draft hash = %s, want %s", olderHash, want)
	}

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"谢谢"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	modelConfig := fmt.Sprintf("[models.test]\nurl=%q\nmodel='test'\nprofile='hy-mt2'\n", server.URL)
	destination := initJSONLWorkspace(t, source)
	configureWorkspaceModel(t, destination, modelConfig)
	var incomplete *workflow.IncompleteError
	if err := run(ctx, []string{"translate", destination, "--previous", original}); !errors.As(err, &incomplete) ||
		incomplete.Summary.Review != 1 || incomplete.Summary.Done != 2 || calls.Load() != 1 {
		t.Fatalf("reuse old result: calls=%d err=%v", calls.Load(), err)
	}
	if history, err := os.ReadFile(originalHistory); err != nil || string(history) != originalRecords {
		t.Fatalf("original history changed: %v", err)
	}
	state, err = store.OpenReadOnly(filepath.Join(destination, "state"))
	if err != nil {
		t.Fatal(err)
	}
	reused := state.Done["1"]
	credits, readErr := state.CreditLines()
	initialModels, modelErr := state.InitialModels()
	if err := errors.Join(readErr, modelErr, state.Close()); err != nil {
		t.Fatal(err)
	}
	if reused.Dst != "你好" || !reused.NeedsReview || len(reused.LegacyRecord) == 0 || len(credits) != 1 || credits[0] != "初翻: previous-model" || initialModels["1"] != "" {
		t.Fatalf("reused=%+v credits=%v initialModels=%v", reused, credits, initialModels)
	}
	repair := translate.ManualEdit{ID: "1", Dst: "您好", ExpectedHash: document.ResultHash(reused)}
	editFile := filepath.Join(destination, "edit.jsonl")
	payload, err := json.Marshal(repair)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editFile, append(payload, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"edit", destination, "--file", editFile}); err != nil {
		t.Fatal(err)
	}
	state, err = store.OpenReadOnly(filepath.Join(destination, "state"))
	if err != nil {
		t.Fatal(err)
	}
	revised := state.Done["1"]
	initialModels, modelErr = state.InitialModels()
	if err := errors.Join(modelErr, state.Close()); err != nil {
		t.Fatal(err)
	}
	if revised.Dst != repair.Dst || revised.NeedsReview || revised.PreviousHash != repair.ExpectedHash || initialModels["1"] != "" {
		t.Fatalf("revised=%+v initialModels=%v", revised, initialModels)
	}
	if err := run(ctx, []string{"translate", destination}); err != nil || calls.Load() != 1 {
		t.Fatalf("resume after revision: calls=%d err=%v", calls.Load(), err)
	}

	configureWorkspaceModel(t, original, modelConfig)
	if err := run(ctx, []string{"translate", original}); !errors.As(err, &incomplete) || incomplete.Summary.Failed != 1 || calls.Load() != 1 {
		t.Fatalf("imported failure retried without flag: calls=%d err=%v", calls.Load(), err)
	}
	if err := run(ctx, []string{"translate", original, "--retry-failed"}); !errors.As(err, &incomplete) || incomplete.Summary.Failed != 0 || calls.Load() != 2 {
		t.Fatalf("imported failure did not retry: calls=%d err=%v", calls.Load(), err)
	}
}
