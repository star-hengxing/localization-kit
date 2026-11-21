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
	"log/slog"
	"os"
)

func runTermModel(ctx context.Context, args []string, configPath string, cfg config.Config, validate func() error) error {
	flags := flag.NewFlagSet("terms run", flag.ContinueOnError)
	jobPath := flags.String("job", "", "作业目录")
	sourcePath := flags.String("source", "", "原文路径")
	adapter := flags.String("adapter", "jsonl", "原文格式")
	runDir := flags.String("run-dir", "", "批次记录目录")
	out := flags.String("out", "", "新的候选术语材料")
	modelName := flags.String("model", "", "执行术语提取的模型名")
	maxOutput := flags.Int("max-output-tokens", common.DefaultQualityMaxOutput, "每批输出 token 上限")
	retry := flags.Bool("retry-failed", false, "重试已有失败的批次")
	var glossaries common.StringList
	flags.Var(&glossaries, "glossary", "已有术语表，可多次指定")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if (*jobPath == "") == (*sourcePath == "") || *runDir == "" || *out == "" || *modelName == "" || *maxOutput <= 0 || len(flags.Args()) != 0 {
		return fmt.Errorf("需要 --job 或 --source，以及 --model、--run-dir、--out 和正数输出 token 上限")
	}
	logging.ConfigureLogger(cfg.LogLevel)
	summary, err := quality.RunTermModel(ctx, quality.TermRunOptions{JobPath: *jobPath, SourcePath: *sourcePath, Adapter: *adapter, RunDir: *runDir, Output: *out, ConfigPath: configPath, Config: cfg, ModelName: *modelName, MaxOutput: *maxOutput, Retry: *retry, Glossaries: glossaries, Validate: validate})
	if err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("术语候选 %s：批次=%v 条目=%v", *out, summary["batches"], summary["terms"]))
	return json.NewEncoder(os.Stdout).Encode(summary)
}
