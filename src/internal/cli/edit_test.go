package cli

import (
	"bytes"
	"context"
	"encoding/json"
	jobcli "localization-kit/src/internal/cli/job"
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/config"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/model"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
)

func TestManualEditProtectedFromRetranslation(t *testing.T) {
	root := t.TempDir()
	jobPath := filepath.Join(root, "job")
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}}
	job, err := store.OpenOrCreate(jobPath, segments)
	if err != nil {
		t.Fatal(err)
	}
	old := document.Result{ID: "1", Src: "こんにちは", Dst: "你好", Status: "done", Model: "old-model", Profile: "hy-mt2", Signature: "old-signature", TermsHash: translate.TermsHashes(segments, nil)["g"]}
	if err := job.Append(old); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(translate.ManualEdit{ID: "1", ExpectedHash: document.ResultHash(old), Dst: "您好"})
	if err != nil {
		t.Fatal(err)
	}
	editFile := filepath.Join(root, "edit.jsonl")
	if err := os.WriteFile(editFile, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := jobcli.Edit(jobPath, []string{"--file", editFile}); err != nil {
		t.Fatal(err)
	}
	if err := jobcli.Edit(jobPath, []string{"--file", editFile}); err == nil {
		t.Fatal("stale edit accepted")
	}
	job, err = store.OpenExisting(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	selected := config.Model{URL: "http://127.0.0.1:1", Model: "new-model", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := translate.Run(context.Background(), client, selected, segments, nil, job, false)
	if err != nil || summary.Review != 1 || job.Done["1"].Origin != "manual" || job.Done["1"].Dst != "您好" || job.Done["1"].PreviousHash != document.ResultHash(old) {
		t.Fatalf("summary=%+v result=%+v err=%v", summary, job.Done["1"], err)
	}
	marked := job.Done["1"]
	if !marked.NeedsReview || translate.Summarize(job).Review != 1 {
		t.Fatalf("review state lost: %+v", marked)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(translate.ManualEdit{ID: "1", ExpectedHash: document.ResultHash(marked), Dst: "您好"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editFile, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := jobcli.Edit(jobPath, []string{"--file", editFile}); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenExisting(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	summary, err = translate.Run(context.Background(), client, selected, segments, nil, job, false)
	if err != nil || summary.Review != 0 || job.Done["1"].NeedsReview {
		t.Fatalf("confirmed edit still needs review: %+v %v", summary, err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenExisting(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	terms := []rules.Entry{{Src: "こんに", Dst: "您好", Kind: "term", Constraint: "soft"}}
	summary, err = translate.Run(context.Background(), client, selected, segments, terms, job, false)
	if err != nil || summary.Review != 1 {
		t.Fatalf("term change not detected: %+v %v", summary, err)
	}
	marked = job.Done["1"]
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(translate.ManualEdit{ID: "1", ExpectedHash: document.ResultHash(marked), Dst: "您好"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editFile, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := jobcli.Edit(jobPath, []string{"--file", editFile}); err == nil {
		t.Fatal("review accepted with missing glossary")
	}
	termFile := filepath.Join(root, "glossary.json")
	termData, err := json.Marshal(map[string]any{"entries": terms})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(termFile, termData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := jobcli.Edit(jobPath, []string{"--file", editFile, "--glossary", termFile}); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenExisting(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	summary, err = translate.Run(context.Background(), client, selected, segments, terms, job, false)
	if err != nil || summary.Review != 0 {
		t.Fatalf("correct glossary review not cleared: %+v %v", summary, err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEditInvalidatesLaterHistoryWithoutOverwritingManualWork(t *testing.T) {
	root := t.TempDir()
	jobPath := filepath.Join(root, "job")
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "ありがとう"}, {ID: "3", Group: "g", Src: "こんばんは"}}
	job, err := store.OpenOrCreate(jobPath, segments)
	if err != nil {
		t.Fatal(err)
	}
	termsHash := translate.TermsHashes(segments, nil)["g"]
	var first document.Result
	for i, segment := range segments {
		result := document.Result{ID: segment.ID, Src: segment.Src, Dst: "你好", Status: "done", Signature: "signature", TermsHash: termsHash, HistoryUsed: i > 0}
		if i == 2 {
			result.Origin = "manual"
		}
		if err := job.Append(result); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = result
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(translate.ManualEdit{ID: "1", ExpectedHash: document.ResultHash(first), Dst: "您好"})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "edit.jsonl")
	if err := os.WriteFile(file, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := jobcli.Edit(jobPath, []string{"--file", file}); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenReadOnly(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := job.Done["2"]; ok {
		t.Fatal("later machine translation stayed done")
	}
	if result := job.Done["3"]; result.Dst != "你好" || !result.NeedsReview || !result.ContextReview {
		t.Fatalf("manual work lost or not marked: %+v", result)
	}
	third := job.Done["3"]
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(translate.ManualEdit{ID: "3", ExpectedHash: document.ResultHash(third), Dst: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := jobcli.Edit(jobPath, []string{"--file", file}); err == nil {
		t.Fatal("accepted review while previous translation is pending")
	}
}

func TestBatchEditRejectsChangedHistoryRegardlessOfOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, secondDst := range []string{"你好", "晚上好"} {
			root := t.TempDir()
			jobPath := filepath.Join(root, "job")
			segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "こんばんは"}}
			job, err := store.OpenOrCreate(jobPath, segments)
			if err != nil {
				t.Fatal(err)
			}
			terms := translate.TermsHashes(segments, nil)["g"]
			first := document.Result{ID: "1", Src: segments[0].Src, Dst: "你好", Status: "done", TermsHash: terms}
			second := document.Result{ID: "2", Src: segments[1].Src, Dst: "你好", Status: "done", TermsHash: terms, HistoryUsed: true}
			if err := job.Append(first); err != nil {
				t.Fatal(err)
			}
			if err := job.Append(second); err != nil {
				t.Fatal(err)
			}
			if err := job.Close(); err != nil {
				t.Fatal(err)
			}
			edits := []translate.ManualEdit{{ID: "1", ExpectedHash: document.ResultHash(first), Dst: "您好"}, {ID: "2", ExpectedHash: document.ResultHash(second), Dst: secondDst}}
			if reverse {
				edits[0], edits[1] = edits[1], edits[0]
			}
			var data bytes.Buffer
			for _, item := range edits {
				if err := json.NewEncoder(&data).Encode(item); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(root, "edits.jsonl")
			if err := os.WriteFile(file, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := jobcli.Edit(jobPath, []string{"--file", file}); err == nil {
				t.Fatal("accepted edits based on stale context")
			}
			job, err = store.OpenReadOnly(jobPath)
			if err != nil {
				t.Fatal(err)
			}
			if document.ResultHash(job.Done["1"]) != document.ResultHash(first) || document.ResultHash(job.Done["2"]) != document.ResultHash(second) {
				t.Fatal("job changed before rejection")
			}
			if err := job.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}
