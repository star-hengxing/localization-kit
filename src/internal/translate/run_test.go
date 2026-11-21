package translate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"localization-kit/src/internal/config"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/model"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate/rules"
)

func TestAbnormalCompletionIsNotSaved(t *testing.T) {
	for _, content := range []string{"你好", ""} {
		t.Run(content, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "test", "choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "content_filter"}}})
			}))
			defer server.Close()
			selected := config.Model{URL: server.URL, Model: "test", Profile: "hy-mt2"}
			client, err := model.New(selected)
			if err != nil {
				t.Fatal(err)
			}
			segments := []document.Segment{{ID: "1", Group: "1", Src: "こんにちは"}}
			job, err := store.OpenOrCreate(t.TempDir(), segments)
			if err != nil {
				t.Fatal(err)
			}
			defer job.Close()
			if _, err := Run(context.Background(), client, selected, segments, nil, job, false); err != nil || len(job.Done) != 0 || job.Failed["1"].Finish != "content_filter" {
				t.Fatalf("abnormal result not recorded as failure: done=%v failed=%v err=%v", job.Done, job.Failed, err)
			}
		})
	}
}

func TestRunAndResume(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls++
		var input model.Request
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input.Model != "test" || input.Messages[1].Content != "こんにちは" {
			t.Errorf("input=%+v", input)
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	selected := config.Model{URL: server.URL, Model: "test", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "♪"}}
	job, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	for i := 0; i < 2; i++ {
		result, err := Run(context.Background(), client, selected, segments, nil, job, false)
		if err != nil || result.Done != 2 || result.Failed != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	if calls != 1 || job.Done["1"].Dst != "你好" || !job.Done["2"].Passthrough {
		t.Fatalf("calls=%d done=%+v", calls, job.Done)
	}
}

func TestMisalignedBatchRetriesIndividually(t *testing.T) {
	calls := 0
	var secondFallback []model.Message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls++
		var input model.Request
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		result := "你好"
		current := input.Messages[len(input.Messages)-1].Content
		if strings.Contains(current, "\n") {
			result = "合并一行"
		} else if strings.Contains(current, "ありがとう") {
			result = "谢谢"
			secondFallback = input.Messages
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "test", "choices": []any{map[string]any{"message": map[string]string{"content": result}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	selected := config.Model{URL: server.URL, Model: "test", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "ありがとう"}}
	job, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	summary, err := Run(context.Background(), client, selected, segments, nil, job, false)
	if err != nil || summary.Done != 2 || calls != 5 || job.Done["2"].Dst != "谢谢" {
		t.Fatalf("summary=%+v calls=%d err=%v", summary, calls, err)
	}
	if len(secondFallback) != 4 || secondFallback[1].Content != "こんにちは" || secondFallback[2].Content != "你好" {
		t.Fatalf("second fallback history=%+v", secondFallback)
	}
}

func TestHySingleRequestUsesTwoPreviousTranslations(t *testing.T) {
	testSingleRequest(t, "hy-mt2", []string{"二", "译二", "三", "译三", "四"}, true)
}

func TestOpenSakuraSingleRequestOmitsHistory(t *testing.T) {
	testSingleRequest(t, "opensakura", []string{"输入(总行数: 1)：\n四"}, false)
}

func testSingleRequest(t *testing.T, profile string, wantMessages []string, usesHistory bool) {
	t.Helper()
	var received model.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	selected := config.Model{URL: server.URL, Model: "test", Profile: profile}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	segments := []document.Segment{{ID: "0", Group: "other", Src: "別"}, {ID: "1", Group: "g", Src: "一"}, {ID: "2", Group: "g", Src: "二"}, {ID: "3", Group: "g", Src: "三"}, {ID: "4", Group: "g", Src: "四"}}
	job, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	contexts := groupContexts(segments, nil)
	for _, segment := range segments[:4] {
		result := document.Result{ID: segment.ID, Src: segment.Src, Dst: "译" + segment.Src, Status: "done", Signature: signature(segment, selected, contexts[segment.Group])}
		if err := job.Append(result); err != nil {
			t.Fatal(err)
		}
	}
	if summary, err := Run(context.Background(), client, selected, segments, nil, job, false); err != nil || summary.Done != len(segments) || job.Done["4"].HistoryUsed != usesHistory {
		t.Fatalf("run single-line translation: %+v %v", summary, err)
	}
	var messages []string
	for _, message := range received.Messages {
		if message.Role != "system" {
			messages = append(messages, message.Content)
		}
	}
	if !slices.Equal(messages, wantMessages) {
		t.Fatalf("history=%+v", received.Messages)
	}
}

func TestRetryEarlierLineInvalidatesLaterHistoryResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好\n谢谢"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	selected := config.Model{URL: server.URL, Model: "test", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "ありがとう"}, {ID: "3", Group: "g", Src: "こんばんは"}}
	job, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	ctx := groupContexts(segments, nil)["g"]
	for i, segment := range segments {
		result := document.Result{ID: segment.ID, Src: segment.Src, Dst: "你好", Status: "done", Model: "test", Profile: "hy-mt2", Signature: signature(segment, selected, ctx), TermsHash: ctx.Terms, HistoryUsed: i > 0}
		if i == 0 {
			result.Status = "failed"
		}
		if i == 2 {
			result.Origin = "manual"
			result.Model = ""
			result.Profile = ""
		}
		if err := job.Append(result); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := Run(context.Background(), client, selected, segments, nil, job, true)
	if err != nil || summary.Done != 3 || summary.Review != 1 || !job.Done["3"].ContextReview {
		t.Fatalf("summary=%+v result=%+v err=%v", summary, job.Done["3"], err)
	}
	summary, err = Run(context.Background(), client, selected, segments, nil, job, false)
	if err != nil || summary.Review != 1 || !job.Done["3"].ContextReview {
		t.Fatalf("context review silently cleared: %+v %v", summary, err)
	}
}

func TestContextLimitFallsBackToIndividualLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var input model.Request
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		content := input.Messages[len(input.Messages)-1].Content
		if strings.Contains(content, "\n") || strings.Contains(content, "長い") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"context window exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	selected := config.Model{URL: server.URL, Model: "test", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "長い文章"}}
	job, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	summary, err := Run(context.Background(), client, selected, segments, nil, job, false)
	if err != nil || summary.Done != 1 || summary.Failed != 1 || job.Failed["2"].Violations[0].Code != "context_budget" {
		t.Fatalf("summary=%+v failed=%+v err=%v", summary, job.Failed, err)
	}
}

func TestModelChangeDoesNotReusePreviousResults(t *testing.T) {
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}}
	job, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if err := job.Append(document.Result{ID: "1", Src: "こんにちは", Dst: "你好", Status: "done", Model: "old", Profile: "hy-mt2"}); err != nil {
		t.Fatal(err)
	}
	selected := config.Model{URL: "http://localhost", Model: "new", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), client, selected, segments, nil, job, false); err == nil {
		t.Fatal("changed model accepted")
	}
}

