package agent

import (
	"fmt"
	"path/filepath"
	"slices"

	"localization-kit/src/internal/store"
)

// CheckWorkspaceMaterials prevents a saved stage configuration from hiding a changed workspace configuration.
func CheckWorkspaceMaterials(jobPath string, glossaries []string, cards string, stage string) error {
	job, err := store.OpenReadOnly(jobPath)
	if err != nil {
		return err
	}
	defer job.Close()
	return CheckWorkspaceMaterialsLocked(jobPath, glossaries, cards, stage)
}

func CheckWorkspaceMaterialsLocked(jobPath string, glossaries []string, cards string, stage string) error {
	state, err := readClaims(jobPath)
	if err != nil {
		return err
	}
	for _, name := range []string{"polish", "proofread"} {
		if stage != "" && stage != name {
			continue
		}
		cfg, ok := state.Stages[name]
		if !ok {
			continue
		}
		if !slices.Equal(cfg.Glossaries, glossaries) || filepath.Clean(cfg.Cards) != filepath.Clean(cards) {
			return fmt.Errorf("%s 阶段材料配置已变化，请重新领取并检查阶段状态", name)
		}
	}
	return nil
}
