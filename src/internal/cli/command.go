package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"localization-kit/src/internal/cli/common"
)

const modelOptions = "--model 名称 [--retry-failed]"
const qualityOptions = modelOptions + " [--max-output-tokens 数量]"
const contextOptions = " [--review-input 材料 --review-report 报告] [--ttl 时长] [--context-scope group|task] [--context 数量] [--max-lines 数量]"

var commandUsage = map[string]string{
	"image ingest":        "--source PATH --project DIR",
	"image prepare":       "(--image PATH --out-dir DIR [--page-id ID] | --project DIR [--pages CSV] [--redetect]) [--no-ogkalu]",
	"image ocr":           "(--work-dir DIR | --project DIR [--pages CSV]) [--force]",
	"image export-text":   "(--work-dir DIR | --project DIR [--pages CSV]) --out JSONL [--group-by chapter|page]",
	"image render":        "(--work-dir DIR [--out-dir DIR] [--baseline PATH] | --project DIR --out-dir DIR [--pages CSV]) [--translations JSONL] [--inpaint cpu|auto|lama] [--readback]",
	"image fingerprint":   "--work-dir PATH --rendered PATH [--translations PATH]",
	"image review-serve":  "[--dir PATH] [--root PATH] [--web-dist PATH] [--host HOST] [--port PORT]",
	"init":                "--adapter artc|vtt|jsonl [--input source/原文]",
	"translate":           "[--model 名称] [--previous 作品目录] [--retry-failed]",
	"run":                 "--stage polish|proofread --model 名称 [--max-output-tokens 数量]" + contextOptions,
	"status":              "[--details] [--id 条目]",
	"results":             "",
	"audit":               "",
	"next":                "--stage polish|proofread --worker 执行者 --model 模型 [--format json|text]" + contextOptions,
	"submit":              "--worker 执行者 --task 任务ID (--file 结果.json | --changes 改动.json)",
	"release":             "--worker 执行者",
	"redo":                "--stage 阶段 --id 条目 [--id 条目]",
	"edit":                "--file 修订.jsonl",
	"export":              "--stage 阶段 --out 路径 [--bilingual]",
	"terms init":          "[--refresh]",
	"terms validate":      "[--input 文件]",
	"terms run":           qualityOptions + " [--run-dir 目录] [--out 文件]",
	"characters init":     "",
	"characters run":      "--model 名称 --out 新文件 [--max-output-tokens 数量]",
	"characters validate": "[--input 文件]",
	"characters warnings": "[--narration 文件]",
	"review prepare":      "--out 文件 [--kind fidelity|subject] [--batch-size 数量] [--id 条目] [--group 分组] [--candidate 文件]",
	"review run":          "--input 文件 --run-dir 目录 --out 文件 " + qualityOptions,
	"review validate":     "--input 文件 --report 文件",
	"polish prepare":      "--mode fix|rewrite --input 文件 --out 文件 [--report 文件] [--additional-input 文件] [--additional-report 文件]",
	"polish run":          "--input 文件 --run-dir 目录 --out 文件 " + qualityOptions,
	"polish validate":     "--input 文件 --file 文件",
	"polish apply":        "--input 文件 --file 文件 --review-input 文件 --review-report 文件",
	"eval compare":        "--baseline 作品 --candidate 作品 --out 报告",
	"eval blind-prepare":  "--input 报告 --out 目录 [--batch-size 数量]",
	"eval blind-score":    "--dir 盲评目录 --reports 结果目录 --out 报告",
}

func isGroup(name string) bool {
	switch name {
	case "terms", "characters", "review", "polish", "eval", "image":
		return true
	}
	return false
}

func independent(name string) bool {
	return strings.HasPrefix(name, "eval ") || strings.HasPrefix(name, "image ")
}

func helpFlag(arg string) bool {
	return arg == "--help" || arg == "-h" || arg == "-help" || arg == "--h"
}

func showHelp(name string) {
	if usage, ok := commandUsage[name]; ok {
		target := " [作品目录 | --project-config 文件]"
		if name == "init" {
			target = " [作品目录]"
		}
		if independent(name) {
			target = ""
		}
		fmt.Println(strings.TrimSpace("用法: localization-kit " + name + target + " " + usage))
		if target != "" {
			if name == "init" {
				fmt.Println("默认初始化当前目录。--input 相对作品目录。")
			} else {
				fmt.Println("省略作品位置时读取当前目录的 config.toml。--project-config 指定作品配置文件。")
			}
		}
		if name == "run" || name == "next" {
			fmt.Println("仅润色可成对重复指定 --review-input 和 --review-report。")
			fmt.Printf("默认 --ttl %s、--context %d、--max-lines %d、--context-scope %s。\n", common.DefaultTaskTTL, common.DefaultTaskContext, common.DefaultTaskMaxLines, common.DefaultTaskScope)
		}
		if name == "next" {
			fmt.Println("--format 默认 json，可用 text 逐行阅读任务。")
		}
		if name == "submit" {
			fmt.Println("--file 提交完整批次。--changes 只填写改动，其余条目确认原样通过。")
		}
		if strings.Contains(usage, "--max-output-tokens") {
			fmt.Printf("--max-output-tokens 默认 %d。\n", common.DefaultQualityMaxOutput)
		}
		if name == "polish run" {
			fmt.Println("生成润色候选文件。要完成作品阶段，请使用 localization-kit run --stage polish。")
		}
		if name == "review prepare" {
			fmt.Println("--id 和 --group 可重复；--candidate 不能与它们同时使用。")
		}
		return
	}
	names := []string{}
	for command := range commandUsage {
		if name == "" || strings.HasPrefix(command, name+" ") {
			names = append(names, command)
		}
	}
	sort.Strings(names)
	if name == "" {
		fmt.Println("用法: localization-kit <命令> [作品位置] [选项]")
		fmt.Println("作品位置可用作品目录或 --project-config 文件，省略时使用当前目录。")
		fmt.Println("作品流程: init、translate、run、status、results、export")
		fmt.Println("分配与修订: next、submit、release、redo、audit、edit")
		fmt.Println("材料与候选: terms、characters、review、polish")
		fmt.Println("独立工具: eval、image")
	} else {
		fmt.Printf("%s 子命令:\n  %s\n", name, strings.Join(names, "\n  "))
	}
	fmt.Println("使用 localization-kit <命令> --help 查看具体参数。")
}

