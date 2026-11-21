package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"localization-kit/src/internal/document"
)

const batchJournal = "pending-results.json"

type batchRecord struct {
	Version    int    `json:"version"`
	Offset     int64  `json:"offset"`
	PrefixHash string `json:"prefix_hash"`
	DataHash   string `json:"data_hash"`
	Data       []byte `json:"data"`
}

func filePrefixHash(f *os.File, length int64) (string, error) {
	h := sha256.New()
	if _, err := io.CopyN(h, io.NewSectionReader(f, 0, length), length); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func batchDataHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// AppendBatch commits a group of results as one recoverable transaction.
// An error after journaling may leave a batch for the next writable open to
// recover, or committed results whose stage snapshots still need refreshing.
func (s *Store) AppendBatch(results []document.Result) error {
	if !s.writable {
		return fmt.Errorf("只读作业不能写入批次")
	}
	if len(results) == 0 {
		return nil
	}
	if s.appendErr != nil {
		return fmt.Errorf("result writes disabled after previous failure: %w", s.appendErr)
	}
	var data bytes.Buffer
	if s.needsNewline {
		data.WriteByte('\n')
	}
	encoder := json.NewEncoder(&data)
	for _, result := range results {
		if err := s.validateResult(result); err != nil {
			return err
		}
		if err := encoder.Encode(result); err != nil {
			return err
		}
	}
	info, err := s.results.Stat()
	if err != nil {
		return err
	}
	prefix, err := filePrefixHash(s.results, info.Size())
	if err != nil {
		return err
	}
	journal := batchRecord{Version: 1, Offset: info.Size(), PrefixHash: prefix, DataHash: batchDataHash(data.Bytes()), Data: data.Bytes()}
	if err := s.writeBatchJournal(journal); err != nil {
		s.appendErr = fmt.Errorf("saving batch journal: %w", err)
		return s.appendErr
	}
	if err := s.recoverBatch(true); err != nil {
		s.appendErr = fmt.Errorf("recovering committed batch: %w", err)
		return s.appendErr
	}
	for _, result := range results {
		s.trackResult(result)
	}
	s.needsNewline = false
	return s.publishCommit()
}

func (s *Store) writeBatchJournal(value batchRecord) error {
	path := filepath.Join(s.dir, batchJournal)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("存在尚未恢复的批次记录")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".batch-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(data); err != nil {
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
	return os.Rename(name, path)
}

func (s *Store) recoverBatch(writable bool) error {
	path := filepath.Join(s.dir, batchJournal)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("批次恢复记录不是普通文件: %s", path)
	}
	if !writable {
		return fmt.Errorf("作业存在待恢复的批次，请先通过可写命令恢复")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var journal batchRecord
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("解析批次恢复记录 %s: %w", path, err)
	}
	if journal.Version != 1 {
		return fmt.Errorf("批次恢复记录 %s 的版本 %d 不支持", path, journal.Version)
	}
	if journal.Offset < 0 || len(journal.Data) == 0 || journal.DataHash != batchDataHash(journal.Data) {
		return fmt.Errorf("批次恢复记录损坏: %s", path)
	}
	logInfo, err := s.results.Stat()
	if err != nil {
		return err
	}
	size := logInfo.Size()
	if size < journal.Offset || size-journal.Offset > int64(len(journal.Data)) {
		return fmt.Errorf("结果文件与待恢复批次不一致")
	}
	prefix, err := filePrefixHash(s.results, journal.Offset)
	if err != nil {
		return fmt.Errorf("读取结果文件 %s 的前缀: %w", s.results.Name(), err)
	}
	if prefix != journal.PrefixHash {
		return fmt.Errorf("结果文件前缀与待恢复批次不一致")
	}
	tail := make([]byte, size-journal.Offset)
	if _, err := io.ReadFull(io.NewSectionReader(s.results, journal.Offset, int64(len(tail))), tail); err != nil {
		return fmt.Errorf("读取结果文件 %s 的尾部: %w", s.results.Name(), err)
	}
	if !bytes.Equal(tail, journal.Data[:len(tail)]) {
		return fmt.Errorf("结果文件尾部与待恢复批次不一致")
	}
	remaining := journal.Data[len(tail):]
	if len(remaining) > 0 {
		n, err := s.results.WriteAt(remaining, size)
		if err != nil {
			return err
		}
		if n != len(remaining) {
			return io.ErrShortWrite
		}
	}
	if err := s.results.Sync(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("批次结果已保存，但恢复记录无法删除: %w", err)
	}
	_, err = s.results.Seek(0, io.SeekEnd)
	return err
}
