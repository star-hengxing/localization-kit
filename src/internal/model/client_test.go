package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"localization-kit/src/internal/config"
)

func TestCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var request Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "test-model" || len(request.Messages) != 1 || request.Messages[0].Content != "こんにちは" || request.Temperature != 0.3 {
			t.Errorf("unexpected request: %+v", request)
		}
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client, err := New(config.Model{URL: server.URL + "/v1", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "こんにちは"}}, Temperature: 0.3})
	if err != nil || result.Choices[0].Message.Content != "你好" {
		t.Fatalf("result: %+v, error: %v", result, err)
	}
}

func TestCompletionErrors(t *testing.T) {
	for _, body := range []string{`not json`, `{"choices":[]}`, `{"choices":[{"message":{"content":""}}]}`, `{"choices":[{"message":{"content":"text"}}]}`, `{"model":"other","choices":[{"message":{"content":"text"}}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		client, err := New(config.Model{URL: server.URL, Model: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Complete(context.Background(), Request{}); err == nil {
			t.Errorf("accepted %s", body)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	client, err := New(config.Model{URL: server.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("accepted HTTP error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Complete(ctx, Request{}); err == nil {
		t.Fatal("accepted canceled request")
	}
}

func TestContextErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status       int
		body         string
		contextLimit bool
	}{
		{400, `{"error":{"code":400,"message":"request exceeds available context size"}}`, true},
		{400, `{"error":{"code":"context_length_exceeded"}}`, true},
		{400, `{"error":{"message":"bad request"}}`, false},
		{401, `{"error":{"message":"invalid context window credentials"}}`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		client, err := New(config.Model{URL: server.URL, Model: "test"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Complete(context.Background(), Request{})
		var contextErr *ContextLimitError
		if errors.As(err, &contextErr) != tc.contextLimit {
			t.Errorf("status=%d body=%s error=%v", tc.status, tc.body, err)
		}
		server.Close()
	}
}

func TestCompletionDistinguishesBrokenAndOversizedResponses(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "50")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "{")
			}))
			defer server.Close()
			client, err := New(config.Model{URL: server.URL, Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Complete(context.Background(), Request{}); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("expected a wrapped short read, got %v", err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", int(maxChatResponseBytes)+1))
	}))
	defer server.Close()
	client, err := New(config.Model{URL: server.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "超过大小限制") {
		t.Fatalf("expected a size limit error, got %v", err)
	}
}

func TestCompletionDoesNotRetryErrors(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantCalls int
	}{{503, 1}, {429, 1}, {401, 1}, {400, 1}} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(tc.status) }))
		client, err := New(config.Model{URL: server.URL, Model: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Complete(context.Background(), Request{}); err == nil || calls != tc.wantCalls {
			t.Errorf("status=%d calls=%d err=%v", tc.status, calls, err)
		}
		server.Close()
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer server.Close()
	client, err := New(config.Model{URL: server.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Complete(ctx, Request{}); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
