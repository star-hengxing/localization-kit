package evaluation

import (
	"fmt"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
)

func CompareFiles(baselineValue, candidateValue, outputValue string) (summary map[string]any, resultErr error) {
	if baselineValue == "" || candidateValue == "" || outputValue == "" {
		return nil, fmt.Errorf("missing evaluation paths")
	}
	baselinePath, candidatePath, output := &baselineValue, &candidateValue, &outputValue
	run := func() error {

		baseAbs, err := fileutil.Canonical(*baselinePath)
		if err != nil {
			return err
		}
		candidateAbs, err := fileutil.Canonical(*candidatePath)
		if err != nil {
			return err
		}
		outAbs, err := fileutil.Canonical(*output)
		if err != nil {
			return err
		}
		if fileutil.Overlaps(baseAbs, candidateAbs) || fileutil.Overlaps(baseAbs, outAbs) || fileutil.Overlaps(candidateAbs, outAbs) {
			return fmt.Errorf("作业和报告路径不能互相重叠")
		}
		baseline, err := store.OpenReadOnly(*baselinePath)
		if err != nil {
			return err
		}
		defer baseline.Close()
		candidate, err := store.OpenReadOnly(*candidatePath)
		if err != nil {
			return err
		}
		defer candidate.Close()
		comparison, err := Compare(baseline, candidate)
		if err != nil {
			return err
		}
		comparison.BaselineJob = baseAbs
		comparison.CandidateJob = candidateAbs
		if err := jsonfile.Create(*output, comparison); err != nil {
			return err
		}
		summary = map[string]any{"same": comparison.Same, "changed": comparison.Changed, "unpaired": comparison.Unpaired, "report": *output}
		return nil
	}
	resultErr = run()
	return summary, resultErr
}
