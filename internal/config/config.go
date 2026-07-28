package config

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App       AppConfig       `yaml:"app"`
	OneBot    OneBotConfig    `yaml:"onebot"`
	WPS       WPSConfig       `yaml:"wps"`
	Database  DatabaseConfig  `yaml:"database"`
	AI        AIConfig        `yaml:"ai"`
	Quote     QuoteConfig     `yaml:"quote"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
}

type AppConfig struct {
	Timezone string `yaml:"timezone"`
}

type OneBotConfig struct {
	WSURL                string `yaml:"ws_url"`
	AccessToken          string `yaml:"access_token"`
	APITimeoutSec        int    `yaml:"api_timeout_sec"`
	ReconnectIntervalSec int    `yaml:"reconnect_interval_sec"`
}

type WPSConfig struct {
	ShareURL   string `yaml:"share_url"`
	SID        string `yaml:"sid"`
	Sheet      string `yaml:"sheet"`
	CacheFile  string `yaml:"cache_file"`
	TimeoutSec int    `yaml:"timeout_sec"`
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
	// TriggerLogRetentionDays controls how many days of trigger logs to keep.
	// Zero or negative disables automatic purging.
	TriggerLogRetentionDays int `yaml:"trigger_log_retention_days"`
}

type AIConfig struct {
	Enabled          bool   `yaml:"enabled"`
	Provider         string `yaml:"provider"`
	BaseURL          string `yaml:"base_url"`
	APIKey           string `yaml:"api_key"`
	Model            string `yaml:"model"`
	TimeoutSec       int    `yaml:"timeout_sec"`
	MaxQuestionChars int    `yaml:"max_question_chars"`
}

type QuoteConfig struct {
	BaseURL    string `yaml:"base_url"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type SchedulerConfig struct {
	Timezone string `yaml:"timezone"`
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
		App: AppConfig{Timezone: "Asia/Shanghai"},
		OneBot: OneBotConfig{
			WSURL:                "ws://127.0.0.1:3001",
			APITimeoutSec:        30,
			ReconnectIntervalSec: 5,
		},
		WPS: WPSConfig{
			Sheet:      "release",
			CacheFile:  "./data/cache/knowledge.xlsx",
			TimeoutSec: 120,
		},
		Database: DatabaseConfig{
			Host:                    "127.0.0.1",
			Port:                    3306,
			User:                    "jxh",
			Name:                    "jxh_bot",
			Charset:                 "utf8mb4",
			ParseTime:               true,
			Loc:                     "Local",
			TriggerLogRetentionDays: 180,
		},
		AI: AIConfig{
			Enabled:          true,
			Provider:         "openai",
			TimeoutSec:       30,
			MaxQuestionChars: 500,
		},
		Quote:     QuoteConfig{BaseURL: "http://quote:5000", TimeoutSec: 30},
		Scheduler: SchedulerConfig{Timezone: "Asia/Shanghai"},
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
	override("JXH_DATABASE_HOST", func(v string) { cfg.Database.Host = v })
	override("JXH_DATABASE_PORT", func(v string) {
		if parsed, err := strconv.Atoi(v); err == nil {
			cfg.Database.Port = parsed
		}
	})
	override("JXH_DATABASE_USER", func(v string) { cfg.Database.User = v })
	override("JXH_DATABASE_NAME", func(v string) { cfg.Database.Name = v })
	override("JXH_DATABASE_CHARSET", func(v string) { cfg.Database.Charset = v })
	override("JXH_DATABASE_PARSE_TIME", func(v string) {
		if parsed, err := strconv.ParseBool(v); err == nil {
			cfg.Database.ParseTime = parsed
		}
	})
	override("JXH_DATABASE_LOC", func(v string) { cfg.Database.Loc = v })
	override("JXH_WPS_SID", func(v string) { cfg.WPS.SID = v })
	override("JXH_WPS_SHARE_URL", func(v string) { cfg.WPS.ShareURL = v })
	override("JXH_WPS_TIMEOUT_SEC", func(v string) {
		if parsed, err := strconv.Atoi(v); err == nil {
			cfg.WPS.TimeoutSec = parsed
		}
	})
	override("JXH_MYSQL_PASSWORD", func(v string) { cfg.Database.Password = v })
	override("JXH_MYSQL_DSN", func(v string) { cfg.Database.DSN = v })
	override("JXH_QUOTE_BASE_URL", func(v string) { cfg.Quote.BaseURL = v })
	override("JXH_AI_PROVIDER", func(v string) { cfg.AI.Provider = v })
	override("JXH_AI_BASE_URL", func(v string) { cfg.AI.BaseURL = v })
	override("JXH_AI_API_KEY", func(v string) { cfg.AI.APIKey = v })
	override("JXH_AI_MODEL", func(v string) { cfg.AI.Model = v })
}

func normalize(cfg *Config) {
	if cfg.WPS.Sheet == "" {
		cfg.WPS.Sheet = "release"
	}
	if cfg.WPS.TimeoutSec <= 0 {
		cfg.WPS.TimeoutSec = 120
	}
	if cfg.OneBot.APITimeoutSec <= 0 {
		cfg.OneBot.APITimeoutSec = 30
	}
	if cfg.OneBot.ReconnectIntervalSec <= 0 {
		cfg.OneBot.ReconnectIntervalSec = 5
	}
	if cfg.AI.TimeoutSec <= 0 {
		cfg.AI.TimeoutSec = 30
	}
	if cfg.AI.MaxQuestionChars <= 0 {
		cfg.AI.MaxQuestionChars = 500
	}
	if cfg.AI.Provider == "" {
		cfg.AI.Provider = "openai"
	}
	if cfg.Quote.TimeoutSec <= 0 {
		cfg.Quote.TimeoutSec = 30
	}
	if cfg.Scheduler.Timezone == "" {
		cfg.Scheduler.Timezone = cfg.App.Timezone
	}
}
