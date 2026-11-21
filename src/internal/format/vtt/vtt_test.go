package vtt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localization-kit/src/internal/document"
)

func TestVTTTracksAndExport(t *testing.T) {
	input, output := t.TempDir(), t.TempDir()
	for _, group := range []string{"有SE", "无SE"} {
		dir := filepath.Join(input, group)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "01.vtt"), []byte("\ufeffWEBVTT\n\nSTYLE\n::cue {color:red}\n\n1\n00:01.000 --> 00:02.000\nこんにちは\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tracks, segments, err := LoadVTT(input)
	if err != nil || len(tracks) != 2 || len(segments) != 2 {
		t.Fatalf("tracks=%+v segments=%+v err=%v", tracks, segments, err)
	}
	done := map[string]document.Result{}
	for _, segment := range segments {
		done[segment.ID] = document.Result{Dst: "你好っ", Origin: "manual"}
	}
	if err := ExportVTT(tracks, done, output, nil, []string{"初翻: model-a"}); err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"有SE", "无SE"} {
		content, err := os.ReadFile(filepath.Join(output, group, "01.vtt"))
		if err != nil || !strings.Contains(string(content), "00:01.000 --> 00:02.000\nこんにちは\n你好っ\n") || !strings.Contains(string(content), "STYLE") || !strings.Contains(string(content), "NOTE 译制\n初翻: model-a") {
			t.Fatalf("content=%s err=%v", content, err)
		}
	}
}
