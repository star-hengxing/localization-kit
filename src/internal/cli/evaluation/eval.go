package evaluation

import (
	"encoding/json"
	"flag"
	"fmt"
	"localization-kit/src/internal/evaluation"
	"localization-kit/src/internal/workspace"
	"os"
)

func Run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: localization-kit eval compare|blind-prepare|blind-score [选项]")
	}
	if args[0] == "blind-prepare" {
		return prepareBlind(args[1:])
	}
	if args[0] == "blind-score" {
		return scoreBlind(args[1:])
	}
	if args[0] != "compare" {
		return fmt.Errorf("未知评测命令 %q", args[0])
	}
	flags := flag.NewFlagSet("eval compare", flag.ContinueOnError)
	baselinePath := flags.String("baseline", "", "基线作品目录")
	candidatePath := flags.String("candidate", "", "候选作品目录")
	output := flags.String("out", "", "新的比较报告")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *baselinePath == "" || *candidatePath == "" || *output == "" || len(flags.Args()) != 0 {
		return fmt.Errorf("需要 --baseline、--candidate 和 --out")
	}
	baseline, err := workspace.Load(*baselinePath)
	if err != nil {
		return err
	}
	candidate, err := workspace.Load(*candidatePath)
	if err != nil {
		return err
	}
	summary, err := evaluation.CompareFiles(baseline.State(), candidate.State(), *output)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(summary)
}