func TestBatchDoesNotJoinAcrossPassthrough(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls++
		var input model.Request
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(input.Messages[len(input.Messages)-1].Content, "\n") {
			t.Error("non-adjacent lines were batched")
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	selected := config.Model{URL: server.URL, Model: "test", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "♪"}, {ID: "3", Group: "g", Src: "こんばんは"}}
	job, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	summary, err := Run(context.Background(), client, selected, segments, nil, job, false)
	if err != nil || summary.Done != 3 || calls != 2 {
		t.Fatalf("summary=%+v calls=%d err=%v", summary, calls, err)
	}
}

func TestSeedPreviousOnlyUnchangedCompatibleResults(t *testing.T) {
	selected := config.Model{Model: "test", Profile: "hy-mt2"}
	oldSegments := []document.Segment{{ID: "1", Group: "first", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "ありがとう"}, {ID: "3", Group: "g", Src: "さようなら"}}
	previous, err := store.OpenOrCreate(t.TempDir(), oldSegments)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	for _, segment := range oldSegments {
		if err := previous.Append(document.Result{ID: segment.ID, Src: segment.Src, Dst: "译文", Model: "test", Profile: "hy-mt2", Status: "done", Signature: signature(segment, selected, groupContexts(oldSegments, nil)[segment.Group])}); err != nil {
			t.Fatal(err)
		}
	}
	newSegments := []document.Segment{{ID: "1", Group: "first", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "変更"}, {ID: "3", Group: "other", Src: "さようなら"}, {ID: "4", Group: "g", Src: "新しい"}}
	current, err := store.OpenOrCreate(t.TempDir(), newSegments)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	count, err := SeedPrevious(previous, current, selected, nil)
	if err != nil || count != 1 || current.Done["1"].Dst != "译文" || len(current.Done) != 1 {
		t.Fatalf("count=%d done=%+v err=%v", count, current.Done, err)
	}
	count, err = SeedPrevious(previous, current, config.Model{Model: "other", Profile: "hy-mt2"}, nil)
	if err != nil || count != 0 {
		t.Fatalf("mismatched model reused results: %d %v", count, err)
	}
}