func resolveCommand(args []string) ([]string, string, bool, error) {
	if len(args) == 0 || helpFlag(args[0]) {
		showHelp("")
		return nil, "", true, nil
	}
	if args[0] == "help" {
		name := strings.Join(args[1:], " ")
		if _, ok := commandUsage[name]; name != "" && !ok && !isGroup(name) {
			return nil, "", false, fmt.Errorf("未知命令 %q", name)
		}
		showHelp(name)
		return nil, "", true, nil
	}
	name := args[0]
	size := 1
	if isGroup(name) {
		if len(args) == 1 || helpFlag(args[1]) {
			showHelp(name)
			return nil, "", true, nil
		}
		name += " " + args[1]
		size = 2
	}
	if _, ok := commandUsage[name]; !ok {
		return nil, "", false, fmt.Errorf("未知命令 %q，请使用 localization-kit --help", name)
	}
	rest := args[size:]
	dir := ""
	config := ""
	flags := []string{}
	if !independent(name) && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		dir = rest[0]
		if dir == "" {
			return nil, "", false, fmt.Errorf("作品目录为空")
		}
		rest = rest[1:]
	}
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if helpFlag(arg) {
			showHelp(name)
			return nil, "", true, nil
		}
		option, value, inline := strings.Cut(arg, "=")
		if inline && helpFlag(option) {
			return nil, "", false, fmt.Errorf("帮助参数不接受值: %s", arg)
		}
		if !strings.HasPrefix(option, "-") || option == "--" {
			return nil, "", false, fmt.Errorf("%s: 多余的位置参数 %q", name, arg)
		}
		key := strings.TrimLeft(option, "-")
		if key == "config" {
			return nil, "", false, fmt.Errorf("--config 已移除，作品配置使用 --project-config")
		}
		if key == "models-config" {
			return nil, "", false, fmt.Errorf("模型配置请写入作品 config.toml")
		}
		if key == "project-config" {
			if independent(name) || name == "init" {
				return nil, "", false, fmt.Errorf("%s 不接受 --project-config", name)
			}
			if config != "" || dir != "" {
				return nil, "", false, fmt.Errorf("作品目录和 --project-config 只能指定一次")
			}
			if !inline {
				i++
				if i >= len(rest) || strings.HasPrefix(rest[i], "-") {
					return nil, "", false, fmt.Errorf("--project-config 缺少路径")
				}
				value = rest[i]
			}
			if value == "" || !strings.EqualFold(filepath.Base(value), "config.toml") {
				return nil, "", false, fmt.Errorf("--project-config 需要作品的 config.toml")
			}
			config = value
			continue
		}
		flags = append(flags, arg)
		if inline {
			continue
		}
		switch key {
		case "details", "refresh", "retry-failed", "bilingual", "pdf", "no-ogkalu", "redetect", "force", "readback":
			continue
		}
		if i+1 < len(rest) {
			i++
			flags = append(flags, rest[i])
		}
	}
	if independent(name) {
		return args, "", false, nil
	}
	if config != "" {
		resolved, err := filepath.EvalSymlinks(config)
		if err != nil {
			absolute, _ := filepath.Abs(config)
			return nil, "", false, fmt.Errorf("读取作品配置 %s: %w", absolute, err)
		}
		dir = filepath.Dir(resolved)
		chosen, err := os.Stat(resolved)
		if err != nil {
			return nil, "", false, err
		}
		standard, err := os.Stat(filepath.Join(dir, "config.toml"))
		if err != nil || !chosen.Mode().IsRegular() || !os.SameFile(chosen, standard) {
			return nil, "", false, fmt.Errorf("指定的文件须为作品实际读取的 config.toml: %s", resolved)
		}
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return nil, "", false, err
		}
		if name != "init" {
			dir, err = currentWorkspace(dir)
			if err != nil {
				return nil, "", false, err
			}
		}
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", false, err
	}
	normalized := append([]string{}, args[:size]...)
	normalized = append(normalized, path)
	normalized = append(normalized, flags...)
	return normalized, path, false, nil
}

func currentWorkspace(start string) (string, error) {
	path := filepath.Join(start, "config.toml")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("无法读取当前目录的作品配置 %s，请指定作品目录或 --project-config: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("作品配置不是普通文件: %s", path)
	}
	return start, nil
}
