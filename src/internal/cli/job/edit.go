package job

import (
	"encoding/json"
	"flag"
	"fmt"
	"localization-kit/src/internal/cli/common"
	"log/slog"
	"os"
	"path/filepath"

	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
)

func Edit(jobPath string, args []string) (resultErr error) {
	flags := flag.NewFlagSet("job edit", flag.ContinueOnError)
	file := flags.String("file", "", "人工修订 JSONL")
	var glossary common.StringList
	flags.Var(&glossary, "glossary", "修订检查使用的术语表，可多次指定")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *file == "" || len(flags.Args()) != 0 {
		return fmt.Errorf("需要作业目录和 --file")
	}
	if err := rules.LoadSimplified(filepath.Join(common.ResourceRoot, rules.DefaultDataPath)); err != nil {
		return err
	}
	entries, err := rules.LoadGlossaries(glossary)
	if err != nil {
		return err
	}
	edits, err := translate.ReadManualEdits(*file)
	if err != nil {
		return err
	}

	if err := translate.ApplyEdits(jobPath, edits, entries, nil); err != nil {
		return err
	}
	slog.Info("人工修订完成")
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"edited": len(edits)})
}
