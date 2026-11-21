package quality

import (
	"fmt"

	"localization-kit/src/internal/jsonfile"
)

func BuildTermBatches(rows []TermSource, batchLines int) (TermMaterial, error) {
	if batchLines < 1 || len(rows) == 0 {
		return TermMaterial{}, fmt.Errorf("术语原文为空或批次大小无效")
	}
	material := TermMaterial{Version: 2, Kind: "terms", Groups: []string{}, Batches: []TermBatch{}, Candidates: []TermCandidate{}}
	groups := map[string]bool{}
	for _, row := range rows {
		if !groups[row.Group] {
			groups[row.Group] = true
			material.Groups = append(material.Groups, row.Group)
		}
		n := len(material.Batches)
		if n == 0 || material.Batches[n-1].Group != row.Group || len(material.Batches[n-1].Rows) >= batchLines {
			material.Batches = append(material.Batches, TermBatch{Group: row.Group, Rows: []TermSource{}})
			n++
		}
		material.Batches[n-1].Rows = append(material.Batches[n-1].Rows, row)
	}
	sourceHash, err := jsonfile.Digest(rows)
	if err != nil {
		return TermMaterial{}, err
	}
	material.SourceHash = sourceHash
	material.MaterialHash, err = termMaterialDigest(material)
	if err != nil {
		return TermMaterial{}, err
	}
	return material, nil
}
