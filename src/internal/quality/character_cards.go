package quality

import (
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/materials"
	"localization-kit/src/internal/translate/rules"
)

type CardFact = materials.CardFact
type CardCharacter = materials.CardCharacter
type CardFile = materials.CardFile

type CharacterSource struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Src   string `json:"src"`
}

type CharacterInput struct {
	Source   []CharacterSource `json:"source"`
	Glossary []rules.Entry     `json:"glossary,omitempty"`
}

func NewCharacterInput(segments []document.Segment, glossary []rules.Entry) CharacterInput {
	input := CharacterInput{Source: make([]CharacterSource, 0, len(segments)), Glossary: glossary}
	for _, segment := range segments {
		input.Source = append(input.Source, CharacterSource{ID: segment.ID, Group: segment.Group, Src: segment.Src})
	}
	return input
}

func (input CharacterInput) Segments() []document.Segment {
	segments := make([]document.Segment, 0, len(input.Source))
	for _, source := range input.Source {
		segments = append(segments, document.Segment{ID: source.ID, Group: source.Group, Src: source.Src})
	}
	return segments
}

type CharacterCards struct {
	SourceHash   string          `json:"source_hash"`
	MaterialHash string          `json:"material_hash"`
	Groups       []string        `json:"groups"`
	Characters   []CardCharacter `json:"characters"`
}

func LoadCharacterCards(path string, segments []document.Segment) (CharacterCards, error) {
	var file CardFile
	if err := jsonfile.Read(path, &file); err != nil {
		return CharacterCards{}, err
	}
	if len(file.Characters) == 0 {
		return CharacterCards{}, fmt.Errorf("角色卡没有角色；请先填写或不启用")
	}
	return ValidateCharacterCards(file, segments)
}

func ValidateNewCharacterCards(file CardFile, segments []document.Segment, glossary []rules.Entry) (CharacterCards, error) {
	cards, err := ValidateCharacterCards(file, segments)
	if err != nil {
		return CharacterCards{}, err
	}
	names := map[string]string{}
	for _, entry := range glossary {
		for _, form := range append([]string{entry.Src}, entry.Variants...) {
			if _, exists := names[form]; !exists {
				names[form] = entry.Dst
			}
		}
	}
	for _, character := range file.Characters {
		if translation, ok := names[character.NameSrc]; ok && translation != character.NameDst {
			return CharacterCards{}, fmt.Errorf("角色 %q 的译名冲突：角色卡为 %q，术语表为 %q", character.NameSrc, character.NameDst, translation)
		}
		for _, facts := range [][]CardFact{character.SelfRef, character.Address, character.Laugh, character.Catchphrases} {
			for _, fact := range facts {
				if utf8.RuneCountInString(fact.Evidence) > 160 {
					return CharacterCards{}, fmt.Errorf("角色 %s 的引文超过 160 个字符", character.NameSrc)
				}
			}
		}
	}
	return cards, nil
}

func ValidateCharacterCards(file CardFile, segments []document.Segment) (CharacterCards, error) {
	if file.Characters == nil {
		return CharacterCards{}, fmt.Errorf("角色卡需要 characters 数组")
	}
	rows := make([]TermSource, 0, len(segments))
	groups := []string{}
	seenGroups := map[string]bool{}
	for _, segment := range segments {
		rows = append(rows, TermSource{Segment: segment})
		if !seenGroups[segment.Group] {
			groups = append(groups, segment.Group)
			seenGroups[segment.Group] = true
		}
	}
	materialHash, err := jsonfile.Digest(file)
	if err != nil {
		return CharacterCards{}, err
	}
	sourceHash, err := jsonfile.Digest(rows)
	if err != nil {
		return CharacterCards{}, err
	}
	cards := CharacterCards{SourceHash: sourceHash, MaterialHash: materialHash, Groups: groups, Characters: file.Characters}
	seen := map[string]bool{}
	for _, item := range file.Characters {
		if strings.TrimSpace(item.NameSrc) == "" || strings.TrimSpace(item.NameDst) == "" || seen[item.NameSrc] {
			return CharacterCards{}, fmt.Errorf("角色姓名为空或重复: %s", item.NameSrc)
		}
		nameFound := false
		for _, segment := range segments {
			if strings.Contains(segment.Src, item.NameSrc) {
				nameFound = true
				break
			}
		}
		if !nameFound {
			slog.Warn("角色原名未在原文中出现，可能是听者或旁观者", "name", item.NameSrc)
		}
		if item.Gender != "female" && item.Gender != "male" && item.Gender != "futanari" && item.Gender != "unknown" {
			return CharacterCards{}, fmt.Errorf("角色 %s 的 gender 无效", item.NameSrc)
		}
		if item.Pronoun != "她" && item.Pronoun != "他" && item.Pronoun != "unknown" {
			return CharacterCards{}, fmt.Errorf("角色 %s 的 pronoun 无效", item.NameSrc)
		}
		seen[item.NameSrc] = true
		fields := []struct {
			name   string
			values []CardFact
		}{{"self_ref", item.SelfRef}, {"address", item.Address}, {"laugh", item.Laugh}, {"catchphrase", item.Catchphrases}}
		for _, field := range fields {
			for _, fact := range field.values {
				if strings.TrimSpace(fact.Dst) == "" || strings.TrimSpace(fact.Evidence) == "" || (fact.Src == "" && (field.name != "address" || fact.To != "listener")) || (fact.Src != "" && !strings.Contains(fact.Evidence, fact.Src)) {
					return CharacterCards{}, fmt.Errorf("角色 %s 的 %s 事实无效", item.NameSrc, field.name)
				}
				if field.name == "address" && strings.TrimSpace(fact.To) == "" {
					return CharacterCards{}, fmt.Errorf("角色 %s 的称呼缺少对象", item.NameSrc)
				}
				found := false
				for _, segment := range segments {
					if strings.Contains(segment.Src, fact.Evidence) {
						found = true
						break
					}
				}
				if !found {
					return CharacterCards{}, fmt.Errorf("角色 %s 的 %s 引文不在原文中", item.NameSrc, field.name)
				}
			}
		}
	}
	return cards, nil
}
