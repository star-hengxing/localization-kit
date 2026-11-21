package quality

import (
	"context"
	"fmt"
	"localization-kit/src/internal/config"
)

func TermsWithValidation(ctx context.Context, args []string, configPath string, cfg config.Config, validate func() error) error {
	if len(args) > 0 && args[0] == "run" {
		return runTermModel(ctx, args[1:], configPath, cfg, validate)
	}
	return fmt.Errorf("术语命令不支持该操作")
}
