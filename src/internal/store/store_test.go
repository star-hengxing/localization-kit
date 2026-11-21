package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"localization-kit/src/internal/document"
)

func TestResumeAndConflict(t *testing.T) {
	dir := t.TempDir()
	segments := []document.Segment{{ID: "1", Group: "1", Src: "こんにちは"}}
	first, err := OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOrCreate(dir, segments); err == nil {
		t.Fatal("concurrent writer accepted")
	}
	if err := first.Append(document.Result{ID: "1", Src: "こんにちは", Dst: "你好", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "results.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":`)
	_ = f.Close()
	second, err := OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	if second.Done["1"].Dst != "你好" {
		t.Fatalf("done=%+v", second.Done)
	}
	_ = second.Close()
	if _, err := OpenOrCreate(dir, []document.Segment{{ID: "1", Group: "1", Src: "変わった"}}); err == nil {
		t.Fatal("changed source accepted")
	}
}

func TestOpenResumesInterruptedInitialization(t *testing.T) {
	segments := []document.Segment{{ID: "1", Src: "a"}}
	for name, files := range map[string]map[string]string{
		"after history creation": {resultsFileName: ""},
		"while writing manifest": {resultsFileName: "", pendingManifestFileName: "{\"version\":"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for path, data := range files {
				if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			job, err := OpenOrCreate(dir, segments)
			if err != nil {
				t.Fatal(err)
			}
			if err := job.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(dir, pendingManifestFileName)); !os.IsNotExist(err) {
				t.Fatalf("temporary manifest remains: %v", err)
			}
			reopened, err := OpenReadOnly(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := reopened.VerifySegments(segments); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManifestDiagnosticsIncludePathAndCause(t *testing.T) {
	dir := t.TempDir()
	job, err := OpenOrCreate(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	var syntaxErr *json.SyntaxError
	if _, err := OpenReadOnly(dir); !errors.As(err, &syntaxErr) || !strings.Contains(err.Error(), path) {
		t.Fatalf("expected the manifest path and a JSON syntax error, got %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":42}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifySourceSnapshot(dir, nil); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "42") {
		t.Fatalf("expected the manifest path and unsupported version, got %v", err)
	}
}

func TestOpenPreservesNonemptyHistoryWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	history := filepath.Join(dir, resultsFileName)
	before := []byte("{\"id\":\"1\",\"src\":\"a\",\"dst\":\"A\",\"status\":\"done\"}\n")
	if err := os.WriteFile(history, before, 0600); err != nil {
		t.Fatal(err)
	}
	if job, err := OpenOrCreate(dir, []document.Segment{{ID: "1", Src: "a"}}); err == nil {
		_ = job.Close()
		t.Fatal("open initialized a job over existing history")
	}
	if after, err := os.ReadFile(history); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("open changed result history: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("open created a manifest over existing history: %v", err)
	}
}

func TestFailedReplayPreservesStageSnapshotsAndReleasesLock(t *testing.T) {
	segments := []document.Segment{{ID: "1", Src: "a"}, {ID: "2", Src: "b"}}
	for _, open := range []struct {
		name string
		run  func(string) (*Store, error)
	}{
		{"OpenOrCreate", func(dir string) (*Store, error) { return OpenOrCreate(dir, segments) }},
		{"OpenExisting", OpenExisting},
	} {
		t.Run(open.name, func(t *testing.T) {
			dir := t.TempDir()
			job, err := OpenOrCreate(dir, segments)
			if err != nil {
				t.Fatal(err)
			}
			err = job.AppendBatch([]document.Result{
				{ID: "1", Src: "a", Dst: "A", Status: "done"},
				{ID: "2", Src: "b", Dst: "B", Status: "done"},
			})
			closeErr := job.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("seed results: %v, close: %v", err, closeErr)
			}
			history := filepath.Join(dir, "results.jsonl")
			snapshot := filepath.Join(dir, "translate", "translation.json")
			valid, err := os.ReadFile(history)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			first, rest, found := bytes.Cut(valid, []byte{'\n'})
			if !found || len(rest) == 0 {
				t.Fatal("expected two results in history")
			}
			damaged := bytes.Join([][]byte{first, []byte("{"), rest}, []byte{'\n'})
			if err := os.WriteFile(history, damaged, 0600); err != nil {
				t.Fatal(err)
			}
			reopened, err := open.run(dir)
			if err == nil {
				_ = reopened.Close()
				t.Fatal("open accepted a malformed record between valid results")
			}
			if !strings.Contains(err.Error(), "结果记录在字节") {
				t.Fatalf("expected a replay error: %v", err)
			}
			if after, err := os.ReadFile(snapshot); err != nil || !bytes.Equal(after, before) {
				t.Fatalf("failed replay changed the stage snapshot: %v", err)
			}
			if after, err := os.ReadFile(history); err != nil || !bytes.Equal(after, damaged) {
				t.Fatalf("failed replay changed the result history: %v", err)
			}
			if err := os.WriteFile(history, valid, 0600); err != nil {
				t.Fatal(err)
			}
			reopened, err = open.run(dir)
			if err != nil {
				t.Fatalf("open after restoring history: %v", err)
			}
			if got := reopened.Done["2"].Dst; got != "B" {
				_ = reopened.Close()
				t.Fatalf("last result after replay = %q", got)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplayRejectsInvalidUTF8InCompleteRecord(t *testing.T) {
	dir := t.TempDir()
	job, err := OpenOrCreate(dir, []document.Segment{{ID: "1", Src: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Append(document.Result{ID: "1", Src: "a", Dst: "字�", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(dir, "results.jsonl")
	valid, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Done["1"].Dst; got != "字�" {
		t.Errorf("valid UTF-8 = %q", got)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	damaged := bytes.Replace(valid, []byte("字�"), []byte{0xff}, 1)
	for _, data := range [][]byte{damaged, bytes.TrimSuffix(damaged, []byte{'\n'})} {
		if err := os.WriteFile(history, data, 0600); err != nil {
			t.Fatal(err)
		}
		for _, open := range []func(string) (*Store, error){OpenReadOnly, OpenExisting} {
			reopened, err := open(dir)
			if err == nil {
				_ = reopened.Close()
				t.Fatal("open accepted invalid UTF-8 in a complete record")
			}
			if !strings.Contains(err.Error(), "UTF-8") {
				t.Fatalf("expected an encoding error: %v", err)
			}
			if after, err := os.ReadFile(history); err != nil || !bytes.Equal(after, data) {
				t.Fatalf("failed replay changed the result history: %v", err)
			}
		}
	}
}

func TestResultWritesRejectInvalidRecordsBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	job, err := OpenOrCreate(dir, []document.Segment{{ID: "1", Src: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	valid := document.Result{ID: "1", Src: "a", Dst: "A", Status: "done"}
	for name, invalid := range map[string]document.Result{
		"unknown ID":        {ID: "2", Src: "a", Dst: "A", Status: "done"},
		"wrong source":      {ID: "1", Src: "b", Dst: "A", Status: "done"},
		"wrong status":      {ID: "1", Src: "a", Dst: "A", Status: "invalid"},
		"empty result":      {ID: "1", Src: "a", Status: "done"},
		"invalid UTF-8":     {ID: "1", Src: "a", Dst: "\xff", Status: "done"},
		"invalid raw UTF-8": {ID: "1", Src: "a", Dst: "A", Status: "done", Usage: []byte("\"\xff\"")},
	} {
		t.Run(name, func(t *testing.T) {
			if err := job.Append(invalid); err == nil {
				t.Fatal("Append accepted an invalid result")
			}
			if err := job.AppendBatch([]document.Result{valid, invalid}); err == nil {
				t.Fatal("AppendBatch accepted an invalid result")
			}
			if data, err := os.ReadFile(filepath.Join(dir, "results.jsonl")); err != nil || len(data) != 0 {
				t.Fatalf("rejected writes changed history: len=%d err=%v", len(data), err)
			}
			if _, err := os.Stat(filepath.Join(dir, batchJournal)); !os.IsNotExist(err) {
				t.Fatalf("rejected batch left a journal: %v", err)
			}
			if len(job.Done) != 0 || len(job.Failed) != 0 || len(job.stages) != 0 {
				t.Fatal("rejected writes changed in-memory state")
			}
		})
	}
	if t.Failed() {
		return
	}
	if err := job.AppendBatch([]document.Result{valid}); err != nil {
		t.Fatalf("valid batch after rejected writes: %v", err)
	}
}

func TestExportCreditsFollowCurrentManualHistory(t *testing.T) {
	segments := []document.Segment{{ID: "1", Src: "a"}, {ID: "2", Src: "b"}, {ID: "3", Src: "c"}}
	s, err := OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := document.Result{ID: "1", Src: "a", Dst: "A", Status: "done", Model: "model-a"}
	if err := s.Append(first); err != nil {
		t.Fatal(err)
	}
	manual := document.Result{ID: "1", Src: "a", Dst: "edited", Status: "done", Origin: "manual", PreviousHash: document.ResultHash(first)}
	if err := s.Append(manual); err != nil {
		t.Fatal(err)
	}
	if models, err := s.InitialModels(); err != nil || models["1"] != "model-a" {
		t.Fatalf("models before further appends: %v, %v", models, err)
	}
	if err := s.Append(document.Result{ID: "2", Src: "b", Dst: "B", Status: "done", Model: "model-b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(document.Result{ID: "3", Src: "c", Dst: "C", Status: "done", Model: "discarded"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(document.Result{ID: "3", Src: "c", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInitialCreditLines([]string{"初翻: model-a", "校对: editor"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ExportCreditLines()
	if err != nil || !reflect.DeepEqual(got, []string{"初翻: model-a / model-b", "校对: editor"}) {
		t.Fatalf("credits=%q err=%v", got, err)
	}
	stored, err := s.CreditLines()
	if err != nil || !reflect.DeepEqual(stored, []string{"初翻: model-a", "校对: editor"}) {
		t.Fatalf("stored credits=%q err=%v", stored, err)
	}
}
