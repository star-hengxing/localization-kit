package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"localization-kit/src/internal/translate/rules"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "-m" && os.Getenv("LOCALIZATION_KIT_IMAGE_TEST_CHILD") != "" {
		os.Exit(imageTestChild())
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot locate test data")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	if err := os.Chdir(root); err != nil {
		panic(err)
	}
	if err := rules.LoadSimplified(filepath.Join(root, "data", "simplified.json")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
