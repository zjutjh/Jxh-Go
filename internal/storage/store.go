package storage

import (
	"context"
	"time"

	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

func (s *Store) RecordKnowledgeTriggers(ctx context.Context, events []triggerstats.Event) error {
	if len(events) == 0 {
		return nil
	}
	models := make([]KnowledgeTriggerLog, 0, len(events))
	for _, event := range events {
		models = append(models, KnowledgeTriggerLog{
			SourceKey: event.SourceKey, TriggerType: event.TriggerType,
			GroupID: event.GroupID, TriggeredAt: event.TriggeredAt,
		})
	}
	return s.db.WithContext(ctx).CreateInBatches(models, len(models)).Error
}

func (s *Store) ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]triggerstats.Summary, error) {
	query := s.db.WithContext(ctx).
		Table((KnowledgeTriggerLog{}).TableName()).
		Select(`source_key,
SUM(CASE WHEN trigger_type = ? THEN 1 ELSE 0 END) AS keyword_reply_count,
SUM(CASE WHEN trigger_type = ? THEN 1 ELSE 0 END) AS ai_retrieval_count,
COUNT(*) AS total_count,
MAX(triggered_at) AS last_triggered`, triggerstats.TriggerTypeKeywordReply, triggerstats.TriggerTypeAIRetrieval)
	if since != nil {
		query = query.Where("triggered_at >= ?", *since)
	}
	query = query.Group("source_key").Order("total_count DESC").Order("source_key")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var summaries []triggerstats.Summary
	return summaries, query.Scan(&summaries).Error
}

// PurgeOldTriggerLogs deletes trigger log entries older than the given time.
// Returns the number of rows deleted.
func (s *Store) PurgeOldTriggerLogs(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("triggered_at < ?", before).
		Delete(&KnowledgeTriggerLog{})
	return result.RowsAffected, result.Error
}

func (s *Store) ListScheduledJobs(ctx context.Context, groupID int64) ([]commands.ScheduledJobView, error) {
	var jobs []ScheduledJob
	if err := s.db.WithContext(ctx).Where("group_id = ? AND enabled = ?", groupID, true).Order("id").Find(&jobs).Error; err != nil {
		return nil, err
	}
	out := make([]commands.ScheduledJobView, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, commands.ScheduledJobView{
			ID:       job.ID,
			Type:     job.Type,
			TimeHHMM: job.TimeHHMM,
			RunDate:  job.RunDate,
			GroupID:  job.GroupID,
			Message:  job.Message,
		})
	}
	return out, nil
}

func (s *Store) AddScheduledJob(ctx context.Context, input commands.ScheduledJobInput) (uint64, error) {
	job := &ScheduledJob{
		Type:     input.Type,
		TimeHHMM: input.TimeHHMM,
		RunDate:  input.RunDate,
		GroupID:  input.GroupID,
		Message:  input.Message,
		Enabled:  true,
	}
	if input.Type == scheduler.JobTypeDaily && scheduler.TimeReached(input.CreatedAt, input.TimeHHMM) {
		createdAt := input.CreatedAt
		job.LastRunAt = &createdAt
	}
	err := s.db.WithContext(ctx).Create(job).Error
	return job.ID, err
}

func (s *Store) RemoveScheduledJob(ctx context.Context, groupID int64, id uint64) (bool, error) {
	result := s.db.WithContext(ctx).Model(&ScheduledJob{}).Where("id = ? AND group_id = ?", id, groupID).Update("enabled", false)
	return result.RowsAffected > 0, result.Error
}

func (s *Store) ListActiveSchedulerJobs(ctx context.Context) ([]scheduler.Job, error) {
	var jobs []ScheduledJob
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&jobs).Error; err != nil {
		return nil, err
	}
	out := make([]scheduler.Job, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, scheduler.Job{
			ID:        job.ID,
			Type:      job.Type,
			GroupID:   job.GroupID,
			Message:   job.Message,
			TimeHHMM:  job.TimeHHMM,
			RunDate:   job.RunDate,
			Enabled:   job.Enabled,
			LastRunAt: job.LastRunAt,
		})
	}
	return out, nil
}

func (s *Store) MarkScheduledJobRan(ctx context.Context, id uint64, at time.Time, disable bool) error {
	updates := map[string]any{"last_run_at": at}
	if disable {
		updates["enabled"] = false
	}
	return s.db.WithContext(ctx).Model(&ScheduledJob{}).Where("id = ?", id).Updates(updates).Error
}

func (s *Store) UpsertGroupJoinRequest(ctx context.Context, record grouprequest.Record) error {
	model := groupJoinRequestToModel(record)
	updates := map[string]any{
		"group_id":     model.GroupID,
		"user_id":      model.UserID,
		"sub_type":     model.SubType,
		"comment":      model.Comment,
		"source":       model.Source,
		"raw_json":     model.RawJSON,
		"requested_at": model.RequestedAt,
		"last_seen_at": model.LastSeenAt,
	}
	if record.Source == grouprequest.SourceSystem {
		updates = systemGroupRequestUpdates(record)
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "flag"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&model).Error
}

