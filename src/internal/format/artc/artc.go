package artc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/translate/rules"
)

type Book struct {
	Root     string
	Title    string
	Lang     string
	BookRaw  []byte
	TocRaw   []byte
	Chapters []Chapter
}

type Chapter struct {
	File     string `toml:"file"`
	Title    string `toml:"title"`
	Group    string
	Data     []byte
	SHA256   string
	Verbatim bool
}

type lineSlot struct {
	Lead, Indent, Body, Trail string
	Paragraph, Line           int
}

var lineParts = regexp.MustCompile(`^(\x{FEFF}?[ \t]*)(\x{3000}*)(.*?)([ \t\r]*)$`)
var rubyPattern = regexp.MustCompile(`\[\[rb:\s*(.*?)\s*>\s*(.*?)\s*\]\]`)

func hash(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func scanLines(value string) []lineSlot {
	var result []lineSlot
	paragraph, line, inParagraph := 0, 0, false
	for _, raw := range strings.Split(value, "\n") {
		parts := lineParts.FindStringSubmatch(raw)
		if len(parts) != 5 {
			result = append(result, lineSlot{Body: raw})
			continue
		}
		slot := lineSlot{Lead: parts[1], Indent: parts[2], Body: parts[3], Trail: parts[4]}
		if slot.Indent == "" && slot.Body == "" {
			inParagraph = false
		} else {
			if !inParagraph {
				paragraph++
				line, inParagraph = 0, true
			}
			line++
			slot.Paragraph, slot.Line = paragraph, line
		}
		result = append(result, slot)
	}
	return result
}

func stripRuby(value string) (string, map[string]string) {
	readings := map[string]string{}
	out := rubyPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := rubyPattern.FindStringSubmatch(match)
		base, reading := strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if base == "" {
			return match
		}
		readings[base] = reading
		return base
	})
	return out, readings
}

func artcSegment(id, group, value string) document.Segment {
	src, readings := stripRuby(value)
	s := document.Segment{ID: id, Group: group, Src: src}
	if len(readings) > 0 {
		s.Meta = map[string]any{"ruby": readings}
	}
	return s
}

func safeSourcePath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || strings.Contains(relative, "\\") || filepath.VolumeName(relative) != "" {
		return "", fmt.Errorf("章节路径无效: %s", relative)
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("章节路径无效: %s", relative)
		}
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("章节路径超出项目: %s", relative)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("章节路径超出项目: %s", relative)
	}
	return resolved, nil
}

func LoadArtc(root string) (Book, []document.Segment, error) {
	bookPath := filepath.Join(root, "book.toml")
	tocPath := filepath.Join(root, "toc.toml")
	bookRaw, err := os.ReadFile(bookPath)
	if err != nil {
		return Book{}, nil, fmt.Errorf("读取 book.toml: %w", err)
	}
	tocRaw, err := os.ReadFile(tocPath)
	if err != nil {
		return Book{}, nil, fmt.Errorf("读取 toc.toml: %w", err)
	}
	var bookConfig struct {
		Title    string         `toml:"title"`
		Lang     string         `toml:"lang"`
		Original map[string]any `toml:"original"`
	}
	var toc struct {
		Chapters []Chapter `toml:"chapter"`
	}
	if err := toml.Unmarshal([]byte(strings.TrimPrefix(string(bookRaw), "\ufeff")), &bookConfig); err != nil {
		return Book{}, nil, fmt.Errorf("解析 %s: %w", bookPath, err)
	}
	if bookConfig.Title == "" {
		return Book{}, nil, fmt.Errorf("%s 缺少标题", bookPath)
	}
	if strings.HasPrefix(strings.ToLower(bookConfig.Lang), "zh") || bookConfig.Original != nil {
		return Book{}, nil, fmt.Errorf("输入已经是中文或双语项目")
	}
	if err := toml.Unmarshal([]byte(strings.TrimPrefix(string(tocRaw), "\ufeff")), &toc); err != nil {
		return Book{}, nil, fmt.Errorf("解析 %s: %w", tocPath, err)
	}
	if len(toc.Chapters) == 0 {
		return Book{}, nil, fmt.Errorf("%s 没有章节", tocPath)
	}
	book := Book{Root: root, Title: bookConfig.Title, Lang: bookConfig.Lang, BookRaw: bookRaw, TocRaw: tocRaw}
	titles := []document.Segment{artcSegment("book:title", "titles", book.Title)}
	var body []document.Segment
	seen := map[string]bool{"book": true, "titles": true}
	for _, chapter := range toc.Chapters {
		if chapter.File == "" || chapter.Title == "" || !strings.EqualFold(filepath.Ext(chapter.File), ".txt") {
			return Book{}, nil, fmt.Errorf("章节缺少标题或不是 .txt: %s", chapter.File)
		}
		path, err := safeSourcePath(root, chapter.File)
		if err != nil {
			return Book{}, nil, fmt.Errorf("读取章节 %s: %w", chapter.File, err)
		}
		chapter.Data, err = os.ReadFile(path)
		if err != nil {
			return Book{}, nil, fmt.Errorf("读取章节 %s: %w", chapter.File, err)
		}
		if !utf8.Valid(chapter.Data) {
			return Book{}, nil, fmt.Errorf("章节不是有效 UTF-8: %s", path)
		}
		chapter.Group = strings.TrimPrefix(strings.TrimSuffix(chapter.File, filepath.Ext(chapter.File)), "chapters/")
		if seen[chapter.Group] {
			return Book{}, nil, fmt.Errorf("章节分组冲突: %s", chapter.Group)
		}
		seen[chapter.Group] = true
		chapter.SHA256 = hash(chapter.Data)
		chapter.Verbatim = chapter.File == "chapters/info.txt"
		book.Chapters = append(book.Chapters, chapter)
		if chapter.Verbatim {
			continue
		}
		titles = append(titles, artcSegment(chapter.Group+":title", "titles", chapter.Title))
		for _, slot := range scanLines(string(chapter.Data)) {
			if slot.Body != "" {
				id := fmt.Sprintf("%s:p%03d:l%d", chapter.Group, slot.Paragraph, slot.Line)
				body = append(body, artcSegment(id, chapter.Group, slot.Body))
			}
		}
	}
	var sources strings.Builder
	sources.WriteString(hash(bookRaw))
	sources.WriteString(hash(tocRaw))
	for _, chapter := range book.Chapters {
		sources.WriteString(chapter.SHA256)
	}
	if titles[0].Meta == nil {
		titles[0].Meta = map[string]any{}
	}
	titles[0].Meta["source_sha256"] = hash([]byte(sources.String()))
	segments := append(titles, body...)
	var kanaCount, hanCount int
	for _, segment := range segments {
		for _, r := range segment.Src {
			if rules.IsKana(r) {
				kanaCount++
			} else if r >= '\u3400' && r <= '\u9fff' || r >= '\uf900' && r <= '\ufaff' {
				hanCount++
			}
		}
	}
	if kanaCount+hanCount > 0 && float64(kanaCount)/float64(kanaCount+hanCount) < 0.02 {
		return Book{}, nil, fmt.Errorf("输入看起来是中文原作")
	}
	return book, segments, nil
}
