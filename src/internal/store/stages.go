package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"localization-kit/src/internal/document"
)

func (s *Store) trackStage(result document.Result) {
	// A review marker changes live status without replacing the accepted stage text.
	if result.NeedsReview {
		return
	}
	if strings.HasPrefix(result.EditMethod, "redo-") {
		if result.EditMethod == "redo-translate" || result.EditMethod == "redo-context" {
			if s.stages == nil {
				s.stages = map[string]map[string]document.Result{}
			}
			if s.stages["translate"] == nil {
				s.stages["translate"] = map[string]document.Result{}
			}
			s.stages["translate"][result.ID] = result
		}
		return
	}
	stage := result.Stage
	if stage == "" {
		switch result.Origin {
		case "polish":
			stage = "polish"
		case "proofread":
			stage = "proofread"
		case "manual":
			stage = "translate"
		default:
			stage = "translate"
		}
	}
	if stage != "translate" && stage != "polish" && stage != "proofread" {
		return
	}
	if s.stages == nil {
		s.stages = map[string]map[string]document.Result{}
	}
	if s.stages[stage] == nil {
		s.stages[stage] = map[string]document.Result{}
	}
	s.stages[stage][result.ID] = result
}

// StageResults returns the accepted text recorded for one stage.
func (s *Store) StageResults(stage string) (map[string]document.Result, error) {
	if stage != "translate" && stage != "polish" && stage != "proofread" {
		return nil, fmt.Errorf("未知阶段 %q", stage)
	}
	result := make(map[string]document.Result, len(s.stages[stage]))
	for id, item := range s.stages[stage] {
		result[id] = item
	}
	if stage != "translate" {
		for id, item := range s.stages["translate"] {
			if item.Passthrough && item.Status == "done" {
				result[id] = item
			}
		}
	}
	return result, nil
}

func (s *Store) writeStageSnapshots() error {
	for _, stage := range []string{"translate", "polish", "proofread"} {
		if len(s.stages[stage]) == 0 {
			continue
		}
		path := filepath.Join(s.dir, stage, "translation.json")
		results, _ := s.StageResults(stage)
		items := make([]document.Result, 0, len(s.Segments))
		for _, segment := range s.Segments {
			item, ok := results[segment.ID]
			if !ok {
				item = document.Result{ID: segment.ID, Src: segment.Src, Status: "pending"}
			}
			items = append(items, item)
		}
		value := struct {
			Stage   string            `json:"stage"`
			Results []document.Result `json:"results"`
		}{stage, items}
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if existing, err := os.ReadFile(path); err == nil {
			if bytes.Equal(existing, data) {
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		dir := filepath.Join(s.dir, stage)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, ".translation-*.json")
		if err != nil {
			return err
		}
		name := f.Name()
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err == nil {
			err = os.Rename(name, path)
		}
		if err != nil {
			_ = os.Remove(name)
			return fmt.Errorf("写入 %s 阶段文件: %w", stage, err)
		}
	}
	return nil
}
