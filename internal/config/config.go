package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App         AppConfig         `yaml:"app"`
	Server      ServerConfig      `yaml:"server"`
	OneBot      OneBotConfig      `yaml:"onebot"`
	WPS         WPSConfig         `yaml:"wps"`
	Database    DatabaseConfig    `yaml:"database"`
	AI          AIConfig          `yaml:"ai"`
	Embedding   EmbeddingConfig   `yaml:"embedding"`
	Vector      VectorConfig      `yaml:"vector"`
	EventDedupe EventDedupeConfig `yaml:"event_dedupe"`
	Cache       CacheConfig       `yaml:"cache"`
	Quote       QuoteConfig       `yaml:"quote"`
	Scheduler   SchedulerConfig   `yaml:"scheduler"`
	Debug       DebugConfig       `yaml:"debug"`
}

type AppConfig struct {
	Debug    bool   `yaml:"debug"`
	LogLevel string `yaml:"log_level"`
	Timezone string `yaml:"timezone"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
}

type OneBotConfig struct {
	WSURL                string        `yaml:"ws_url"`
	AccessToken          string        `yaml:"access_token"`
	APITimeoutSec        int           `yaml:"api_timeout_sec"`
	ReconnectIntervalSec int           `yaml:"reconnect_interval_sec"`
	APITimeout           time.Duration `yaml:"-"`
	ReconnectInterval    time.Duration `yaml:"-"`
}

type WPSConfig struct {
	ShareURL    string `yaml:"share_url"`
	SID         string `yaml:"sid"`
	Sheet       string `yaml:"sheet"`
	CacheFile   string `yaml:"cache_file"`
	SyncOnStart bool   `yaml:"sync_on_start"`
}

type DatabaseConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	User      string `yaml:"user"`
	Password  string `yaml:"password"`
	Name      string `yaml:"name"`
	Charset   string `yaml:"charset"`
	ParseTime bool   `yaml:"parse_time"`
	Loc       string `yaml:"loc"`
	DSN       string `yaml:"dsn"`
}

type AIConfig struct {
	Enabled          bool    `yaml:"enabled"`
	BaseURL          string  `yaml:"base_url"`
	APIKey           string  `yaml:"api_key"`
	Model            string  `yaml:"model"`
	TimeoutSec       int     `yaml:"timeout_sec"`
	MaxQuestionChars int     `yaml:"max_question_chars"`
	TopK             int     `yaml:"top_k"`
	ScoreThreshold   float64 `yaml:"score_threshold"`
}

type EmbeddingConfig struct {
	Enabled    bool   `yaml:"enabled"`
	BaseURL    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"`
	Model      string `yaml:"model"`
	Dimensions int    `yaml:"dimensions"`
}

type VectorConfig struct {
	Enabled        bool    `yaml:"enabled"`
	Address        string  `yaml:"address"`
	DBName         string  `yaml:"db_name"`
	CollectionName string  `yaml:"collection_name"`
	MetricType     string  `yaml:"metric_type"`
	TopK           int     `yaml:"top_k"`
	ScoreThreshold float64 `yaml:"score_threshold"`
}

type EventDedupeConfig struct {
	RetentionHours       int `yaml:"retention_hours"`
	CleanupIntervalHours int `yaml:"cleanup_interval_hours"`
}

type CacheConfig struct {
	AIRetrievalTTLSec int `yaml:"ai_retrieval_ttl_sec"`
}

