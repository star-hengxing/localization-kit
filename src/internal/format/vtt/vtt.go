package vtt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/translate/rules"
)

type Track struct {
	Group    string
	Header   string
	Preamble []string
	Segments []document.Segment
}

func LoadVTT(input string) ([]Track, []document.Segment, error) {
	info, err := os.Stat(input)
	if err != nil {
		return nil, nil, err
	}
	var files []string
	if info.IsDir() {
		err = filepath.WalkDir(input, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".vtt") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	} else {
		files = []string{input}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("没有找到 VTT 文件: %s", input)
	}
	var tracks []Track
	var segments []document.Segment
	groups := map[string]bool{}
	for _, path := range files {
		rel := filepath.Base(path)
		if info.IsDir() {
			rel, err = filepath.Rel(input, path)
			if err != nil {
				return nil, nil, err
			}
		}
		group := filepath.ToSlash(strings.TrimSuffix(rel, filepath.Ext(rel)))
		if groups[group] {
			return nil, nil, fmt.Errorf("音轨路径重复: %s", group)
		}
		groups[group] = true
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		if !utf8.Valid(data) {
			return nil, nil, fmt.Errorf("VTT 不是有效 UTF-8: %s", path)
		}
		track, err := parseVTT(group, string(data))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		if len(track.Segments) > 0 {
			track.Segments[0].Meta["source_sha256"] = hash(data)
		}
		tracks = append(tracks, track)
		segments = append(segments, track.Segments...)
	}
	return tracks, segments, nil
}

func parseVTT(group, input string) (Track, error) {
	value := strings.TrimPrefix(input, "\ufeff")
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	var blocks []string
	var block []string
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(block) > 0 {
				blocks = append(blocks, strings.Join(block, "\n"))
				block = nil
			}
		} else {
			block = append(block, line)
		}
	}
	if len(block) > 0 {
		blocks = append(blocks, strings.Join(block, "\n"))
	}
	if len(blocks) == 0 || !strings.HasPrefix(blocks[0], "WEBVTT") {
		return Track{}, fmt.Errorf("缺少 WEBVTT 文件头")
	}
	track := Track{Group: group, Header: blocks[0]}
	for _, value := range blocks[1:] {
		lines := strings.Split(value, "\n")
		if lines[0] == "NOTE" || strings.HasPrefix(lines[0], "NOTE ") || strings.HasPrefix(lines[0], "NOTE\t") {
			continue
		}
		if (strings.HasPrefix(lines[0], "STYLE") || strings.HasPrefix(lines[0], "REGION")) && !strings.Contains(lines[0], "-->") && len(track.Segments) == 0 {
			track.Preamble = append(track.Preamble, value)
			continue
		}
		ident, timing, start := "", "", 1
		if strings.Contains(lines[0], "-->") {
			timing = strings.TrimSpace(lines[0])
		} else if len(lines) > 1 && strings.Contains(lines[1], "-->") {
			ident, timing, start = strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), 2
		} else {
			return Track{}, fmt.Errorf("无法解析 VTT 块: %s", lines[0])
		}
		var source []string
		for _, line := range lines[start:] {
			if strings.TrimSpace(line) != "" {
				source = append(source, strings.TrimSpace(line))
			}
		}
		id := fmt.Sprintf("%s#%d", group, len(track.Segments)+1)
		track.Segments = append(track.Segments, document.Segment{ID: id, Group: group, Src: strings.Join(source, " "), Meta: map[string]any{"ident": ident, "timing": timing}})
	}
	return track, nil
}

func ExportVTT(tracks []Track, results map[string]document.Result, output string, entries []rules.Entry, credits []string) error {
	for _, track := range tracks {
		path := filepath.Join(output, filepath.FromSlash(track.Group)+".vtt")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		blocks := []string{track.Header}
		blocks = append(blocks, strings.Join(append([]string{"NOTE 译制"}, credits...), "\n"))
		blocks = append(blocks, track.Preamble...)
		for _, segment := range track.Segments {
			var lines []string
			if ident, _ := segment.Meta["ident"].(string); ident != "" {
				lines = append(lines, ident)
			}
			lines = append(lines, segment.Meta["timing"].(string))
			if segment.Src != "" {
				lines = append(lines, segment.Src)
			}
			if result, ok := results[segment.ID]; ok {
				dst := rules.Postfix(segment.Src, result.Dst, entries)
				if strings.TrimSpace(dst) != "" && dst != segment.Src {
					lines = append(lines, dst)
				}
			}
			blocks = append(blocks, strings.Join(lines, "\n"))
		}
		if err := os.WriteFile(path, []byte(strings.Join(blocks, "\n\n")+"\n"), 0600); err != nil {
			return fmt.Errorf("写入 VTT %s: %w", path, err)
		}
	}
	return nil
}

func hash(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}
