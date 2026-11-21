package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"localization-kit/src/internal/config"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/format"
	"localization-kit/src/internal/materials"
)

type Config = config.Config

type Workspace struct {
	Root   string
	Config Config
}

func managedPath(root, name string) error {
	path := filepath.Join(root, name)
	resolved, err := fileutil.Canonical(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(path), resolved) {
		return fmt.Errorf("作品 %s 路径包含链接或越界", name)
	}
	return nil
}

func (w Workspace) State() string { return filepath.Join(w.Root, "state") }
func (w Workspace) Input() string { return w.Resolve(w.Config.Input) }
func (w Workspace) Resolve(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(w.Root, filepath.FromSlash(path))
}
func (w Workspace) Glossaries() []string {
	paths := make([]string, 0, len(w.Config.Glossaries))
	for _, path := range w.Config.Glossaries {
		paths = append(paths, w.Resolve(path))
	}
	if len(paths) > 0 {
		return paths
	}
	workGlossary := filepath.Join(w.Root, "materials", "glossary.json")
	if _, err := os.Stat(workGlossary); err == nil || !errors.Is(err, os.ErrNotExist) {
		paths = append(paths, workGlossary)
	}
	return paths
}
func (w Workspace) Cards() string {
	path := filepath.Join(w.Root, "materials", "characters.json")
	if w.Config.Cards != "" {
		path = w.Resolve(w.Config.Cards)
	}
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if w.Config.Cards != "" {
			return path
		}
		return ""
	}
	if err != nil {
		return path
	}
	if empty, err := materials.IsUnfilledCard(path); err == nil && empty {
		return ""
	}
	return path
}

func (w Workspace) MaterialDigest(includeCards bool) (string, error) {
	h := sha256.New()
	paths := w.Glossaries()
	if includeCards {
		if cards := w.Cards(); cards != "" {
			paths = append(paths, cards)
		}
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "%d\x00", len(data))
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Load(path string) (Workspace, error) {
	root, err := fileutil.Canonical(path)
	if err != nil {
		return Workspace{}, err
	}
	for _, name := range []string{"source", "state", "build", "materials"} {
		if err := managedPath(root, name); err != nil {
			return Workspace{}, err
		}
	}
	f, err := os.Open(filepath.Join(root, "config.toml"))
	if err != nil {
		return Workspace{}, fmt.Errorf("读取作品配置: %w", err)
	}
	defer f.Close()
	var cfg Config
	decoder := toml.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Workspace{}, fmt.Errorf("作品配置无效: %w", err)
	}
	if cfg.Adapter != "artc" && cfg.Adapter != "vtt" && cfg.Adapter != "jsonl" {
		return Workspace{}, fmt.Errorf("作品格式必须为 artc、vtt 或 jsonl")
	}
	if cfg.Input == "" {
		return Workspace{}, fmt.Errorf("作品配置缺少 input")
	}
	w := Workspace{Root: root, Config: cfg}
	input, err := fileutil.Canonical(w.Input())
	if err != nil {
		return Workspace{}, err
	}
	source, err := fileutil.Canonical(filepath.Join(root, "source"))
	if err != nil {
		return Workspace{}, err
	}
	rel, err := filepath.Rel(source, input)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
		return Workspace{}, fmt.Errorf("原文必须位于作品 source 目录")
	}
	return w, nil
}

func Init(path, adapter, input string) (Workspace, error) {
	root, err := fileutil.Canonical(path)
	if err != nil {
		return Workspace{}, err
	}
	for _, name := range []string{"source", "state", "build", "materials"} {
		if err := managedPath(root, name); err != nil {
			return Workspace{}, err
		}
	}
	if adapter != "artc" && adapter != "vtt" && adapter != "jsonl" {
		return Workspace{}, fmt.Errorf("需要 --adapter artc|vtt|jsonl")
	}
	if input == "" {
		input = "source"
		if adapter == "jsonl" {
			input = "source/input.jsonl"
		}
	}
	cfg := Config{Adapter: adapter, Input: filepath.ToSlash(input)}
	w := Workspace{Root: root, Config: cfg}
	source, err := fileutil.Canonical(filepath.Join(root, "source"))
	if err != nil {
		return Workspace{}, err
	}
	original, err := fileutil.Canonical(w.Input())
	if err != nil {
		return Workspace{}, err
	}
	rel, err := filepath.Rel(source, original)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
		return Workspace{}, fmt.Errorf("原文必须位于作品 source 目录")
	}
	loaded, err := format.LoadSource(adapter, original)
	if err != nil {
		return Workspace{}, err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return Workspace{}, err
	}
	data = append(data, []byte("\n# 模型示例：取消注释并填写部署地址与模型 ID。\n# model = 'translate'\n# [models.translate]\n# url = '<模型 API 地址>'\n# model = '<模型 ID>'\n# profile = 'hy-mt2'\n")...)
	configPath := filepath.Join(root, "config.toml")
	if _, err := os.Lstat(configPath); err == nil {
		return Workspace{}, fmt.Errorf("作品配置已存在")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Workspace{}, err
	}
	if existing, err := os.ReadDir(w.State()); err == nil && len(existing) != 0 {
		return Workspace{}, fmt.Errorf("状态目录非空，无法初始化")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Workspace{}, err
	}
	{
		f, err := os.CreateTemp(root, ".config-*.toml")
		if err != nil {
			return Workspace{}, err
		}
		name := f.Name()
		defer os.Remove(name)
		_, writeErr := f.Write(data)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return Workspace{}, err
		}
		if err := os.Link(name, configPath); err != nil {
			return Workspace{}, err
		}
	}
	stats, err := materials.InitTerms(root, loaded.Segments, false)
	if err != nil {
		return Workspace{}, fmt.Errorf("作品配置已创建，但术语初始化失败；可运行 terms init --project-config %s 重试: %w", configPath, err)
	}
	slog.Info(fmt.Sprintf("术语表=%s 统计=%s 词语=%v", stats["glossary"], stats["statistics"], stats["words"]))
	return w, nil
}
