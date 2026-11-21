package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/materials"
	"localization-kit/src/internal/workspace"
)

func TestMaterialInitialization(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "input.jsonl"), []byte("{\"id\":\"1\",\"src\":\"花子は花子です\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"init", root, "--adapter", "jsonl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("state created: %v", err)
	}
	for _, name := range []string{"glossary.json", "term-statistics.json"} {
		if _, err := os.Stat(filepath.Join(root, "materials", name)); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "config.toml")
	if err := run(context.Background(), []string{"terms", "init", "--project-config", config}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"characters", "init", "--project-config", config}); err != nil {
		t.Fatal(err)
	}
	work, err := workspace.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if work.Cards() != "" {
		t.Fatal("empty template treated as a completed card")
	}
	path := filepath.Join(root, "materials", "characters.json")
	var card materials.CardFile
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &card) != nil || len(card.Characters) != 1 {
		t.Fatalf("template=%s err=%v", data, err)
	}
	card.Characters[0].NameSrc, card.Characters[0].NameDst = "花子", "花子"
	data, err = json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if work.Cards() != path {
		t.Fatal("filled card not discovered")
	}
	if err := run(context.Background(), []string{"characters", "validate", root}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"characters", "init", "--project-config", config}); err == nil {
		t.Fatal("existing card overwritten")
	}
	for _, kind := range []string{"terms", "characters"} {
		if err := run(context.Background(), []string{kind, "prepare", root}); err == nil {
			t.Fatalf("%s prepare accepted", kind)
		}
	}
}
