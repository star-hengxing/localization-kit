package quality

import (
	"fmt"
	"reflect"
	"strings"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/translate/rules"
)

type TermEvidence struct {
	SegmentID string `json:"segment_id"`
	Quote     string `json:"quote"`
}

type TermCandidate struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	Src        string         `json:"src"`
	Dst        string         `json:"dst"`
	Kind       string         `json:"kind"`
	Constraint string         `json:"constraint"`
	Variants   []string       `json:"variants,omitempty"`
	Note       string         `json:"note,omitempty"`
	Evidence   []TermEvidence `json:"evidence"`
}

type TermSource struct {
	document.Segment
	Dst        string `json:"dst,omitempty"`
	ResultHash string `json:"result_hash,omitempty"`
}

type TermBatch struct {
	Group string       `json:"group"`
	Rows  []TermSource `json:"rows"`
}

type TermMaterial struct {
	Version      int             `json:"version"`
	Kind         string          `json:"kind"`
	SourceHash   string          `json:"source_hash"`
	Groups       []string        `json:"groups"`
	MaterialHash string          `json:"material_hash"`
	Batches      []TermBatch     `json:"batches"`
	Candidates   []TermCandidate `json:"candidates"`
}

func termMaterialDigest(p TermMaterial) (string, error) {
	return jsonfile.Digest(struct {
		Kind       string      `json:"kind"`
		SourceHash string      `json:"source_hash"`
		Groups     []string    `json:"groups"`
		Batches    []TermBatch `json:"batches"`
	}{p.Kind, p.SourceHash, p.Groups, p.Batches})
}

// Hashes detect accidental material changes; they do not authenticate an untrusted editor.
func validateTermCandidates(p TermMaterial, sources []TermSource, glossaries []string) error {
	if p.Candidates == nil {
		return fmt.Errorf("candidates must be an array")
	}
	byID := map[string]TermSource{}
	for _, source := range sources {
		byID[source.ID] = source
	}
	selected := map[string]TermSource{}
	selectedGroups := map[string]bool{}
	for _, group := range p.Groups {
		if selectedGroups[group] {
			return fmt.Errorf("duplicate selected group %q", group)
		}
		selectedGroups[group] = true
	}
	if len(selectedGroups) == 0 {
		return fmt.Errorf("material package has no selected groups")
	}
	expectedRows := []TermSource{}
	for _, source := range sources {
		if selectedGroups[source.Group] {
			expectedRows = append(expectedRows, source)
		}
	}
	sourceHash, err := jsonfile.Digest(expectedRows)
	if err != nil {
		return err
	}
	if p.SourceHash != sourceHash {
		return fmt.Errorf("selected source snapshot is stale; prepare again")
	}
	lastIndex := -1
	positions := map[string]int{}
	for i, source := range sources {
		positions[source.ID] = i
	}
	for _, batch := range p.Batches {
		if !selectedGroups[batch.Group] {
			return fmt.Errorf("unselected material group %q", batch.Group)
		}
		if len(batch.Rows) == 0 {
			return fmt.Errorf("empty material batch")
		}
		for _, row := range batch.Rows {
			original, ok := byID[row.ID]
			if !ok || !reflect.DeepEqual(original, row) || row.Group != batch.Group || positions[row.ID] <= lastIndex {
				return fmt.Errorf("altered, duplicated or reordered source %q", row.ID)
			}
			selected[row.ID] = row
			lastIndex = positions[row.ID]
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("material package has no sources")
	}
	for _, source := range sources {
		if selectedGroups[source.Group] {
			if _, ok := selected[source.ID]; !ok {
				return fmt.Errorf("incomplete group %q", source.Group)
			}
		}
	}
	base, err := rules.LoadGlossaries(glossaries)
	if err != nil {
		return err
	}
	forms := map[string]rules.Entry{}
	for _, entry := range base {
		for _, form := range append([]string{entry.Src}, entry.Variants...) {
			if old, ok := forms[form]; ok && (old.Dst != entry.Dst || old.Kind != entry.Kind || old.Constraint != entry.Constraint) {
				return fmt.Errorf("existing glossary conflict for %q", form)
			}
			forms[form] = entry
		}
	}
	ids := map[string]bool{}
	addAcceptedForms := func(candidate TermCandidate) error {
		entry := rules.Entry{Src: candidate.Src, Dst: candidate.Dst, Kind: candidate.Kind, Constraint: candidate.Constraint}
		spellings := append([]string{candidate.Src}, candidate.Variants...)
		for _, form := range spellings {
			if old, ok := forms[form]; ok && (old.Dst != entry.Dst || old.Kind != entry.Kind || old.Constraint != entry.Constraint) {
				return fmt.Errorf("term %q collides with %q", form, old.Src)
			}
		}
		for _, form := range spellings {
			if _, ok := forms[form]; !ok {
				forms[form] = entry
			}
		}
		return nil
	}
	checkItem := func(id, status string, evidence []TermEvidence, src string) error {
		if strings.TrimSpace(id) == "" || ids[id] {
			return fmt.Errorf("empty or duplicate id %q", id)
		}
		ids[id] = true
		if status != "pending" && status != "accepted" && status != "rejected" {
			return fmt.Errorf("invalid status for %q", id)
		}
		if strings.TrimSpace(src) == "" || len(evidence) == 0 {
			return fmt.Errorf("missing source/evidence for %q", id)
		}
		found := false
		for _, ev := range evidence {
			row, ok := selected[ev.SegmentID]
			if !ok || strings.TrimSpace(ev.Quote) == "" || !strings.Contains(row.Src, ev.Quote) {
				return fmt.Errorf("invalid evidence for %q", id)
			}
			found = found || strings.Contains(ev.Quote, src)
		}
		if !found {
			return fmt.Errorf("source does not occur in evidence for %q", id)
		}
		return nil
	}
	validate := func(c TermCandidate) error {
		if err := checkItem(c.ID, c.Status, c.Evidence, c.Src); err != nil {
			return err
		}
		if strings.TrimSpace(c.Dst) == "" || (c.Kind != "term" && c.Kind != "name" && c.Kind != "onomatopoeia") || (c.Constraint != "hard" && c.Constraint != "soft") {
			return fmt.Errorf("invalid term fields for %q", c.ID)
		}
		seen := map[string]bool{c.Src: true}
		for _, variant := range c.Variants {
			found := false
			for _, ev := range c.Evidence {
				found = found || strings.Contains(ev.Quote, variant)
			}
			if strings.TrimSpace(variant) == "" || seen[variant] || !found {
				return fmt.Errorf("duplicate or unsupported variant for %q", c.ID)
			}
			seen[variant] = true
		}
		if c.Status != "accepted" {
			return nil
		}
		return addAcceptedForms(c)
	}
	if p.Kind != "terms" {
		return fmt.Errorf("invalid term material kind")
	}
	for _, c := range p.Candidates {
		if err := validate(c); err != nil {
			return err
		}
	}
	return nil
}
