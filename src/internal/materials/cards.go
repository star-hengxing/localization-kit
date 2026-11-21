package materials

import (
	"reflect"

	"localization-kit/src/internal/jsonfile"
)

type CardFact struct {
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	Evidence string `json:"evidence"`
	To       string `json:"to,omitempty"`
	Note     string `json:"note,omitempty"`
}

type CardCharacter struct {
	NameSrc      string     `json:"name_src"`
	NameDst      string     `json:"name_dst"`
	Aliases      []string   `json:"aliases_dst"`
	Gender       string     `json:"gender"`
	Pronoun      string     `json:"pronoun"`
	Register     string     `json:"register"`
	SelfRef      []CardFact `json:"self_ref"`
	Address      []CardFact `json:"address"`
	Laugh        []CardFact `json:"laugh"`
	Catchphrases []CardFact `json:"catchphrases"`
}

type CardFile struct {
	Characters []CardCharacter `json:"characters"`
}

func CardTemplate() CardFile {
	return CardFile{Characters: []CardCharacter{{Aliases: []string{}, Gender: "unknown", Pronoun: "unknown", SelfRef: []CardFact{}, Address: []CardFact{}, Laugh: []CardFact{}, Catchphrases: []CardFact{}}}}
}

func IsUnfilledCard(path string) (bool, error) {
	var card CardFile
	if err := jsonfile.Read(path, &card); err != nil {
		return false, err
	}
	return (card.Characters != nil && len(card.Characters) == 0) || reflect.DeepEqual(card, CardTemplate()), nil
}
