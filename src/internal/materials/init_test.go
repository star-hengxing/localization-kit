package materials

import (
	"os"
	"path/filepath"
	"testing"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
)

func TestInitTermsPreservesTableAndRequiresRefreshForChangedSource(t *testing.T) {
	root := t.TempDir()
	segments := []document.Segment{{ID: "1", Src: "あああ"}}
	if _, err := InitTerms(root, segments, false); err != nil {
		t.Fatal(err)
	}
	statistics := filepath.Join(root, "materials", "term-statistics.json")
	if err := os.WriteFile(statistics, []byte(`{"version":1,"substrings":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InitTerms(root, segments, false); err == nil {
		t.Fatal("old statistics accepted")
	}
	if _, err := InitTerms(root, segments, true); err != nil {
		t.Fatal(err)
	}
	glossary := filepath.Join(root, "materials", "glossary.json")
	want := []byte(`{"entries":[{"src":"あ","dst":"啊"}]}`)
	if err := os.WriteFile(glossary, want, 0600); err != nil {
		t.Fatal(err)
	}
	changed := []document.Segment{{ID: "1", Src: "いいい"}}
	if _, err := InitTerms(root, changed, false); err == nil {
		t.Fatal("stale statistics accepted")
	}
	if _, err := InitTerms(root, changed, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(glossary)
	if err != nil || string(data) != string(want) {
		t.Fatalf("glossary changed: %s %v", data, err)
	}
	var stats Statistics
	sourceHash, err := jsonfile.Digest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonfile.Read(statistics, &stats); err != nil || stats.SourceHash != sourceHash {
		t.Fatalf("statistics not refreshed: %+v %v", stats, err)
	}
}
