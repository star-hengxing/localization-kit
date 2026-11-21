package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
)

type Manifest struct {
	Version  int                `json:"version"`
	Segments []document.Segment `json:"segments"`
}

// Keep the lock file between runs. Unlinking it can let Unix processes lock
// different inodes for the same workspace.
const lockFileName = ".lock"
const resultsFileName = "results.jsonl"
const pendingManifestFileName = ".manifest.json.tmp"

var ErrLocked = errors.New("作品状态已锁定")

func VerifySourceSnapshot(dir string, segments []document.Segment) error {
	manifest, err := readManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	return verifySegments(manifest.Segments, segments)
}

func readManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("读取作品清单: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("解析作品清单 %s: %w", path, err)
	}
	if manifest.Version != 1 {
		return Manifest{}, fmt.Errorf("作品清单 %s 的版本 %d 不支持", path, manifest.Version)
	}
	return manifest, nil
}

type Store struct {
	dir          string
	lock         *os.File
	results      *os.File
	writable     bool
	needsNewline bool
	appendErr    error
	Segments     []document.Segment
	positions    map[string]int
	agentTasks   map[agentTaskKey]map[string]agentTaskResult
	Done         map[string]document.Result
	Failed       map[string]document.Result
	stages       map[string]map[string]document.Result
	AfterCommit  func(*Store) error
	BeforeUnlock func() error
}

func LockExistingFile(path string) (func() error, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() error { return errors.Join(unlock(f), f.Close()) }, nil
}

func OpenOrCreate(dir string, segments []document.Segment) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	s := &Store{dir: dir, lock: f, writable: true, Segments: segments, Done: map[string]document.Result{}, Failed: map[string]document.Result{}}
	s.indexSegments()
	if err := s.initialize(segments); err != nil {
		return nil, errors.Join(err, s.closeFiles(nil))
	}
	return s, nil
}

// OpenReadOnly takes the same lock as writers but never repairs or changes job files.
func OpenReadOnly(dir string) (*Store, error) {
	return openExisting(dir, false)
}

// OpenExisting opens a writable store and recovers journaled batches.
// It discards an unterminated log tail only if the tail contains invalid JSON.
func OpenExisting(dir string) (*Store, error) {
	return openExisting(dir, true)
}

func openExisting(dir string, writable bool) (*Store, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("打开作业锁: %w", err)
	}
	if err = lock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	s := &Store{dir: dir, lock: f, writable: writable, Done: map[string]document.Result{}, Failed: map[string]document.Result{}}
	manifest, err := readManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, errors.Join(err, s.closeFiles(nil))
	}
	s.Segments = manifest.Segments
	s.indexSegments()
	if err := s.openResults(); err != nil {
		return nil, errors.Join(err, s.closeFiles(nil))
	}
	return s, nil
}

func (s *Store) indexSegments() {
	s.positions = make(map[string]int, len(s.Segments))
	for i, segment := range s.Segments {
		s.positions[segment.ID] = i
	}
}

func (s *Store) SegmentIndex(id string) (int, bool) {
	index, ok := s.positions[id]
	return index, ok
}

func (s *Store) VerifySegments(segments []document.Segment) error {
	return verifySegments(s.Segments, segments)
}

func verifySegments(oldSegments, segments []document.Segment) error {
	old, err := json.Marshal(oldSegments)
	if err != nil {
		return err
	}
	current, err := json.Marshal(segments)
	if err != nil {
		return err
	}
	if !bytes.Equal(old, current) {
		return fmt.Errorf("输入与作品清单不一致，请确认源文件")
	}
	return nil
}

func (s *Store) initialize(segments []document.Segment) error {
	manifestPath := filepath.Join(s.dir, "manifest.json")
	manifest, err := readManifest(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.createJob(manifestPath, segments); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := verifySegments(manifest.Segments, segments); err != nil {
		return fmt.Errorf("核对作品清单 %s: %w", manifestPath, err)
	}
	return s.openResults()
}

func (s *Store) createJob(manifestPath string, segments []document.Segment) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	hasHistory, hasPendingManifest := false, false
	for _, entry := range entries {
		switch entry.Name() {
		case lockFileName:
		case resultsFileName, pendingManifestFileName:
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || (entry.Name() == resultsFileName && info.Size() != 0) {
				return fmt.Errorf("目录 %s 不是新的空作品目录", s.dir)
			}
			hasHistory = hasHistory || entry.Name() == resultsFileName
			hasPendingManifest = hasPendingManifest || entry.Name() == pendingManifestFileName
		default:
			return fmt.Errorf("目录 %s 不是新的空作品目录", s.dir)
		}
	}
	// The manifest commits initialization. An interrupted first open can leave
	// an empty history and a draft, both of which are safe to resume under the lock.
	if !hasHistory {
		f, err := os.OpenFile(filepath.Join(s.dir, resultsFileName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	pending := filepath.Join(s.dir, pendingManifestFileName)
	if hasPendingManifest {
		if err := os.Remove(pending); err != nil {
			return err
		}
	}
	if err := jsonfile.Create(pending, Manifest{Version: 1, Segments: segments}); err != nil {
		return err
	}
	return os.Rename(pending, manifestPath)
}

func (s *Store) openResults() error {
	flags := os.O_RDONLY
	if s.writable {
		flags = os.O_RDWR
	}
	f, err := os.OpenFile(filepath.Join(s.dir, resultsFileName), flags, 0)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("缺少结果历史 %s，请恢复原文件后重试: %w", resultsFileName, err)
	}
	if err != nil {
		return err
	}
	s.results = f
	if err := s.recoverBatch(s.writable); err != nil {
		return err
	}
	if _, err := s.results.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return s.readResults(s.writable)
}

func (s *Store) Close() error {
	var err error
	if s.writable && s.results != nil && s.appendErr == nil {
		err = s.writeStageSnapshots()
	}
	return errors.Join(err, s.closeFiles(s.BeforeUnlock))
}

// An open failure can leave only a prefix of the history in memory. Release
// the files independently of the snapshot publication in Close.
func (s *Store) closeFiles(beforeUnlock func() error) error {
	var err error
	if s.results != nil {
		err = s.results.Close()
	}
	if s.lock != nil {
		if beforeUnlock != nil {
			err = errors.Join(err, beforeUnlock())
		}
		err = errors.Join(err, unlock(s.lock), s.lock.Close())
	}
	return err
}
