package quality

import (
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/translate/rules"
)

func TestJSONInputsRejectInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, 0600); err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := jsonfile.Read(path, &value); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf{\"x\":\"你好\"}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := jsonfile.Read(path, &value); err != nil || value["x"] != "你好" {
		t.Fatalf("valid UTF-8 with BOM rejected: value=%v err=%v", value, err)
	}
}

func TestGlossaryRejectsInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "glossary.json")
	data := append([]byte(`{"entries":[{"src":"source","dst":"`), 0xff)
	data = append(data, []byte(`"}]}`)...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rules.LoadGlossaries([]string{path}); err == nil {
		t.Fatal("invalid UTF-8 in glossary was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"entries":[{"src":"source","dst":"�"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := rules.LoadGlossaries([]string{path})
	if err != nil || len(entries) != 1 || entries[0].Dst != "�" {
		t.Fatalf("valid UTF-8 glossary rejected: entries=%v err=%v", entries, err)
	}
}
