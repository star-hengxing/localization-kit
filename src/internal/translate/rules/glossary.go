package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Entry struct {
	Src        string   `json:"src"`
	Dst        string   `json:"dst"`
	Kind       string   `json:"kind"`
	Constraint string   `json:"constraint"`
	Variants   []string `json:"variants"`
	Note       string   `json:"note"`
}

type Term struct {
	Form  string `json:"form"`
	Entry Entry  `json:"entry"`
}

func LoadGlossaries(paths []string) ([]Entry, error) {
	var entries []Entry
	index := map[string]int{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取术语表 %s: %w", path, err)
		}
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("术语表 %s 不是有效 UTF-8", path)
		}
		var table struct {
			Entries []Entry `json:"entries"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(string(data), "\ufeff")), &table); err != nil || table.Entries == nil {
			return nil, fmt.Errorf("术语表 %s 格式无效", path)
		}
		for _, entry := range table.Entries {
			if entry.Src == "" || entry.Dst == "" {
				return nil, fmt.Errorf("术语表 %s 存在空的原文或译文", path)
			}
			if entry.Kind == "" {
				entry.Kind = "term"
			}
			if entry.Constraint == "" {
				entry.Constraint = "soft"
			}
			if entry.Kind != "term" && entry.Kind != "name" && entry.Kind != "onomatopoeia" {
				return nil, fmt.Errorf("术语 %q 的 kind 无效", entry.Src)
			}
			if entry.Constraint != "hard" && entry.Constraint != "soft" {
				return nil, fmt.Errorf("术语 %q 的 constraint 无效", entry.Src)
			}
			if i, ok := index[entry.Src]; ok {
				entries[i] = entry
			} else {
				index[entry.Src] = len(entries)
				entries = append(entries, entry)
			}
		}
	}
	return entries, nil
}

func Hits(src string, entries []Entry) []Term {
	var hits []Term
	seen := map[string]bool{}
	words := strings.FieldsFunc(src, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("…‥・、。，,.!！?？～〜~―—-♡♥❤♪☆★「」『』（）()", r)
	})
	starts := make([]int, len(words))
	ends := make([]int, len(words))
	cursor := 0
	for i, word := range words {
		starts[i] = cursor + strings.Index(src[cursor:], word)
		ends[i] = starts[i] + len(word)
		cursor = ends[i]
	}
	for _, entry := range entries {
		forms := append([]string{entry.Src}, entry.Variants...)
		for _, form := range forms {
			if form == "" || seen[form] {
				continue
			}
			matched := strings.Contains(src, form)
			if entry.Kind == "onomatopoeia" {
				matched = false
				for i, word := range words {
					if len(word)%len(form) == 0 && strings.Repeat(form, len(word)/len(form)) == word {
						if len([]rune(form)) == 1 {
							j := i + 1
							for j < len(words) && words[j] == form {
								j++
							}
							stutter := j < len(words) && strings.HasPrefix(words[j], form) && len(words[j]) > len(word)
							for k := i; stutter && k < j; k++ {
								stutter = strings.TrimFunc(src[ends[k]:starts[k+1]], func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("、,，", r) }) == ""
							}
							if stutter {
								continue
							}
						}
						matched = true
						break
					}
				}
			}
			if matched {
				seen[form] = true
				hits = append(hits, Term{Form: form, Entry: entry})
			}
		}
	}
	return hits
}
