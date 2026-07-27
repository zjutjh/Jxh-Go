package ai

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxFinalAnswerRunes = 4000

var (
	codeFencePattern   = regexp.MustCompile("(?m)^[ \t]*(```|~~~)[^\n]*$")
	htmlTagPattern     = regexp.MustCompile(`(?is)</?(pre|code|div|span|p|br|table|tr|td|th|tbody|thead|ul|ol|li|strong|em|b|i|h[1-6])(\s[^>]*)?/?>`)
	listBulletPattern  = regexp.MustCompile(`(?m)^[ \t]*[-*+][ \t]+`)
	headingPattern     = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+`)
	blockquotePattern  = regexp.MustCompile(`(?m)^[ \t]*>[ \t]?`)
	yamlDocSepPattern  = regexp.MustCompile(`(?m)^[ \t]*---[ \t]*$`)
	tableDividerRegexp = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t]*:?-{2,}:?[ \t]*(\|[ \t]*:?-{2,}:?[ \t]*)+\|?[ \t]*$`)
	emphasisPattern    = regexp.MustCompile(`(\*\*|__|~~)`)
	inlineCodePattern  = regexp.MustCompile("`+")
	htmlEntityPattern  = regexp.MustCompile(`&(lt|gt|amp|quot|#39|apos);`)
	internalJSONField  = regexp.MustCompile(`(?i)"(?:matches|content|context|category|source_key|keyword|answer|path|tool_calls?|tool_call_id)"\s*:`)
)

func enforceFinalAnswer(answer string) string {
	sanitized := sanitizeOutput(answer)
	if strings.TrimSpace(sanitized) == "" {
		return ReviewFallbackAnswer
	}
	if utf8.RuneCountInString(sanitized) > maxFinalAnswerRunes {
		return ReviewFallbackAnswer
	}
	if hasInternalFieldLeak(sanitized) || looksLikeStructuredDump(sanitized) {
		return ReviewFallbackAnswer
	}
	return sanitized
}

// sanitizeOutput 把模型回答归一化为纯文本。它只移除标记语法本身，
// 从不丢弃正文内容，避免整段被吞造成空消息。
func sanitizeOutput(answer string) string {
	answer = strings.ReplaceAll(answer, "\r\n", "\n")
	answer = htmlEntityPattern.ReplaceAllStringFunc(answer, decodeHTMLEntity)
	answer = codeFencePattern.ReplaceAllString(answer, "")
	answer = htmlTagPattern.ReplaceAllString(answer, "")
	answer = tableDividerRegexp.ReplaceAllString(answer, "")
	answer = yamlDocSepPattern.ReplaceAllString(answer, "")
	answer = headingPattern.ReplaceAllString(answer, "")
	answer = blockquotePattern.ReplaceAllString(answer, "")
	answer = listBulletPattern.ReplaceAllString(answer, "")
	answer = emphasisPattern.ReplaceAllString(answer, "")
	answer = inlineCodePattern.ReplaceAllString(answer, "")
	return collapseBlankLines(answer)
}

func hasInternalFieldLeak(answer string) bool {
	lower := strings.ToLower(answer)
	markers := []string{
		"source_key",
		"sourcekey",
		"search_knowledge",
		"critical_security_instruction",
		"identity_and_style",
		"tool_usage_rules",
		"answer_constraints",
		"system prompt",
		"system_prompt",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return internalJSONField.MatchString(answer)
}

func decodeHTMLEntity(entity string) string {
	switch entity {
	case "&lt;":
		return "<"
	case "&gt;":
		return ">"
	case "&amp;":
		return "&"
	case "&quot;":
		return `"`
	case "&#39;", "&apos;":
		return "'"
	default:
		return entity
	}
}

func collapseBlankLines(value string) string {
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// looksLikeStructuredDump 判断回答是否为机器可解析的数据转储而非自然语言。
// 只在信号足够强时返回 true，避免误伤含少量符号的正常回答。
func looksLikeStructuredDump(answer string) bool {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return false
	}
	if isJSONEnvelope(trimmed) {
		return true
	}
	if hasToolFieldSignature(trimmed) {
		return true
	}
	return isDelimiterTable(trimmed)
}

// isJSONEnvelope 检测整体被 JSON/数组括号包裹且含有键值结构的回答。
func isJSONEnvelope(answer string) bool {
	first, _ := utf8.DecodeRuneInString(answer)
	last, _ := utf8.DecodeLastRuneInString(answer)
	wrapped := (first == '{' && last == '}') || (first == '[' && last == ']')
	if !wrapped {
		return false
	}
	return strings.Count(answer, `":`) >= 2 || strings.Count(answer, `" :`) >= 2
}

// hasToolFieldSignature 检测回答里出现了工具返回结构的字段名，
// 说明模型在照搬工具载荷而不是总结内容。
func hasToolFieldSignature(answer string) bool {
	lower := strings.ToLower(answer)
	signatures := []string{
		`"matches"`, `"content"`, `"context"`, `"category"`,
		`matches:`, `content:`, `context:`, `category:`,
		"source_key", `"keyword"`, `"answer"`, `"path"`,
	}
	hits := 0
	for _, signature := range signatures {
		if strings.Contains(lower, signature) {
			hits++
		}
	}
	return hits >= 2
}

// isDelimiterTable 检测竖线分隔表格。注意 sanitizeOutput 已经删掉了
// ---|--- 分隔行，所以剩下的每行可能只有一个竖线，阈值必须按每行 >= 1 判定，
// 并要求连续出现，避免误伤正文里偶尔出现的单个竖线。
func isDelimiterTable(answer string) bool {
	const minTableRows = 3
	streak := 0
	for _, line := range strings.Split(answer, "\n") {
		if strings.Contains(line, "|") {
			streak++
			if streak >= minTableRows {
				return true
			}
			continue
		}
		streak = 0
	}
	return false
}
