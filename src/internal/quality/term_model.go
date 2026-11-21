package quality

import (
	"encoding/json"
	"fmt"
	"io"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/format"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
	"strings"
)

type TermModelItem struct {
	Src        string         `json:"src"`
	Dst        string         `json:"dst"`
	Kind       string         `json:"kind"`
	Constraint string         `json:"constraint"`
	Variants   []string       `json:"variants,omitempty"`
	Note       string         `json:"note,omitempty"`
	Evidence   []TermEvidence `json:"evidence"`
}

func ParseTermRunResponse(content string) ([]TermModelItem, error) {
	var payload struct {
		Items []TermModelItem `json:"items"`
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("模型结果不是有效 JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || payload.Items == nil {
		return nil, fmt.Errorf("模型结果含多余内容或缺少 items 数组")
	}
	return payload.Items, nil
}

func TermRunSources(jobPath, sourcePath, adapter string) ([]TermSource, error) {
	var segments []document.Segment
	if jobPath != "" {
		job, err := store.OpenReadOnly(jobPath)
		if err != nil {
			return nil, err
		}
		segments = job.Segments
		if err := job.Close(); err != nil {
			return nil, err
		}
	} else {
		source, err := format.LoadSource(adapter, sourcePath)
		if err != nil {
			return nil, err
		}
		segments = source.Segments
	}
	rows := make([]TermSource, 0, len(segments))
	for _, segment := range segments {
		rows = append(rows, TermSource{Segment: segment})
	}
	return rows, nil
}

func TermRunCandidates(batch TermBatch, index int, items []TermModelItem) ([]TermCandidate, error) {
	allowed := map[string]bool{}
	for _, row := range batch.Rows {
		allowed[row.ID] = true
	}
	result := make([]TermCandidate, 0, len(items))
	for i, item := range items {
		for _, evidence := range item.Evidence {
			if !allowed[evidence.SegmentID] {
				return nil, fmt.Errorf("术语证据不属于当前批次: %s", evidence.SegmentID)
			}
		}
		result = append(result, TermCandidate{ID: fmt.Sprintf("b%04d-c%04d", index+1, i+1), Status: "pending", Src: item.Src, Dst: item.Dst, Kind: item.Kind, Constraint: item.Constraint, Variants: item.Variants, Note: item.Note, Evidence: item.Evidence})
	}
	return result, nil
}

func VerifyTermRunInput(glossaries []string, glossaryHash string) error {
	entries, err := rules.LoadGlossaries(glossaries)
	if err != nil {
		return err
	}
	currentHash, err := jsonfile.Digest(entries)
	if err != nil {
		return err
	}
	if currentHash != glossaryHash {
		return fmt.Errorf("现有术语表已变化，请使用新运行目录")
	}
	return nil
}

func InspectTermRun(jobPath, sourcePath, adapter string, original TermMaterial, candidates []TermCandidate, glossaries []string, glossaryHash string) error {
	if err := VerifyTermRunInput(glossaries, glossaryHash); err != nil {
		return err
	}
	rows, err := TermRunSources(jobPath, sourcePath, adapter)
	if err != nil {
		return err
	}
	check := original
	check.Candidates = candidates
	if err := validateTermCandidates(check, rows, glossaries); err != nil {
		return err
	}
	return VerifyTermRunInput(glossaries, glossaryHash)
}
