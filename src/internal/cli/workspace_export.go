package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/workflow"
	"localization-kit/src/internal/workspace"
)

func exportWorkspace(w workspace.Workspace, rest []string) error {
	state := w.State()
	f := flag.NewFlagSet("export", flag.ContinueOnError)
	stage := f.String("stage", "", "translate、polish 或 proofread")
	out := f.String("out", "", "输出路径")
	bilingual := f.Bool("bilingual", false, "双语小说")
	if err := f.Parse(rest); err != nil {
		return err
	}
	if f.NArg() != 0 || *out == "" || *stage == "" {
		return fmt.Errorf("export 需要 --stage 和 --out")
	}
	switch *stage {
	case "translate":
		if err := checkInitialExportPath(w, *out); err != nil {
			return err
		}
	case "polish", "proofread":
	default:
		return fmt.Errorf("未知阶段 %q", *stage)
	}
	if err := fileutil.CheckPaths(w.Input(), state, *out); err != nil {
		return err
	}
	if err := fileutil.CheckOutput(*out); err != nil {
		return err
	}
	usesCards := *stage != "translate"
	materialDigest, err := w.MaterialDigest(usesCards)
	if err != nil {
		return err
	}
	if err := recoverWorkspaceState(state); err != nil {
		return err
	}
	summary, err := workflow.Export(workflow.ExportOptions{ResourceRoot: common.ResourceRoot, JobPath: state, Adapter: w.Config.Adapter, Input: w.Input(), Output: *out, Glossary: w.Glossaries(), Bilingual: *bilingual, Stage: *stage, Validate: func(job *store.Store) error {
		current, err := validateWorkspaceSource(w, job, usesCards)
		if err != nil {
			return err
		}
		if !usesCards && !sameTranslationInputs(w.Config, current.Config) {
			return fmt.Errorf("作品配置已变化，请重新执行")
		}
		currentDigest, err := current.MaterialDigest(usesCards)
		if err != nil {
			return err
		}
		if currentDigest != materialDigest {
			return fmt.Errorf("作品材料在导出期间发生变化")
		}
		if usesCards {
			return agent.CheckWorkspaceMaterialsLocked(state, current.Glossaries(), current.Cards(), *stage)
		}
		return nil
	}})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(summary)
}

func checkInitialExportPath(w workspace.Workspace, destination string) error {
	path, err := fileutil.Canonical(destination)
	if err != nil {
		return err
	}
	cardPaths := []string{w.Resolve("materials/characters.json")}
	if w.Config.Cards != "" {
		cardPaths = append(cardPaths, w.Resolve(w.Config.Cards))
	}
	for _, cards := range cardPaths {
		if fileutil.Overlaps(path, cards) {
			return fmt.Errorf("导出路径不能与角色卡路径重叠: %s", cards)
		}
	}
	return nil
}
