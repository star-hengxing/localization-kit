package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/document"
)

func TestBatchRecoversAtSelectedWriteBoundaries(t *testing.T) {
	segments := []document.Segment{{ID: "1", Group: "g", Src: "a"}, {ID: "2", Group: "g", Src: "b"}}
	results := []document.Result{{ID: "1", Src: "a", Dst: "A", Status: "done"}, {ID: "2", Src: "b", Dst: "B", Status: "done"}}
	var data bytes.Buffer
	for _, result := range results {
		if err := json.NewEncoder(&data).Encode(result); err != nil {
			t.Fatal(err)
		}
	}
	firstLine, _ := json.Marshal(results[0])
	for _, cut := range []int{0, 1, len(firstLine) + 1, data.Len() / 2, data.Len() - 1, data.Len()} {
		t.Run(fmt.Sprintf("cut-%d", cut), func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenOrCreate(dir, segments)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Append(document.Result{ID: "1", Src: "a", Dst: "old", Status: "done"}); err != nil {
				t.Fatal(err)
			}
			base, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := filePrefixHash(s.results, int64(len(base)))
			if err != nil {
				t.Fatal(err)
			}
			j := batchRecord{Version: 1, Offset: int64(len(base)), PrefixHash: prefix, DataHash: batchDataHash(data.Bytes()), Data: data.Bytes()}
			if err := s.writeBatchJournal(j); err != nil {
				t.Fatal(err)
			}
			if _, err := s.results.Write(data.Bytes()[:cut]); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenReadOnly(dir); err == nil {
				t.Fatal("read-only open accepted pending batch")
			}
			s, err = OpenExisting(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Done) != 2 || s.Done["1"].Dst != "A" || s.Done["2"].Dst != "B" {
				s.Close()
				t.Fatalf("recovered=%+v", s.Done)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
			if err != nil || !bytes.Equal(got, append(base, data.Bytes()...)) {
				t.Fatalf("unexpected recovered data: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, batchJournal)); !os.IsNotExist(err) {
				t.Fatal("journal still present after recovery")
			}
		})
	}
}

func TestBatchAppendsAfterLastLineWithoutNewline(t *testing.T) {
	dir := t.TempDir()
	segments := []document.Segment{{ID: "1", Group: "g", Src: "a"}, {ID: "2", Group: "g", Src: "b"}}
	s, err := OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(document.Result{ID: "1", Src: "a", Dst: "A", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "results.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)-1], 0600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.HistoryFor(map[string]bool{"1": true})
	if err != nil || len(history) != 1 || history[0].Dst != "A" {
		t.Fatalf("unterminated history record: %+v, %v", history, err)
	}
	if err := s.AppendBatch([]document.Result{{ID: "2", Src: "b", Dst: "B", Status: "done"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Done) != 2 {
		t.Fatalf("results=%+v", s.Done)
	}
}

func TestBatchRejectsReadOnlyStore(t *testing.T) {
	dir := t.TempDir()
	segments := []document.Segment{{ID: "1", Group: "g", Src: "a"}}
	s, err := OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendBatch([]document.Result{{ID: "1", Src: "a", Dst: "A", Status: "done"}}); err == nil {
		s.Close()
		t.Fatal("read-only store accepted batch")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, batchJournal)); !os.IsNotExist(err) {
		t.Fatal("read-only batch created a journal")
	}
}

func TestBatchRejectsUnexpectedLogChanges(t *testing.T) {
	dir := t.TempDir()
	segments := []document.Segment{{ID: "1", Group: "g", Src: "a"}}
	s, err := OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(document.Result{ID: "1", Src: "a", Dst: "A", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	info, _ := s.results.Stat()
	prefix, _ := filePrefixHash(s.results, info.Size())
	data := []byte("unexpected batch bytes\n")
	if err := s.writeBatchJournal(batchRecord{Version: 1, Offset: info.Size(), PrefixHash: prefix, DataHash: batchDataHash(data), Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.results.Write([]byte("different")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "results.jsonl")
	before, _ := os.ReadFile(path)
	if _, err := OpenExisting(dir); err == nil {
		t.Fatal("unexpected tail accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("conflicting result log changed")
	}
	if _, err := os.Stat(filepath.Join(dir, batchJournal)); err != nil {
		t.Fatalf("journal was lost: %v", err)
	}
}

func TestBatchRejectsChangedLogPrefix(t *testing.T) {
	dir := t.TempDir()
	segments := []document.Segment{{ID: "1", Group: "g", Src: "a"}}
	s, err := OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(document.Result{ID: "1", Src: "a", Dst: "A", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	info, _ := s.results.Stat()
	prefix, _ := filePrefixHash(s.results, info.Size())
	data := []byte("new batch\n")
	if err := s.writeBatchJournal(batchRecord{Version: 1, Offset: info.Size(), PrefixHash: prefix, DataHash: batchDataHash(data), Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.results.WriteAt([]byte("X"), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "results.jsonl")
	before, _ := os.ReadFile(path)
	if _, err := OpenExisting(dir); err == nil {
		t.Fatal("changed prefix accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("changed prefix was overwritten")
	}
}
