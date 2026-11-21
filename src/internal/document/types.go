package document

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type Segment struct {
	ID    string         `json:"id"`
	Group string         `json:"group"`
	Src   string         `json:"src"`
	Meta  map[string]any `json:"meta,omitempty"`
}

type Issue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type ExportLine struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

type StageCompletion struct {
	TaskID         string       `json:"task_id"`
	TaskSize       int          `json:"task_size,omitempty"`
	Worker         string       `json:"worker"`
	Model          string       `json:"model"`
	Revision       string       `json:"revision,omitempty"`
	Context        int          `json:"context,omitempty"`
	MaxLines       int          `json:"max_lines,omitempty"`
	Scope          string       `json:"scope,omitempty"`
	BlockStart     string       `json:"block_start,omitempty"`
	BlockEnd       string       `json:"block_end,omitempty"`
	Digest         string       `json:"digest"`
	Basis          string       `json:"basis"`
	SubmissionHash string       `json:"submission_hash"`
	Issues         []StageIssue `json:"issues,omitempty"`
}

type StageIssue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Quote  string `json:"quote"`
}

type Result struct {
	ID                  string           `json:"id"`
	Stage               string           `json:"stage,omitempty"`
	Src                 string           `json:"src"`
	Dst                 string           `json:"dst"`
	Warnings            []Issue          `json:"warnings,omitempty"`
	Model               string           `json:"model"`
	InitialModel        string           `json:"initial_model,omitempty"`
	ModelURL            string           `json:"model_url,omitempty"`
	ModelRevision       string           `json:"model_revision,omitempty"`
	Profile             string           `json:"profile"`
	Usage               json.RawMessage  `json:"usage,omitempty"`
	Finish              string           `json:"finish_reason,omitempty"`
	Passthrough         bool             `json:"passthrough,omitempty"`
	Status              string           `json:"status,omitempty"`
	Signature           string           `json:"signature,omitempty"`
	Violations          []Issue          `json:"violations,omitempty"`
	Origin              string           `json:"origin,omitempty"`
	PreviousHash        string           `json:"previous_hash,omitempty"`
	NeedsReview         bool             `json:"needs_review,omitempty"`
	ReviewSignature     string           `json:"review_signature,omitempty"`
	TermsHash           string           `json:"terms_hash,omitempty"`
	ReviewTermsHash     string           `json:"review_terms_hash,omitempty"`
	LegacyRecord        json.RawMessage  `json:"legacy_record,omitempty"`
	HistoryUsed         bool             `json:"history_used,omitempty"`
	HistoryHash         string           `json:"history_hash,omitempty"`
	ContextReview       bool             `json:"context_review,omitempty"`
	ContextRevision     int              `json:"context_revision,omitempty"`
	ReviewHistoryHash   string           `json:"review_history_hash,omitempty"`
	EditMethod          string           `json:"edit_method,omitempty"`
	EditActor           string           `json:"edit_actor,omitempty"`
	EditReason          string           `json:"edit_reason,omitempty"`
	PolishContextHash   string           `json:"polish_context_hash,omitempty"`
	PolishContextReview bool             `json:"polish_context_review,omitempty"`
	PolishActors        []string         `json:"polish_actors,omitempty"`
	ProofreadActors     []string         `json:"proofread_actors,omitempty"`
	PolishCompletion    *StageCompletion `json:"polish_completion,omitempty"`
	ProofreadCompletion *StageCompletion `json:"proofread_completion,omitempty"`
}

func (r Result) IsRevision() bool {
	return r.Origin == "manual" || r.Origin == "polish" || r.Origin == "proofread"
}

// HasProtectedOrigin includes translations already imported into a workspace.
func (r Result) HasProtectedOrigin() bool { return r.IsRevision() || r.Origin == "legacy" }

func ResultHash(result Result) string {
	// Existing history links include all stored fields, including LegacyRecord.
	data, _ := json.Marshal(result)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
