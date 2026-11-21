package rules

import (
	"strings"
	"unicode"
)

func separator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("…‥・、。，,.!！?？～〜~―—-♡♥❤♪☆★「」『』（）()", r)
}

func fixSound(src, dst string, entries []Entry) string {
	type part struct {
		value string
		term  string
		sound string
	}
	var parts []part
	runes := []rune(strings.TrimSpace(src))
	for i := 0; i < len(runes); {
		j := i + 1
		isSep := separator(runes[i])
		for j < len(runes) && separator(runes[j]) == isSep {
			j++
		}
		value := string(runes[i:j])
		p := part{value: value}
		if !isSep {
			for _, e := range entries {
				if e.Kind != "onomatopoeia" || e.Constraint != "hard" {
					continue
				}
				for _, form := range append([]string{e.Src}, e.Variants...) {
					if form != "" && len(value)%len(form) == 0 && strings.Repeat(form, len(value)/len(form)) == value {
						p.term, p.sound = e.Dst, strings.Repeat(e.Dst, len(value)/len(form))
						break
					}
				}
				if p.term != "" {
					break
				}
			}
			if p.term == "" {
				return dst
			}
		}
		parts = append(parts, p)
		i = j
	}
	allPresent, allInitial, found := true, true, false
	for _, p := range parts {
		if p.term != "" {
			found = true
			allPresent = allPresent && termSatisfied(dst, p.term)
			first := []rune(p.term)
			allInitial = allInitial && len(first) > 0 && strings.ContainsRune(dst, first[0])
		}
	}
	question := strings.TrimRight(strings.TrimSpace(src), "」』）)")
	if !found || allPresent || (strings.HasSuffix(question, "？") || strings.HasSuffix(question, "?")) && allInitial {
		return dst
	}
	var out strings.Builder
	normalize := strings.NewReplacer("?", "？", "!", "！", "~", "～", ",", "，", "、", "，")
	for _, p := range parts {
		if p.sound != "" {
			out.WriteString(p.sound)
		} else {
			out.WriteString(normalize.Replace(p.value))
		}
	}
	return out.String()
}

func fixQuotes(value string) string {
	if !strings.ContainsAny(value, "“”‘’\"") || strings.Count(value, "\"")%2 != 0 {
		return value
	}
	open := map[rune]rune{'“': '”', '‘': '’', '「': '」', '『': '』'}
	close := map[rune]rune{'”': '“', '’': '‘', '」': '「', '』': '『'}
	var stack []rune
	var out strings.Builder
	for _, r := range value {
		if r == '"' && (len(stack) == 0 || stack[len(stack)-1] != '"') {
			stack = append(stack, r)
			if len(stack) == 1 {
				out.WriteRune('「')
			} else {
				out.WriteRune('『')
			}
		} else if _, ok := open[r]; ok {
			stack = append(stack, r)
			if len(stack) == 1 {
				out.WriteRune('「')
			} else {
				out.WriteRune('『')
			}
		} else if opener, ok := close[r]; ok || r == '"' {
			if r == '"' {
				opener = '"'
			}
			if len(stack) == 0 || stack[len(stack)-1] != opener && !(r == '”' && stack[len(stack)-1] == '"') {
				return value
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				out.WriteRune('」')
			} else {
				out.WriteRune('』')
			}
		} else {
			out.WriteRune(r)
		}
	}
	if len(stack) != 0 {
		return value
	}
	return out.String()
}

func Postfix(src, dst string, entries []Entry) string {
	var keep []string
	for _, term := range Hits(src, entries) {
		keep = append(keep, term.Entry.Dst)
	}
	dst = Simplify(dst, keep)
	dst = fixSound(src, dst, entries)
	dst = fixQuotes(dst)
	trimmedSource := strings.TrimSpace(src)
	if !strings.HasSuffix(trimmedSource, "。") && !strings.HasSuffix(trimmedSource, "．") {
		body := strings.TrimRightFunc(dst, unicode.IsSpace)
		trimmed := strings.TrimRight(body, "。．")
		if trimmed != "" {
			dst = trimmed + dst[len(body):]
		}
	}
	return dst
}
