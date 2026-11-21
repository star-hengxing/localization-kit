package artc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/translate/rules"
)

var chapterHeader = regexp.MustCompile(`^\s*\[\[\s*chapter\s*\]\]\s*(#.*)?$`)
var tomlKey = regexp.MustCompile(`^([ \t]*)([A-Za-z0-9_-]+)\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')([ \t]*(?:#.*)?)$`)

func tomlString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func rewriteBook(value, title string, original bool, lang, oldTitle string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimPrefix(value, "\ufeff"), "\r\n", "\n"), "\n")
	titleFound, langFound, table := false, false, false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			table = true
		}
		if table {
			continue
		}
		m := tomlKey.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		if m[2] == "title" {
			lines[i], titleFound = m[1]+"title = "+tomlString(title)+m[4], true
		}
		if m[2] == "lang" {
			lines[i], langFound = m[1]+"lang = \"zh-CN\""+m[4], true
		}
	}
	if !titleFound {
		return "", fmt.Errorf("book.toml 缺少单行 title")
	}
	if !langFound {
		for i, line := range lines {
			m := tomlKey.FindStringSubmatch(line)
			if len(m) > 0 && m[2] == "title" {
				lines = append(lines[:i+1], append([]string{"lang = \"zh-CN\""}, lines[i+1:]...)...)
				break
			}
		}
	}
	result := strings.Join(lines, "\n")
	if original {
		if lang == "" {
			lang = "ja"
		}
		result = strings.TrimRight(result, "\n") + "\n\n[original]\nlang = " + tomlString(lang) + "\ntitle = " + tomlString(oldTitle) + "\n"
	}
	var check map[string]any
	if err := toml.Unmarshal([]byte(result), &check); err != nil {
		return "", fmt.Errorf("改写 book.toml: %w", err)
	}
	return result, nil
}

func rewriteToc(value string, chapters []Chapter, titles []string, bilingual bool) (string, error) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimPrefix(value, "\ufeff"), "\r\n", "\n"), "\n")
	var result []string
	chapter := -1
	seenTitle, seenFile, inChapter := false, false, false
	for _, line := range lines {
		if chapterHeader.MatchString(line) {
			if chapter >= 0 && (!seenTitle || !seenFile) {
				return "", fmt.Errorf("toc.toml 章节缺少单行 title / file")
			}
			chapter++
			seenTitle, seenFile = false, false
			inChapter = true
			if chapter >= len(chapters) {
				return "", fmt.Errorf("toc.toml 章节数不一致")
			}
		} else if strings.HasPrefix(strings.TrimSpace(line), "[") {
			inChapter = false
		}
		if chapter >= 0 && inChapter {
			m := tomlKey.FindStringSubmatch(line)
			if len(m) > 0 {
				switch m[2] {
				case "title":
					line, seenTitle = m[1]+"title = "+tomlString(titles[chapter])+m[4], true
				case "file":
					seenFile = true
					result = append(result, line)
					if bilingual && !chapters[chapter].Verbatim {
						result = append(result, m[1]+"original = "+tomlString("original/"+chapters[chapter].Group+".txt"), m[1]+"original_title = "+tomlString(chapters[chapter].Title))
					}
					continue
				case "original", "original_title":
					return "", fmt.Errorf("源 toc.toml 已包含双语原文")
				}
			}
		}
		result = append(result, line)
	}
	if chapter+1 != len(chapters) || !seenTitle || !seenFile {
		return "", fmt.Errorf("toc.toml 章节数不一致或缺少 title / file")
	}
	out := strings.Join(result, "\n")
	var check map[string]any
	if err := toml.Unmarshal([]byte(out), &check); err != nil {
		return "", fmt.Errorf("改写 toc.toml: %w", err)
	}
	return out, nil
}

func safeTitle(value string) string {
	replacer := strings.NewReplacer("<", "＜", ">", "＞", ":", "：", "\"", "＂", "/", "／", "\\", "＼", "|", "｜", "?", "？", "*", "＊")
	value = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, replacer.Replace(value))), " ")
	value = strings.TrimRight(value, ". ")
	if value == "" {
		return "untitled"
	}
	base := strings.ToUpper(strings.Split(value, ".")[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || regexp.MustCompile(`^(COM|LPT)[1-9]$`).MatchString(base) {
		return "_" + value
	}
	return value
}

func writeExportFile(path string, contents io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("artc 导出路径冲突: %w", err)
	}
	if err != nil {
		return err
	}
	_, writeErr := io.Copy(f, contents)
	return errors.Join(writeErr, f.Close())
}

