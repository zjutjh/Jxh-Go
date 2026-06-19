package knowledge

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestRetrievalEngineAppliesScoreThreshold(t *testing.T) {
	engine := NewRetrievalEngine(RetrievalOptions{
		Entries: []Entry{
			{
				SourceKey: "exact-traffic",
				Keyword:   "交通",
				Answer:    "交通说明",
				Content:   "知识正文：交通说明",
				Enabled:   true,
				AIEnabled: true,
			},
			{
				SourceKey: "weak-traffic",
				Keyword:   "报到",
				Answer:    "报到时可以查看交通指南",
				Content:   "知识正文：报到时可以查看交通指南",
				Enabled:   true,
				AIEnabled: true,
			},
		},
		ScoreThreshold: 0.5,
	})

	docs, err := engine.Retrieve(context.Background(), "交通", 10)
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs length = %d, want 1", len(docs))
	}
	if docs[0].Entry.SourceKey != "exact-traffic" {
		t.Fatalf("source key = %q, want exact-traffic", docs[0].Entry.SourceKey)
	}
}

func TestRetrievalEngineMergesSourcesForSameEntry(t *testing.T) {
	engine := NewRetrievalEngine(RetrievalOptions{
		Entries: []Entry{{
			SourceKey: "traffic",
			Keyword:   "交通",
			Aliases:   []string{"怎么坐车"},
			Answer:    "交通说明",
			Content:   "知识正文：交通说明",
			Enabled:   true,
			AIEnabled: true,
		}},
	})

	docs, err := engine.Retrieve(context.Background(), "交通", 10)
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs length = %d, want 1", len(docs))
	}
	if !slices.Contains(docs[0].Sources, "exact") {
		t.Fatalf("sources = %v, want exact", docs[0].Sources)
	}
	if !slices.Contains(docs[0].Sources, "text") {
		t.Fatalf("sources = %v, want text", docs[0].Sources)
	}
}

func TestRetrievalEngineCachesUntilTTLExpires(t *testing.T) {
	now := time.Date(2026, 6, 19, 17, 30, 0, 0, time.UTC)
	engine := NewRetrievalEngine(RetrievalOptions{
		Entries: []Entry{{
			SourceKey: "traffic",
			Keyword:   "交通",
			Answer:    "交通说明",
			Content:   "知识正文：交通说明",
			Enabled:   true,
			AIEnabled: true,
		}},
		CacheTTL: time.Minute,
	})
	engine.now = func() time.Time { return now }

	first, err := engine.Retrieve(context.Background(), "交通", 10)
	if err != nil {
		t.Fatalf("first Retrieve returned error: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first docs length = %d, want 1", len(first))
	}

	engine.entries = nil
	second, err := engine.Retrieve(context.Background(), "交通", 10)
	if err != nil {
		t.Fatalf("second Retrieve returned error: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second docs length = %d, want cached result", len(second))
	}

	now = now.Add(2 * time.Minute)
	third, err := engine.Retrieve(context.Background(), "交通", 10)
	if err != nil {
		t.Fatalf("third Retrieve returned error: %v", err)
	}
	if len(third) != 0 {
		t.Fatalf("third docs length = %d, want expired cache miss", len(third))
	}
}
