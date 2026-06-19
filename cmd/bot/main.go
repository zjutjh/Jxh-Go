package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/cache"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/napcat"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	"github.com/zjutjh/jxh-go/internal/storage"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	db, err := openDB(cfg)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	store := storage.NewStore(db)

	knowledgeCache := cache.NewKnowledge()
	eventDedupe := persistentDedupe{
		memory: cache.NewEventDedupe(time.Duration(cfg.EventDedupe.RetentionHours) * time.Hour),
		store:  store,
	}
	go cleanupProcessedEvents(ctx, store, cfg)
	entries, err := store.ListEnabledKnowledge(ctx)
	if err != nil {
		log.Fatalf("load knowledge: %v", err)
	}
	domainEntries := storage.ToKnowledgeEntries(entries)
	knowledgeCache.Replace(knowledge.NewKeywordIndex(domainEntries))
	knowledgeRetrieverOptions := ai.KnowledgeRetrieverOptions{
		ScoreThreshold: cfg.AI.ScoreThreshold,
		CacheTTL:       time.Duration(cfg.Cache.AIRetrievalTTLSec) * time.Second,
	}
	aiRetriever := ai.NewRetrieverRef(ai.NewKnowledgeRetriever(domainEntries, knowledgeRetrieverOptions))
	knowledgeSync := knowledge.NewSyncer(knowledge.SyncerOptions{
		Source: knowledge.WPSClient{
			ShareURL:  cfg.WPS.ShareURL,
			SID:       cfg.WPS.SID,
			CacheFile: cfg.WPS.CacheFile,
			Timeout:   time.Duration(cfg.WPS.TimeoutSec) * time.Second,
		},
		Sheet: cfg.WPS.Sheet,
		Store: store,
		OnSynced: func(entries []knowledge.Entry) {
			knowledgeCache.Replace(knowledge.NewKeywordIndex(entries))
			aiRetriever.Set(ai.NewKnowledgeRetriever(entries, knowledgeRetrieverOptions))
		},
	})
	if cfg.WPS.SyncOnStart && cfg.WPS.ShareURL != "" {
		if err := knowledgeSync.Reload(ctx); err != nil {
			log.Printf("sync wps on start failed: %v", err)
		}
	}

	chat := ai.Chat(ai.ExtractiveChat{})
	if shouldCreateEinoChat(cfg.AI) {
		einoChat, err := ai.NewEinoChat(ctx, ai.EinoChatConfig{
			Provider: cfg.AI.Provider,
			BaseURL:  cfg.AI.BaseURL,
			APIKey:   cfg.AI.APIKey,
			Model:    cfg.AI.Model,
		})
		if err != nil {
			log.Fatalf("create eino chat: %v", err)
		}
		chat = einoChat
	}
	aiSvc := ai.NewService(ai.Options{
		Retriever:        aiRetriever,
		Chat:             chat,
		TopK:             cfg.AI.TopK,
		MaxQuestionChars: cfg.AI.MaxQuestionChars,
	})
	pipeline := bot.NewPipeline(bot.Options{
		Knowledge: knowledgeCache,
		AI:        aiSvc,
		Blacklist: store,
		Reloader:  knowledgeSync,
		Admin:     commands.NewAdminHandler(store),
		Quote:     quote.NewClient(cfg.Quote.BaseURL, &http.Client{Timeout: time.Duration(cfg.Quote.TimeoutSec) * time.Second}),
	})
	go scheduler.NewRuntime(scheduler.RuntimeOptions{
		Store:    store,
		Send:     pipeline.SendGroupText,
		Location: schedulerLocation(cfg),
		Logf:     log.Printf,
	}).Run(ctx)

	server := napcat.Server{
		Addr:           cfg.Server.Addr,
		WSURL:          cfg.OneBot.WSURL,
		Token:          cfg.OneBot.AccessToken,
		RequestTimeout: cfg.OneBot.APITimeout,
		ReconnectDelay: cfg.OneBot.ReconnectInterval,
		Handler:        pipeline,
		Dedupe:         eventDedupe,
	}
	if cfg.OneBot.WSURL != "" {
		log.Printf("connecting napcat websocket %s", cfg.OneBot.WSURL)
	} else {
		log.Printf("starting reverse websocket server on %s", cfg.Server.Addr)
	}
	if err := server.Serve(ctx); err != nil {
		log.Fatalf("serve napcat websocket: %v", err)
	}
}

func shouldCreateEinoChat(cfg config.AIConfig) bool {
	if cfg.Model == "" {
		return false
	}
	if cfg.APIKey != "" {
		return true
	}
	return false
}

type persistentDedupe struct {
	memory *cache.EventDedupe
	store  *storage.Store
}

func (d persistentDedupe) SeenOrMark(key string) bool {
	if d.memory != nil && d.memory.SeenOrMark(key) {
		return true
	}
	if d.store == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	seen, err := d.store.SeenOrMarkProcessedEvent(ctx, key, time.Now())
	if err != nil {
		log.Printf("dedupe store failed: %v", err)
		return false
	}
	return seen
}

func cleanupProcessedEvents(ctx context.Context, store *storage.Store, cfg config.Config) {
	retention := time.Duration(cfg.EventDedupe.RetentionHours) * time.Hour
	interval := time.Duration(cfg.EventDedupe.CleanupIntervalHours) * time.Hour
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	cleanup := func() {
		_, _ = store.CleanupProcessedEvents(ctx, time.Now().Add(-retention))
	}
	cleanup()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func schedulerLocation(cfg config.Config) *time.Location {
	loc := time.Local
	if cfg.Scheduler.Timezone != "" {
		if loaded, err := time.LoadLocation(cfg.Scheduler.Timezone); err == nil {
			loc = loaded
		} else {
			log.Printf("load scheduler timezone failed: %v", err)
		}
	}
	return loc
}

func openDB(cfg config.Config) (*gorm.DB, error) {
	dsn := cfg.Database.DSN
	if dsn == "" {
		parseTime := "False"
		if cfg.Database.ParseTime {
			parseTime = "True"
		}
		dsn = cfg.Database.User + ":" + cfg.Database.Password + "@tcp(" + cfg.Database.Host + ":" + itoa(cfg.Database.Port) + ")/" + cfg.Database.Name + "?charset=" + cfg.Database.Charset + "&parseTime=" + parseTime + "&loc=" + cfg.Database.Loc
	}
	return gorm.Open(mysql.Open(dsn), &gorm.Config{})
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
