package evaluation

import (
	"encoding/json"
	"flag"
	"fmt"
	"localization-kit/src/internal/evaluation"
	"os"
)

func prepareBlind(args []string) error {
	flags := flag.NewFlagSet("eval blind-prepare", flag.ContinueOnError)
	input := flags.String("input", "", "eval compare 生成的报告")
	out := flags.String("out", "", "新的评测目录")
	batchSize := flags.Int("batch-size", 20, "每批条目数")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || *out == "" || *batchSize < 1 || len(flags.Args()) != 0 {
		return fmt.Errorf("需要 --input、--out 和正数 --batch-size")
	}
	summary, err := evaluation.PrepareBlind(*input, *out, *batchSize)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(summary)
}

func scoreBlind(args []string) error {
	flags := flag.NewFlagSet("eval blind-score", flag.ContinueOnError)
	dir := flags.String("dir", "", "盲评目录")
	reports := flags.String("reports", "", "评审结果目录")
	out := flags.String("out", "", "新的评分报告")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *reports == "" || *out == "" || len(flags.Args()) != 0 {
		return fmt.Errorf("需要 --dir、--reports 和 --out")
	}
	summary, err := evaluation.ScoreBlind(*dir, *reports, *out)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(summary)
}
