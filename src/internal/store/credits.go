package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"localization-kit/src/internal/document"
)

func (s *Store) CreditLines() ([]string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "credits.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	if err := json.Unmarshal(data, &lines); err != nil || lines == nil {
		return nil, fmt.Errorf("译制署名文件无效")
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.ContainsAny(line, "\r\n") {
			return nil, fmt.Errorf("译制署名包含空行或换行")
		}
	}
	return lines, nil
}

var creditedLineCount = regexp.MustCompile(` \([0-9]+ 行\)$`)

// InitialModels traces the initial model of each current result when known.
func (s *Store) InitialModels() (map[string]string, error) {
	var history map[string]document.Result
	needHistory := false
	for _, result := range s.Done {
		if result.IsRevision() && result.InitialModel == "" && result.PreviousHash != "" {
			needHistory = true
			break
		}
	}
	if needHistory {
		var err error
		history, err = s.historyByHash()
		if err != nil {
			return nil, fmt.Errorf("读取署名所需的作业历史: %w", err)
		}
	}
	models := map[string]string{}
	for id, current := range s.Done {
		seen := map[string]bool{}
		for {
			if current.IsRevision() && current.InitialModel != "" {
				models[id] = current.InitialModel
				break
			}
			if !current.IsRevision() {
				if !current.HasProtectedOrigin() && !current.Passthrough && current.Model != "" {
					models[id] = current.Model
				}
				break
			}
			if current.PreviousHash == "" || seen[current.PreviousHash] {
				break
			}
			seen[current.PreviousHash] = true
			prior, ok := history[current.PreviousHash]
			if !ok || prior.ID != current.ID {
				break
			}
			current = prior
		}
	}
	for _, model := range models {
		if strings.ContainsAny(model, "\r\n") {
			return nil, fmt.Errorf("模型署名包含换行")
		}
	}
	return models, nil
}

// ExportCreditLines adds models that contributed to the current results.
func (s *Store) ExportCreditLines() ([]string, error) {
	credits, err := s.CreditLines()
	if err != nil {
		return nil, err
	}
	initial, err := s.InitialModels()
	if err != nil {
		return nil, err
	}
	models := map[string]bool{}
	for _, model := range initial {
		models[model] = true
	}
	first := -1
	for i, line := range credits {
		if strings.HasPrefix(line, "初翻: ") {
			first = i
			for _, name := range strings.Split(strings.TrimPrefix(line, "初翻: "), " / ") {
				delete(models, creditedLineCount.ReplaceAllString(name, ""))
			}
			break
		}
	}
	var added []string
	for name := range models {
		added = append(added, name)
	}
	sort.Strings(added)
	if len(added) > 0 {
		if first >= 0 {
			credits[first] += " / " + strings.Join(added, " / ")
		} else {
			credits = append([]string{"初翻: " + strings.Join(added, " / ")}, credits...)
		}
	}
	polishers, err := s.PolishActors()
	if err != nil {
		return nil, err
	}
	index := -1
	for i, line := range credits {
		if strings.HasPrefix(line, "润色: ") {
			index = i
			for _, actor := range strings.Split(strings.TrimPrefix(line, "润色: "), " / ") {
				delete(polishers, creditedLineCount.ReplaceAllString(actor, ""))
			}
			break
		}
	}
	added = added[:0]
	for actor := range polishers {
		added = append(added, actor)
	}
	sort.Strings(added)
	if len(added) > 0 {
		if index >= 0 {
			credits[index] += " / " + strings.Join(added, " / ")
		} else {
			credits = append(credits, "润色: "+strings.Join(added, " / "))
		}
	}
	proofreaders := map[string]bool{}
	for _, result := range s.Done {
		for _, actor := range result.ProofreadActors {
			if strings.TrimSpace(actor) == "" || strings.ContainsAny(actor, "\r\n") || strings.Contains(actor, " / ") {
				return nil, fmt.Errorf("invalid stored proofread actor")
			}
			proofreaders[actor] = true
		}
	}
	index = -1
	for i, line := range credits {
		if strings.HasPrefix(line, "校对: ") {
			index = i
			for _, actor := range strings.Split(strings.TrimPrefix(line, "校对: "), " / ") {
				delete(proofreaders, creditedLineCount.ReplaceAllString(actor, ""))
			}
			break
		}
	}
	added = added[:0]
	for actor := range proofreaders {
		added = append(added, actor)
	}
	sort.Strings(added)
	if len(added) > 0 {
		if index >= 0 {
			credits[index] += " / " + strings.Join(added, " / ")
		} else {
			credits = append(credits, "校对: "+strings.Join(added, " / "))
		}
	}
	return credits, nil
}

func (s *Store) PolishActorsByID() (map[string][]string, error) {
	history, err := s.historyByHash()
	if err != nil {
		return nil, fmt.Errorf("读取润色署名历史: %w", err)
	}
	actorsByID := map[string][]string{}
	for id, current := range s.Done {
		actors := map[string]bool{}
		seen := map[string]bool{}
		for {
			for _, actor := range current.PolishActors {
				if strings.TrimSpace(actor) == "" || strings.ContainsAny(actor, "\r\n") || strings.Contains(actor, " / ") {
					return nil, fmt.Errorf("invalid stored polish actor")
				}
				actors[actor] = true
			}
			if current.Origin == "polish" && current.EditActor != "" {
				if strings.ContainsAny(current.EditActor, "\r\n") {
					return nil, fmt.Errorf("润色署名包含换行")
				}
				actors[current.EditActor] = true
			}
			if current.PreviousHash == "" || seen[current.PreviousHash] {
				break
			}
			seen[current.PreviousHash] = true
			previous, ok := history[current.PreviousHash]
			if !ok || previous.ID != current.ID {
				break
			}
			current = previous
		}
		for actor := range actors {
			actorsByID[id] = append(actorsByID[id], actor)
		}
		sort.Strings(actorsByID[id])
	}
	return actorsByID, nil
}

func (s *Store) PolishActors() (map[string]bool, error) {
	byID, err := s.PolishActorsByID()
	if err != nil {
		return nil, err
	}
	actors := map[string]bool{}
	for _, names := range byID {
		for _, name := range names {
			actors[name] = true
		}
	}
	return actors, nil
}

func (s *Store) SetInitialCreditLines(lines []string) error {
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.ContainsAny(line, "\r\n") {
			return fmt.Errorf("译制署名包含空行或换行")
		}
	}
	if lines == nil {
		lines = []string{}
	}
	data, err := json.MarshalIndent(lines, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "credits.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}
