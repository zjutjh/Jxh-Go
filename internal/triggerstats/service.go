package triggerstats

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/safego"
)

const (
	TriggerTypeKeywordReply = "keyword_reply"
	TriggerTypeAIRetrieval  = "ai_retrieval"
)

type Event struct {
	SourceKey   string
	TriggerType string
	GroupID     int64
	TriggeredAt time.Time
}

type Summary struct {
	SourceKey         string
	Keyword           string
	KeywordReplyCount int64
	AIRetrievalCount  int64
	TotalCount        int64
	LastTriggered     time.Time
}

type Store interface {
	RecordKnowledgeTriggers(ctx context.Context, events []Event) error
	ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error)
	PurgeOldTriggerLogs(ctx context.Context, before time.Time) (int64, error)
}

type Options struct {
	Now            func() time.Time
	ExportDir      string
	ResolveKeyword func(sourceKey string) string
	Location       *time.Location
}

type Service struct {
	store          Store
	now            func() time.Time
	exportDir      string
	resolveKeyword func(string) string
	location       *time.Location
}

func NewService(store Store, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	exportDir := strings.TrimSpace(opts.ExportDir)
	if exportDir == "" {
		exportDir = filepath.Join("data", "exports", "trigger_stats")
	}
	location := opts.Location
	if location == nil {
		location = time.Local
	}
	return &Service{store: store, now: now, exportDir: exportDir, resolveKeyword: opts.ResolveKeyword, location: location}
}

func (s *Service) RecordKeywordReply(ctx context.Context, sourceKey string, groupID int64) error {
	return s.record(ctx, []string{sourceKey}, TriggerTypeKeywordReply, groupID)
}

func (s *Service) RecordAIRetrievals(ctx context.Context, sourceKeys []string, groupID int64) error {
	return s.record(ctx, uniqueSourceKeys(sourceKeys), TriggerTypeAIRetrieval, groupID)
}

func (s *Service) record(ctx context.Context, sourceKeys []string, triggerType string, groupID int64) error {
	if s == nil || s.store == nil || len(sourceKeys) == 0 {
		return nil
	}
	now := s.now()
	events := make([]Event, 0, len(sourceKeys))
	for _, sourceKey := range sourceKeys {
		if sourceKey = strings.TrimSpace(sourceKey); sourceKey != "" {
			events = append(events, Event{SourceKey: sourceKey, TriggerType: triggerType, GroupID: groupID, TriggeredAt: now})
		}
	}
	if len(events) == 0 {
		return nil
	}
	return s.store.RecordKnowledgeTriggers(ctx, events)
}

func (s *Service) Summaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	summaries, err := s.store.ListKnowledgeTriggerSummaries(ctx, since, limit)
	if err != nil {
		return nil, err
	}
	if s.resolveKeyword != nil {
		for i := range summaries {
			summaries[i].Keyword = s.resolveKeyword(summaries[i].SourceKey)
		}
	}
	return summaries, nil
}

// PurgeOldLogs deletes trigger log entries older than retentionDays.
// If retentionDays <= 0, no purge is performed.
func (s *Service) PurgeOldLogs(ctx context.Context, retentionDays int) (int64, error) {
	if s == nil || s.store == nil || retentionDays <= 0 {
		return 0, nil
	}
	cutoff := s.now().In(s.location).AddDate(0, 0, -retentionDays)
	return s.store.PurgeOldTriggerLogs(ctx, cutoff)
}

func (s *Service) RunPurgeLoop(ctx context.Context, retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		s.purgeOnce(ctx, retentionDays)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) purgeOnce(ctx context.Context, retentionDays int) {
	// 恢复边界放在每轮工作上，一轮 panic 不会让整个清理循环静默退出。
	defer safego.Recover("trigger log purge")
	if deleted, err := s.PurgeOldLogs(ctx, retentionDays); err != nil {
		log.Printf("purge trigger logs failed: %v", err)
	} else if deleted > 0 {
		log.Printf("purged %d old trigger log entries", deleted)
	}
}

func (s *Service) SummariesForDays(ctx context.Context, days, limit int) ([]Summary, error) {
	if days < 0 {
		return nil, fmt.Errorf("days must not be negative")
	}
	if days == 0 {
		return s.Summaries(ctx, nil, limit)
	}
	now := s.now().In(s.location)
	year, month, day := now.Date()
	since := time.Date(year, month, day, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -days+1)
	return s.Summaries(ctx, &since, limit)
}

func uniqueSourceKeys(sourceKeys []string) []string {
	seen := make(map[string]struct{}, len(sourceKeys))
	out := make([]string, 0, len(sourceKeys))
	for _, sourceKey := range sourceKeys {
		sourceKey = strings.TrimSpace(sourceKey)
		if sourceKey == "" {
			continue
		}
		if _, ok := seen[sourceKey]; ok {
			continue
		}
		seen[sourceKey] = struct{}{}
		out = append(out, sourceKey)
	}
	return out
}

func (s *Service) formatTime(t time.Time) string {
	if t.IsZero() {
		return "无"
	}
	return t.In(s.location).Format("2006-01-02 15:04:05")
}
