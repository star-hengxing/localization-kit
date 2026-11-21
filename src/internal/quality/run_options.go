package quality

import "localization-kit/src/internal/config"

type ModelRunOptions struct {
	ResourceRoot, JobPath, Input, RunDir, Output, ConfigPath, ModelName string
	Config                                                              config.Config
	MaxOutput                                                           int
	Retry                                                               bool
}