func TestSeedPreviousChangedGroupProtectsManualAndRetranslatesMachine(t *testing.T) {
	selected := config.Model{Model: "test", Profile: "hy-mt2"}
	oldSegments := []document.Segment{{ID: "1", Group: "g", Src: "姉が来た"}, {ID: "2", Group: "g", Src: "おはよう"}, {ID: "3", Group: "g", Src: "ありがとう"}}
	previous, err := store.OpenOrCreate(t.TempDir(), oldSegments)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	for _, segment := range oldSegments[1:] {
		result := document.Result{ID: segment.ID, Src: segment.Src, Dst: "译文", Model: "test", Profile: "hy-mt2", Status: "done", Signature: signature(segment, selected, groupContexts(oldSegments, nil)[segment.Group])}
		if segment.ID == "3" {
			result.Origin = "manual"
			result.Model = ""
			result.Profile = ""
		}
		if err := previous.Append(result); err != nil {
			t.Fatal(err)
		}
	}
	newSegments := []document.Segment{{ID: "1", Group: "g", Src: "兄が来た"}, oldSegments[1], oldSegments[2]}
	current, err := store.OpenOrCreate(t.TempDir(), newSegments)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	count, err := SeedPrevious(previous, current, selected, nil)
	if err != nil || count != 1 || len(current.Done) != 1 || !current.Done["3"].NeedsReview || current.Done["3"].ReviewSignature == "" {
		t.Fatalf("count=%d done=%+v err=%v", count, current.Done, err)
	}
}

