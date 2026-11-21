package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"localization-kit/src/internal/format"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/workspace"
)

func sameWorkspaceInputs(a, b workspace.Config) bool {
	return sameTranslationInputs(a, b) && a.Cards == b.Cards
}

func sameTranslationInputs(a, b workspace.Config) bool {
	return a.Adapter == b.Adapter && a.Input == b.Input && slices.Equal(a.Glossaries, b.Glossaries)
}

func validateWorkspaceSource(w workspace.Workspace, job *store.Store, unchangedConfig bool) (workspace.Workspace, error) {
	current, err := workspace.Load(w.Root)
	if err != nil {
		return workspace.Workspace{}, err
	}
	if unchangedConfig && !sameWorkspaceInputs(current.Config, w.Config) {
		return workspace.Workspace{}, fmt.Errorf("作品配置已变化，请重新执行")
	}
	source, err := format.LoadSource(current.Config.Adapter, current.Input())
	if err != nil {
		return workspace.Workspace{}, err
	}
	if err := job.VerifySegments(source.Segments); err != nil {
		return workspace.Workspace{}, err
	}
	return current, nil
}

func checkWorkspaceSource(w workspace.Workspace) error {
	job, err := store.OpenReadOnly(w.State())
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(w.State()); statErr == nil {
			return err
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		_, err = format.LoadSource(w.Config.Adapter, w.Input())
		return err
	}
	if err != nil {
		return err
	}
	defer job.Close()
	_, err = validateWorkspaceSource(w, job, true)
	return err
}
