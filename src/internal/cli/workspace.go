package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"localization-kit/src/internal/agent"
	jobcli "localization-kit/src/internal/cli/job"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/workspace"
)

func workspaceDefaults(w workspace.Workspace, cards bool) []string {
	var args []string
	for _, path := range w.Glossaries() {
		args = append(args, "--glossary", path)
	}
	if cards && w.Cards() != "" {
		args = append(args, "--cards", w.Cards())
	}
	return args
}

func workspaceFlagName(arg string) string {
	if !strings.HasPrefix(arg, "-") {
		return ""
	}
	return strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
}

func workspaceRun(ctx context.Context, args []string) (bool, error) {
	size := 1
	if isGroup(args[0]) {
		size = 2
	}
	path := args[size]
	rest := args[size+1:]
	if size == 2 && args[1] == "init" {
		return true, initMaterials(args[0], path, rest)
	}
	if args[0] == "init" {
		f := flag.NewFlagSet("init", flag.ContinueOnError)
		adapter := f.String("adapter", "", "原文格式: artc、vtt、jsonl")
		input := f.String("input", "", "source 内的原文路径")
		if err := f.Parse(rest); err != nil {
			return true, err
		}
		if f.NArg() != 0 {
			return true, fmt.Errorf("init 不接受额外位置参数")
		}
		w, err := workspace.Init(path, *adapter, *input)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(os.Stdout).Encode(map[string]any{"directory": w.Root, "adapter": w.Config.Adapter, "input": w.Input()})
	}
	if len(rest) != 0 {
		switch args[0] {
		case "results":
			return true, fmt.Errorf("用法: localization-kit results [作品目录 | --project-config 文件]")
		case "audit":
			return true, fmt.Errorf("job audit 不接受额外参数")
		}
	}
	w, err := workspace.Load(path)
	if err != nil {
		return true, err
	}
	state := w.State()
	if !slices.Contains([]string{"status", "translate", "next", "submit", "export"}, args[0]) {
		if err := recoverWorkspaceState(state); err != nil {
			return true, err
		}
	}
	if args[0] == "terms" || args[0] == "characters" || args[0] == "review" || args[0] == "polish" {
		return true, workspaceQuality(ctx, w, args[0], args[1], rest)
	}
	if args[0] == "translate" || args[0] == "next" || args[0] == "edit" {
		for _, arg := range rest {
			if name := workspaceFlagName(arg); name == "glossary" || name == "cards" {
				return true, fmt.Errorf("作品材料请在 config.toml 中配置")
			}
		}
	}
	switch args[0] {
	case "run":
		return true, runWorkspaceStage(ctx, w, rest)
	case "translate":
		return true, translateWorkspace(ctx, w, rest)
	case "status":
		return true, statusWorkspace(w, rest)
	case "results":
		return true, jobcli.Results(state)
	case "audit":
		if err := checkWorkspaceSource(w); err != nil {
			return true, err
		}
		if err := agent.CheckWorkspaceMaterials(state, w.Glossaries(), w.Cards(), "polish"); err != nil {
			return true, err
		}
		return true, jobcli.Audit(state)
	case "redo":
		if err := checkWorkspaceSource(w); err != nil {
			return true, err
		}
		return true, jobcli.Redo(state, rest)
	case "release":
		return true, jobcli.Release(state, rest)
	case "submit":
		return true, jobcli.SubmitWithValidation(state, rest, func(job *store.Store) error {
			current, err := validateWorkspaceSource(w, job, false)
			if err != nil {
				return err
			}
			return agent.CheckWorkspaceMaterialsLocked(state, current.Glossaries(), current.Cards(), "")
		})
	case "next":
		return true, jobcli.NextWithValidation(state, append(workspaceDefaults(w, true), rest...), func(job *store.Store) error {
			_, err := validateWorkspaceSource(w, job, true)
			return err
		})
	case "edit":
		if err := checkWorkspaceSource(w); err != nil {
			return true, err
		}
		return true, jobcli.Edit(state, append(workspaceDefaults(w, false), rest...))
	case "export":
		return true, exportWorkspace(w, rest)
	}
	return false, nil
}

func recoverWorkspaceState(state string) error {
	if _, err := os.Stat(state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	job, err := store.OpenExisting(state)
	if err != nil {
		return err
	}
	return job.Close()
}
