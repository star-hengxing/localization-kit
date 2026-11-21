package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"localization-kit/src/internal/document"
)

func (s *Store) readResults(repair bool) error {
	needsNewline, err := s.scanHistory(s.results, s.trackResult, repair)
	if err != nil {
		return err
	}
	s.needsNewline = needsNewline
	_, err = s.results.Seek(0, io.SeekEnd)
	return err
}

// VisitHistory visits all results in append order, leaving the append offset
// unchanged. The visitor must only read the store.
func (s *Store) VisitHistory(visit func(document.Result)) error {
	info, err := s.results.Stat()
	if err != nil {
		return err
	}
	_, err = s.scanHistory(io.NewSectionReader(s.results, 0, info.Size()), visit, false)
	return err
}

// HistoryFor returns the complete history for the selected entries in its original order.
func (s *Store) HistoryFor(ids map[string]bool) ([]document.Result, error) {
	var results []document.Result
	err := s.VisitHistory(func(result document.Result) {
		if ids[result.ID] {
			results = append(results, result)
		}
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) historyByHash() (map[string]document.Result, error) {
	history := map[string]document.Result{}
	err := s.VisitHistory(func(result document.Result) {
		history[document.ResultHash(result)] = result
	})
	if err != nil {
		return nil, err
	}
	return history, nil
}

func (s *Store) scanHistory(source io.Reader, visit func(document.Result), repair bool) (needsNewline bool, err error) {
	reader := bufio.NewReader(source)
	var offset int64
	for {
		line, err := reader.ReadBytes('\n')
		last := errors.Is(err, io.EOF)
		if err != nil && !last {
			return false, err
		}
		if last && len(line) == 0 {
			return false, nil
		}
		if last && !json.Valid(line) {
			if !repair {
				return false, fmt.Errorf("结果文件末尾不完整；请先通过翻译命令恢复作业")
			}
			if err := s.results.Truncate(offset); err != nil {
				return false, err
			}
			return false, s.results.Sync()
		}
		if !utf8.Valid(line) {
			return false, fmt.Errorf("结果记录在字节 %d 处损坏: 非法 UTF-8", offset)
		}
		var result document.Result
		if err := json.Unmarshal(line, &result); err != nil {
			return false, fmt.Errorf("结果记录在字节 %d 处损坏: %w", offset, err)
		}
		if err := s.validateResult(result); err != nil {
			return false, err
		}
		offset += int64(len(line))
		visit(result)
		if last {
			return true, nil
		}
	}
}

func (s *Store) validateResult(result document.Result) error {
	index, ok := s.SegmentIndex(result.ID)
	validStatus := result.Status == "failed" || result.Status == "done" || result.Status == "pending"
	emptyTranslation := result.Status == "done" && result.Dst == "" && !result.Passthrough
	if !ok || s.Segments[index].Src != result.Src || !validStatus || emptyTranslation {
		return fmt.Errorf("结果记录 %q 与输入不一致或译文为空", result.ID)
	}
	if !utf8.ValidString(result.Dst) || !utf8.Valid(result.Usage) || !utf8.Valid(result.LegacyRecord) {
		return fmt.Errorf("结果记录 %q 包含非法 UTF-8", result.ID)
	}
	return nil
}

func (s *Store) Append(result document.Result) error {
	if !s.writable {
		return fmt.Errorf("只读作业不能写入结果")
	}
	// A failed write may leave a partial tail; another append would make it unrecoverable.
	if s.appendErr != nil {
		return fmt.Errorf("result writes disabled after previous failure: %w", s.appendErr)
	}
	if err := s.validateResult(result); err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if s.needsNewline {
		data = append([]byte{'\n'}, data...)
	}
	n, err := s.results.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		s.appendErr = fmt.Errorf("writing result %s: %w", result.ID, err)
		return s.appendErr
	}
	s.needsNewline = false
	if err = s.results.Sync(); err != nil {
		s.appendErr = fmt.Errorf("syncing result %s: %w", result.ID, err)
		return s.appendErr
	}
	s.trackResult(result)
	return s.publishCommit()
}

func (s *Store) publishCommit() error {
	if err := s.writeStageSnapshots(); err != nil {
		return fmt.Errorf("结果已保存，更新阶段文件失败: %w", err)
	}
	if s.AfterCommit != nil {
		if err := s.AfterCommit(s); err != nil {
			return fmt.Errorf("结果已保存，更新进度失败: %w", err)
		}
	}
	return nil
}

func (s *Store) trackResult(result document.Result) {
	s.indexAgentTasks(result)
	s.trackStage(result)
	if result.Status == "pending" {
		delete(s.Done, result.ID)
		delete(s.Failed, result.ID)
	} else if result.Status == "failed" {
		delete(s.Done, result.ID)
		s.Failed[result.ID] = result
	} else {
		delete(s.Failed, result.ID)
		s.Done[result.ID] = result
	}
}

func (s *Store) FindAgentTask(taskID, worker, segmentID string) (string, bool) {
	result, ok := s.agentTasks[agentTaskKey{taskID, worker}][segmentID]
	return result.hash, ok
}

func (s *Store) CheckAgentSubmission(taskID, worker, digest string) (bool, error) {
	results := s.agentTasks[agentTaskKey{taskID, worker}]
	incomplete := false
	for _, result := range results {
		if result.hash != digest {
			return false, fmt.Errorf("任务已经以不同结果提交")
		}
		incomplete = incomplete || result.size != len(results)
	}
	if incomplete {
		return false, fmt.Errorf("任务部分结果已保存，请重新领取剩余条目")
	}
	return len(results) > 0, nil
}

type agentTaskKey struct{ taskID, worker string }

type agentTaskResult struct {
	hash string
	size int
}

func (s *Store) indexAgentTasks(result document.Result) {
	for _, completion := range []*document.StageCompletion{result.PolishCompletion, result.ProofreadCompletion} {
		if completion == nil {
			continue
		}
		if s.agentTasks == nil {
			s.agentTasks = make(map[agentTaskKey]map[string]agentTaskResult)
		}
		key := agentTaskKey{completion.TaskID, completion.Worker}
		if s.agentTasks[key] == nil {
			s.agentTasks[key] = make(map[string]agentTaskResult)
		}
		s.agentTasks[key][result.ID] = agentTaskResult{completion.SubmissionHash, completion.TaskSize}
	}
}
