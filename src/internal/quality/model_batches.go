package quality

import (
	"context"
	"errors"
	"fmt"
	"localization-kit/src/internal/config"
	"localization-kit/src/internal/jsonfile"
	"os"
	"path/filepath"
	"strings"

	"localization-kit/src/internal/model"
	"localization-kit/src/internal/store"
)

type Batch[T any] struct {
	Request  model.Request
	Validate func([]T) error
}

func RunBatches[T any](ctx context.Context, dir string, retry bool, client *model.Client, manifest Manifest, batches []Batch[T], verifyInput func() error, parse func(string) ([]T, error)) ([][]T, error) {
	if len(batches) != len(manifest.Batches) {
		return nil, fmt.Errorf("批次清单不匹配")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, ".lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile.Close(); err != nil {
		return nil, err
	}
	unlock, err := store.LockExistingFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("批次目录正在使用: %w", err)
	}
	defer unlock()
	manifestPath := filepath.Join(dir, "manifest.json")
	var existing Manifest
	if err := jsonfile.Read(manifestPath, &existing); os.IsNotExist(err) {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return nil, readErr
		}
		if len(entries) != 1 || entries[0].Name() != ".lock" {
			return nil, fmt.Errorf("批次目录非空且没有有效清单")
		}
		if err := jsonfile.Publish(manifestPath, manifest); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		same, err := sameJSON(existing, manifest)
		if err != nil {
			return nil, err
		}
		if !same {
			return nil, fmt.Errorf("批次目录对应不同材料或模型，请使用新目录")
		}
	}
	var all [][]T
	for i, batch := range batches {
		if err := verifyInput(); err != nil {
			return nil, err
		}
		prefix := fmt.Sprintf("%04d", i+1)
		resultPath := filepath.Join(dir, prefix+".json")
		var result reviewRunResult[T]
		err := jsonfile.Read(resultPath, &result)
		if err == nil {
			resultHash, err := reviewRunResultHash(result)
			if err != nil {
				return nil, fmt.Errorf("计算批次 %s 摘要: %w", prefix, err)
			}
			if result.BatchHash != manifest.Batches[i] || result.Hash == "" || result.Hash != resultHash {
				return nil, fmt.Errorf("批次 %s 摘要不匹配", prefix)
			}
			if err := batch.Validate(result.Items); err != nil {
				return nil, fmt.Errorf("已保存批次 %s 无效或材料已过时: %w", prefix, err)
			}
			all = append(all, result.Items)
			continue
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		previousAttempt := false
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), prefix+"-attempt-") && strings.HasSuffix(entry.Name(), ".json") {
				previousAttempt = true
				break
			}
		}
		if previousAttempt && !retry {
			return nil, fmt.Errorf("批次 %s 有未完成的尝试，检查记录后使用 --retry-failed", prefix)
		}
		request, err := client.Prepare(ctx, batch.Request)
		if err != nil {
			return nil, fmt.Errorf("批次 %s 的上下文预算不足或探测失败: %w", prefix, err)
		}
		attempt := reviewRunAttempt{BatchHash: manifest.Batches[i], MaxTokens: request.MaxTokens}
		attemptNumber, err := appendReviewAttempt(dir, prefix, attempt)
		if err != nil {
			return nil, fmt.Errorf("记录批次 %s 的请求失败: %w", prefix, err)
		}
		response, callErr := client.Complete(ctx, request)
		if callErr == nil {
			attempt.Response = response.Choices[0].Message.Content
			if response.Choices[0].FinishReason != "stop" {
				callErr = fmt.Errorf("模型输出未正常结束: %s；可缩小材料范围并使用新运行目录", response.Choices[0].FinishReason)
			}
		}
		var items []T
		if callErr == nil {
			items, callErr = parse(attempt.Response)
		}
		if callErr == nil {
			callErr = verifyInput()
		}
		if callErr == nil {
			callErr = batch.Validate(items)
		}
		if callErr != nil {
			attempt.Error = callErr.Error()
		}
		responsePath := filepath.Join(dir, fmt.Sprintf("%s-response-%04d.json", prefix, attemptNumber))
		if err := jsonfile.Publish(responsePath, attempt); err != nil {
			return nil, fmt.Errorf("保存批次 %s 的模型响应失败: %w", prefix, err)
		}
		if callErr != nil {
			return nil, fmt.Errorf("批次 %s 执行失败: %w", prefix, callErr)
		}
		result = reviewRunResult[T]{BatchHash: manifest.Batches[i], Items: items}
		result.Hash, err = reviewRunResultHash(result)
		if err != nil {
			return nil, fmt.Errorf("计算批次 %s 摘要: %w", prefix, err)
		}
		if err := jsonfile.Publish(resultPath, result); err != nil {
			return nil, fmt.Errorf("保存批次 %s 失败: %w", prefix, err)
		}
		all = append(all, result.Items)
	}
	return all, nil
}

type Manifest struct {
	Version         int                `json:"version"`
	MaterialHash    string             `json:"material_hash"`
	Model           config.ModelRecord `json:"model"`
	Prompt          string             `json:"prompt_version"`
	MaxOutputTokens int                `json:"max_output_tokens"`
	Batches         []string           `json:"batches"`
}

type reviewRunResult[T any] struct {
	BatchHash string `json:"batch_hash"`
	Items     []T    `json:"items"`
	Hash      string `json:"hash"`
}

func reviewRunResultHash[T any](result reviewRunResult[T]) (string, error) {
	result.Hash = ""
	return jsonfile.Digest(result)
}

type reviewRunAttempt struct {
	BatchHash string `json:"batch_hash"`
	MaxTokens int    `json:"max_tokens"`
	Response  string `json:"response,omitempty"`
	Error     string `json:"error,omitempty"`
}

func appendReviewAttempt(dir, prefix string, attempt reviewRunAttempt) (int, error) {
	for i := 1; ; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%s-attempt-%04d.json", prefix, i))
		err := jsonfile.Publish(path, attempt)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return i, err
	}
}
