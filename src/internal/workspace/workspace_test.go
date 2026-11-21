package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitDoesNotCreateStateOrOverwriteExistingData(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "source", "input.jsonl")
	if err := os.WriteFile(input, []byte("{\"id\":\"1\",\"src\":\"こんにちは\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Init(root, "jsonl", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("state created during init: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	results := filepath.Join(root, "state", "results.jsonl")
	if err := os.WriteFile(results, []byte("existing data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(root, "jsonl", ""); err == nil {
		t.Fatal("nonempty results accepted")
	}
	data, err := os.ReadFile(results)
	if err != nil || string(data) != "existing data" {
		t.Fatalf("existing results changed: %q %v", data, err)
	}
}

func TestMaterialDigestTracksContents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "glossary.json")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	w := Workspace{Root: root, Config: Config{Glossaries: []string{"glossary.json"}}}
	before, err := w.MaterialDigest(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := w.MaterialDigest(false)
	if err != nil || before == after {
		t.Fatalf("material edit was not detected: %v", err)
	}
}
