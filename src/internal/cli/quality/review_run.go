package quality

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/config"
	"localization-kit/src/internal/logging"
	"localization-kit/src/internal/quality"
	"os"
)

func runReviewModel(ctx context.Context, args []string, configPath string, cfg config.Config) error {
	flags := flag.NewFlagSet("review run", flag.ContinueOnError)
	jobPath := flags.String("job", "", "作业目录")
	input := flags.String("input", "", "检查材料 JSON")
	runDir := flags.String("run-dir", "", "批次记录目录")
	out := flags.String("out", "", "新的检查报告")
	modelName := flags.String("model", "", "执行检查的模型名")
	maxOutput := flags.Int("max-output-tokens", common.DefaultQualityMaxOutput, "每批输出 token 上限")
	retry := flags.Bool("retry-failed", false, "重试已有失败的批次")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *jobPath == "" || *input == "" || *runDir == "" || *out == "" || *modelName == "" || *maxOutput <= 0 || len(flags.Args()) != 0 {
		return fmt.Errorf("需要 --job、--input、--model、--run-dir、--out 和正数输出 token 上限")
	}
	logging.ConfigureLogger(cfg.LogLevel)
	summary, err := quality.RunReviewModel(ctx, quality.ModelRunOptions{ResourceRoot: common.ResourceRoot, JobPath: *jobPath, Input: *input, RunDir: *runDir, Output: *out, ConfigPath: configPath, Config: cfg, ModelName: *modelName, MaxOutput: *maxOutput, Retry: *retry})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(summary)
}
