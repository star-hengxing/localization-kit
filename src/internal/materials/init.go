package materials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ikawaha/kagome-dict/ipa"
	"github.com/ikawaha/kagome/v2/tokenizer"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
)

type Frequency struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

type Statistics struct {
	Version    int         `json:"version"`
	SourceHash string      `json:"source_hash"`
	Method     string      `json:"method"`
	Words      []Frequency `json:"words"`
}

const statisticsMethod = "kagome-ipadic-nouns-v1"

func Count(segments []document.Segment) (Statistics, error) {
	sourceHash, err := jsonfile.Digest(segments)
	if err != nil {
		return Statistics{}, err
	}
	analyzer, err := tokenizer.New(ipa.DictShrink())
	if err != nil {
		return Statistics{}, fmt.Errorf("初始化日语分词: %w", err)
	}
	counts := map[string]int{}
	for _, segment := range segments {
		var compound strings.Builder
		parts := 0
		end := 0
		flush := func() {
			if parts > 1 {
				counts[compound.String()]++
			}
			compound.Reset()
			parts = 0
		}
		for _, token := range analyzer.Tokenize(segment.Src) {
			pos := token.POS()
			if len(pos) < 2 || pos[0] != "名詞" || pos[1] == "代名詞" || pos[1] == "非自立" || pos[1] == "数" || pos[1] == "接尾" {
				flush()
				continue
			}
			if parts > 0 && token.Start != end {
				flush()
			}
			counts[token.Surface]++
			compound.WriteString(token.Surface)
			parts++
			end = token.End
		}
		flush()
	}
	result := Statistics{Version: 2, SourceHash: sourceHash, Method: statisticsMethod, Words: []Frequency{}}
	for text, count := range counts {
		if count > 1 {
			result.Words = append(result.Words, Frequency{Text: text, Count: count})
		}
	}
	sort.Slice(result.Words, func(i, j int) bool {
		if result.Words[i].Count != result.Words[j].Count {
			return result.Words[i].Count > result.Words[j].Count
		}
		return result.Words[i].Text < result.Words[j].Text
	})
	return result, nil
}

func InitTerms(root string, segments []document.Segment, refresh bool) (map[string]any, error) {
	dir := filepath.Join(root, "materials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	glossary := filepath.Join(dir, "glossary.json")
	if err := jsonfile.Publish(glossary, struct {
		Entries []any `json:"entries"`
	}{Entries: []any{}}); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	path := filepath.Join(dir, "term-statistics.json")
	if data, err := os.ReadFile(path); err == nil && !refresh {
		sourceHash, err := jsonfile.Digest(segments)
		if err != nil {
			return nil, err
		}
		var existing Statistics
		if err := json.Unmarshal(data, &existing); err != nil || existing.Version != 2 || existing.Method != statisticsMethod || existing.SourceHash != sourceHash || existing.Words == nil {
			return nil, fmt.Errorf("术语统计与原文或统计方法不一致；使用 --refresh 重新统计")
		}
		return map[string]any{"glossary": glossary, "statistics": path, "words": len(existing.Words), "status": "existing"}, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	stats, err := Count(segments)
	if err != nil {
		return nil, err
	}
	if refresh {
		data, err := json.MarshalIndent(stats, "", "  ")
		if err != nil {
			return nil, err
		}
		f, err := os.CreateTemp(dir, ".statistics-*.json")
		if err != nil {
			return nil, err
		}
		defer os.Remove(f.Name())
		_, writeErr := f.Write(append(data, '\n'))
		if writeErr == nil {
			writeErr = f.Sync()
		}
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return nil, err
		}
		if err := os.Rename(f.Name(), path); err != nil {
			return nil, err
		}
	} else if err := jsonfile.Publish(path, stats); err != nil {
		return nil, err
	}
	return map[string]any{"glossary": glossary, "statistics": path, "words": len(stats.Words), "status": "written"}, nil
}

func InitCharacters(root string) (string, error) {
	dir := filepath.Join(root, "materials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "characters.json")
	if err := jsonfile.Publish(path, CardTemplate()); err != nil {
		return "", err
	}
	return path, nil
}
