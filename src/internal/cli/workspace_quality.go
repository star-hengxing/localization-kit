package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	qualitycli "localization-kit/src/internal/cli/quality"
	"localization-kit/src/internal/config"
	"localization-kit/src/internal/format"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/materials"
	"localization-kit/src/internal/quality"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
	"localization-kit/src/internal/workspace"
)

func initMaterials(kind, root string, args []string) error {
	flags := flag.NewFlagSet(kind+" init", flag.ContinueOnError)
	refresh := false
	if kind == "terms" {
		flags.BoolVar(&refresh, "refresh", false, "重新生成统计")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("不接受额外位置参数")
	}
	w, err := workspace.Load(root)
	if err != nil {
		return err
	}
	if err := checkWorkspaceSource(w); err != nil {
		return err
	}
	if kind == "characters" {
		path, err := materials.InitCharacters(w.Root)
		if err != nil {
			return err
		}
		slog.Info("角色卡模板已创建 " + path)
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"characters": path})
	}
	source, err := format.LoadSource(w.Config.Adapter, w.Input())
	if err != nil {
		return err
	}
	result, err := materials.InitTerms(w.Root, source.Segments, refresh)
	if err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("术语表=%s 统计=%s 词语=%v 状态=%s", result["glossary"], result["statistics"], result["words"], result["status"]))
	return json.NewEncoder(os.Stdout).Encode(result)
}

func workspaceQuality(ctx context.Context, w workspace.Workspace, kind, action string, rest []string) error {
	if (kind == "terms" || kind == "characters") && action == "prepare" {
		return fmt.Errorf("此命令已移除，请使用 %s init [作品目录]", kind)
	}
	if (kind == "terms" || kind == "characters") && action == "validate" {
		return validateMaterials(w, kind, rest)
	}
	if (kind == "terms" || kind == "characters") && action == "compile" {
		return fmt.Errorf("材料直接填写到 JSON 文件，不需要 compile")
	}
	if err := checkWorkspaceSource(w); err != nil {
		return err
	}
	if kind == "characters" && action == "run" {
		return runCharacters(ctx, w, rest)
	}
	for _, arg := range rest {
		name := workspaceFlagName(arg)
		if name == "job" || name == "source" || name == "adapter" {
			return fmt.Errorf("原文和状态由作品目录指定")
		}
		if kind == "terms" && action == "run" && name == "glossary" {
			return fmt.Errorf("作品术语表请在 config.toml 中配置")
		}
		if kind == "review" && action == "prepare" && (name == "cards" || name == "glossary") || kind == "characters" && action == "warnings" && name == "cards" {
			return fmt.Errorf("作品材料请在 config.toml 中配置")
		}
	}
	params := []string{action, "--job", w.State()}
	var models config.Config
	if action == "run" {
		var err error
		models, err = w.Config.ValidateModelSettings()
		if err != nil {
			return err
		}
	}
	if kind == "terms" {
		if action != "run" {
			return fmt.Errorf("未知术语命令 %s；可用 init、validate、run", action)
		}
		if err := os.MkdirAll(filepath.Join(w.Root, "materials"), 0700); err != nil {
			return err
		}
		glossaries := w.Glossaries()
		params = []string{"run", "--source", w.Input(), "--adapter", w.Config.Adapter, "--run-dir", filepath.Join(w.Root, "materials", "terms-run"), "--out", filepath.Join(w.Root, "materials", "terms-candidates.json")}
		params = append(params, workspaceDefaults(w, false)...)
		params = append(params, rest...)
		return qualitycli.TermsWithValidation(ctx, params, filepath.Join(w.Root, "config.toml"), models, func() error {
			current, err := workspace.Load(w.Root)
			if err != nil {
				return err
			}
			if current.Config.Adapter != w.Config.Adapter || current.Config.Input != w.Config.Input || !slices.Equal(current.Glossaries(), glossaries) {
				return fmt.Errorf("作品配置或术语路径在提取期间发生变化")
			}
			return nil
		})
	}
	if kind == "characters" {
		if action != "warnings" {
			return fmt.Errorf("未知角色命令 %s；可用 init、validate、warnings", action)
		}
		if w.Cards() != "" {
			params = append(params, "--cards", w.Cards())
		}
		params = append(params, rest...)
		return qualitycli.Characters(ctx, params)
	}
	if err := os.MkdirAll(filepath.Join(w.Root, "materials"), 0700); err != nil {
		return err
	}
	if kind == "review" && action == "prepare" {
		params = append(params, workspaceDefaults(w, true)...)
	}
	params = append(params, rest...)
	if kind == "review" {
		return qualitycli.Review(ctx, params, filepath.Join(w.Root, "config.toml"), models)
	}
	return qualitycli.PolishWithValidation(ctx, params, filepath.Join(w.Root, "config.toml"), models, func(job *store.Store, material quality.ReviewMaterial) error {
		current, err := validateWorkspaceSource(w, job, false)
		if err != nil {
			return err
		}
		entries, err := rules.LoadGlossaries(current.Glossaries())
		if err != nil {
			return err
		}
		return quality.ValidateCurrentMaterials(material, entries, current.Cards(), job.Segments)
	})
}

func validateMaterials(w workspace.Workspace, kind string, args []string) error {
	flags := flag.NewFlagSet(kind+" validate", flag.ContinueOnError)
	path := flags.String("input", filepath.Join(w.Root, "materials", "glossary.json"), "材料 JSON")
	if kind == "characters" {
		*path = filepath.Join(w.Root, "materials", "characters.json")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("不接受额外位置参数")
	}
	if err := checkWorkspaceSource(w); err != nil {
		return err
	}
	source, err := format.LoadSource(w.Config.Adapter, w.Input())
	if err != nil {
		return err
	}
	if kind == "characters" {
		var file quality.CardFile
		if err := jsonfile.Read(*path, &file); err != nil {
			return err
		}
		entries, err := rules.LoadGlossaries(w.Glossaries())
		if err != nil {
			return err
		}
		cards, err := quality.ValidateNewCharacterCards(file, source.Segments, entries)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "valid", "characters": len(cards.Characters)})
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	entries, err := rules.LoadGlossaries([]string{*path})
	if err != nil {
		return err
	}
	current, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, current) {
		return fmt.Errorf("术语表在校验期间发生变化")
	}
	var raw struct {
		Entries []rules.Entry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(string(data), "\ufeff")), &raw); err != nil {
		return err
	}
	forms := map[string]rules.Entry{}
	for _, entry := range raw.Entries {
		if entry.Kind == "" {
			entry.Kind = "term"
		}
		if entry.Constraint == "" {
			entry.Constraint = "soft"
		}
		for _, form := range append([]string{entry.Src}, entry.Variants...) {
			if previous, ok := forms[form]; ok && (previous.Dst != entry.Dst || previous.Kind != entry.Kind || previous.Constraint != entry.Constraint) {
				return fmt.Errorf("术语 %q 存在不同译法或约束，请先选择", form)
			}
			forms[form] = entry
		}
	}
	for _, entry := range entries {
		for _, word := range append([]string{entry.Src}, entry.Variants...) {
			found := false
			for _, segment := range source.Segments {
				if strings.Contains(segment.Src, word) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("术语 %q 不在原文中", word)
			}
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "valid", "entries": len(entries)})
}
