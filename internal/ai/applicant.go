package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const applicantPrompt = `你负责从 QQ 群申请验证答案中提取学生资料的姓名、学号和专业等信息。
只输出一个 JSON 对象，字段固定为 student_id、student_name、major。
字段值必须逐字来自原文；无法确认时使用空字符串。不得推断、补全或改写专业名称。
不要输出 Markdown、解释或额外字段。`

type ApplicantFields struct {
	StudentID   string `json:"student_id"`
	StudentName string `json:"student_name"`
	Major       string `json:"major"`
}

type ApplicantExtractor struct {
	model   model.ToolCallingChatModel
	timeout time.Duration
}

func NewApplicantExtractor(chatModel model.ToolCallingChatModel, timeout time.Duration) *ApplicantExtractor {
	if chatModel == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &ApplicantExtractor{model: chatModel, timeout: timeout}
}

func (e *ApplicantExtractor) Extract(ctx context.Context, comment string) (ApplicantFields, error) {
	if e == nil || e.model == nil {
		return ApplicantFields{}, fmt.Errorf("applicant extractor is not initialized")
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return ApplicantFields{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	response, err := e.model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(applicantPrompt),
		schema.UserMessage(comment),
	})
	if err != nil {
		return ApplicantFields{}, fmt.Errorf("extract applicant fields: %w", err)
	}
	if response == nil {
		return ApplicantFields{}, fmt.Errorf("extract applicant fields: model returned no message")
	}
	return parseApplicantResponse(comment, response.Content)
}

func parseApplicantResponse(comment, content string) (ApplicantFields, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end < start {
		return ApplicantFields{}, fmt.Errorf("extract applicant fields: model returned invalid JSON")
	}
	var fields ApplicantFields
	if err := json.Unmarshal([]byte(content[start:end+1]), &fields); err != nil {
		return ApplicantFields{}, fmt.Errorf("extract applicant fields: decode model JSON: %w", err)
	}
	fields.StudentID = strings.TrimSpace(fields.StudentID)
	fields.StudentName = strings.TrimSpace(fields.StudentName)
	fields.Major = strings.TrimSpace(fields.Major)
	fields.StudentID = sanitizeApplicantField(comment, fields.StudentID, 32)
	if fields.StudentID != "" {
		for _, char := range fields.StudentID {
			if char < '0' || char > '9' {
				fields.StudentID = ""
				break
			}
		}
	}
	fields.StudentName = sanitizeApplicantField(comment, fields.StudentName, 64)
	for _, char := range fields.StudentName {
		if unicode.IsNumber(char) {
			fields.StudentName = ""
			break
		}
	}
	if fields.StudentName != "" && strings.IndexFunc(fields.StudentName, unicode.IsLetter) < 0 {
		fields.StudentName = ""
	}
	fields.Major = sanitizeApplicantField(comment, fields.Major, 128)
	if fields.Major != "" && strings.IndexFunc(fields.Major, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsNumber(r)
	}) < 0 {
		fields.Major = ""
	}
	return fields, nil
}

func sanitizeApplicantField(comment, value string, maxRunes int) string {
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return ""
	}
	if strings.ContainsAny(value, "\r\n\t") {
		return ""
	}
	if !strings.Contains(comment, value) {
		return ""
	}
	return value
}
