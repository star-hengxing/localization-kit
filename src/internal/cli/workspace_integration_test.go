package cli

import (
	"context"
	"encoding/json"
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
)

func configureWorkspaceModel(t *testing.T, root, modelConfig string) {
	t.Helper()
	path := filepath.Join(root, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("\n"+modelConfig)...), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceStagesAndExports(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "input.jsonl"), []byte("{\"id\":\"1\",\"src\":\"こんにちは\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
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
	modelConfig := "[models.test]\nurl='" + server.URL + "'\nmodel='test'\nprofile='hy-mt2'\n"
	if err := run(context.Background(), []string{"init", root, "--adapter", "jsonl"}); err != nil {
		t.Fatal(err)
	}
	configureWorkspaceModel(t, root, modelConfig)
	if err := run(context.Background(), []string{"translate", root, "--model", "test"}); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	opts := agent.Options{Job: state, Stage: "polish", Worker: "editor", Model: "editor", TTL: time.Hour, Context: 5, MaxLines: 20, Glossaries: []string{filepath.Join(root, "materials", "glossary.json")}}
	task, err := agent.Next(opts)
	if err != nil || task.Status != "ready" {
		t.Fatalf("polish: %+v %v", task, err)
	}
	submissionFile := filepath.Join(t.TempDir(), "submission.json")
	payload, err := json.Marshal([]agent.Submission{{ID: "1", Dst: "您好", Reason: "语气"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(submissionFile, payload, 0600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(root, "config.toml")
	originalConfig, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, append(originalConfig, []byte("\ncards='materials/new.json'\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	submit := []string{"submit", root, "--worker", "editor", "--task", task.TaskID, "--file", submissionFile}
	if err := run(context.Background(), submit); err == nil {
		t.Fatal("submit accepted changed materials")
	}
	if err := os.WriteFile(configFile, originalConfig, 0600); err != nil {
		t.Fatal(err)
	}
	inputFile := filepath.Join(root, "source", "input.jsonl")
	if err := os.WriteFile(inputFile, []byte("{\"id\":\"1\",\"src\":\"さようなら\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), submit); err == nil {
		t.Fatal("submit accepted changed source")
	}
	if err := os.WriteFile(inputFile, []byte("{\"id\":\"1\",\"src\":\"こんにちは\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"next", root, "--stage", "polish", "--worker", "other", "--model", "editor", "-cards", "other.json"}); err == nil {
		t.Fatal("single-dash cards override accepted")
	}
	if err := agent.Submit(state, "editor", task.TaskID, []agent.Submission{{ID: "1", Dst: "您好", Reason: "语气"}}); err != nil {
		t.Fatal(err)
	}
	opts.Stage, opts.Worker, opts.Model = "proofread", "reader", "reader"
	task, err = agent.Next(opts)
	if err != nil || task.Status != "ready" {
		t.Fatalf("proofread: %+v %v", task, err)
	}
	if err := agent.Submit(state, "reader", task.TaskID, []agent.Submission{{ID: "1", Dst: "您好吗", Reason: "补充语气", Issues: []document.StageIssue{{Code: "tone", Detail: "语气", Quote: "こんにちは"}}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ stage, want string }{{"translate", "你好"}, {"polish", "您好"}, {"proofread", "您好吗"}} {
		data, err := os.ReadFile(filepath.Join(state, tc.stage, "translation.json"))
		if err != nil {
			t.Fatal(err)
		}
		var snapshot struct {
			Results []document.Result `json:"results"`
		}
		if err := json.Unmarshal(data, &snapshot); err != nil || len(snapshot.Results) != 1 || snapshot.Results[0].Dst != tc.want {
			t.Fatalf("%s snapshot=%s err=%v", tc.stage, data, err)
		}
		out := filepath.Join(t.TempDir(), tc.stage+".jsonl")
		if err := run(context.Background(), []string{"export", root, "--stage", tc.stage, "--out", out}); err != nil {
			t.Fatalf("%s export: %v", tc.stage, err)
		}
		data, err = os.ReadFile(out)
		if err != nil || !strings.Contains(string(data), tc.want) {
			t.Fatalf("%s export=%s err=%v", tc.stage, data, err)
		}
	}
	second := t.TempDir()
	if err := os.Mkdir(filepath.Join(second, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "source", "input.jsonl"), []byte("{\"id\":\"1\",\"src\":\"こんにちは\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"init", second, "--adapter", "jsonl"}); err != nil {
		t.Fatal(err)
	}
	configureWorkspaceModel(t, second, modelConfig)
	if err := run(context.Background(), []string{"terms", "prepare", root, "-job", filepath.Join(second, "state")}); err == nil {
		t.Fatal("single-dash job override accepted")
	}
	if err := run(context.Background(), []string{"translate", root, "--job", filepath.Join(second, "state")}); err == nil {
		t.Fatal("workspace translation accepted storage override")
	}
	before := calls.Load()
	if err := run(context.Background(), []string{"translate", second, "--model", "test", "--previous", root}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("reused entry was translated again")
	}
	for _, tc := range []struct{ stage, want string }{{"translate", "你好"}, {"polish", "您好"}, {"proofread", "您好吗"}} {
		data, err := os.ReadFile(filepath.Join(second, "state", tc.stage, "translation.json"))
		if err != nil || !strings.Contains(string(data), tc.want) {
			t.Fatalf("reused %s: %s %v", tc.stage, data, err)
		}
	}
	reused := filepath.Join(t.TempDir(), "reused.jsonl")
	if err := run(context.Background(), []string{"export", second, "--stage", "translate", "--out", reused}); err != nil {
		t.Fatalf("reused initial: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "materials", "glossary.json"), []byte(`{"entries":[{"src":"こんにちは","dst":"您好","kind":"term"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, []byte("adapter='jsonl'\ninput='source/input.jsonl'\nglossaries=['materials/glossary.json']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "stale.jsonl")
	if err := run(context.Background(), []string{"export", root, "--stage", "translate", "--out", out}); err == nil {
		t.Fatal("changed glossary accepted without retranslation")
	}
	if err := run(context.Background(), []string{"status", root}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, []byte("adapter='jsonl'\ninput='source/input.jsonl'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Redo(state, "translate", []string{"1"}); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"polish", "proofread"} {
		out := filepath.Join(t.TempDir(), stage+".jsonl")
		if err := run(context.Background(), []string{"export", root, "--stage", stage, "--out", out}); err == nil {
			t.Fatalf("stale %s was exported", stage)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("stale %s published: %v", stage, err)
		}
	}
}