func TestSeedPreviousUsesGroupTermsAndModelEndpoint(t *testing.T) {
	selected := config.Model{URL: "http://localhost", Model: "test", Profile: "hy-mt2"}
	segments := []document.Segment{{ID: "1", Group: "g", Src: "猫が来た"}, {ID: "2", Group: "g", Src: "おはよう"}, {ID: "3", Group: "g", Src: "ありがとう"}, {ID: "4", Group: "other", Src: "こんばんは"}}
	oldTerms := []rules.Entry{{Src: "猫", Dst: "猫", Kind: "term"}}
	newTerms := []rules.Entry{{Src: "猫", Dst: "猫咪", Kind: "term"}}
	previous, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	oldContexts := groupContexts(segments, oldTerms)
	for _, segment := range segments {
		record := document.Result{ID: segment.ID, Src: segment.Src, Dst: "译文", Model: "test", Profile: "hy-mt2", Status: "done", Signature: signature(segment, selected, oldContexts[segment.Group]), TermsHash: oldContexts[segment.Group].Terms}
		if segment.ID == "3" {
			record.Origin = "manual"
			record.Model = ""
			record.Profile = ""
		}
		if err := previous.Append(record); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	count, err := SeedPrevious(previous, current, selected, newTerms)
	if err != nil || count != 2 || len(current.Done) != 2 || !current.Done["3"].NeedsReview || current.Done["4"].Dst != "译文" {
		t.Fatalf("count=%d done=%+v err=%v", count, current.Done, err)
	}
	newEndpointJob, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer newEndpointJob.Close()
	selected.URL = "http://localhost:1234"
	count, err = SeedPrevious(previous, newEndpointJob, selected, oldTerms)
	if err != nil || count != 1 || !newEndpointJob.Done["3"].NeedsReview || len(newEndpointJob.Done) != 1 {
		t.Fatalf("new endpoint reused incompatible results: count=%d done=%+v err=%v", count, newEndpointJob.Done, err)
	}
}

func TestStaleResultBecomesPendingBeforeRequest(t *testing.T) {
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if fail {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	selected := config.Model{URL: server.URL, Model: "test", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}}
	dir := t.TempDir()
	job, err := store.OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), client, selected, segments, nil, job, false); err != nil {
		t.Fatal(err)
	}
	fail = true
	terms := []rules.Entry{{Src: "こんに", Dst: "你好", Kind: "term"}}
	summary, err := Run(context.Background(), client, selected, segments, terms, job, false)
	if err == nil || summary.Pending != 1 || len(job.Done) != 0 {
		t.Fatalf("stale result still done: %+v %+v %v", summary, job.Done, err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	job, err = store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if status(segments, job).Pending != 1 || len(job.Done) != 0 {
		t.Fatal("pending state not persisted")
	}
}

func TestGroupTermsIncludeSingleLineAndVariantMatches(t *testing.T) {
	segments := []document.Segment{{ID: "1", Group: "g", Src: "あ"}, {ID: "2", Group: "g", Src: "あの"}}
	term := rules.Entry{Src: "あ", Dst: "啊", Kind: "onomatopoeia"}
	if len(rules.Hits("あ", []rules.Entry{term})) == 0 || len(rules.Hits("あ\nあの", []rules.Entry{term})) != 0 {
		t.Fatal("fixture does not exercise single-line matching")
	}
	first := groupContexts(segments, []rules.Entry{term})["g"].Terms
	term.Dst = "呀"
	if first == groupContexts(segments, []rules.Entry{term})["g"].Terms {
		t.Fatal("changed single-line term not detected")
	}
	term = rules.Entry{Src: "猫", Dst: "猫", Kind: "term"}
	before := groupContexts(segments, []rules.Entry{term})["g"].Terms
	term.Variants = []string{"あの"}
	if before == groupContexts(segments, []rules.Entry{term})["g"].Terms {
		t.Fatal("newly matching variant not detected")
	}
}

func TestManualReviewTargetFollowsCurrentRules(t *testing.T) {
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}}
	selected := config.Model{URL: "http://127.0.0.1:1", Model: "test", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	a := []rules.Entry{{Src: "こんに", Dst: "你好", Kind: "term"}}
	b := []rules.Entry{{Src: "こんに", Dst: "您好", Kind: "term"}}
	c := []rules.Entry{{Src: "こんに", Dst: "哈喽", Kind: "term"}}
	contextA := groupContexts(segments, a)["g"]
	if err := job.Append(document.Result{ID: "1", Src: segments[0].Src, Dst: "你好", Status: "done", Origin: "manual", Signature: signature(segments[0], selected, contextA), TermsHash: contextA.Terms}); err != nil {
		t.Fatal(err)
	}
	if summary, err := Run(context.Background(), client, selected, segments, b, job, false); err != nil || summary.Review != 1 {
		t.Fatalf("B: %+v %v", summary, err)
	}
	if summary, err := Run(context.Background(), client, selected, segments, c, job, false); err != nil || summary.Review != 1 || job.Done["1"].ReviewTermsHash != groupContexts(segments, c)["g"].Terms {
		t.Fatalf("C: %+v %v", summary, err)
	}
	if summary, err := Run(context.Background(), client, selected, segments, a, job, false); err != nil || summary.Review != 0 || job.Done["1"].NeedsReview {
		t.Fatalf("A: %+v %v", summary, err)
	}
}

