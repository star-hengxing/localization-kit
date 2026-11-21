package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/store"
)

type claim struct {
	TaskID, Stage, Worker, Model, Revision, Group string
	Cards, Scope                                  string
	Items                                         []claimItem
	Glossaries                                    []string
	Reviews                                       []ReviewFiles
	Expires                                       time.Time
	Context, MaxLines                             int
}

type claimItem struct{ SegmentID, ExpectedHash, InputDigest string }

type claimsFile struct {
	Version int                    `json:"version"`
	Claims  []claim                `json:"claims"`
	Stages  map[string]stageConfig `json:"stages,omitempty"`
	Live    *Status                `json:"live,omitempty"`
}

type stageConfig struct {
	Model, Revision, Cards, Scope string
	Glossaries                    []string
	Reviews                       []ReviewFiles
	Context, MaxLines             int
}

func (c stageConfig) matches(options Options) bool {
	return c.Model == options.Model && c.Revision == options.Revision && c.Cards == options.Cards && scopeName(c.Scope) == scopeName(options.Scope) && c.Context == options.Context && c.MaxLines == options.MaxLines && slices.Equal(c.Glossaries, options.Glossaries) && slices.Equal(c.Reviews, options.Reviews)
}

func statePath(jobPath string) string {
	return filepath.Join(jobPath, "status.json")
}

func claimPaths(jobPath string, state claimsFile, writing bool) claimsFile {
	root := filepath.Dir(jobPath)
	convert := func(path string) string {
		if path == "" {
			return ""
		}
		if !writing {
			if !filepath.IsAbs(path) {
				return filepath.Join(root, filepath.FromSlash(path))
			}
			return path
		}
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return filepath.ToSlash(rel)
		}
		return path
	}
	paths := func(values []string) []string {
		result := append([]string(nil), values...)
		for i := range result {
			result[i] = convert(result[i])
		}
		return result
	}
	reviews := func(values []ReviewFiles) []ReviewFiles {
		result := append([]ReviewFiles(nil), values...)
		for i := range result {
			result[i].Input = convert(result[i].Input)
			result[i].Report = convert(result[i].Report)
		}
		return result
	}
	state.Claims = append([]claim(nil), state.Claims...)
	for i := range state.Claims {
		c := &state.Claims[i]
		c.Cards, c.Glossaries, c.Reviews = convert(c.Cards), paths(c.Glossaries), reviews(c.Reviews)
	}
	stages := make(map[string]stageConfig, len(state.Stages))
	for name, cfg := range state.Stages {
		cfg.Cards, cfg.Glossaries, cfg.Reviews = convert(cfg.Cards), paths(cfg.Glossaries), reviews(cfg.Reviews)
		stages[name] = cfg
	}
	state.Stages = stages
	return state
}

func readClaims(jobPath string) (claimsFile, error) {
	path := statePath(jobPath)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return claimsFile{Version: 1}, nil
	}
	if err != nil {
		return claimsFile{}, err
	}
	if !info.Mode().IsRegular() {
		return claimsFile{}, fmt.Errorf("任务领取记录不是普通文件: %s", path)
	}
	data, err := fileutil.ReadFileShared(path)
	if err != nil {
		return claimsFile{}, err
	}
	var state claimsFile
	if !utf8.Valid(data) || json.Unmarshal(data, &state) != nil || state.Version != 1 {
		return claimsFile{}, fmt.Errorf("任务领取记录无效: %s", path)
	}
	return claimPaths(jobPath, state, false), nil
}

func writeClaims(jobPath string, state claimsFile) error {
	data, err := json.Marshal(claimPaths(jobPath, state, true))
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(jobPath, ".status-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), statePath(jobPath))
}

func claimValid(job *store.Store, c claim, now time.Time) (bool, error) {
	if !c.Expires.After(now) || len(c.Items) == 0 {
		return false, nil
	}
	for _, item := range c.Items {
		if _, saved := job.FindAgentTask(c.TaskID, c.Worker, item.SegmentID); saved {
			return false, nil
		}
	}
	entries, cards, err := materials(c.Glossaries, c.Cards, job.Segments)
	if err != nil {
		return false, err
	}
	deps, err := stageDependencies(job, c.Stage, entries, cards, c.Reviews)
	if err != nil {
		return false, err
	}
	_, err = taskFor(job, c, entries, cards, deps)
	return err == nil, nil
}

func Release(jobPath, worker string) (count int, resultErr error) {
	if jobPath == "" || !validIdentity(worker) {
		return 0, fmt.Errorf("需要作业和 worker")
	}
	job, err := store.OpenExisting(jobPath)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := job.Close(); resultErr == nil {
			resultErr = closeErr
		}
	}()
	state, err := readClaims(jobPath)
	if err != nil {
		return 0, err
	}
	kept := state.Claims[:0]
	for _, c := range state.Claims {
		if c.Worker == worker {
			count++
		} else {
			kept = append(kept, c)
		}
	}
	if count > 0 {
		state.Claims = kept
		if err := writeClaims(jobPath, state); err != nil {
			return 0, err
		}
	}
	return count, nil
}
