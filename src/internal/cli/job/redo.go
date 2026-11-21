package job

import (
	"encoding/json"
	"flag"
	"fmt"
	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/cli/common"
	"os"
)

func Redo(jobPath string, args []string) error {
	flags := flag.NewFlagSet("job redo", flag.ContinueOnError)
	stage := flags.String("stage", "", "translate、polish 或 proofread")
	var ids common.StringList
	flags.Var(&ids, "id", "需要重做的条目 ID，可重复")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("job redo 不接受多余位置参数")
	}
	count, err := agent.Redo(jobPath, *stage, ids)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"stage": *stage, "reopened": count})
}