func TestSeedPreviousRejectsConflictWithProtectedResult(t *testing.T) {
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}}
	previous, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	if err := previous.Append(document.Result{ID: "1", Src: "こんにちは", Dst: "您好", Status: "done", Origin: "manual"}); err != nil {
		t.Fatal(err)
	}
	current, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err := current.Append(document.Result{ID: "1", Src: "こんにちは", Dst: "你好", Status: "done", Model: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedPrevious(previous, current, config.Model{Model: "test", Profile: "hy-mt2"}, nil); err == nil {
		t.Fatal("protected previous result was silently ignored")
	}
	if current.Done["1"].Dst != "你好" {
		t.Fatal("current result changed on conflict")
	}
}

func TestSeedPreviousPreservesCreditsAndRejectsConflict(t *testing.T) {
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}}
	previous, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	if err := previous.SetInitialCreditLines([]string{"校对: Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := previous.Append(document.Result{ID: "1", Src: "こんにちは", Dst: "您好", Status: "done", Origin: "manual"}); err != nil {
		t.Fatal(err)
	}
	selected := config.Model{Model: "test", Profile: "hy-mt2"}
	current, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if n, err := SeedPrevious(previous, current, selected, nil); err != nil || n != 1 {
		t.Fatalf("seed=%d err=%v", n, err)
	}
	credits, err := current.CreditLines()
	if err != nil || len(credits) != 1 || credits[0] != "校对: Alice" {
		t.Fatalf("credits=%v err=%v", credits, err)
	}
	conflicting, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer conflicting.Close()
	if err := conflicting.SetInitialCreditLines([]string{"校对: Bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedPrevious(previous, conflicting, selected, nil); err == nil || len(conflicting.Done) != 0 {
		t.Fatalf("conflicting credits accepted: %v", err)
	}
}

func TestSeedPreviousChecksHistoryBeforePersistence(t *testing.T) {
	segments := []document.Segment{{ID: "1", Group: "g", Src: "first"}, {ID: "2", Group: "g", Src: "second"}, {ID: "3", Group: "g", Src: "third"}}
	selected := config.Model{Model: "test", Profile: "hy-mt2"}
	previous, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	group := groupContexts(segments, nil)["g"]
	for i, segment := range segments {
		result := document.Result{ID: segment.ID, Src: segment.Src, Dst: "old", Status: "done", Model: "test", Profile: "hy-mt2", Signature: signature(segment, selected, group), HistoryUsed: i > 0}
		if i > 0 {
			result.HistoryHash = HistoryDigest(previous, segment)
		}
		if i == 2 {
			result.Origin = "manual"
		}
		if err := previous.Append(result); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	current, err := store.OpenOrCreate(dir, segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Append(document.Result{ID: "1", Src: "first", Dst: "new", Status: "done", Origin: "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedPrevious(previous, current, selected, nil); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	current, err = store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if _, ok := current.Done["2"]; ok {
		t.Fatal("stale machine result persisted as done")
	}
	if result := current.Done["3"]; !result.NeedsReview || !result.ContextReview {
		t.Fatalf("manual result not marked for review: %+v", result)
	}
}

func TestSeedPreviousPreservesOnlyReusedManualInitialModelWithLongHistory(t *testing.T) {
	oldSegments := []document.Segment{{ID: "1", Group: "g1", Src: "first"}, {ID: "2", Group: "g2", Src: "second"}}
	newSegments := []document.Segment{{ID: "1", Group: "g1", Src: "first"}, {ID: "2", Group: "g2", Src: "changed"}}
	previous, err := store.OpenOrCreate(t.TempDir(), oldSegments)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	selected := config.Model{Model: "model-a", Profile: "hy-mt2"}
	contexts := groupContexts(oldSegments, nil)
	first := document.Result{ID: "1", Src: "first", Dst: strings.Repeat("x", 16<<20), Status: "done", Model: "model-a", Profile: "hy-mt2", Signature: signature(oldSegments[0], selected, contexts["g1"])}
	manual := document.Result{ID: "1", Src: "first", Dst: "edited", Status: "done", Origin: "manual", PreviousHash: document.ResultHash(first), Signature: first.Signature}
	if err := previous.AppendBatch([]document.Result{
		first, manual, {ID: "2", Src: "second", Dst: "other", Status: "done", Model: "model-b"},
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.OpenOrCreate(t.TempDir(), newSegments)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if n, err := SeedPrevious(previous, current, selected, nil); err != nil || n != 1 {
		t.Fatalf("seed=%d err=%v", n, err)
	}
	if result := current.Done["1"]; result.Dst != "edited" || result.InitialModel != "model-a" || result.PreviousHash != manual.PreviousHash {
		t.Fatal("reuse lost the manual edit or its initial model")
	}
	history, err := current.HistoryFor(map[string]bool{"1": true})
	if err != nil || len(history) != 3 || history[0].Dst != first.Dst {
		t.Fatalf("reused history: count=%d err=%v", len(history), err)
	}
	credits, err := current.ExportCreditLines()
	if err != nil || len(credits) != 1 || credits[0] != "初翻: model-a" {
		t.Fatalf("credits=%v err=%v", credits, err)
	}
}

func TestSeedPreviousRejectsPartialCountedCredits(t *testing.T) {
	old := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "ありがとう"}}
	previous, err := store.OpenOrCreate(t.TempDir(), old)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	if err := previous.SetInitialCreditLines([]string{"润色: a (1 行) / b (1 行)"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range old {
		if err := previous.Append(document.Result{ID: s.ID, Src: s.Src, Dst: "译文", Status: "done", Origin: "legacy"}); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.OpenOrCreate(t.TempDir(), []document.Segment{old[0]})
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if _, err := SeedPrevious(previous, current, config.Model{Model: "test", Profile: "hy-mt2"}, nil); err == nil || len(current.Done) != 0 {
		t.Fatalf("partial counted credits accepted: %v", err)
	}
}

func TestSeedPreviousRetranslatesChangedPriorHistory(t *testing.T) {
	calls := 0
	var received model.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls++
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"谢谢"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	selected := config.Model{URL: server.URL, Model: "test", Profile: "hy-mt2"}
	client, err := model.New(selected)
	if err != nil {
		t.Fatal(err)
	}
	segments := []document.Segment{{ID: "1", Group: "g", Src: "こんにちは"}, {ID: "2", Group: "g", Src: "ありがとう"}}
	previous, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	group := groupContexts(segments, nil)["g"]
	first := document.Result{ID: "1", Src: segments[0].Src, Dst: "你好", Status: "done", Model: "test", Profile: "hy-mt2", Signature: signature(segments[0], selected, group), TermsHash: group.Terms}
	if err := previous.Append(first); err != nil {
		t.Fatal(err)
	}
	second := document.Result{ID: "2", Src: segments[1].Src, Dst: "多谢", Status: "done", Model: "test", Profile: "hy-mt2", Signature: signature(segments[1], selected, group), TermsHash: group.Terms, HistoryUsed: true, HistoryHash: HistoryDigest(previous, segments[1])}
	if err := previous.Append(second); err != nil {
		t.Fatal(err)
	}
	current, err := store.OpenOrCreate(t.TempDir(), segments)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	first.Dst, first.Origin, first.Model, first.Profile = "您好", "manual", "", ""
	if err := current.Append(first); err != nil {
		t.Fatal(err)
	}
	if n, err := SeedPrevious(previous, current, selected, nil); err != nil || n != 0 {
		t.Fatalf("seed=%d err=%v", n, err)
	}
	if _, ok := current.Done["2"]; ok {
		t.Fatal("stale history-dependent result was seeded")
	}
	summary, err := Run(context.Background(), client, selected, segments, nil, current, false)
	if err != nil || summary.Done != 2 || calls != 1 || current.Done["2"].Dst != "谢谢" {
		t.Fatalf("summary=%+v calls=%d err=%v", summary, calls, err)
	}
	if len(received.Messages) != 4 || received.Messages[2].Content != "您好" {
		t.Fatalf("new history missing: %+v", received.Messages)
	}
}
