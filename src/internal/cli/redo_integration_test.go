package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"localization-kit/src/internal/store"
)

func TestRedoTranslationAndResume(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	root := initJSONLWorkspace(t, "{\"id\":\"1\",\"src\":\"こんにちは\"}\n")
	configureWorkspaceModel(t, root, "[models.test]\nurl='"+server.URL+"'\nmodel='test'\nprofile='hy-mt2'\n")
	ctx := context.Background()
	translate := []string{"translate", root, "--model", "test"}
	redo := []string{"redo", root, "--stage", "translate", "--id", "1"}
	if err := run(ctx, translate); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, redo); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, redo); err == nil {
		t.Fatal("already pending entry accepted")
	}
	if err := run(ctx, translate); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected two model calls, got %d", got)
	}
	current, err := store.OpenReadOnly(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if row := current.Done["1"]; row.Dst != "你好" {
		t.Fatalf("translation not restored: %+v", row)
	}
}
