package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/format"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/quality"
	"localization-kit/src/internal/translate/rules"
	"localization-kit/src/internal/workspace"
)

func runCharacters(ctx context.Context, w workspace.Workspace, args []string) error {
	f := flag.NewFlagSet("characters run", flag.ContinueOnError)
	modelName := f.String("model", "", "质量模型名称")
	configPath := filepath.Join(w.Root, "config.toml")
	out := f.String("out", "", "新角色卡 JSON")
	maxOutput := f.Int("max-output-tokens", common.DefaultQualityMaxOutput, "输出 token 上限")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *modelName == "" || *out == "" || *maxOutput <= 0 {
		return fmt.Errorf("characters run 需要 --model、--out 和正数输出 token 上限")
	}
	paths := w.Glossaries()
	protected := append([]string{w.Input(), w.State(), configPath}, paths...)
	if err := fileutil.CheckNewPath(*out, protected...); err != nil {
		return err
	}
	models, err := w.Config.ValidateModelSettings()
	if err != nil {
		return err
	}
	selected, err := models.SelectModel(*modelName)
	if err != nil {
		return err
	}
	source, err := format.LoadSource(w.Config.Adapter, w.Input())
	if err != nil {
		return err
	}
	entries, err := rules.LoadGlossaries(paths)
	if err != nil {
		return err
	}
	input := quality.NewCharacterInput(source.Segments, entries)
	cards, err := quality.GenerateCharacterCards(ctx, selected, input, *maxOutput)
	if err != nil {
		return err
	}
	current, err := workspace.Load(w.Root)
	if err != nil {
		return err
	}
	if !sameWorkspaceInputs(current.Config, w.Config) || !slices.Equal(current.Glossaries(), paths) {
		return fmt.Errorf("作品配置或术语路径在生成期间发生变化")
	}
	if err := checkWorkspaceSource(current); err != nil {
		return err
	}
	currentSource, err := format.LoadSource(current.Config.Adapter, current.Input())
	if err != nil {
		return err
	}
	currentEntries, err := rules.LoadGlossaries(paths)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(quality.NewCharacterInput(currentSource.Segments, currentEntries), input) {
		return fmt.Errorf("原文或术语表在生成期间发生变化")
	}
	currentModels, err := current.Config.ValidateModelSettings()
	if err != nil {
		return err
	}
	currentModel, err := currentModels.SelectModel(*modelName)
	if err != nil {
		return err
	}
	if currentModel != selected {
		return fmt.Errorf("模型配置在生成期间发生变化")
	}
	if err := fileutil.CheckNewPath(*out, protected...); err != nil {
		return err
	}
	if err := jsonfile.Publish(*out, cards); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"characters": len(cards.Characters), "output": *out})
}
