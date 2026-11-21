package translate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

func ReadManualEdits(path string) ([]ManualEdit, error) {

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var edits []ManualEdit
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Text()
		if line == 1 {
			raw = strings.TrimPrefix(raw, "\ufeff")
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var item ManualEdit
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&item)
		var trailing any
		endErr := decoder.Decode(&trailing)
		if !utf8.ValidString(raw) || decodeErr != nil || endErr != io.EOF || item.ID == "" || item.ExpectedHash == "" || strings.TrimSpace(item.Dst) == "" || seen[item.ID] {
			return nil, fmt.Errorf("修订文件第 %d 行无效或 ID 重复", line)
		}
		seen[item.ID] = true
		edits = append(edits, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return nil, fmt.Errorf("修订文件没有条目")
	}
	return edits, nil
}
