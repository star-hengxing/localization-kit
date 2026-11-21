package rules

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMain(m *testing.M) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot locate test data")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", ".."))
	if err := LoadSimplified(filepath.Join(root, "data", "simplified.json")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
