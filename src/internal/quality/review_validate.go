package quality

import (
	"fmt"
	"localization-kit/src/internal/store"
)

func ValidateReviewFiles(jobPathValue, inputPath, reportPathValue string) (summary map[string]any, resultErr error) {
	if jobPathValue == "" || inputPath == "" || reportPathValue == "" {
		return nil, fmt.Errorf("missing review inputs")
	}
	jobPath, input, reportPath := &jobPathValue, &inputPath, &reportPathValue
	run := func() error {

		material, err := ReadReviewMaterial(*input)
		if err != nil {
			return err
		}
		report, err := ReadReviewReport(*reportPath)
		if err != nil {
			return err
		}
		job, err := store.OpenReadOnly(*jobPath)
		if err != nil {
			return err
		}
		defer job.Close()
		if err := ValidateReview(job, material, report); err != nil {
			return err
		}
		counts := map[string]int{"pass": 0, "issues": 0, "uncertain": 0}
		for _, item := range report.Items {
			counts[item.Verdict]++
		}
		summary = map[string]any{"valid": true, "verdicts": counts, "all_pass": counts["pass"] == len(report.Items)}
		return nil
	}
	resultErr = run()
	return summary, resultErr
}
