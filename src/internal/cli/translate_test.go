package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWorkspaceTranslateRejectsMissingResultHistory(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte("{\"model\":\"test\",\"choices\":[{\"message\":{\"content\":\"你好\"},\"finish_reason\":\"stop\"}]}"))
	}))
	defer server.Close()
	root := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"こんにちは\"}\n{\"id\":\"2\",\"src\":\"ありがとう\"}\n")
	configureWorkspaceModel(t, root, "[models.test]\nurl='"+server.URL+"'\nmodel='test'\nprofile='hy-mt2'\n")
	ctx := context.Background()
	args := []string{"translate", root}
	if err := run(ctx, args); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	history := filepath.Join(state, "results.jsonl")
	snapshot := filepath.Join(state, "translate", "translation.json")
	before, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(root, "saved-results.jsonl")
	if err := os.Rename(history, saved); err != nil {
		t.Fatal(err)
	}
	modelCalls := calls.Load()
	if err := run(ctx, args); err == nil || !strings.Contains(err.Error(), "缺少结果历史") {
		t.Fatalf("translate with missing history: %v", err)
	}
	if got := calls.Load(); got != modelCalls {
		t.Fatalf("translate made %d additional model requests", got-modelCalls)
	}
	if _, err := os.Lstat(history); !os.IsNotExist(err) {
		t.Fatalf("translate recreated history: %v", err)
	}
	if after, err := os.ReadFile(snapshot); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("translate changed the stage snapshot: %v", err)
	}
	if err := os.Rename(saved, history); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, args); err != nil {
		t.Fatalf("translate after restoring the history: %v", err)
	}
	if got := calls.Load(); got != modelCalls {
		t.Fatalf("restored job made %d additional model requests", got-modelCalls)
	}
}

func TestIncrementalTranslation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	config := "[models.test]\nurl = \"" + server.URL + "\"\nmodel = \"test\"\nprofile = \"hy-mt2\"\n"
	oldWork := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"こんにちは\"}\n{\"id\":\"2\",\"src\":\"ありがとう\"}\n")
	newWork := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"こんにちは\"}\n{\"id\":\"2\",\"src\":\"こんばんは\"}\n")
	configureWorkspaceModel(t, oldWork, config)
	configureWorkspaceModel(t, newWork, config)
	oldState, newState := filepath.Join(oldWork, "state"), filepath.Join(newWork, "state")
	ctx := context.Background()
	if err := run(ctx, []string{"translate", oldWork, "--model", "test"}); err != nil {
		t.Fatal(err)
	}
	oldBefore, err := os.ReadFile(filepath.Join(oldState, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	legacyLog := filepath.Join(oldState, "runs.jsonl")
	if err := os.WriteFile(legacyLog, []byte("previous log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"translate", newWork, "--model", "test", "--previous", oldWork}); err != nil {
		t.Fatal(err)
	}
	oldAfter, err := os.ReadFile(filepath.Join(oldState, "results.jsonl"))
	if err != nil || string(oldAfter) != string(oldBefore) {
		t.Fatalf("old job changed: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 2 initial calls and 1 new call; got %d", got)
	}
	if data, err := os.ReadFile(legacyLog); err != nil || string(data) != "previous log\n" {
		t.Fatalf("old log changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newState, "runs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("unexpected application log file: %v", err)
	}
}

func TestWorkspaceTranslateRequiresSeparateExport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	root := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"こんにちは\"}\n")
	configureWorkspaceModel(t, root, "[models.test]\nurl='"+server.URL+"'\nmodel='test'\nprofile='hy-mt2'\n")
	ctx := context.Background()
	args := []string{"translate", root}
	if err := run(ctx, args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "results.jsonl")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "output.jsonl")
	for _, flags := range [][]string{{"--out", out}, {"--bilingual"}, {"--no-export"}, {"--previous", root}} {
		if err := run(ctx, append(args, flags...)); err == nil {
			t.Fatalf("translate accepted %v", flags)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("translate created output: %v", err)
	}
	if err := run(ctx, []string{"export", root, "--stage", "translate", "--out", out}); err != nil {
		t.Fatal(err)
	}
}
