package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localization-kit/src/internal/config"
)

func TestBudgetUsesPropertiesAndTokenization(t *testing.T) {
	propsCalls, tokenCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			propsCalls++
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":1024}}`))
		case "/tokenize":
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, 300)})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(config.Model{URL: server.URL + "/v1", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Messages: []Message{{Role: "system", Content: "system"}, {Role: "user", Content: "hello"}}}
	for i := 0; i < 2; i++ {
		result, err := client.Prepare(context.Background(), request)
		if err != nil || result.MaxTokens != 580 {
			t.Fatalf("request=%+v err=%v", result, err)
		}
	}
	if propsCalls != 1 || tokenCalls != 2 {
		t.Fatalf("props=%d tokenize=%d", propsCalls, tokenCalls)
	}
}

func TestBudgetLeavesUnknownContextToServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	client, err := New(config.Model{URL: server.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Messages: []Message{{Role: "user", Content: strings.Repeat("あ", 3000)}}, MaxTokens: 8192}
	result, err := client.Prepare(context.Background(), request)
	if err != nil || result.MaxTokens != 8192 {
		t.Fatalf("unknown context=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Prepare(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestBudgetRetriesTransientProbeAndKeepsRequestedLimit(t *testing.T) {
	propsCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			propsCalls++
			if propsCalls == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"n_ctx":8192}`))
		case "/tokenize":
			_, _ = w.Write([]byte(`{"tokens":[1,2,3]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := New(config.Model{URL: server.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Messages: []Message{{Role: "user", Content: "こんにちは"}}, MaxTokens: 4096}
	for i := 0; i < 2; i++ {
		result, err := client.Prepare(context.Background(), request)
		if err != nil || result.MaxTokens != 4096 {
			t.Fatalf("request=%+v err=%v", result, err)
		}
	}
	if propsCalls != 2 {
		t.Fatalf("temporary probe failure was cached: %d", propsCalls)
	}
}
