package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"localization-kit/src/internal/document"
)

const DefaultDataPath = "data/simplified.json"

type simplifiedTable struct {
	Map         map[string]string `json:"map"`
	Traditional []string          `json:"traditional"`
	Ambiguous   map[string]string `json:"ambiguous"`
	Keep        []string          `json:"keep"`
}

var simplified simplifiedTable
var traditional map[string]bool
var simplifiedDigest string

func LoadSimplified(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析繁简字形表路径 %s: %w", path, err)
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return fmt.Errorf("读取繁简字形表 %s: %w", absolute, err)
	}
	var table simplifiedTable
	if err := json.Unmarshal(data, &table); err != nil {
		return fmt.Errorf("解析繁简字形表 %s: %w", absolute, err)
	}
	if len(table.Map) == 0 || table.Traditional == nil || table.Ambiguous == nil || table.Keep == nil {
		return fmt.Errorf("繁简字形表 %s 缺少必要数据", absolute)
	}
	known := make(map[string]bool, len(table.Traditional))
	for _, value := range table.Traditional {
		known[value] = true
	}
	canonical, err := json.Marshal(table)
	if err != nil {
		return fmt.Errorf("计算繁简字形表摘要: %w", err)
	}
	digest := sha256.Sum256(canonical)
	simplified, traditional = table, known
	simplifiedDigest = hex.EncodeToString(digest[:])
	return nil
}

func SimplifiedDigest() string { return simplifiedDigest }

func IsKana(r rune) bool { return r >= '\u3040' && r <= '\u30ff' }

func Translatable(s string) bool {
	for _, r := range s {
		if IsKana(r) || r >= '\u3400' && r <= '\u9fff' || r >= '\uf900' && r <= '\ufaff' {
			return true
		}
	}
	return false
}

func protectedPositions(text string, terms []string) map[int]bool {
	positions := map[int]bool{}
	runes := []rune(text)
	for _, term := range terms {
		target := []rune(term)
		if len(target) == 0 {
			continue
		}
		for i := 0; i+len(target) <= len(runes); i++ {
			if string(runes[i:i+len(target)]) == term {
				for j := i; j < i+len(target); j++ {
					positions[j] = true
				}
			}
		}
	}
	return positions
}

func Simplify(value string, keep []string) string {
	if simplified.Map == nil {
		panic("繁简字形表尚未加载")
	}
	runes := []rune(value)
	protected := protectedPositions(value, keep)
	convertedTraditional := false
	for i, r := range runes {
		if protected[i] {
			continue
		}
		original := string(r)
		if r >= '\uf900' && r <= '\ufaff' {
			original = norm.NFC.String(original)
		}
		replacement, exists := simplified.Map[original]
		if exists {
			convertedTraditional = convertedTraditional || traditional[original]
			if v := []rune(replacement); len(v) == 1 {
				runes[i] = v[0]
			}
		} else if v := []rune(original); len(v) == 1 {
			runes[i] = v[0]
		}
	}
	if convertedTraditional {
		for i := range protectedPositions(string(runes), simplified.Keep) {
			protected[i] = true
		}
		for i, r := range runes {
			if replacement, ok := simplified.Ambiguous[string(r)]; ok && !protected[i] {
				if v := []rune(replacement); len(v) == 1 {
					runes[i] = v[0]
				}
			}
		}
	}
	return string(runes)
}

