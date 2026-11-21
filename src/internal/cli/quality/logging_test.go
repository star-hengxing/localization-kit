package quality

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/config"
	"localization-kit/src/internal/logging"
)

func loadModelConfig(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestModelCommandsApplyConfiguredLogLevel(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	dir := t.TempDir()
	configPath := filepath.Join(dir, "models.toml")
	config := "log_level = 'error'\n[models.test]\nurl = 'http://localhost'\nmodel = 'test'\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := loadModelConfig(t, configPath)
	commonArgs := []string{"--input", filepath.Join(dir, "missing.json"), "--model", "test", "--run-dir", filepath.Join(dir, "run"), "--out", filepath.Join(dir, "out.json")}
	for _, tc := range []struct {
		name string
		run  func(context.Context, []string) error
		args []string
	}{
		{"terms", func(ctx context.Context, args []string) error { return runTermModel(ctx, args, configPath, cfg, nil) }, []string{"--source", filepath.Join(dir, "source.jsonl"), "--model", "test", "--run-dir", filepath.Join(dir, "run"), "--out", filepath.Join(dir, "out.json")}},
		{"review", func(ctx context.Context, args []string) error { return runReviewModel(ctx, args, configPath, cfg) }, append([]string{"--job", filepath.Join(dir, "job")}, commonArgs...)},
		{"polish", func(ctx context.Context, args []string) error { return runPolishModel(ctx, args, configPath, cfg) }, append([]string{"--job", filepath.Join(dir, "job")}, commonArgs...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logging.ConfigureLogger("info")
			if err := tc.run(context.Background(), tc.args); err == nil {
				t.Fatal("missing input unexpectedly accepted")
			}
			if slog.Default().Enabled(context.Background(), slog.LevelWarn) {
				t.Fatal("configured error level was not applied")
			}
		})
	}
}