func ExportArtc(book Book, results map[string]document.Result, output string, entries []rules.Entry, bilingual bool, credits []string, exportedLines map[string]document.ExportLine) error {
	bookPath := filepath.Join(book.Root, "book.toml")
	bookNow, err := os.ReadFile(bookPath)
	if err != nil {
		return fmt.Errorf("读取原始书籍配置: %w", err)
	}
	if !bytes.Equal(bookNow, book.BookRaw) {
		return fmt.Errorf("原始书籍配置已改变: %s", bookPath)
	}
	tocPath := filepath.Join(book.Root, "toc.toml")
	tocNow, err := os.ReadFile(tocPath)
	if err != nil {
		return fmt.Errorf("读取原始目录: %w", err)
	}
	if !bytes.Equal(tocNow, book.TocRaw) {
		return fmt.Errorf("原始目录已改变: %s", tocPath)
	}
	for _, chapter := range book.Chapters {
		path, err := safeSourcePath(book.Root, chapter.File)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取原始章节: %w", err)
		}
		if hash(data) != chapter.SHA256 {
			return fmt.Errorf("原始章节已改变: %s", path)
		}
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	translated := func(id, src string) string {
		if result, ok := results[id]; ok && !result.Passthrough {
			dst := rules.Postfix(src, result.Dst, entries)
			if exportedLines != nil {
				exportedLines[id] = document.ExportLine{Src: src, Dst: dst}
			}
			return dst
		}
		return src
	}
	var titles []string
	hasInfo := false
	for _, chapter := range book.Chapters {
		title := chapter.Title
		if !chapter.Verbatim {
			title = translated(chapter.Group+":title", chapter.Title)
		}
		titles = append(titles, title)
		var out []string
		for _, slot := range scanLines(string(chapter.Data)) {
			value := slot.Body
			if !chapter.Verbatim && value != "" {
				id := fmt.Sprintf("%s:p%03d:l%d", chapter.Group, slot.Paragraph, slot.Line)
				if result, ok := results[id]; ok && !result.Passthrough {
					value = translated(id, value)
				}
			}
			out = append(out, slot.Lead+slot.Indent+value+slot.Trail)
		}
		path := filepath.Join(output, filepath.FromSlash(chapter.File))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		content := strings.Join(out, "\n")
		if chapter.Verbatim && chapter.File == "chapters/info.txt" && len(credits) > 0 {
			hasInfo = true
			content = strings.TrimRight(content, "\n")
			if content != "" {
				content += "\n"
			}
			content += strings.Join(credits, "\n") + "\n"
		}
		if err := writeExportFile(path, strings.NewReader(content), 0600); err != nil {
			return err
		}
		if bilingual && !chapter.Verbatim {
			path = filepath.Join(output, "original", filepath.FromSlash(chapter.Group)+".txt")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			if err := writeExportFile(path, bytes.NewReader(chapter.Data), 0600); err != nil {
				return err
			}
		}
	}
	if err := copyAssets(book.Root, output, book.Chapters); err != nil {
		return err
	}
	title := safeTitle(translated("book:title", book.Title))
	if bilingual {
		title += "（双语）"
	}
	if line, ok := exportedLines["book:title"]; ok {
		line.Dst = title
		exportedLines["book:title"] = line
	}
	bookOut, err := rewriteBook(string(book.BookRaw), title, bilingual, book.Lang, book.Title)
	if err != nil {
		return err
	}
	tocOut, err := rewriteToc(string(book.TocRaw), book.Chapters, titles, bilingual)
	if err != nil {
		return err
	}
	if len(credits) > 0 && !hasInfo {
		var name string
		for i := 0; ; i++ {
			name = "localization-credits.txt"
			if i > 0 {
				name = fmt.Sprintf("localization-credits-%d.txt", i)
			}
			path := filepath.Join(book.Root, "chapters", name)
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				break
			} else if err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Join(output, "chapters"), 0700); err != nil {
			return err
		}
		if err := writeExportFile(filepath.Join(output, "chapters", name), strings.NewReader(strings.Join(credits, "\n")+"\n"), 0600); err != nil {
			return err
		}
		tocOut = strings.TrimRight(tocOut, "\n") + "\n\n[[chapter]]\ntitle = \"译制信息\"\nfile = " + tomlString("chapters/"+name) + "\n"
		var check map[string]any
		if err := toml.Unmarshal([]byte(tocOut), &check); err != nil {
			return fmt.Errorf("添加译制信息章节: %w", err)
		}
	}
	if err := writeExportFile(filepath.Join(output, "book.toml"), strings.NewReader(bookOut), 0600); err != nil {
		return err
	}
	return writeExportFile(filepath.Join(output, "toc.toml"), strings.NewReader(tocOut), 0600)
}

func copyAssets(root, output string, chapters []Chapter) error {
	pathKey := func(path string) string {
		path = filepath.Clean(path)
		if runtime.GOOS == "windows" {
			return strings.ToLower(path)
		}
		return path
	}
	chapterFiles := make(map[string]bool, len(chapters))
	for _, chapter := range chapters {
		chapterFiles[pathKey(filepath.FromSlash(chapter.File))] = true
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		if chapterFiles[pathKey(rel)] {
			return nil
		}
		first := strings.Split(rel, string(filepath.Separator))[0]
		if strings.HasPrefix(first, ".") || first == "chapters" || first == "original" || first == "book.toml" || first == "toc.toml" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("不复制符号链接: %s", path)
		}
		target := filepath.Join(output, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("不复制非常规文件: %s", path)
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		return writeExportFile(target, src, 0666)
	})
}
