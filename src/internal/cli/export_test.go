package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/format/jsonl"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
)

func initJSONLWorkspace(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "source", "input.jsonl")
	if err := os.Mkdir(filepath.Dir(input), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"init", root, "--adapter", "jsonl"}); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStatusAndExportDoNotNeedModelOrChangeJob(t *testing.T) {
	root := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"こんにちは\"}\n")
	configPath := filepath.Join(root, "config.toml")
	configWithoutModel, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	configureWorkspaceModel(t, root, "[models.test]\nurl='"+server.URL+"'\nmodel='test'\nprofile='hy-mt2'\n")
	err = run(context.Background(), []string{"translate", root})
	server.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configWithoutModel, 0600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(root, "state", "results.jsonl")
	before, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"status", root}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "result.jsonl")
	existing := filepath.Join(root, "existing.jsonl")
	if err := os.WriteFile(existing, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	damaged := string(before) + "{\"id\":"
	if err := os.WriteFile(history, []byte(damaged), 0600); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{
		{"--out", output},
		{"--stage", "translate"},
		{"--stage", "unknown", "--out", output},
		{"--stage", "translate", "--out", output, "--unknown"},
		{"--stage", "translate", "--out", output, "extra"},
		{"--stage", "translate", "--out", existing},
		{"--stage", "translate", "--out", filepath.Join(root, "state", "result.jsonl")},
	} {
		if err := run(context.Background(), append([]string{"export", root}, flags...)); err == nil {
			t.Fatalf("export accepted invalid arguments %v", flags)
		}
		if current, err := os.ReadFile(history); err != nil || string(current) != damaged {
			t.Fatalf("export %v repaired history before rejecting arguments: %v", flags, err)
		}
	}
	export := []string{"export", root, "--stage", "translate", "--out", output}
	if err := run(context.Background(), export); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(data), "你好") {
		t.Fatalf("output=%s err=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(root, "state", "credits.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	repeated := filepath.Join(root, "result-with-invalid-credits.jsonl")
	if err := run(context.Background(), []string{"export", root, "--stage", "translate", "--out", repeated}); err != nil {
		t.Fatalf("JSONL export with invalid credits: %v", err)
	}
	if repeatData, err := os.ReadFile(repeated); err != nil || string(repeatData) != string(data) {
		t.Fatalf("JSONL changed with invalid credits: output=%s err=%v", repeatData, err)
	}
	if err := run(context.Background(), export); err == nil {
		t.Fatal("existing output overwritten")
	}
	if current, err := os.ReadFile(output); err != nil || string(data) != string(current) {
		t.Fatalf("output changed: %v", err)
	}
	existingDir := filepath.Join(root, "existing")
	if err := os.Mkdir(existingDir, 0700); err != nil {
		t.Fatal(err)
	}
	export[len(export)-1] = existingDir
	if err := run(context.Background(), export); err == nil {
		t.Fatal("existing directory overwritten")
	}
	if entries, err := os.ReadDir(existingDir); err != nil || len(entries) != 0 {
		t.Fatalf("existing directory changed: entries=%v err=%v", entries, err)
	}
	after, err := os.ReadFile(history)
	if err != nil || string(before) != string(after) {
		t.Fatalf("job changed: %v", err)
	}
}

func TestIncompleteTailRecoveryDependsOnCommandAndArguments(t *testing.T) {
	root := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"こんにちは\"}\n")
	segments, err := jsonl.LoadJSONL(filepath.Join(root, "source", "input.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(root, "state")
	job, err := store.OpenOrCreate(jobPath, segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(jobPath, "results.jsonl")
	for _, tc := range []struct{ command, extra, wantErr string }{
		{"status", "", "末尾不完整"},
		{"results", "--unknown", "用法:"},
		{"audit", "--unknown", "不接受额外参数"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			tail := "{\"id\":"
			if err := os.WriteFile(path, []byte(tail), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{tc.command, root}
			invalidArgs := args
			if tc.extra != "" {
				invalidArgs = append(args, tc.extra)
			}
			if err := run(context.Background(), invalidArgs); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%v: %v", invalidArgs, err)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != tail {
				t.Fatalf("command changed history: %q %v", data, err)
			}
			if tc.extra == "" {
				return
			}
			if err := run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
				t.Fatalf("command failed to recover history: %q %v", data, err)
			}
		})
	}
}

func TestLongResultHistoryCanBeAuditedAndExported(t *testing.T) {
	root := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"123\"}\n")
	segments, err := jsonl.LoadJSONL(filepath.Join(root, "source", "input.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.OpenOrCreate(filepath.Join(root, "state"), segments)
	if err != nil {
		t.Fatal(err)
	}
	segment := segments[0]
	old := document.Result{ID: segment.ID, Src: segment.Src, Dst: strings.Repeat("x", 16<<20), Status: "done"}
	current := document.Result{
		ID: segment.ID, Src: segment.Src, Dst: segment.Src, Status: "done", Passthrough: true,
		TermsHash: translate.TermsHashes(segments, nil)[segment.Group],
	}
	err = job.AppendBatch([]document.Result{old, current})
	if closeErr := job.Close(); err != nil || closeErr != nil {
		t.Fatalf("seed history: append=%v close=%v", err, closeErr)
	}
	output := filepath.Join(root, "result.jsonl")
	for _, args := range [][]string{
		{"status", root},
		{"audit", root},
		{"export", root, "--stage", "translate", "--out", output},
	} {
		t.Run(args[0], func(t *testing.T) {
			if err := run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
		})
	}
	if data, err := os.ReadFile(output); err != nil || !strings.Contains(string(data), "\"dst\":\"123\"") {
		t.Fatalf("exported current result: %q %v", data, err)
	}
}

func TestPublishDirectoryDoesNotReplaceExistingDirectory(t *testing.T) {
	root := t.TempDir()
	stage, output := filepath.Join(root, "stage"), filepath.Join(root, "output")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err := fileutil.PublishDirectory(stage, output); err == nil {
		t.Fatal("existing empty directory was replaced")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("staging directory lost: %v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("existing directory lost: %v", err)
	}
}
