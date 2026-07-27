package storage

import "time"

type KnowledgeTriggerLog struct {
	ID          uint64    `gorm:"primaryKey"`
	SourceKey   string    `gorm:"size:255;not null;index:idx_trigger_stats,priority:2"`
	TriggerType string    `gorm:"size:32;not null;index:idx_trigger_stats,priority:3"`
	GroupID     int64     `gorm:"not null"`
	TriggeredAt time.Time `gorm:"not null;index:idx_trigger_stats,priority:1"`
}

func (KnowledgeTriggerLog) TableName() string {
	return "knowledge_trigger_logs"
}

type ScheduledJob struct {
	ID        uint64 `gorm:"primaryKey"`
	Type      string `gorm:"size:16;not null"`
	TimeHHMM  string `gorm:"column:time_hhmm;size:5;not null"`
	RunDate   *time.Time
	GroupID   int64  `gorm:"not null"`
	Message   string `gorm:"type:text;not null"`
	Enabled   bool   `gorm:"not null"`
	LastRunAt *time.Time
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

func (ScheduledJob) TableName() string {
	return "scheduled_jobs"
}

type GroupJoinRequest struct {
	ID              uint64 `gorm:"primaryKey"`
	Flag            string `gorm:"size:512;not null;uniqueIndex"`
	GroupID         int64  `gorm:"index"`
	UserID          int64  `gorm:"index"`
	StudentID       string `gorm:"size:64"`
	StudentName     string `gorm:"size:64"`
	Major           string `gorm:"size:128"`
	SubType         string `gorm:"size:32"`
	Comment         string `gorm:"type:text"`
	Status          string `gorm:"size:32;not null;index"`
	Source          string `gorm:"size:32;not null"`
	RawJSON         string `gorm:"type:mediumtext"`
	SystemRawJSON   string `gorm:"type:mediumtext"`
	AIParseStatus   string `gorm:"column:ai_parse_status;size:32;not null;index"`
	AIParseAttempts uint   `gorm:"column:ai_parse_attempts"`
	RequestedAt     time.Time
	ProcessedAt     *time.Time
	FirstSeenAt     time.Time
	LastSeenAt      time.Time  `gorm:"index"`
	AIParsedAt      *time.Time `gorm:"column:ai_parsed_at"`
}

func (GroupJoinRequest) TableName() string {
	return "group_join_requests"
}
