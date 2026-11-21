package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"localization-kit/src/internal/quality"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
)

func TestReviewRunResumesOnlyValidatedBatches(t *testing.T) {
	root := t.TempDir()
	jobPath := filepath.Join(root, "job")
	segments := []document.Segment{{ID: "one", Group: "a", Src: "こんにちは"}, {ID: "two", Group: "b", Src: "ありがとう"}}
	job, err := store.OpenOrCreate(jobPath, segments)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range segments {
		if err := job.Append(document.Result{ID: s.ID, Src: s.Src, Dst: "你好", Status: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	material := filepath.Join(root, "review.json")
	if err := prepareReview([]string{"--job", jobPath, "--kind", "fidelity", "--out", material}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			fmt.Fprint(w, `{"n_ctx":8192}`)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1,2,3]}`)
		case "/v1/chat/completions":
			calls++
			var request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 || !strings.Contains(request.Messages[0].Content, "只返回 JSON 对象") || strings.Contains(request.Messages[0].Content, "material_hash,candidate_hash") {
				t.Errorf("invalid prompt: %+v err=%v", request, err)
			}
			id := "one"
			if calls >= 2 {
				id = "two"
			}
			content := fmt.Sprintf(`{"items":[{"id":%q,"verdict":"pass"}]}`, id)
			if calls == 2 {
				content = `{"items":[]}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "quality", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(root, "models.toml")
	config := fmt.Sprintf("[models.quality]\nurl = %q\nmodel = 'quality'\n", server.URL)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	runDir, output := filepath.Join(root, "run"), filepath.Join(root, "report.json")
	args := []string{"--job", jobPath, "--input", material, "--model", "quality", "--run-dir", runDir, "--out", output}
	cfg := loadModelConfig(t, configPath)
	if err := runReviewModel(context.Background(), args, configPath, cfg); err == nil || calls != 2 {
		t.Fatalf("invalid second batch accepted, calls=%d err=%v", calls, err)
	}
	if err := os.Remove(filepath.Join(runDir, "0002-response-0001.json")); err != nil {
		t.Fatal(err)
	}
	if err := runReviewModel(context.Background(), args, configPath, cfg); err == nil || calls != 2 {
		t.Fatalf("unfinished request retried without approval, calls=%d err=%v", calls, err)
	}
	if err := runReviewModel(context.Background(), append(args, "--retry-failed"), configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("successful batch was rerun, calls=%d", calls)
	}
	report, err := quality.ReadReviewReport(output)
	if err != nil || len(report.Items) != 2 {
		t.Fatalf("invalid report: %+v, %v", report, err)
	}
	changed := append(append([]string{}, args...), "--out", filepath.Join(root, "other-report.json"), "--max-output-tokens", "4096")
	if err := runReviewModel(context.Background(), changed, configPath, cfg); err == nil || calls != 3 {
		t.Fatalf("changed output budget reused old batches: calls=%d err=%v", calls, err)
	}
}
