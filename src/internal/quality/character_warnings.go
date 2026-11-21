package quality

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

func NamedCharacterWarning(src, dst string, characters []CardCharacter) string {
	var present []CardCharacter
	for _, character := range characters {
		for _, name := range append([]string{character.NameDst}, character.Aliases...) {
			// Single-character and shared aliases cannot reliably identify a person.
			if utf8.RuneCountInString(strings.TrimSpace(name)) >= 2 && strings.Contains(dst, name) {
				present = append(present, character)
				break
			}
		}
	}
	if len(present) != 1 || present[0].Pronoun == "unknown" {
		return ""
	}
	character := present[0]
	wrong := "他"
	if character.Pronoun == "他" {
		wrong = "她"
	}
	for _, other := range characters {
		if other.NameSrc != character.NameSrc && (other.Pronoun == wrong || other.Pronoun == "unknown") {
			return ""
		}
	}
	clean := strings.NewReplacer("其他", "", "其它", "", "他人", "", "他乡", "", "他日", "", "吉他", "", "利他", "", "排他", "", "扶他", "").Replace(dst)
	if !strings.Contains(clean, wrong) || strings.Contains(clean, character.Pronoun) {
		return ""
	}
	// Generic people and source pronouns may refer to someone outside the cards.
	for _, word := range []string{"男人", "女人", "男性", "女性", "男孩", "女孩", "父", "母", "兄", "弟", "姐", "妹", "客人", "同学", "对方", "别人", "先生", "小姐", "丈夫", "妻子", "男", "女", "彼", "夫", "おじ", "パパ"} {
		if strings.Contains(src, word) || strings.Contains(clean, word) {
			return ""
		}
	}
	return fmt.Sprintf("Named character %s has card pronoun %s, but this translation contains only %s; the referent may be another person, so verify against the source.", character.NameDst, character.Pronoun, wrong)
}

var narrationQuotes = []*regexp.Regexp{
	regexp.MustCompile(`「[^「」]*」|『[^『』]*』`),
	regexp.MustCompile(`（[^（）]*）|\([^()]*\)`),
	regexp.MustCompile(`“[^“”]*”|‘[^‘’]*’|"[^"]*"`),
}
var narrationOpen = regexp.MustCompile(`(?s)[「『（(“‘"].*$`)
var narrationClose = regexp.MustCompile(`(?s)^.*[」』）)”’]`)
var sourceFirstPerson = regexp.MustCompile(`私|俺|僕|わたし|あたし|わたくし|うち|あたい|ボク|オレ|アタシ|ワタシ|我|咱|吾|余|(?i:\bI\b|\bwe\b|\bme\b|\bmy\b|\bour\b)`)

func narrationText(text string) string {
	for {
		before := text
		for _, quoted := range narrationQuotes {
			text = quoted.ReplaceAllString(text, "")
		}
		if text == before {
			break
		}
	}
	return narrationOpen.ReplaceAllString(narrationClose.ReplaceAllString(text, ""), "")
}

func NarrationPersonWarning(src, dst string) bool {
	source := strings.TrimSpace(narrationText(src))
	if source == "" || sourceFirstPerson.MatchString(source) {
		return false
	}
	target := narrationText(dst)
	for _, exempt := range []string{"自我", "忘我", "无我", "唯我", "我行我素", "你争我夺", "你来我往", "你一言我一语"} {
		target = strings.ReplaceAll(target, exempt, "")
	}
	return strings.Contains(target, "我")
}
