package exporter

import (
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/format"
)

func TestFailedPublishLeavesNoOutput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(input, "chapters"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"book.toml": "title = \"日本語\"\nlang = \"ja\"\n", "toc.toml": "[[chapter]]\ntitle = \"章\"\nfile = \"chapters/01.txt\"\n", "chapters/01.txt": "こんにちは\n"}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(input, filepath.FromSlash(name)), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := format.LoadSource("artc", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "chapters", "01.txt"), []byte("変更"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out")
	if err := Publish(source, nil, output, nil, nil, false); err == nil {
		t.Fatal("changed source accepted")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("failed output exists: %v", err)
	}
}

func TestVerifySourceAllowsArtcAssetChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "chapters"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"book.toml": "title = \"日本語\"\nlang = \"ja\"\n", "toc.toml": "[[chapter]]\ntitle = \"章\"\nfile = \"chapters/01.txt\"\n", "chapters/01.txt": "こんにちは\n", "cover.svg": "original"}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := format.LoadSource("artc", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cover.svg"), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySource(source); err != nil {
		t.Fatalf("asset change blocked source verification: %v", err)
	}
}
