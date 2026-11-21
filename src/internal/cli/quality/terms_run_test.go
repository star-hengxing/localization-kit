package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"localization-kit/src/internal/translate/rules"
)

func TestTermsRunWritesEntriesWithoutSourceBatches(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.jsonl")
	if err := os.WriteFile(source, []byte("{\"id\":\"1\",\"src\":\"先生、おはよう。\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			fmt.Fprint(w, `{"n_ctx":8192}`)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1,2,3]}`)
		case "/v1/chat/completions":
			content := `{"items":[{"src":"先生","dst":"老师","kind":"term","constraint":"hard","evidence":[{"segment_id":"1","quote":"先生"}]}]}`
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "quality", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := filepath.Join(root, "models.toml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("[models.quality]\nurl=%q\nmodel='quality'\n", server.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "candidates.json")
	if err := runTermModel(context.Background(), []string{"--source", source, "--model", "quality", "--run-dir", filepath.Join(root, "run"), "--out", output}, config, loadModelConfig(t, config), nil); err != nil {
		t.Fatal(err)
	}
	entries, err := rules.LoadGlossaries([]string{output})
	if err != nil || len(entries) != 1 || entries[0].Src != "先生" || entries[0].Dst != "老师" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || raw["entries"] == nil {
		t.Fatalf("unexpected fields: %s", data)
	}
}

func TestCharactersRejectsModelRun(t *testing.T) {
	if err := Characters(context.Background(), []string{"run"}); err == nil {
		t.Fatal("character model run was accepted")
	}
}

func TestTermsRunKeepsConflictingCandidates(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.jsonl")
	if err := os.WriteFile(source, []byte("{\"id\":\"1\",\"src\":\"先生です\"}\n{\"id\":\"2\",\"src\":\"先生だ\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			fmt.Fprint(w, `{"n_ctx":8192}`)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1,2,3]}`)
		case "/v1/chat/completions":
			count++
			word := "老师"
			if count == 2 {
				word = "先生"
			}
			content := fmt.Sprintf(`{"items":[{"src":"先生","dst":%q,"kind":"term","constraint":"hard","evidence":[{"segment_id":%q,"quote":"先生"}]}]}`, word, fmt.Sprint(count))
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "quality", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := filepath.Join(root, "models.toml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("[models.quality]\nurl=%q\nmodel='quality'\n", server.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "candidates.json")
	if err := runTermModel(context.Background(), []string{"--source", source, "--model", "quality", "--run-dir", filepath.Join(root, "run"), "--out", output}, config, loadModelConfig(t, config), nil); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Entries []rules.Entry `json:"entries"`
	}
	data, err := os.ReadFile(output)
	if err != nil || json.Unmarshal(data, &result) != nil || len(result.Entries) != 2 || result.Entries[0].Dst == result.Entries[1].Dst {
		t.Fatalf("candidates=%s err=%v", data, err)
	}
}

func TestTermsRunStopsWhenWorkConfigChanges(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.jsonl")
	if err := os.WriteFile(source, []byte("{\"id\":\"1\",\"src\":\"先生です\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			fmt.Fprint(w, `{"n_ctx":8192}`)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1,2,3]}`)
		case "/v1/chat/completions":
			changed.Store(true)
			content := `{"items":[{"src":"先生","dst":"老师","kind":"term","constraint":"hard","evidence":[{"segment_id":"1","quote":"先生"}]}]}`
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "quality", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := filepath.Join(root, "models.toml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("[models.quality]\nurl=%q\nmodel='quality'\n", server.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "candidates.json")
	validate := func() error {
		if changed.Load() {
			return fmt.Errorf("config changed")
		}
		return nil
	}
	err := runTermModel(context.Background(), []string{"--source", source, "--model", "quality", "--run-dir", filepath.Join(root, "run"), "--out", output}, config, loadModelConfig(t, config), validate)
	if err == nil {
		t.Fatal("changed work config accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("candidate published: %v", err)
	}
}
