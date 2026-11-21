package job

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/format/jsonl"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
)

func StatusWith(args []string, inspect func(bool, string) (agent.Status, error)) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	details := flags.Bool("details", false, "显示所有条目的阶段状态")
	id := flags.String("id", "", "查看单个条目及其原文和译文")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *details && *id != "" {
		return fmt.Errorf("用法: localization-kit status [作品目录 | --project-config 文件] [--details | --id ID]")
	}
	if err := rules.LoadSimplified(filepath.Join(common.ResourceRoot, rules.DefaultDataPath)); err != nil {
		return err
	}
	status, err := inspect(*details, *id)
	if err != nil {
		return err
	}
	message := fmt.Sprintf("状态：总计=%d 初翻完成=%d 原样保留=%d 待处理=%d 失败=%d 待复核=%d 润色完成=%d 无需润色=%d 校对完成=%d 无需校对=%d", status.Total, status.Translate["done"], status.Translate["passthrough"], status.Translate["pending"], status.Translate["failed"], status.Translate["review"], status.Polish["done"], status.Polish["not_applicable"], status.Proofread["done"], status.Proofread["not_applicable"])
	if *id != "" {
		message += " ID=" + *id
	}
	slog.Info(message)
	return json.NewEncoder(os.Stdout).Encode(status)
}

func Results(jobPath string) error {
	job, err := store.OpenReadOnly(jobPath)
	if err != nil {
		return err
	}
	defer job.Close()
	return jsonl.ExportJSONL(os.Stdout, job.Segments, job.Done, job.Failed)
}
