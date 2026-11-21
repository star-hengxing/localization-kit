package job

import (
	"encoding/json"
	"os"
	"path/filepath"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/translate/rules"
)

func Audit(jobPath string) error {
	if err := rules.LoadSimplified(filepath.Join(common.ResourceRoot, rules.DefaultDataPath)); err != nil {
		return err
	}
	report, err := agent.AuditPolish(jobPath)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
