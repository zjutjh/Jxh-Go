package grouprequest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"github.com/zjutjh/jxh-go/internal/safego"
)

const (
	StatusPending   = "pending"
	StatusProcessed = "processed"

	SourceEvent  = "event"
	SourceSystem = "system"

	AIParsePending   = "pending"
	AIParseCompleted = "completed"
	AIParseFailed    = "failed"
	AIParseSkipped   = "skipped"

	maxFlagRunes       = 512
	maxAIParseAttempts = 3
	aiParseBatchSize   = 10
	aiParseInterval    = 2 * time.Second
)

type Record struct {
	ID              uint64
	Flag            string
	GroupID         int64
	UserID          int64
	StudentID       string
	StudentName     string
	Major           string
	SubType         string
	Comment         string
	Status          string
	Source          string
	RawJSON         string
	SystemRawJSON   string
	AIParseStatus   string
	AIParseAttempts uint
	RequestedAt     time.Time
	ProcessedAt     *time.Time
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	AIParsedAt      *time.Time
}

type SystemMessage struct {
	RequestID string
	GroupID   int64
	UserID    int64
	Message   string
	Checked   bool
	RawJSON   string
}

type Store interface {
	UpsertGroupJoinRequest(ctx context.Context, record Record) error
	ListGroupJoinRequests(ctx context.Context, limit int) ([]Record, error)
	ListPendingGroupJoinRequests(ctx context.Context, limit int) ([]Record, error)
	CompleteGroupJoinRequestAI(ctx context.Context, id uint64, fields ExtractedFields, at time.Time) error
	FailGroupJoinRequestAI(ctx context.Context, id uint64, maxAttempts int) error
}

type ExtractedFields struct {
	StudentID   string
	StudentName string
	Major       string
}

type ExtractApplicantFunc func(context.Context, string) (ExtractedFields, error)

type Options struct {
	ExportDir        string
	Now              func() time.Time
	Location         *time.Location
	ExtractApplicant ExtractApplicantFunc
}

type Service struct {
	store            Store
	exportDir        string
	now              func() time.Time
	location         *time.Location
	extractApplicant ExtractApplicantFunc
}

type ExportResult struct {
	Dir   string
	Count int
	Files []ExportFile
}

type ExportFile struct {
	GroupID int64
	Path    string
	Count   int
}

func NewService(store Store, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	exportDir := strings.TrimSpace(opts.ExportDir)
	if exportDir == "" {
		exportDir = filepath.Join("data", "exports", "group_requests")
	}
	location := opts.Location
	if location == nil {
		location = time.Local
	}
	return &Service{
		store: store, exportDir: exportDir, now: now, location: location,
		extractApplicant: opts.ExtractApplicant,
	}
}

func (s *Service) Record(ctx context.Context, record Record) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("群申请存储未初始化")
	}
	if record.Flag == "" {
		return fmt.Errorf("群申请 flag 为空")
	}
	if utf8.RuneCountInString(record.Flag) > maxFlagRunes {
		return fmt.Errorf("群申请 flag 超过 %d 个字符", maxFlagRunes)
	}
	if record.GroupID <= 0 || record.UserID <= 0 {
		return fmt.Errorf("群申请群号或申请人 QQ 无效")
	}
	record = normalizeRecord(record, s.now(), s.extractApplicant != nil)
	return s.store.UpsertGroupJoinRequest(ctx, record)
}

func (s *Service) Reconcile(ctx context.Context, records []Record) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("群申请存储未初始化")
	}
	var reconcileErrors []error
	for index, record := range records {
		if record.Flag == "" {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("第 %d 条群申请 flag 为空", index+1))
			continue
		}
		if utf8.RuneCountInString(record.Flag) > maxFlagRunes {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("第 %d 条群申请 flag 超过 %d 个字符", index+1, maxFlagRunes))
			continue
		}
		if record.GroupID <= 0 || record.UserID <= 0 {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("第 %d 条群申请群号或申请人 QQ 无效", index+1))
			continue
		}
		record = normalizeRecord(record, s.now(), s.extractApplicant != nil)
		if err := s.store.UpsertGroupJoinRequest(ctx, record); err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("同步第 %d 条群申请: %w", index+1, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (s *Service) RunAIParser(ctx context.Context) {
	if s == nil || s.store == nil || s.extractApplicant == nil {
		return
	}
	s.processPendingAI(ctx)
	ticker := time.NewTicker(aiParseInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processPendingAI(ctx)
		}
	}
}

