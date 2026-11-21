package jsonfile

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
	"unicode/utf8"
)

func Digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("计算 JSON 摘要: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func Read(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON 文件不是有效 UTF-8: %s", path)
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("解析 JSON 文件 %s: %w", path, err)
	}
	var extra json.RawMessage
	switch err := decoder.Decode(&extra); err {
	case io.EOF:
		return nil
	case nil:
		return fmt.Errorf("JSON 文件 %s 包含多余的值", path)
	default:
		return fmt.Errorf("解析 JSON 文件 %s 的尾随内容: %w", path, err)
	}
}

// Create writes directly to an exclusive new file.
func Create(path string, value any) error {
	data, err := encode(path, value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Close())
}

// Publish makes the completed file visible at an unused destination.
func Publish(path string, value any) error {
	data, err := encode(path, value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".json-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}

func encode(path string, value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("编码 JSON 文件 %s: %w", path, err)
	}
	return append(data, '\n'), nil
}
