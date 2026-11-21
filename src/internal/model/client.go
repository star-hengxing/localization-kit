package model

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"localization-kit/src/internal/config"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	Model              string         `json:"model"`
	Messages           []Message      `json:"messages"`
	Temperature        float64        `json:"temperature"`
	TopP               float64        `json:"top_p"`
	TopK               *int           `json:"top_k,omitempty"`
	MaxTokens          int            `json:"max_tokens"`
	Stop               []string       `json:"stop,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type Response struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	Timings json.RawMessage `json:"timings,omitempty"`
}

type Client struct {
	model               config.Model
	http                *http.Client
	probeMu             sync.Mutex
	contextProbed       bool
	contextTokens       int
	tokenizeUnavailable bool
}

type ContextLimitError struct {
	Status int
}

func (e *ContextLimitError) Error() string {
	if e.Status == 0 {
		return "请求超过模型上下文预算"
	}
	return fmt.Sprintf("模型上下文超限（HTTP %d）", e.Status)
}

func New(cfg config.Model) (*Client, error) {
	return &Client{model: cfg, http: &http.Client{Timeout: 300 * time.Second}}, nil
}
