package profile

import (
	"fmt"
	"regexp"
	"strings"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/model"
	"localization-kit/src/internal/translate/rules"
)

const (
	hyMT2      = "hy-mt2"
	openSakura = "opensakura"
)

type BatchPolicy struct {
	MaxLines             int
	HistoryForSingleLine bool
}

func BatchPolicyFor(name string) (BatchPolicy, bool) {
	switch name {
	case hyMT2:
		return BatchPolicy{MaxLines: 3, HistoryForSingleLine: true}, true
	case openSakura:
		return BatchPolicy{MaxLines: 10}, true
	default:
		return BatchPolicy{}, false
	}
}

const sakuraSystem = "你是专业的日文轻小说中文译者。请将输入的日文原文翻译为流畅、自然、具有轻小说/二次元语感的简体中文。\n\n要求：\n1. 严格保持行数一一对应：输入多少行，输出多少行，不合并、不拆分、不遗漏。\n2. 根据上下文自然补全日文省略的主语，并保持角色口吻、性格、性别感和对话关系。\n3. 保留原文的特殊格式，包括行首全角空格、转义符、特殊符号等。\n4. Ruby/注音格式如“漢字（よみ）”若承载双关、设定、特殊称呼或魔法名，应在译文中保留“汉字（注音）”结构；普通读音标注则直接翻译含义。\n5. 日文标点转换为中文标点，如「」转为“”，、转为，。\n6. 忠实翻译原文的语气、粗俗度、暴力感、暧昧感和文学风格，不擅自删减、净化、美化或解释。\n\n只输出译文纯文本，不输出说明、分析、JSON、开场白或结束语。"

func Request(profile string, segments []document.Segment, entries []rules.Entry, rung int) (model.Request, error) {
	lines := make([]string, 0, len(segments))
	for _, segment := range segments {
		lines = append(lines, segment.Src)
	}
	joined := strings.Join(lines, "\n")
	hits := rules.Hits(joined, entries)
	temp := 0.3
	if rung > 0 {
		temp = 0.7
	}
	r := model.Request{Temperature: temp, MaxTokens: 2048}
	switch profile {
	case hyMT2:
		system := "Translate Japanese to Chinese."
		if len(hits) > 0 {
			system += "\nGlossary:\n"
			for _, term := range hits {
				system += term.Form + " = " + term.Entry.Dst + "\n"
			}
		}
		r.Messages = []model.Message{{Role: "system", Content: system}, {Role: "user", Content: joined}}
		r.TopP = 0.6
		r.Stop = []string{"<eos:6124c78e>", "<｜hy_User｜>", "<｜hy_Assistant｜>"}
	case openSakura:
		input := ""
		if len(hits) > 0 {
			input = "术语表 <术语原文> -> <术语译文> #<术语信息>:\n"
			for _, term := range hits {
				input += term.Form + " -> " + term.Entry.Dst
				if term.Entry.Note != "" {
					note := strings.FieldsFunc(strings.TrimSpace(term.Entry.Note), func(r rune) bool { return strings.ContainsRune("，。；;：:（(\n", r) })
					if len(note) > 0 {
						part := []rune(strings.TrimSpace(note[0]))
						if len(part) > 16 {
							part = part[:16]
						}
						input += " #" + string(part)
					}
				}
				input += "\n"
			}
		}
		input += fmt.Sprintf("输入(总行数: %d)：\n%s", len(lines), joined)
		r.Messages = []model.Message{{Role: "system", Content: sakuraSystem}, {Role: "user", Content: input}}
		r.TopP = 0.8
		topK := 20
		r.TopK = &topK
		r.Stop = []string{"<|im_end|>"}
		r.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	default:
		return model.Request{}, fmt.Errorf("不支持模型 profile %q", profile)
	}
	return r, nil
}

var thoughts = []*regexp.Regexp{
	regexp.MustCompile(`(?s)<think>.*?</think>`),
	regexp.MustCompile(`(?s)<thinking>.*?</thinking>`),
	regexp.MustCompile(`(?s)<why>.*?</why>`),
	regexp.MustCompile(`(?s)<reason>.*?</reason>`),
}

func Parse(profile, content string, expected int) ([]string, error) {
	for _, pattern := range thoughts {
		content = pattern.ReplaceAllString(content, "")
	}
	if profile == openSakura && strings.Contains(content, "</think>") {
		content = content[strings.LastIndex(content, "</think>")+len("</think>"):]
	}
	for _, stop := range []string{"<eos:6124c78e>", "<｜hy_User｜>", "<｜hy_Assistant｜>", "<|im_end|>"} {
		content = strings.ReplaceAll(content, stop, "")
	}
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != expected {
		return nil, fmt.Errorf("原文 %d 行，模型返回 %d 行", expected, len(lines))
	}
	return lines, nil
}
