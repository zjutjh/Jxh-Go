package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/zjutjh/jxh-go/internal/knowledge"
)

type KnowledgeRetriever struct {
	Retriever *knowledge.RetrievalEngine
}

type KnowledgeRetrieverOptions struct {
	ScoreThreshold float64
	CacheTTL       time.Duration
}

func NewKnowledgeRetriever(entries []knowledge.Entry, options ...KnowledgeRetrieverOptions) KnowledgeRetriever {
	var opts KnowledgeRetrieverOptions
	if len(options) > 0 {
		opts = options[0]
	}
	return KnowledgeRetriever{Retriever: knowledge.NewRetrievalEngine(knowledge.RetrievalOptions{
		Entries:        entries,
		ScoreThreshold: opts.ScoreThreshold,
		CacheTTL:       opts.CacheTTL,
	})}
}

func (r KnowledgeRetriever) Retrieve(ctx context.Context, query string, topK int) ([]Document, error) {
	if r.Retriever == nil {
		return nil, nil
	}
	docs, err := r.Retriever.Retrieve(ctx, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(docs))
	for _, doc := range docs {
		metadata := map[string]string{
			"keyword": doc.Entry.Keyword,
			"answer":  doc.Entry.Answer,
		}
		if doc.Entry.Category != "" {
			metadata["category"] = doc.Entry.Category
		}
		if doc.Entry.Path != "" {
			metadata["path"] = doc.Entry.Path
		}
		out = append(out, Document{
			ID:       fmt.Sprintf("%s", doc.Entry.SourceKey),
			Content:  doc.Entry.Content,
			Metadata: metadata,
			Score:    doc.Score,
		})
	}
	return out, nil
}
