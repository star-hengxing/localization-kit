package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestArtcStatusWithLockedCover(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "chapters"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"book.toml":       "title='日本語'\nlang='ja'\n",
		"toc.toml":        "[[chapter]]\ntitle='章'\nfile='chapters/01.txt'\n",
		"chapters/01.txt": "こんにちは\n",
		"cover.svg":       "<svg/>",
	} {
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := run(ctx, []string{"init", root, "--adapter", "artc"}); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(source, "cover.svg"))
	if err != nil {
		t.Fatal(err)
	}
	cover, err := windows.CreateFile(path, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(cover)
	if err := run(ctx, []string{"status", root}); err != nil {
		t.Fatal(err)
	}
}
