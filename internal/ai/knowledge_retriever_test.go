package ai

import (
	"context"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/knowledge"
)

func TestKnowledgeRetrieverUsesScoreThreshold(t *testing.T) {
	retriever := NewKnowledgeRetriever([]knowledge.Entry{
		{
			SourceKey: "weak",
			Keyword:   "报到",
			Answer:    "报到时可以查看交通指南",
			Content:   "知识正文：报到时可以查看交通指南",
			Enabled:   true,
			AIEnabled: true,
		},
	}, KnowledgeRetrieverOptions{ScoreThreshold: 0.5})

	docs, err := retriever.Retrieve(context.Background(), "交通", 10)
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("docs length = %d, want 0", len(docs))
	}
}

func TestKnowledgeRetrieverPassesCacheTTL(t *testing.T) {
	retriever := NewKnowledgeRetriever([]knowledge.Entry{{
		SourceKey: "traffic",
		Keyword:   "交通",
		Answer:    "交通说明",
		Content:   "知识正文：交通说明",
		Enabled:   true,
		AIEnabled: true,
	}}, KnowledgeRetrieverOptions{CacheTTL: time.Minute})

	if retriever.Retriever == nil {
		t.Fatal("retriever engine is nil")
	}
	if retriever.Retriever.CacheTTL() != time.Minute {
		t.Fatalf("cache TTL = %s, want %s", retriever.Retriever.CacheTTL(), time.Minute)
	}
}
