package profile

import (
	"strings"
	"testing"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/translate/rules"
)

func TestQualityAndProfile(t *testing.T) {
	entries := []rules.Entry{{Src: "お茶", Dst: "茶", Kind: "term", Constraint: "hard"}}
	request, err := Request("hy-mt2", []document.Segment{{Src: "お茶を飲みます"}}, entries, 0)
	if err != nil || !strings.Contains(request.Messages[0].Content, "お茶 = 茶") || request.Temperature != 0.3 {
		t.Fatalf("request=%+v error=%v", request, err)
	}
	blocking, warnings := rules.Check("お茶を飲みます", "喝水", entries)
	if len(blocking) != 0 || len(warnings) != 1 || warnings[0].Code != "term" {
		t.Fatalf("blocking=%+v warnings=%+v", blocking, warnings)
	}
	blocking, _ = rules.Check("こんにちは", "こんにちは", nil)
	if len(blocking) == 0 || blocking[0].Code != "untranslated" {
		t.Fatalf("blocking=%+v", blocking)
	}
	if got := rules.Simplify("我來喝熱茶", nil); got != "我来喝热茶" {
		t.Fatalf("simplify=%q", got)
	}
	if _, err := Parse("hy-mt2", "一行", 2); err == nil {
		t.Fatal("line mismatch accepted")
	}
}

func TestHyOutputCleanup(t *testing.T) {
	lines, err := Parse("hy-mt2", "<think>想一想</think>你好\n\n我来泡茶吧ね<eos:6124c78e>", 2)
	if err != nil || len(lines) != 2 || lines[0] != "你好" || lines[1] != "我来泡茶吧ね" {
		t.Fatalf("lines=%v err=%v", lines, err)
	}
	lines, err = Parse("hy-mt2", "嗯っ", 1)
	if err != nil || len(lines) != 1 || lines[0] != "嗯っ" {
		t.Fatalf("short output=%v err=%v", lines, err)
	}
	if _, err := Parse("opensakura", "第一行\n第二行", 1); err == nil {
		t.Fatal("opensakura accepted extra line")
	}
	if _, err := Parse("hy-mt2", "第一行\n第二行", 1); err == nil {
		t.Fatal("hy-mt2 accepted extra line")
	}
	if _, err := Parse("hy-mt2", "你好\nお茶 = 茶", 1); err == nil {
		t.Fatal("hy-mt2 silently removed a line")
	}
	lines, err = Parse("hy-mt2", "甲 = 乙", 1)
	if err != nil || len(lines) != 1 || lines[0] != "甲 = 乙" {
		t.Fatalf("equation lost: %v err=%v", lines, err)
	}
}
