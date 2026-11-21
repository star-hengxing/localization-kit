package cli

import (
	"errors"
	"os"

	"localization-kit/src/internal/agent"
	jobcli "localization-kit/src/internal/cli/job"
	"localization-kit/src/internal/format"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/workspace"
)

func statusWorkspace(w workspace.Workspace, rest []string) error {
	state := w.State()
	return jobcli.StatusWith(rest, func(details bool, id string) (agent.Status, error) {
		job, err := store.OpenReadOnly(state)
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(state); statErr == nil {
				return agent.Status{}, err
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return agent.Status{}, statErr
			}
			source, err := format.LoadSource(w.Config.Adapter, w.Input())
			if err != nil {
				return agent.Status{}, err
			}
			return agent.InitialStatus(source.Segments, details, id)
		}
		if errors.Is(err, store.ErrLocked) {
			source, err := format.LoadSource(w.Config.Adapter, w.Input())
			if err != nil {
				return agent.Status{}, err
			}
			if err := store.VerifySourceSnapshot(state, source.Segments); err != nil {
				return agent.Status{}, err
			}
			if err := agent.CheckWorkspaceMaterialsLocked(state, w.Glossaries(), w.Cards(), ""); err != nil {
				return agent.Status{}, err
			}
			return agent.ReadLiveStatus(state, details, id)
		}
		if err != nil {
			return agent.Status{}, err
		}
		defer job.Close()
		if _, err := validateWorkspaceSource(w, job, true); err != nil {
			return agent.Status{}, err
		}
		if err := agent.CheckWorkspaceMaterialsLocked(state, w.Glossaries(), w.Cards(), ""); err != nil {
			return agent.Status{}, err
		}
		return agent.InspectLocked(state, job, details, id)
	})
}
