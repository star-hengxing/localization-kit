package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localization-kit/src/internal/quality"
	"localization-kit/src/internal/store"
)

func writeTestCards(t *testing.T, dir, path, name string) []byte {
	t.Helper()
	job, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	segment := job.Segments[0]
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	material := quality.CardFile{Characters: []quality.CardCharacter{{NameSrc: segment.Src, NameDst: name, Gender: "unknown", Pronoun: "unknown"}}}
	data, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFilledCardsReachWorkerWithoutSourceBatches(t *testing.T) {
	dir, _ := fixture(t, false)
	cards := filepath.Join(t.TempDir(), "cards.json")
	if err := os.WriteFile(cards, []byte(`{"characters":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", Cards: cards, TTL: time.Hour, MaxLines: 20}
	if _, err := Next(options); err == nil {
		t.Fatal("empty preparation was accepted as character cards")
	}
	writeTestCards(t, dir, cards, "确认角色")
	task, err := Next(options)
	if err != nil {
		t.Fatal(err)
	}
	var actual quality.CharacterCards
	if err := json.Unmarshal(task.Cards, &actual); err != nil {
		t.Fatal(err)
	}
	if len(actual.Characters) != 1 || actual.Characters[0].NameDst != "确认角色" {
		t.Fatalf("cards=%s", task.Cards)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(task.Cards, &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["batches"]; exists {
		t.Fatal("source batches sent as character cards")
	}
}

func TestStatusUsesPersistedStageDependenciesWithoutWriting(t *testing.T) {
	dir, _ := fixture(t, false)
	cards := filepath.Join(t.TempDir(), "cards.json")
	writeTestCards(t, dir, cards, "名字")
	before, err := Inspect(dir, true, "")
	if err != nil || before.Translate["done"] != 2 || before.Polish["unknown"] != 2 {
		t.Fatalf("initial status=%+v err=%v", before, err)
	}
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", Revision: "v1", Cards: cards, TTL: time.Hour, MaxLines: 20, Context: 5}
	task, err := Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("next=%+v err=%v", task, err)
	}
	claimed, err := Inspect(dir, true, "")
	if err != nil || claimed.Polish["pending"] != 2 || claimed.InProgress["polish"] != 2 || claimed.Lines[0].Polish.Claim == nil {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{{ID: "1", Dst: "你好"}, {ID: "2", Dst: "谢谢"}}); err != nil {
		t.Fatal(err)
	}
	finished, err := Inspect(dir, false, "1")
	if err != nil || finished.Polish["done"] != 2 || finished.Proofread["unknown"] != 2 || len(finished.Lines) != 1 || finished.Lines[0].Source == nil || finished.Lines[0].Target == nil {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	writeTestCards(t, dir, cards, "新名字")
	resultsPath := filepath.Join(dir, "results.jsonl")
	claimsPath := filepath.Join(dir, "status.json")
	resultsBefore, _ := os.ReadFile(resultsPath)
	claimsBefore, _ := os.ReadFile(claimsPath)
	stale, err := Inspect(dir, true, "")
	if err != nil || stale.Polish["review"] != 2 {
		t.Fatalf("stale=%+v err=%v", stale, err)
	}
	resultsAfter, _ := os.ReadFile(resultsPath)
	claimsAfter, _ := os.ReadFile(claimsPath)
	if string(resultsBefore) != string(resultsAfter) || string(claimsBefore) != string(claimsAfter) {
		t.Fatal("status modified job files")
	}
	if err := os.Remove(cards); err != nil {
		t.Fatal(err)
	}
	missing, err := Inspect(dir, true, "")
	if err != nil || missing.Polish["unknown"] != 2 {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
}

func TestStatusSeparatesPolishAndProofread(t *testing.T) {
	dir, _ := fixture(t, false)
	options := Options{Job: dir, Stage: "polish", Worker: "editor", Model: "model", TTL: time.Hour, MaxLines: 20, Context: 5}
	for _, stage := range []string{"polish", "proofread"} {
		options.Stage = stage
		task, err := Next(options)
		if err != nil || task.Status != "ready" {
			t.Fatalf("%s next=%+v err=%v", stage, task, err)
		}
		if err := Submit(dir, options.Worker, task.TaskID, []Submission{{ID: "1", Dst: "你好"}, {ID: "2", Dst: "谢谢"}}); err != nil {
			t.Fatal(err)
		}
	}
	status, err := Inspect(dir, true, "")
	if err != nil || status.Polish["done"] != 2 || status.Proofread["done"] != 2 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err := Inspect(dir, false, "missing"); err == nil {
		t.Fatal("unknown ID was accepted")
	}
}

func TestStaleClaimDoesNotBlockAnotherWorker(t *testing.T) {
	dir, _ := fixture(t, false)
	cards := filepath.Join(t.TempDir(), "cards.json")
	writeTestCards(t, dir, cards, "名字")
	options := Options{Job: dir, Stage: "polish", Worker: "first", Model: "model", Cards: cards, TTL: time.Hour, MaxLines: 20, Context: 5}
	first, err := Next(options)
	if err != nil || first.Status != "ready" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	writeTestCards(t, dir, cards, "新名字")
	status, err := Inspect(dir, true, "")
	if err != nil || status.Lines[0].Polish.Claim != nil {
		t.Fatalf("stale claim still shown: %+v err=%v", status, err)
	}
	options.Worker = "second"
	second, err := Next(options)
	if err != nil || second.Status != "ready" || second.TaskID == first.TaskID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	status, err = Inspect(dir, true, "")
	if err != nil || status.Lines[0].Polish.Claim == nil || status.Lines[0].Polish.Claim.Worker != "second" {
		t.Fatalf("new claim missing: %+v err=%v", status, err)
	}
}

func TestUnreadableClaimMaterialDoesNotDiscardTask(t *testing.T) {
	dir, _ := fixture(t, false)
	cards := filepath.Join(t.TempDir(), "cards.json")
	content := writeTestCards(t, dir, cards, "名字")
	options := Options{Job: dir, Stage: "polish", Worker: "first", Model: "model", Cards: cards, TTL: time.Hour, MaxLines: 20, Context: 5}
	task, err := Next(options)
	if err != nil || task.Status != "ready" {
		t.Fatalf("first=%+v err=%v", task, err)
	}
	if err := os.Remove(cards); err != nil {
		t.Fatal(err)
	}
	other := options
	other.Stage, other.Worker, other.Cards = "proofread", "second", ""
	if _, err := Next(other); err == nil {
		t.Fatal("unreadable claim material should prevent changes to claims")
	}
	if err := os.WriteFile(cards, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Submit(dir, options.Worker, task.TaskID, []Submission{{ID: "1", Dst: "你好"}, {ID: "2", Dst: "谢谢"}}); err != nil {
		t.Fatalf("original task was lost: %v", err)
	}
}
