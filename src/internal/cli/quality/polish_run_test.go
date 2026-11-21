package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"localization-kit/src/internal/quality"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
)

func TestPolishRunProducesValidatedCandidateWithoutEditingJob(t *testing.T) {
	root := t.TempDir()
	jobPath := filepath.Join(root, "job")
	segments := []document.Segment{{ID: "one", Group: "a", Src: "こんにちは"}}
	job, err := store.OpenOrCreate(jobPath, segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Append(document.Result{ID: "one", Src: "こんにちは", Dst: "你好", Status: "done", TermsHash: translate.TermsHashes(segments, nil)["a"]}); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(jobPath, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	reviewPath, materialPath := filepath.Join(root, "review.json"), filepath.Join(root, "polish.json")
	if err := prepareReview([]string{"--job", jobPath, "--kind", "fidelity", "--out", reviewPath}); err != nil {
		t.Fatal(err)
	}
	if err := preparePolish([]string{"--job", jobPath, "--input", reviewPath, "--mode", "fix", "--out", materialPath}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			fmt.Fprint(w, `{"n_ctx":8192}`)
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[1,2,3]}`)
		case "/v1/chat/completions":
			content := `{"items":[{"id":"one","dst":"您好","reason":"调整称呼"}]}`
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "quality", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(root, "models.toml")
	config := fmt.Sprintf("[models.quality]\nurl = %q\nmodel = 'quality'\n", server.URL)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "candidate.json")
	args := []string{"--job", jobPath, "--input", materialPath, "--model", "quality", "--run-dir", filepath.Join(root, "run"), "--out", output}
	if err := runPolishModel(context.Background(), args, configPath, loadModelConfig(t, configPath)); err != nil {
		t.Fatal(err)
	}
	candidate, err := quality.ReadPolishResponse(output)
	if err != nil || len(candidate.Items) != 1 || candidate.Items[0].Dst != "您好" || candidate.Actor != "quality" {
		t.Fatalf("unexpected candidate: %+v err=%v", candidate, err)
	}
	if err := processPolishWithValidation([]string{"--job", jobPath, "--input", materialPath, "--file", output}, false, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(jobPath, "results.jsonl"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("job changed: %v", err)
	}
	postMaterialPath := filepath.Join(root, "post-material.json")
	postReportPath := filepath.Join(root, "post-report.json")
	if err := prepareReview([]string{"--job", jobPath, "--kind", "fidelity", "--candidate", output, "--out", postMaterialPath}); err != nil {
		t.Fatal(err)
	}
	postMaterial, err := quality.ReadReviewMaterial(postMaterialPath)
	if err != nil {
		t.Fatal(err)
	}
	postReport := quality.ReviewReport{MaterialHash: postMaterial.Hash, CandidateHash: postMaterial.CandidateHash, Items: []quality.ReviewVerdict{{ID: "one", Verdict: "issues", Issues: []quality.ReviewIssue{{Code: "meaning", Detail: "复核问题", Evidence: []quality.ReviewEvidence{{SegmentID: "one", Quote: "こんにちは"}}}}}}}
	writeReport := func() {
		t.Helper()
		data, err := json.Marshal(postReport)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(postReportPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeReport()
	applyArgs := []string{"--job", jobPath, "--input", materialPath, "--file", output, "--review-input", postMaterialPath, "--review-report", postReportPath}
	if err := processPolishWithValidation(applyArgs, true, nil); err == nil {
		t.Fatal("unapproved polish was applied")
	}
	after, err = os.ReadFile(filepath.Join(jobPath, "results.jsonl"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("rejected polish changed job: %v", err)
	}
	postReport.Items[0].Verdict = "pass"
	postReport.Items[0].Issues = nil
	writeReport()
	validateChangedCards := func(job *store.Store, material quality.ReviewMaterial) error {
		return quality.ValidateCurrentMaterials(material, nil, filepath.Join(root, "new-cards.json"), job.Segments)
	}
	if err := processPolishWithValidation(applyArgs, true, validateChangedCards); err == nil {
		t.Fatal("polish applied after changing current cards")
	}
	after, err = os.ReadFile(filepath.Join(jobPath, "results.jsonl"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("rejected material change modified history: %v", err)
	}
	if err := processPolishWithValidation(applyArgs, true, nil); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenReadOnly(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if got := job.Done["one"]; got.Dst != "您好" || got.Origin != "polish" || got.EditMethod != "polish-fix" || got.EditActor != "quality" || got.EditReason != "调整称呼" || got.PreviousHash != candidate.Items[0].ExpectedResultHash {
		t.Fatalf("unexpected applied result: %+v", got)
	}
}
