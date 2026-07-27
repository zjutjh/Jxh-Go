package ai

import (
	"context"
	"fmt"
	"strings"

	arkmodel "github.com/cloudwego/eino-ext/components/model/ark"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

type EinoModelConfig struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	JSONOnly bool
}

func NewEinoModel(ctx context.Context, cfg EinoModelConfig) (model.ToolCallingChatModel, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "openai"
	}
	if cfg.APIKey == "" || cfg.Model == "" {
		return nil, fmt.Errorf("eino model config is incomplete")
	}
	switch provider {
	case "openai":
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("eino model config is incomplete")
		}
		modelConfig := &openaimodel.ChatModelConfig{
			BaseURL: cfg.BaseURL,
			APIKey:  cfg.APIKey,
			Model:   cfg.Model,
		}
		if cfg.JSONOnly {
			modelConfig.ResponseFormat = &openaimodel.ChatCompletionResponseFormat{
				Type: openaimodel.ChatCompletionResponseFormatTypeJSONObject,
			}
		}
		return openaimodel.NewChatModel(ctx, modelConfig)
	case "ark":
		modelConfig := &arkmodel.ChatModelConfig{
			BaseURL: cfg.BaseURL,
			APIKey:  cfg.APIKey,
			Model:   cfg.Model,
		}
		if cfg.JSONOnly {
			modelConfig.ResponseFormat = &arkmodel.ResponseFormat{Type: "json_object"}
		}
		return arkmodel.NewChatModel(ctx, modelConfig)
	default:
		return nil, fmt.Errorf("unsupported eino model provider: %s", cfg.Provider)
	}
}