func (s *Service) processPendingAI(ctx context.Context) {
	// 恢复边界放在每轮工作上，一轮 panic 不会让整个解析循环静默退出。
	defer safego.Recover("group request AI parse")
	records, err := s.store.ListPendingGroupJoinRequests(ctx, aiParseBatchSize)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("list group requests pending AI parse failed: %v", err)
		}
		return
	}
	for _, record := range records {
		fields, err := s.extractApplicant(ctx, applicantAnswerText(record.Comment))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("parse group request with AI failed: id=%d: %v", record.ID, err)
			if markErr := s.store.FailGroupJoinRequestAI(ctx, record.ID, maxAIParseAttempts); markErr != nil {
				log.Printf("mark group request AI parse failed: id=%d: %v", record.ID, markErr)
			}
			continue
		}
		if err := s.store.CompleteGroupJoinRequestAI(ctx, record.ID, fields, s.now()); err != nil {
			if ctx.Err() == nil {
				log.Printf("save group request AI fields failed: id=%d: %v", record.ID, err)
			}
		}
	}
}

func (s *Service) Export(ctx context.Context, limit int) (ExportResult, error) {
	if s == nil || s.store == nil {
		return ExportResult{}, fmt.Errorf("群申请存储未初始化")
	}
	if limit < 0 {
		return ExportResult{}, fmt.Errorf("群申请导出数量不能为负数")
	}
	records, err := s.store.ListGroupJoinRequests(ctx, limit)
	if err != nil {
		return ExportResult{}, err
	}
	if len(records) == 0 {
		return ExportResult{}, nil
	}
	if err := os.MkdirAll(s.exportDir, 0o755); err != nil {
		return ExportResult{}, err
	}
	runDir, err := os.MkdirTemp(s.exportDir, "group_requests_"+s.now().In(s.location).Format("20060102_150405")+"_")
	if err != nil {
		return ExportResult{}, err
	}
	groups := make(map[int64][]Record)
	for _, record := range records {
		groups[record.GroupID] = append(groups[record.GroupID], record)
	}
	groupIDs := make([]int64, 0, len(groups))
	for groupID := range groups {
		groupIDs = append(groupIDs, groupID)
	}
	slices.Sort(groupIDs)
	result := ExportResult{Dir: runDir, Count: len(records), Files: make([]ExportFile, 0, len(groupIDs))}
	for _, groupID := range groupIDs {
		path := filepath.Join(runDir, fmt.Sprintf("group_%d.xlsx", groupID))
		if err := s.writeXLSX(path, groups[groupID]); err != nil {
			_ = os.RemoveAll(runDir)
			return ExportResult{}, err
		}
		result.Files = append(result.Files, ExportFile{GroupID: groupID, Path: path, Count: len(groups[groupID])})
	}
	return result, nil
}

