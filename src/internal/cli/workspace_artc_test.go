package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/document"
)

func TestWorkspaceArtcStageExports(t *testing.T) {
	for _, scope := range []string{"group", "task"} {
		t.Run(scope, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.MkdirAll(filepath.Join(source, "chapters"), 0700); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{
				"book.toml":       "title='日本語'\nauthor='作者'\nlang='ja'\n",
				"toc.toml":        "[[chapter]]\ntitle='第一章'\nfile='chapters/01.txt'\n",
				"chapters/01.txt": "こんにちは\nありがとうございます\n",
				"cover.svg":       "<svg/>",
			} {
				if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
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
				content := strings.TrimSuffix(strings.Repeat("你好\n", lines), "\n")
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "test", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
			}))
			defer server.Close()
			modelConfig := fmt.Sprintf("[models.test]\nurl=%q\nmodel='test'\nprofile='hy-mt2'\n", server.URL)
			if err := run(context.Background(), []string{"init", root, "--adapter", "artc"}); err != nil {
				t.Fatal(err)
			}
			configureWorkspaceModel(t, root, modelConfig)
			if err := run(context.Background(), []string{"translate", root, "--model", "test"}); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(root, "state")
			glossary := []string{filepath.Join(root, "materials", "glossary.json")}
			for _, stage := range []string{"polish", "proofread"} {
				opts := agent.Options{Job: state, Stage: stage, Worker: stage, Model: stage, Glossaries: glossary, TTL: time.Hour, Context: 5, MaxLines: 20, Scope: scope}
				for {
					task, err := agent.Next(opts)
					if err != nil {
						t.Fatal(err)
					}
					if task.Status == "done" {
						break
					}
					if task.Status != "ready" {
						t.Fatalf("%s: %+v", stage, task)
					}
					var submissions []agent.Submission
					for _, line := range task.Lines {
						text := "润色译文"
						if stage == "proofread" {
							text = "校对译文"
						}
						entry := agent.Submission{ID: line.ID, Dst: text, Reason: "阶段演示"}
						if stage == "proofread" {
							entry.Issues = []document.StageIssue{{Code: "tone", Detail: "调整", Quote: line.Src}}
						}
						submissions = append(submissions, entry)
					}
					if err := agent.Submit(state, opts.Worker, task.TaskID, submissions); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, tc := range []struct{ stage, want string }{{"translate", "你好"}, {"polish", "润色译文"}, {"proofread", "校对译文"}} {
				output := filepath.Join(root, "exports", tc.stage)
				if err := run(context.Background(), []string{"export", root, "--stage", tc.stage, "--out", output, "--bilingual"}); err != nil {
					t.Fatalf("%s: %v", tc.stage, err)
				}
				path := filepath.Join(output, "chapters", "01.txt")
				data, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(data), tc.want) {
					t.Fatalf("%s output=%s %v", tc.stage, data, err)
				}
				original, err := os.ReadFile(filepath.Join(output, "original", "01.txt"))
				if err != nil || string(original) != "こんにちは\nありがとうございます\n" {
					t.Fatalf("%s original=%s %v", tc.stage, original, err)
				}
				cover, err := os.ReadFile(filepath.Join(output, "cover.svg"))
				if err != nil || string(cover) != "<svg/>" {
					t.Fatalf("%s cover=%q %v", tc.stage, cover, err)
				}
			}
			if _, err := agent.Redo(state, "proofread", []string{"book:title", "01:title"}); err != nil {
				t.Fatal(err)
			}
			options := agent.Options{Job: state, Stage: "proofread", Worker: "proofread", Model: "proofread", Glossaries: glossary, TTL: time.Hour, Context: 5, MaxLines: 20, Scope: scope}
			task, err := agent.Next(options)
			if err != nil || task.Status != "ready" || len(task.Lines) != 2 || task.Lines[0].ID != "book:title" {
				t.Fatalf("reopened title: %+v %v", task, err)
			}
			line := task.Lines[0]
			submissions := []agent.Submission{
				{ID: line.ID, Dst: "标题 world", Reason: "调整", Issues: []document.StageIssue{{Code: "title", Detail: "调整", Quote: line.Src}}},
				{ID: task.Lines[1].ID, Dst: task.Lines[1].Dst},
			}
			if err := agent.Submit(state, options.Worker, task.TaskID, submissions); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "exports", "invalid")
			err = run(context.Background(), []string{"export", root, "--stage", "proofread", "--out", output})
			if err == nil || !strings.Contains(err.Error(), "latin") {
				t.Fatalf("invalid output should fail the Latin text check: %v", err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("failed export left output: %v", err)
			}
			configPath := filepath.Join(root, "config.toml")
			cfg, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, append([]byte("cards = 'materials/pending-cards.json'\n"), cfg...), 0600); err != nil {
				t.Fatal(err)
			}
			output = filepath.Join(root, "exports", "initial-without-cards")
			cards := filepath.Join(root, "materials", "pending-cards.json")
			err = run(context.Background(), []string{"export", root, "--stage", "translate", "--out", cards})
			if err == nil || !strings.Contains(err.Error(), "角色卡路径重叠") {
				t.Fatalf("export at the cards path: %v", err)
			}
			if err := run(context.Background(), []string{"export", root, "--stage", "translate", "--out", output}); err != nil {
				t.Fatalf("initial export with an absent character card: %v", err)
			}
			if err := os.WriteFile(filepath.Join(state, "credits.json"), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			output = filepath.Join(root, "exports", "invalid-credits")
			err = run(context.Background(), []string{"export", root, "--stage", "translate", "--out", output})
			if err == nil || !strings.Contains(err.Error(), "译制署名文件无效") {
				t.Fatalf("artc export should reject invalid credits: %v", err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("failed export left output: %v", err)
			}
		})
	}
}
