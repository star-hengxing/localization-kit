package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const maxChatResponseBytes int64 = 16 << 20

type limitedBody struct {
	io.Reader
	io.Closer
	readErr error
}

func (body *limitedBody) Read(p []byte) (int, error) {
	n, err := body.Reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		body.readErr = err
	}
	return n, err
}

type responseLimit struct {
	body *limitedBody
}

func (limit *responseLimit) apply(request *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	response, err := next(request)
	if response != nil && response.Body != nil {
		limit.body = &limitedBody{Reader: io.LimitReader(response.Body, maxChatResponseBytes+1), Closer: response.Body}
		response.Body = limit.body
	}
	return response, err
}

func (c *Client) Complete(ctx context.Context, input Request) (Response, error) {
	base := strings.TrimRight(c.model.URL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	limit := &responseLimit{}
	options := []option.RequestOption{
		option.WithBaseURL(base + "/"),
		option.WithHTTPClient(c.http),
		// The server may still be generating after the client loses the connection.
		option.WithMaxRetries(0),
		option.WithMiddleware(limit.apply),
	}
	service := openai.NewChatCompletionService(options...)
	params := openai.ChatCompletionNewParams{
		Model:       c.model.Model,
		Temperature: openai.Float(input.Temperature),
		TopP:        openai.Float(input.TopP),
		MaxTokens:   openai.Int(int64(input.MaxTokens)),
		Stop:        openai.ChatCompletionNewParamsStopUnion{OfStringArray: input.Stop},
	}
	requestOptions := []option.RequestOption{option.WithJSONSet("messages", input.Messages)}
	if input.TopK != nil {
		requestOptions = append(requestOptions, option.WithJSONSet("top_k", *input.TopK))
	}
	if input.ChatTemplateKwargs != nil {
		requestOptions = append(requestOptions, option.WithJSONSet("chat_template_kwargs", input.ChatTemplateKwargs))
	}
	var response *http.Response
	var body []byte
	callOptions := append(requestOptions, option.WithResponseInto(&response), option.WithResponseBodyInto(&body))
	_, err := service.New(ctx, params, callOptions...)
	if limit.body != nil && limit.body.readErr != nil {
		return Response{}, fmt.Errorf("读取模型响应: %w", limit.body.readErr)
	}
	if err != nil && response == nil {
		return Response{}, fmt.Errorf("请求模型服务 %s: %w", c.model.URL, err)
	}
	if response == nil {
		return Response{}, fmt.Errorf("模型服务没有返回响应")
	}
	var readErr error
	if response.StatusCode >= 400 && response.Body != nil {
		body, readErr = io.ReadAll(io.LimitReader(response.Body, maxChatResponseBytes+1))
		response.Body.Close()
	}
	if readErr != nil {
		return Response{}, fmt.Errorf("读取模型响应: %w", readErr)
	}
	if int64(len(body)) > maxChatResponseBytes {
		return Response{}, fmt.Errorf("模型响应超过大小限制（%d 字节）", maxChatResponseBytes)
	}
	if err != nil && response.StatusCode < 400 {
		return Response{}, fmt.Errorf("接收模型响应: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Error struct {
				Message string          `json:"message"`
				Code    json.RawMessage `json:"code"`
			} `json:"error"`
		}
		if (response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusRequestEntityTooLarge || response.StatusCode == http.StatusUnprocessableEntity) && json.Unmarshal(body, &payload) == nil {
			detail := strings.ToLower(string(payload.Error.Code) + " " + payload.Error.Message)
			if strings.Contains(detail, "context_length_exceeded") || strings.Contains(detail, "context window") || strings.Contains(detail, "context length") || strings.Contains(detail, "context size") || strings.Contains(detail, "prompt is too long") {
				return Response{}, &ContextLimitError{Status: response.StatusCode}
			}
		}
		return Response{}, fmt.Errorf("模型服务返回 HTTP %d", response.StatusCode)
	}
	var completion openai.ChatCompletion
	if err := json.Unmarshal(body, &completion); err != nil {
		return Response{}, fmt.Errorf("解析模型响应: %w", err)
	}
	if completion.Model == "" || completion.Model != c.model.Model {
		return Response{}, fmt.Errorf("服务返回模型 %q，期望 %q，请检查模型服务", completion.Model, c.model.Model)
	}
	if len(completion.Choices) == 0 || completion.Choices[0].FinishReason == "stop" && strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return Response{}, fmt.Errorf("模型响应没有文本内容")
	}
	result := Response{Model: completion.Model}
	for _, choice := range completion.Choices {
		result.Choices = append(result.Choices, struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		}{Message: Message{Role: string(choice.Message.Role), Content: choice.Message.Content}, FinishReason: choice.FinishReason})
	}
	if raw := completion.JSON.Usage.Raw(); raw != "" {
		result.Usage = json.RawMessage(raw)
	}
	if field, ok := completion.JSON.ExtraFields["timings"]; ok && field.Raw() != "" {
		result.Timings = json.RawMessage(field.Raw())
	}
	return result, nil
}
