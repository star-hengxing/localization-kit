package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const contextMargin = 128
const minimumOutputTokens = 256
const defaultOutputTokens = 2048

type probeStatusError struct{ Status int }

func (e *probeStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

func unsupportedProbe(err error) bool {
	var status *probeStatusError
	return errors.As(err, &status) && (status.Status == http.StatusNotFound || status.Status == http.StatusMethodNotAllowed || status.Status == http.StatusNotImplemented)
}

func (c *Client) baseURL() string {
	return strings.TrimSuffix(strings.TrimRight(c.model.URL, "/"), "/v1")
}

func (c *Client) probeJSON(ctx context.Context, method, path string, payload any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return fmt.Errorf("读取模型探测响应: %w", err)
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("模型探测响应超过大小限制（4 MiB）")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &probeStatusError{Status: response.StatusCode}
	}
	return json.Unmarshal(data, result)
}

func (c *Client) contextLimit(ctx context.Context) int {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if c.contextProbed {
		return c.contextTokens
	}
	limit := 0
	var properties struct {
		Context  int `json:"n_ctx"`
		Settings struct {
			Context int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	err := c.probeJSON(ctx, http.MethodGet, "/props", nil, &properties)
	if err == nil {
		if properties.Settings.Context > 0 {
			limit = properties.Settings.Context
		} else if properties.Context > 0 {
			limit = properties.Context
		}
	}
	if ctx.Err() == nil && (err == nil || unsupportedProbe(err)) {
		c.contextProbed, c.contextTokens = true, limit
	} else if ctx.Err() == nil && err != nil {
		slog.Warn("模型上下文探测失败，使用服务端检查", "error", err)
	}
	return limit
}

func (c *Client) promptTokens(ctx context.Context, messages []Message) int {
	var parts []string
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	content := strings.Join(parts, "\n")
	count := int(float64(utf8.RuneCountInString(content)) * 1.3)
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if !c.tokenizeUnavailable {
		var response struct {
			Tokens []json.RawMessage `json:"tokens"`
		}
		err := c.probeJSON(ctx, http.MethodPost, "/tokenize", map[string]string{"content": content}, &response)
		if err == nil && response.Tokens != nil {
			count = len(response.Tokens)
		} else if ctx.Err() == nil && unsupportedProbe(err) {
			c.tokenizeUnavailable = true
		} else if ctx.Err() == nil && err != nil {
			slog.Warn("模型分词探测失败，使用估算", "error", err)
		}
	}
	return count + 8*len(messages)
}

// Prepare limits generation to the available per-request context budget.
func (c *Client) Prepare(ctx context.Context, request Request) (Request, error) {
	if request.MaxTokens <= 0 {
		request.MaxTokens = defaultOutputTokens
	}
	limit := c.contextLimit(ctx)
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	if limit == 0 {
		return request, nil
	}
	used := c.promptTokens(ctx, request.Messages)
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	available := limit - used - contextMargin
	minimum := min(minimumOutputTokens, request.MaxTokens)
	if available < minimum {
		return Request{}, &ContextLimitError{}
	}
	if request.MaxTokens > available {
		request.MaxTokens = available
	}
	return request, nil
}
