package jsonl

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONLValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in.jsonl")
	if err := os.WriteFile(path, []byte("\ufeff{\"id\":\"1\",\"src\":\"こんにちは\"}\n{\"id\":\"2\",\"src\":\"はい\",\"group\":\"g\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := LoadJSONL(path)
	if err != nil || len(items) != 2 || items[0].Group != "1" || items[1].Group != "g" {
		t.Fatalf("items=%+v error=%v", items, err)
	}
	if err := os.WriteFile(path, []byte("{\"id\":\"1\",\"src\":\"a\"}\n{\"id\":\"1\",\"src\":\"b\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadJSONL(path); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if err := os.WriteFile(path, []byte(`{"id":"1","src":"a","meta":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	var typeErr *json.UnmarshalTypeError
	if _, err := LoadJSONL(path); !errors.As(err, &typeErr) || typeErr.Field != "meta" || !strings.Contains(err.Error(), path+":1") {
		t.Fatalf("expected meta type error with file and line, got %v", err)
	}
}
