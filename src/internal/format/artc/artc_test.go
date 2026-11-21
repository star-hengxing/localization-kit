package artc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"localization-kit/src/internal/document"
)

func TestArtcRejectsNoncanonicalChapterPaths(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"chapters/../file.txt", "chapters/./file.txt", "chapters//file.txt", "../file.txt", "C:/file.txt"} {
		if _, err := safeSourcePath(root, path); err == nil {
			t.Errorf("accepted unsafe chapter path %q", path)
		}
	}
}

func TestArtcChapterOutsideChaptersIsNotOverwritten(t *testing.T) {
	root, output := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "text"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"book.toml":      "title = \"日本語\"\nlang = \"ja\"\n",
		"toc.toml":       "[[chapter]]\ntitle = \"第一章\"\nfile = \"text/01.txt\"\n",
		"text/01.txt":    "こんにちは\n",
		"text/asset.dat": "asset",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	book, _, err := LoadArtc(root)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]document.Result{"book:title": {Dst: "日语"}, "text/01:title": {Dst: "第一章"}, "text/01:p001:l1": {Dst: "你好"}}
	if err := ExportArtc(book, results, output, nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "text", "01.txt"))
	if err != nil || string(data) != "你好\n" {
		t.Fatalf("chapter=%q err=%v", data, err)
	}
	if data, err = os.ReadFile(filepath.Join(output, "text", "asset.dat")); err != nil || string(data) != "asset" {
		t.Fatalf("asset=%q err=%v", data, err)
	}
}

func TestArtcRejectsBilingualChapterPathCollision(t *testing.T) {
	root, output := t.TempDir(), t.TempDir()
	files := map[string]string{
		"book.toml":      "title='日本語'\nlang='ja'\n",
		"toc.toml":       "[[chapter]]\ntitle='第一章'\nfile='original/a.txt'\n[[chapter]]\ntitle='第二章'\nfile='chapters/a.txt'\n",
		"original/a.txt": "こんにちは\n",
		"chapters/a.txt": "ありがとうございます\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	book, _, err := LoadArtc(root)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]document.Result{"original/a:p001:l1": {Dst: "你好"}}
	if err := ExportArtc(book, results, output, nil, true, nil, nil); err == nil || !strings.Contains(err.Error(), "导出路径冲突") {
		t.Fatalf("bilingual chapter collision: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "original", "a.txt"))
	if err != nil || string(data) != "你好\n" {
		t.Fatalf("translated chapter=%q err=%v", data, err)
	}
}

func TestArtcAddsCreditsWhenInfoChapterIsMissing(t *testing.T) {
	root, output := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "chapters"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"book.toml": "title = \"日本語\"\nlang = \"ja\"\n", "toc.toml": "[[chapter]]\ntitle = \"第一章\"\nfile = \"chapters/01.txt\"\n", "chapters/01.txt": "こんにちは\n", "chapters/localization-credits.txt": "source asset"}
	for path, value := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	book, _, err := LoadArtc(root)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]document.Result{"book:title": {Dst: "日语"}, "01:title": {Dst: "第一章"}, "01:p001:l1": {Dst: "你好"}}
	lines := map[string]document.ExportLine{}
	if err := ExportArtc(book, results, output, nil, true, []string{"初翻: model-a"}, lines); err != nil {
		t.Fatal(err)
	}
	if lines["book:title"].Dst != "日语（双语）" || lines["01:p001:l1"].Dst != "你好" {
		t.Fatalf("unexpected exported lines: %+v", lines)
	}
	credits, err := os.ReadFile(filepath.Join(output, "chapters", "localization-credits-1.txt"))
	if err != nil || string(credits) != "初翻: model-a\n" {
		t.Fatalf("credits=%q err=%v", credits, err)
	}
	toc, err := os.ReadFile(filepath.Join(output, "toc.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Chapter []struct {
			File string `toml:"file"`
		} `toml:"chapter"`
	}
	if err := toml.Unmarshal(toc, &parsed); err != nil || len(parsed.Chapter) != 2 || parsed.Chapter[1].File != "chapters/localization-credits-1.txt" {
		t.Fatalf("toc=%s err=%v", toc, err)
	}
}

func TestArtcWindowsChapterPathCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths are case-insensitive")
	}
	root, output := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Text"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"book.toml":   "title = \"日本語\"\nlang = \"ja\"\n",
		"toc.toml":    "[[chapter]]\ntitle = \"第一章\"\nfile = \"text/01.txt\"\n",
		"Text/01.txt": "こんにちは\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	book, _, err := LoadArtc(root)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]document.Result{"book:title": {Dst: "日语"}, "text/01:title": {Dst: "第一章"}, "text/01:p001:l1": {Dst: "你好"}}
	if err := ExportArtc(book, results, output, nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "text", "01.txt"))
	if err != nil || string(data) != "你好\n" {
		t.Fatalf("chapter=%q err=%v", data, err)
	}
}

