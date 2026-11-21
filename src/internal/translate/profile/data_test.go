package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"localization-kit/src/internal/translate/rules"
)

func TestMain(m *testing.M) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot locate test data")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", ".."))
	if err := rules.LoadSimplified(filepath.Join(root, "data", "simplified.json")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