// RecordFromEvent parses OneBot group request events that NapCat SDK exposes as UnknownEvent.
func RecordFromEvent(raw []byte) (Record, bool, error) {
	var event struct {
		Time        int64  `json:"time"`
		PostType    string `json:"post_type"`
		RequestType string `json:"request_type"`
		SubType     string `json:"sub_type"`
		GroupID     int64  `json:"group_id"`
		UserID      int64  `json:"user_id"`
		Comment     string `json:"comment"`
		Flag        string `json:"flag"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return Record{}, false, err
	}
	if event.PostType != "request" || event.RequestType != "group" {
		return Record{}, false, nil
	}
	var requestedAt time.Time
	if event.Time > 0 {
		requestedAt = time.Unix(event.Time, 0)
	}
	return Record{
		Flag:        event.Flag,
		GroupID:     event.GroupID,
		UserID:      event.UserID,
		StudentID:   extractStudentID(event.Comment),
		StudentName: extractStudentName(event.Comment),
		Major:       extractMajor(event.Comment),
		SubType:     event.SubType,
		Comment:     event.Comment,
		Status:      StatusPending,
		Source:      SourceEvent,
		RawJSON:     string(raw),
		RequestedAt: requestedAt,
	}, true, nil
}

// RecordsFromSystemMessages normalizes get_group_system_msg join and invite rows.
func RecordsFromSystemMessages(joinRequests, invitedRequests []SystemMessage) []Record {
	records := make([]Record, 0, len(joinRequests)+len(invitedRequests))
	for _, raw := range joinRequests {
		records = append(records, recordFromSystemMessage(raw, "add"))
	}
	for _, raw := range invitedRequests {
		records = append(records, recordFromSystemMessage(raw, "invite"))
	}
	return records
}

func recordFromSystemMessage(raw SystemMessage, subType string) Record {
	status := StatusPending
	if raw.Checked {
		status = StatusProcessed
	}
	return Record{
		Flag:          raw.RequestID,
		GroupID:       raw.GroupID,
		UserID:        raw.UserID,
		StudentID:     extractStudentID(raw.Message),
		StudentName:   extractStudentName(raw.Message),
		Major:         extractMajor(raw.Message),
		SubType:       subType,
		Comment:       raw.Message,
		Status:        status,
		Source:        SourceSystem,
		SystemRawJSON: raw.RawJSON,
	}
}

func normalizeRecord(record Record, now time.Time, aiEnabled bool) Record {
	if record.Status == "" {
		record.Status = StatusPending
	}
	if record.Source == "" {
		record.Source = SourceEvent
	}
	if record.AIParseStatus == "" {
		if aiEnabled && record.SubType == "add" {
			record.AIParseStatus = AIParsePending
		} else {
			record.AIParseStatus = AIParseSkipped
		}
	}
	if record.RequestedAt.IsZero() {
		record.RequestedAt = now
	}
	if record.FirstSeenAt.IsZero() {
		record.FirstSeenAt = now
	}
	if record.LastSeenAt.IsZero() {
		record.LastSeenAt = now
	}
	if record.Status == StatusProcessed && record.ProcessedAt == nil {
		processedAt := now
		record.ProcessedAt = &processedAt
	}
	return record
}

func extractStudentID(comment string) string {
	value := extractLabeledValue(comment, []string{"学号", "学生号", "学籍号"})
	var b strings.Builder
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 {
			break
		}
	}
	candidate := b.String()
	if utf8.RuneCountInString(candidate) < 6 || utf8.RuneCountInString(candidate) > 32 {
		return ""
	}
	return candidate
}

func extractStudentName(comment string) string {
	candidate := extractLabeledValue(comment, []string{"姓名", "名字"})
	candidate = strings.Trim(candidate, " \t\r\n:：=+＋，,；;。、/|")
	if candidate == "" || utf8.RuneCountInString(candidate) > 16 || strings.IndexFunc(candidate, unicode.IsLetter) < 0 {
		return ""
	}
	for _, r := range candidate {
		if unicode.IsNumber(r) {
			return ""
		}
	}
	return candidate
}

func extractMajor(comment string) string {
	candidate := extractLabeledValue(comment, []string{"专业", "大类"})
	candidate = strings.Trim(candidate, " \t\r\n:：=+＋，,；;。、/|")
	if candidate == "" || utf8.RuneCountInString(candidate) > 128 || strings.IndexFunc(candidate, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsNumber(r)
	}) < 0 {
		return ""
	}
	return candidate
}

func extractLabeledValue(comment string, labels []string) string {
	comment = applicantAnswerText(comment)
	for _, label := range labels {
		idx := strings.Index(comment, label)
		if idx < 0 {
			continue
		}
		rest := comment[idx+len(label):]
		rest = strings.TrimLeft(rest, " \t\r\n:：=-")
		if rest == "" {
			continue
		}
		if value := trimAtBoundary(rest); value != "" {
			return value
		}
	}
	return ""
}

func trimAtBoundary(value string) string {
	stop := len(value)
	for _, boundary := range []string{"\r\n", "\n", "\r", "\t", " ", "+", "＋", "，", ",", "；", ";", "。", "、", "/", "|"} {
		if idx := strings.Index(value, boundary); idx >= 0 && idx < stop {
			stop = idx
		}
	}
	for _, label := range []string{"学号", "学生号", "学籍号", "姓名", "名字", "专业", "班级", "学院", "年级", "QQ", "qq"} {
		if idx := strings.Index(value, label); idx >= 0 && idx < stop {
			stop = idx
		}
	}
	return strings.TrimSpace(value[:stop])
}

func applicantAnswerText(comment string) string {
	comment = strings.TrimSpace(comment)
	for _, marker := range []string{"答案：", "答案:"} {
		if index := strings.LastIndex(comment, marker); index >= 0 {
			if answer := strings.TrimSpace(comment[index+len(marker):]); answer != "" {
				return answer
			}
		}
	}
	return comment
}

func (s *Service) writeXLSX(path string, records []Record) error {
	f := excelize.NewFile()
	defer f.Close()
	const sheet = "群申请"
	defaultSheet := f.GetSheetName(0)
	if err := f.SetSheetName(defaultSheet, sheet); err != nil {
		return err
	}
	headers := []string{"记录ID", "群号", "用户QQ", "学号", "姓名", "专业", "申请类型", "验证信息", "状态", "处理时间", "AI解析状态", "AI解析时间", "来源", "申请时间", "首次记录时间", "最近出现时间", "flag"}
	for i, header := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(sheet, cell, header); err != nil {
			return err
		}
	}
	for row, record := range records {
		values := []any{
			record.ID,
			record.GroupID,
			record.UserID,
			record.StudentID,
			record.StudentName,
			record.Major,
			record.SubType,
			record.Comment,
			record.Status,
			s.formatOptionalTime(record.ProcessedAt),
			record.AIParseStatus,
			s.formatOptionalTime(record.AIParsedAt),
			record.Source,
			s.formatTime(record.RequestedAt),
			s.formatTime(record.FirstSeenAt),
			s.formatTime(record.LastSeenAt),
			record.Flag,
		}
		for col, value := range values {
			cell, _ := excelize.CoordinatesToCellName(col+1, row+2)
			if err := f.SetCellValue(sheet, cell, value); err != nil {
				return err
			}
		}
	}
	if err := f.SaveAs(path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (s *Service) formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return s.formatTime(*t)
}

func (s *Service) formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(s.location).Format("2006-01-02 15:04:05")
}
