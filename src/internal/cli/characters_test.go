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
)

func TestCharactersValidateUsesEffectiveGlossaryNames(t *testing.T) {
	root := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"ミナとミーが来た。ミナさん！\"}\n")
	for path, content := range map[string]string{
		"materials/base.json":     "{\"entries\":[{\"src\":\"ミナ\",\"dst\":\"美奈\",\"variants\":[\"ミナさん\"]}]}",
		"materials/override.json": "{\"entries\":[{\"src\":\"ミナ\",\"dst\":\"米娜\",\"variants\":[\"ミー\"]},{\"src\":\"ミノ\",\"dst\":\"米诺\",\"variants\":[\"ミー\"]}]}",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	configureWorkspaceModel(t, root, "glossaries = ['materials/base.json', 'materials/override.json']\n")
	for _, tc := range []struct {
		name, source, translation string
		conflict                  bool
	}{
		{"canonical conflict", "ミナ", "美奈", true},
		{"alias conflict", "ミー", "美奈", true},
		{"replaced alias", "ミナさん", "美奈", false},
		{"effective canonical", "ミナ", "米娜", false},
		{"first alias wins", "ミー", "米娜", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, "materials", "characters.json")
			if err := os.WriteFile(path, []byte(characterFixture(tc.source, tc.translation)), 0600); err != nil {
				t.Fatal(err)
			}
			err := run(context.Background(), []string{"characters", "validate", root})
			if tc.conflict {
				if err == nil || !strings.Contains(err.Error(), tc.source) || !strings.Contains(err.Error(), tc.translation) || !strings.Contains(err.Error(), "米娜") {
					t.Fatalf("expected the conflicting names: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCharactersRunReportsConflictBeforePublishing(t *testing.T) {
	glossary := []byte("{\"entries\":[{\"src\":\"ミナ\",\"dst\":\"米娜\",\"kind\":\"name\",\"constraint\":\"hard\"}]}")
	for _, translation := range []string{"美奈", "米娜"} {
		t.Run(translation, func(t *testing.T) {
			root := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"ミナが来た。\"}\n")
			if err := os.WriteFile(filepath.Join(root, "materials", "glossary.json"), glossary, 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"model": "quality", "choices": []any{map[string]any{
						"message": map[string]string{"content": characterFixture("ミナ", translation)}, "finish_reason": "stop",
					}},
				})
			}))
			defer server.Close()
			configureWorkspaceModel(t, root, "[models.quality]\nurl='"+server.URL+"'\nmodel='quality'\n")
			output := filepath.Join(root, "materials", "generated.json")
			err := run(context.Background(), []string{"characters", "run", root, "--model", "quality", "--out", output})
			if translation == "美奈" {
				if err == nil || !strings.Contains(err.Error(), "译名冲突") {
					t.Fatalf("expected a translation conflict: %v", err)
				}
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("conflicting cards were published: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(output); err != nil || !strings.Contains(string(data), translation) {
				t.Fatalf("published cards=%q err=%v", data, err)
			}
		})
	}
}

func characterFixture(source, translation string) string {
	return fmt.Sprintf("{\"characters\":[{\"name_src\":%q,\"name_dst\":%q,\"gender\":\"female\",\"pronoun\":\"她\"}]}", source, translation)
}
