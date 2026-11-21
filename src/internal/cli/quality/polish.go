package quality

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/config"
	"localization-kit/src/internal/quality"
	"localization-kit/src/internal/store"
	"os"
)

func PolishWithValidation(ctx context.Context, args []string, configPath string, cfg config.Config, validate func(*store.Store, quality.ReviewMaterial) error) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: polish prepare|run|validate|apply")
	}
	switch args[0] {
	case "prepare":
		return preparePolish(args[1:])
	case "run":
		return runPolishModel(ctx, args[1:], configPath, cfg)
	case "validate":
		return processPolishWithValidation(args[1:], false, validate)
	case "apply":
		return processPolishWithValidation(args[1:], true, validate)
	default:
		return fmt.Errorf("未知润色命令 %q", args[0])
	}
}

func preparePolish(args []string) error {
	flags := flag.NewFlagSet("polish prepare", flag.ContinueOnError)
	jobPath := flags.String("job", "", "作品状态目录")
	input := flags.String("input", "", "检查材料 JSON")
	reportPath := flags.String("report", "", "初翻检查报告 JSON，可选")
	output := flags.String("out", "", "新的润色材料 JSON")
	mode := flags.String("mode", "", "fix 或 rewrite")
	var additionalInputs, additionalReports common.StringList
	flags.Var(&additionalInputs, "additional-input", "其他检查材料 JSON，可重复")
	flags.Var(&additionalReports, "additional-report", "对应的检查报告 JSON，可重复")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *jobPath == "" || *input == "" || *output == "" || (*mode != "fix" && *mode != "rewrite") || flags.NArg() != 0 || len(additionalInputs) != len(additionalReports) {
		return fmt.Errorf("需要 --job、--input、--out 和 --mode fix|rewrite")
	}
	return quality.PreparePolish(quality.PolishOptions{JobPath: *jobPath, Input: *input, ReportPath: *reportPath, Output: *output, Mode: *mode, AdditionalInputs: additionalInputs, AdditionalReports: additionalReports})
}

func processPolishWithValidation(args []string, apply bool, validate func(*store.Store, quality.ReviewMaterial) error) error {
	flags := flag.NewFlagSet("polish", flag.ContinueOnError)
	jobPath := flags.String("job", "", "作品状态目录")
	input := flags.String("input", "", "润色材料 JSON")
	output := flags.String("file", "", "润色结果 JSON")
	reviewPath := flags.String("review-input", "", "润色候选审查材料 JSON")
	reportPath := flags.String("review-report", "", "润色候选审查报告 JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *jobPath == "" || *input == "" || *output == "" || flags.NArg() != 0 || apply && (*reviewPath == "" || *reportPath == "") {
		return fmt.Errorf("需要 --job、--input 和 --file；apply 还需要 --review-input 和 --review-report")
	}
	report, err := quality.ProcessPolish(quality.ApplyPolishOptions{ResourceRoot: common.ResourceRoot, JobPath: *jobPath, Input: *input, Output: *output, ReviewPath: *reviewPath, ReportPath: *reportPath, Apply: apply, Validate: validate})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
