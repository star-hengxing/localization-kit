package jsonl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"localization-kit/src/internal/document"
)

func LoadJSONL(path string) ([]document.Segment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取 JSONL: %w", err)
	}
	defer f.Close()
	var segments []document.Segment
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Text()
		if line == 1 {
			raw = strings.TrimPrefix(raw, "\ufeff")
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if !utf8.ValidString(raw) {
			return nil, fmt.Errorf("%s:%d: 不是有效 UTF-8", path, line)
		}
		var row struct {
			ID    *string        `json:"id"`
			Src   *string        `json:"src"`
			Group string         `json:"group"`
			Meta  map[string]any `json:"meta"`
		}
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("%s:%d: 解析 JSONL: %w", path, line, err)
		}
		if row.ID == nil || *row.ID == "" || row.Src == nil {
			return nil, fmt.Errorf("%s:%d: 必须为含字符串 id 和 src 的 JSON 对象", path, line)
		}
		if seen[*row.ID] {
			return nil, fmt.Errorf("%s:%d: id %q 重复", path, line, *row.ID)
		}
		seen[*row.ID] = true
		group := row.Group
		if group == "" {
			group = *row.ID
		}
		segments = append(segments, document.Segment{ID: *row.ID, Src: *row.Src, Group: group, Meta: row.Meta})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	if len(segments) == 0 {
		return nil, fmt.Errorf("%s 没有文本条目", path)
	}
	return segments, nil
}

func ExportJSONL(w io.Writer, segments []document.Segment, done, failed map[string]document.Result) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	for _, segment := range segments {
		row := map[string]any{"id": segment.ID, "group": segment.Group, "src": segment.Src, "status": "pending", "dst": nil}
		if result, ok := done[segment.ID]; ok {
			row["status"], row["dst"], row["provenance"] = "done", result.Dst, result
			row["result_hash"] = document.ResultHash(result)
			if len(result.Warnings) > 0 {
				row["warnings"] = result.Warnings
			}
		} else if result, ok := failed[segment.ID]; ok {
			row["status"], row["violations"], row["provenance"] = "failed", result.Violations, result
		}
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	return nil
}
