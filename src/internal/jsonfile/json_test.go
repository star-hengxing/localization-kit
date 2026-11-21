package jsonfile

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigestKeepsEncodingAndReportsErrors(t *testing.T) {
	value := map[string]string{"name": "<test>"}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("{\"name\":\"\\u003ctest\\u003e\"}")))
	if got, err := Digest(value); err != nil || got != want {
		t.Fatalf("digest=%q want=%q err=%v", got, want, err)
	}
	var marshalErr *json.MarshalerError
	if got, err := Digest(json.RawMessage("{")); got != "" || !errors.As(err, &marshalErr) {
		t.Fatalf("invalid digest=%q err=%v", got, err)
	}
}

func TestReadDistinguishesTrailingValueFromSyntaxError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	var value struct{}
	for _, input := range []string{"{} []", "{} 1e1000"} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Read(path, &value); err == nil || !strings.Contains(err.Error(), "多余的值") {
			t.Fatalf("second JSON value %q: %v", input, err)
		}
	}
	if err := os.WriteFile(path, []byte("{} ]"), 0600); err != nil {
		t.Fatal(err)
	}
	var syntaxErr *json.SyntaxError
	if err := Read(path, &value); !errors.As(err, &syntaxErr) || !strings.Contains(err.Error(), path) {
		t.Fatalf("trailing syntax error: %v", err)
	}
}