func TestRewriteTocPreservesChapterSubtable(t *testing.T) {
	input := "[[chapter]]\ntitle = \"原文\"\nfile = \"chapters/01.txt\"\n[chapter.extra]\ntitle = \"metadata title\"\nfile = \"metadata file\"\n"
	output, err := rewriteToc(input, []Chapter{{File: "chapters/01.txt", Title: "原文", Group: "01"}}, []string{"译文"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "title = \"译文\"") || !strings.Contains(output, "[chapter.extra]\ntitle = \"metadata title\"\nfile = \"metadata file\"") {
		t.Fatalf("toc=%s", output)
	}
}

func TestArtcImportAndBilingualExport(t *testing.T) {
	root, output := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "chapters"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"book.toml":         "title = \"日本語の本\"\nauthor = \"作者\"\nlang = \"ja\"\n",
		"toc.toml":          "[[chapter]]\ntitle = \"情報\"\nfile = \"chapters/info.txt\"\n\n[[chapter]]\ntitle = \"一章\"\nfile = \"chapters/0001.txt\"\n",
		"chapters/info.txt": "情報\n",
		"chapters/0001.txt": "　[[rb:図書室>としょしつ]]へ行く。  \n\n「おはよう」\n[newpage]\n",
	}
	for path, value := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	book, segments, err := LoadArtc(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 5 || segments[2].ID != "0001:p001:l1" || segments[2].Src != "図書室へ行く。" {
		t.Fatalf("segments=%+v", segments)
	}
	results := map[string]document.Result{
		"book:title":   {Dst: "日语的书"},
		"0001:title":   {Dst: "第一章"},
		"0001:p001:l1": {Dst: "前往图书室。"},
		"0001:p002:l1": {Dst: "「早上好」"},
	}
	if err := ExportArtc(book, results, output, nil, true, []string{"初翻: model-a"}, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.ReadFile(filepath.Join(output, "chapters", "info.txt"))
	if err != nil || string(info) != "情報\n初翻: model-a\n" {
		t.Fatalf("credits=%q err=%v", info, err)
	}
	content, err := os.ReadFile(filepath.Join(output, "chapters", "0001.txt"))
	if err != nil || string(content) != "　前往图书室。  \n\n「早上好」\n[newpage]\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	original, err := os.ReadFile(filepath.Join(output, "original", "0001.txt"))
	if err != nil || string(original) != files["chapters/0001.txt"] {
		t.Fatalf("original=%q err=%v", original, err)
	}
	data, err := os.ReadFile(filepath.Join(output, "book.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := toml.Unmarshal(data, &parsed); err != nil || parsed["lang"] != "zh-CN" || !strings.Contains(parsed["title"].(string), "双语") {
		t.Fatalf("book=%s err=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(root, "chapters", "0001.txt"), []byte("変更"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExportArtc(book, results, t.TempDir(), nil, false, nil, nil); err == nil {
		t.Fatal("changed source accepted")
	}
}
