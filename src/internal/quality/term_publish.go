package quality

import (
	"errors"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
)

func publishTermRun(jobPath, sourcePath, adapter, out string, material, result TermMaterial, glossaries []string, glossaryHash string, batchCount int, validate func() error) (summary map[string]any, resultErr error) {
	run := func() error {
		if err := InspectTermRun(jobPath, sourcePath, adapter, material, result.Candidates, glossaries, glossaryHash); err != nil {
			return err
		}
		entries := make([]rules.Entry, 0, len(result.Candidates))
		for _, c := range result.Candidates {
			entries = append(entries, rules.Entry{Src: c.Src, Dst: c.Dst, Kind: c.Kind, Constraint: c.Constraint, Variants: c.Variants, Note: c.Note})
		}
		if validate != nil {
			if err := validate(); err != nil {
				return err
			}
		}
		if jobPath != "" {
			job, err := store.OpenReadOnly(jobPath)
			if err != nil {
				return err
			}
			rows := make([]TermSource, 0, len(job.Segments))
			for _, segment := range job.Segments {
				rows = append(rows, TermSource{Segment: segment})
			}
			err = VerifyTermRunInput(glossaries, glossaryHash)
			if err == nil {
				err = validateTermCandidates(result, rows, glossaries)
			}
			if err == nil {
				if validate != nil {
					err = validate()
				}
			}
			if err == nil {
				err = VerifyTermRunInput(glossaries, glossaryHash)
			}
			if err == nil {
				err = jsonfile.Publish(out, struct {
					Entries []rules.Entry `json:"entries"`
				}{Entries: entries})
			}
			if err = errors.Join(err, job.Close()); err != nil {
				return err
			}
		} else {
			rows, err := TermRunSources(jobPath, sourcePath, adapter)
			if err != nil {
				return err
			}
			if err := validateTermCandidates(result, rows, glossaries); err != nil {
				return err
			}
			if err := VerifyTermRunInput(glossaries, glossaryHash); err != nil {
				return err
			}
			if validate != nil {
				if err := validate(); err != nil {
					return err
				}
			}
			if err := jsonfile.Publish(out, struct {
				Entries []rules.Entry `json:"entries"`
			}{Entries: entries}); err != nil {
				return err
			}
		}
		summary = map[string]any{"batches": batchCount, "terms": len(entries), "output": out}
		return nil
	}
	resultErr = run()
	return summary, resultErr
}