type QuoteConfig struct {
	BaseURL    string `yaml:"base_url"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type SchedulerConfig struct {
	Timezone string `yaml:"timezone"`
}

type DebugConfig struct {
	EnableTestCommand bool `yaml:"enable_test_command"`
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, err
		}
	}
	applyEnv(&cfg)
	normalize(&cfg)
	return cfg, nil
}

func Default() Config {
	return Config{
		App: AppConfig{LogLevel: "info", Timezone: "Asia/Shanghai"},
		Server: ServerConfig{
			Addr: ":8080",
		},
		OneBot: OneBotConfig{
			WSURL:                "ws://127.0.0.1:3001",
			APITimeoutSec:        30,
			ReconnectIntervalSec: 5,
		},
		WPS: WPSConfig{
			Sheet:       "release",
			CacheFile:   "./data/cache/knowledge.xlsx",
			SyncOnStart: true,
		},
		Database: DatabaseConfig{
			Host:      "mysql",
			Port:      3306,
			User:      "jxh",
			Name:      "jxh_bot",
			Charset:   "utf8mb4",
			ParseTime: true,
			Loc:       "Local",
		},
		AI: AIConfig{
			Enabled:          true,
			TimeoutSec:       30,
			MaxQuestionChars: 500,
			TopK:             5,
			ScoreThreshold:   0.1,
		},
		Embedding: EmbeddingConfig{
			Enabled:    true,
			Dimensions: 1024,
		},
		Vector: VectorConfig{
			Enabled:        true,
			Address:        "milvus:19530",
			DBName:         "default",
			CollectionName: "jxh_knowledge_vectors",
			MetricType:     "COSINE",
			TopK:           8,
			ScoreThreshold: 0.7,
		},
		EventDedupe: EventDedupeConfig{RetentionHours: 72, CleanupIntervalHours: 6},
		Cache:       CacheConfig{AIRetrievalTTLSec: 300},
		Quote:       QuoteConfig{BaseURL: "http://quote:5000", TimeoutSec: 10},
		Scheduler:   SchedulerConfig{Timezone: "Asia/Shanghai"},
		Debug:       DebugConfig{EnableTestCommand: true},
	}
}

func applyEnv(cfg *Config) {
	override := func(key string, set func(string)) {
		if value := os.Getenv(key); value != "" {
			set(value)
		}
	}
	override("JXH_ONEBOT_TOKEN", func(v string) { cfg.OneBot.AccessToken = v })
	override("JXH_ONEBOT_WS_URL", func(v string) { cfg.OneBot.WSURL = v })
	override("JXH_WPS_SID", func(v string) { cfg.WPS.SID = v })
	override("JXH_MYSQL_PASSWORD", func(v string) { cfg.Database.Password = v })
	override("JXH_MYSQL_DSN", func(v string) { cfg.Database.DSN = v })
	override("JXH_AI_BASE_URL", func(v string) { cfg.AI.BaseURL = v })
	override("JXH_AI_API_KEY", func(v string) { cfg.AI.APIKey = v })
	override("JXH_EMBEDDING_BASE_URL", func(v string) { cfg.Embedding.BaseURL = v })
	override("JXH_EMBEDDING_API_KEY", func(v string) { cfg.Embedding.APIKey = v })
}

func normalize(cfg *Config) {
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = ":8080"
	}
	if cfg.WPS.Sheet == "" {
		cfg.WPS.Sheet = "release"
	}
	if cfg.OneBot.APITimeoutSec <= 0 {
		cfg.OneBot.APITimeoutSec = 30
	}
	if cfg.OneBot.ReconnectIntervalSec <= 0 {
		cfg.OneBot.ReconnectIntervalSec = 5
	}
	cfg.OneBot.APITimeout = time.Duration(cfg.OneBot.APITimeoutSec) * time.Second
	cfg.OneBot.ReconnectInterval = time.Duration(cfg.OneBot.ReconnectIntervalSec) * time.Second
	if cfg.AI.TopK <= 0 {
		cfg.AI.TopK = 5
	}
	if cfg.AI.MaxQuestionChars <= 0 {
		cfg.AI.MaxQuestionChars = 500
	}
	if cfg.EventDedupe.RetentionHours <= 0 {
		cfg.EventDedupe.RetentionHours = 72
	}
	if cfg.EventDedupe.CleanupIntervalHours <= 0 {
		cfg.EventDedupe.CleanupIntervalHours = 6
	}
	if cfg.Quote.TimeoutSec <= 0 {
		cfg.Quote.TimeoutSec = 10
	}
	if cfg.Scheduler.Timezone == "" {
		cfg.Scheduler.Timezone = cfg.App.Timezone
	}
}
