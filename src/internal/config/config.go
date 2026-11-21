package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Adapter    string           `toml:"adapter"`
	Input      string           `toml:"input"`
	Model      string           `toml:"model,omitempty"`
	Glossaries []string         `toml:"glossaries,omitempty"`
	Cards      string           `toml:"cards,omitempty"`
	LogLevel   string           `toml:"log_level,omitempty"`
	Models     map[string]Model `toml:"models,omitempty"`
}

type Model struct {
	URL     string `toml:"url"`
	Model   string `toml:"model"`
	Profile string `toml:"profile"`
}

// ModelRecord keeps saved manifests and execution hashes stable.
type ModelRecord struct {
	Model
	LegacyRevision string `json:"Revision"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取模型配置 %s: %w", path, err)
	}
	var cfg Config
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("解析模型配置 %s: %w", path, err)
	}
	validated, err := cfg.ValidateModelSettings()
	if err != nil {
		return Config{}, fmt.Errorf("作品配置 %s: %w", path, err)
	}
	return validated, nil
}

// ValidateModelSettings checks shared settings and fills the default log level.
// SelectModel validates the entry chosen for the current command.
func (cfg Config) ValidateModelSettings() (Config, error) {
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return Config{}, fmt.Errorf("log_level 无效: %w", err)
	}
	if len(cfg.Models) == 0 {
		return Config{}, fmt.Errorf("作品配置没有模型，请填写 [models.<名称>]")
	}
	return cfg, nil
}

func (c Config) SelectModel(name string) (Model, error) {
	if name == "" {
		name = c.Model
		if name == "" && len(c.Models) == 1 {
			for key := range c.Models {
				name = key
			}
		}
		if name == "" {
			return Model{}, fmt.Errorf("请在作品配置中设置 model 或使用 --model 指定模型")
		}
	}
	model, ok := c.Models[name]
	if !ok {
		return Model{}, fmt.Errorf("模型 %q 未在配置中登记", name)
	}
	address, err := url.Parse(model.URL)
	if err != nil || address == nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return Model{}, fmt.Errorf("模型 %q 的 URL 无效", name)
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(model.Model) == "" {
		return Model{}, fmt.Errorf("模型 %q 必须填写 model", name)
	}
	if model.Profile != "" && strings.TrimSpace(model.Profile) == "" {
		return Model{}, fmt.Errorf("模型 %q 的 profile 无效", name)
	}
	return model, nil
}

func VerifyModel(path, name string, expected Model) error {
	current, err := Load(path)
	if err != nil {
		return err
	}
	selected, err := current.SelectModel(name)
	if err != nil {
		return err
	}
	if selected != expected {
		return fmt.Errorf("模型配置已变化，请重新执行")
	}
	return nil
}