func systemGroupRequestUpdates(record grouprequest.Record) map[string]any {
	updates := map[string]any{
		"group_id": record.GroupID,
		"user_id":  record.UserID,
		"sub_type": record.SubType,
		"comment":  record.Comment,
		"status": gorm.Expr(
			"IF(status = ?, status, ?)", grouprequest.StatusProcessed, record.Status,
		),
		"system_raw_json": record.SystemRawJSON,
		"last_seen_at":    record.LastSeenAt,
	}
	if record.StudentID != "" {
		updates["student_id"] = record.StudentID
	}
	if record.StudentName != "" {
		updates["student_name"] = record.StudentName
	}
	if record.Major != "" {
		updates["major"] = record.Major
	}
	if record.ProcessedAt != nil {
		updates["processed_at"] = gorm.Expr("COALESCE(processed_at, ?)", *record.ProcessedAt)
	}
	return updates
}

func (s *Store) ListGroupJoinRequests(ctx context.Context, limit int) ([]grouprequest.Record, error) {
	var models []GroupJoinRequest
	query := s.db.WithContext(ctx).Order("requested_at DESC").Order("id DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}
	records := make([]grouprequest.Record, 0, len(models))
	for _, model := range models {
		records = append(records, groupJoinRequestFromModel(model))
	}
	return records, nil
}

func (s *Store) ListPendingGroupJoinRequests(ctx context.Context, limit int) ([]grouprequest.Record, error) {
	var models []GroupJoinRequest
	query := s.db.WithContext(ctx).
		Where("ai_parse_status = ? AND sub_type = ?", grouprequest.AIParsePending, "add").
		Order("id")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}
	records := make([]grouprequest.Record, 0, len(models))
	for _, model := range models {
		records = append(records, groupJoinRequestFromModel(model))
	}
	return records, nil
}

func (s *Store) CompleteGroupJoinRequestAI(ctx context.Context, id uint64, fields grouprequest.ExtractedFields, at time.Time) error {
	updates := map[string]any{
		"ai_parse_status": grouprequest.AIParseCompleted,
		"ai_parsed_at":    at,
	}
	if fields.StudentID != "" {
		updates["student_id"] = fields.StudentID
	}
	if fields.StudentName != "" {
		updates["student_name"] = fields.StudentName
	}
	if fields.Major != "" {
		updates["major"] = fields.Major
	}
	return s.db.WithContext(ctx).Model(&GroupJoinRequest{}).
		Where("id = ? AND ai_parse_status = ?", id, grouprequest.AIParsePending).
		Updates(updates).Error
}

func (s *Store) FailGroupJoinRequestAI(ctx context.Context, id uint64, maxAttempts int) error {
	return s.db.WithContext(ctx).Exec(
		`UPDATE group_join_requests
SET ai_parse_status = IF(ai_parse_attempts + 1 >= ?, ?, ?),
    ai_parse_attempts = ai_parse_attempts + 1
WHERE id = ? AND ai_parse_status = ?`,
		maxAttempts, grouprequest.AIParseFailed, grouprequest.AIParsePending, id, grouprequest.AIParsePending,
	).Error
}

func groupJoinRequestToModel(record grouprequest.Record) GroupJoinRequest {
	return GroupJoinRequest{
		ID:              record.ID,
		Flag:            record.Flag,
		GroupID:         record.GroupID,
		UserID:          record.UserID,
		StudentID:       record.StudentID,
		StudentName:     record.StudentName,
		Major:           record.Major,
		SubType:         record.SubType,
		Comment:         record.Comment,
		Status:          record.Status,
		Source:          record.Source,
		RawJSON:         record.RawJSON,
		SystemRawJSON:   record.SystemRawJSON,
		AIParseStatus:   record.AIParseStatus,
		AIParseAttempts: record.AIParseAttempts,
		RequestedAt:     record.RequestedAt,
		ProcessedAt:     record.ProcessedAt,
		FirstSeenAt:     record.FirstSeenAt,
		LastSeenAt:      record.LastSeenAt,
		AIParsedAt:      record.AIParsedAt,
	}
}

func groupJoinRequestFromModel(model GroupJoinRequest) grouprequest.Record {
	return grouprequest.Record{
		ID:              model.ID,
		Flag:            model.Flag,
		GroupID:         model.GroupID,
		UserID:          model.UserID,
		StudentID:       model.StudentID,
		StudentName:     model.StudentName,
		Major:           model.Major,
		SubType:         model.SubType,
		Comment:         model.Comment,
		Status:          model.Status,
		Source:          model.Source,
		RawJSON:         model.RawJSON,
		SystemRawJSON:   model.SystemRawJSON,
		AIParseStatus:   model.AIParseStatus,
		AIParseAttempts: model.AIParseAttempts,
		RequestedAt:     model.RequestedAt,
		ProcessedAt:     model.ProcessedAt,
		FirstSeenAt:     model.FirstSeenAt,
		LastSeenAt:      model.LastSeenAt,
		AIParsedAt:      model.AIParsedAt,
	}
}
