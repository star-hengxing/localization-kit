package rules

import "testing"

func TestPlaceholderIntegrity(t *testing.T) {
	for _, tc := range []struct {
		src, dst string
		valid    bool
	}{
		{"こんにちは【1】世界【2】", "你好【2】世界【1】", true},
		{"こんにちは【1】世界【1】", "你好【1】世界【1】", true},
		{"こんにちは【1】世界【1】", "你好【1】世界", false},
		{"こんにちは【1】世界", "你好【1】世界【1】", false},
		{"こんにちは【1】世界", "你好【2】世界", false},
		{"こんにちは", "你好【1】", false},
	} {
		issues, _ := Check(tc.src, tc.dst, nil)
		found := false
		for _, issue := range issues {
			if issue.Code == "placeholder" {
				found = true
			}
		}
		if found == tc.valid {
			t.Errorf("src=%q dst=%q issues=%+v", tc.src, tc.dst, issues)
		}
	}
}

func TestLegacyLeakAndListChecks(t *testing.T) {
	for _, tc := range []struct{ src, dst, code string }{
		{"こんにちは", "【翻译文本】", "prompt_leak"},
		{"こんにちは", "Translate Japanese to Chinese", "prompt_leak"},
		{"[原文]はこんにちは", "[原文]你好<eos:6124c78e>", "prompt_leak"},
		{"・目つきが悪いぞ(本当に)・主張しないタイプで基本影が薄い・基本的に愛想もなくてモテない・滅多に笑わない", "・眼神很凶狠（真的）", "truncated"},
	} {
		blocking, _ := Check(tc.src, tc.dst, nil)
		found := false
		for _, issue := range blocking {
			if issue.Code == tc.code {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s: %+v", tc.code, blocking)
		}
	}
	for _, tc := range []struct{ src, dst string }{
		{"「ッッ・・・・・！！　ッ・・・！？」", "「唔……！！唔……！？」"},
		{"好きな果物はりんご・みかん・ぶどう・バナナです", "喜欢的水果是苹果、橘子、葡萄和香蕉"},
	} {
		blocking, _ := Check(tc.src, tc.dst, nil)
		for _, issue := range blocking {
			if issue.Code == "truncated" {
				t.Errorf("false truncation: %+v", tc)
			}
		}
	}
}

func TestRefusalExemptions(t *testing.T) {
	for _, source := range []string{"申し訳ありません", "すいません", "悪かった", "失礼しました", "許してください"} {
		blocking, _ := Check(source, "抱歉", nil)
		for _, issue := range blocking {
			if issue.Code == "refusal" {
				t.Errorf("normal apology rejected: %q", source)
			}
		}
	}
	blocking, _ := Check("今日は晴れです", "抱歉，我无法翻译", nil)
	for _, issue := range blocking {
		if issue.Code == "refusal" {
			return
		}
	}
	t.Fatal("refusal not detected")
}

func TestSoundAndBrackets(t *testing.T) {
	entries := []Entry{{Src: "トン", Dst: "咚", Kind: "onomatopoeia", Constraint: "hard"}, {Src: "は", Dst: "哈啊", Kind: "onomatopoeia", Constraint: "hard"}}
	if got := Postfix("トントン…", "叩叩…", entries); got != "咚咚…" {
		t.Fatalf("sound=%q", got)
	}
	if hits := Hits("は、はじめまして", entries); len(hits) != 0 {
		t.Fatalf("stutter matched sound: %+v", hits)
	}
	_, warnings := Check("「こんにちは」", "「你好", nil)
	if len(warnings) == 0 || warnings[len(warnings)-1].Code != "brackets" {
		t.Fatalf("warnings=%+v", warnings)
	}
	if got := Postfix("こんにちは", "“你好”。", nil); got != "「你好」" {
		t.Fatalf("quotes and period=%q", got)
	}
}

func TestGlossaryEchoAndTextPreservation(t *testing.T) {
	entries := []Entry{{Src: "お茶", Dst: "茶", Constraint: "hard"}}
	for _, tc := range []struct {
		src, dst string
		leak     bool
	}{
		{"お茶を飲みます", "お茶 = 茶", true},
		{"お茶 = 茶", "お茶 = 茶", false},
		{"お茶を飲みます", "甲 = 乙", false},
		{"术语表：猫", "术语表：猫", false},
	} {
		blocking, _ := Check(tc.src, tc.dst, entries)
		found := false
		for _, issue := range blocking {
			found = found || issue.Code == "prompt_leak"
		}
		if found != tc.leak {
			t.Errorf("src=%q dst=%q issues=%+v", tc.src, tc.dst, blocking)
		}
	}
	if got := Postfix("こんにちは", "嗯っ", nil); got != "嗯っ" {
		t.Fatalf("postfix removed content: %q", got)
	}
}