var latinRE = regexp.MustCompile(`[A-Za-z]{2,}`)
var leakPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\[(Source Text|原文|翻译|译文|Translation)\]`),
	regexp.MustCompile(`(?im)^(术语表|Glossary|原文|译文|翻译|Translation)\s*[：:]`),
	regexp.MustCompile(`(?i)参考下面的翻译|Translate (the following|Japanese)|【(背景信息|待?翻译文本)】`),
	regexp.MustCompile(`</?(think|thinking|why|reason)>|<eos:|<｜hy_|<\|im_(start|end)\|>`),
	regexp.MustCompile(`\[ERROR:`),
}
var refusalRE = regexp.MustCompile(`(?i)(作为.{0,4}(AI|人工智能|语言模型)|AI助手|语言模型助手|I can'?t|I cannot|I'?m sorry|As an AI)`)
var apologySourceRE = regexp.MustCompile(`(?i)すみません|すいません|ごめん|御免|申し訳|すまな|すまん|悪かった|悪い|失礼|許して|sorry`)
var refusalActionRE = regexp.MustCompile(`(?:无法|不能)(?:协助|提供|翻译)`)
var refusalActionSourceRE = regexp.MustCompile(`翻訳|提供|手伝|協力|助け`)
var censoredNumber = regexp.MustCompile(`[0-9０-９][○●◆×＊◯]|[○●◆×＊◯][0-9０-９]`)
var placeholderRE = regexp.MustCompile(`【[0-9]+】`)

func placeholdersEqual(src, dst string) bool {
	counts := map[string]int{}
	for _, value := range placeholderRE.FindAllString(src, -1) {
		counts[value]++
	}
	for _, value := range placeholderRE.FindAllString(dst, -1) {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func words(value string) []rune {
	return []rune(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("「」『』（）()[]【】…‥♪♥❤☆★♡～〜~。、，,.！!？?・：:；;\"'“”‘’—―-*＊", r) {
			return -1
		}
		return r
	}, value))
}

func termSatisfied(dst, term string) bool {
	if strings.Contains(dst, term) {
		return true
	}
	parts := []rune(term)
	if len(parts) < 2 {
		return false
	}
	var pattern []string
	for _, r := range parts {
		pattern = append(pattern, regexp.QuoteMeta(string(r)))
	}
	return regexp.MustCompile(strings.Join(pattern, `.{0,2}`)).MatchString(dst)
}

func balanced(value, opens, closes string) bool {
	depth := 0
	for _, r := range value {
		if r == '(' {
			r = '（'
		}
		if r == ')' {
			r = '）'
		}
		if strings.ContainsRune(opens, r) {
			depth++
		} else if strings.ContainsRune(closes, r) {
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func listItemCount(value string) int {
	runes := []rune(value)
	count := 0
	for i, r := range runes {
		if !strings.ContainsRune("・·•", r) {
			continue
		}
		if i > 0 && strings.ContainsRune("・·•", runes[i-1]) {
			continue
		}
		if i+1 < len(runes) && strings.ContainsRune("・·•", runes[i+1]) {
			continue
		}
		count++
	}
	return count
}

func Check(src, dst string, entries []Entry) ([]document.Issue, []document.Issue) {
	if strings.TrimSpace(dst) == "" {
		return []document.Issue{{Code: "empty", Detail: "译文为空"}}, nil
	}
	var blocking, warnings []document.Issue
	addBlock := func(code, msg string) { blocking = append(blocking, document.Issue{Code: code, Detail: msg}) }
	addWarn := func(code, msg string) { warnings = append(warnings, document.Issue{Code: code, Detail: msg}) }
	if strings.Count(src, "\n") != strings.Count(dst, "\n") {
		addBlock("line_count", "译文行数与原文不一致")
	}
	if !placeholdersEqual(src, dst) {
		addBlock("placeholder", "占位符数量或编号与原文不一致")
	}
	srcKana, dstKana := 0, 0
	for _, r := range words(src) {
		if IsKana(r) {
			srcKana++
		}
	}
	dstWords := words(dst)
	for _, r := range dstWords {
		if IsKana(r) {
			dstKana++
		}
	}
	if strings.TrimSpace(src) == strings.TrimSpace(dst) && srcKana > 0 || dstKana >= 2 && len(dstWords) > 0 && float64(dstKana)/float64(len(dstWords)) > 0.3 {
		addBlock("untranslated", "译文疑似未翻译")
	}
	for _, pattern := range leakPatterns {
		if pattern.MatchString(dst) && !pattern.MatchString(src) {
			addBlock("prompt_leak", "译文包含提示词或特殊标记")
			break
		}
	}
	if refusalRE.MatchString(dst) && !refusalRE.MatchString(src) || strings.Contains(dst, "抱歉") && !strings.Contains(src, "抱歉") && !apologySourceRE.MatchString(src) || refusalActionRE.MatchString(dst) && !refusalActionRE.MatchString(src) && !refusalActionSourceRE.MatchString(src) {
		addBlock("refusal", "译文疑似拒答")
	}
	hits := Hits(src, entries)
	if !strings.ContainsAny(src, "=＝") {
		left, right, hasEqual := strings.Cut(strings.TrimSpace(dst), "=")
		if hasEqual && !strings.ContainsAny(right, "=\n") {
			for _, term := range hits {
				if strings.TrimSpace(left) == term.Form && strings.TrimSpace(right) == term.Entry.Dst {
					addBlock("prompt_leak", "译文疑似术语表回显")
					break
				}
			}
		}
	}
	var keep []string
	for _, term := range hits {
		keep = append(keep, term.Entry.Dst)
		if term.Entry.Constraint == "hard" && !termSatisfied(dst, term.Entry.Dst) {
			addWarn("term", fmt.Sprintf("译文缺少术语 %s", term.Entry.Dst))
		}
	}
	if Simplify(dst, keep) != dst {
		addWarn("traditional", "译文含繁体或日文字形")
	}
	if censoredNumber.MatchString(src) && !censoredNumber.MatchString(dst) {
		addWarn("censor", "原文数字旁的伏字符号在译文中丢失")
	}
	allowed := strings.ToLower(norm.NFKC.String(src + " " + strings.Join(keep, " ")))
	known := map[string]bool{}
	for _, word := range latinRE.FindAllString(allowed, -1) {
		known[word] = true
	}
	for _, word := range latinRE.FindAllString(norm.NFKC.String(dst), -1) {
		if !known[strings.ToLower(word)] {
			addWarn("latin", "译文包含原文未出现的英文词: "+word)
			break
		}
	}
	nSrc, nDst := len(words(src)), len(dstWords)
	itemsSrc, itemsDst := listItemCount(src), listItemCount(dst)
	if itemsSrc >= 3 && float64(itemsDst) < float64(itemsSrc)*0.6 && float64(nDst) < float64(nSrc)*0.6 {
		addBlock("truncated", "译文列表项过少，疑似漏译")
	} else if nSrc >= 40 && float64(nDst) < float64(nSrc)*0.25 {
		addBlock("truncated", "译文过短，疑似漏译")
	}
	for _, pair := range [][2]string{{"「『", "」』"}, {"（", "）"}, {"【", "】"}} {
		present := pair[0] + pair[1]
		if pair[0] == "（" {
			present += "()"
		}
		if strings.ContainsAny(src, present) && balanced(src, pair[0], pair[1]) && !balanced(dst, pair[0], pair[1]) {
			addWarn("brackets", "原文成对的括号在译文中不成对")
			break
		}
	}
	if nSrc >= 12 && float64(nDst) > float64(nSrc)*1.3 {
		addWarn("length", "译文较长")
	}
	return blocking, warnings
}
