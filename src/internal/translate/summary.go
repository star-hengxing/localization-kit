package translate

import (
	"localization-kit/src/internal/store"
)

func Summarize(job *store.Store) Summary {
	summary := Summary{Total: len(job.Segments), Done: len(job.Done), Failed: len(job.Failed)}
	for _, record := range job.Done {
		if record.NeedsReview {
			summary.Review++
		}
	}
	summary.Pending = summary.Total - summary.Done - summary.Failed
	return summary
}
