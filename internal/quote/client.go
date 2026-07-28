package quote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type MessageSegment struct {
	Type string `json:"type"`
	Kind string `json:"kind,omitempty"`
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
	ID   string `json:"id,omitempty"`
}

type Message struct {
	UserID       int64            `json:"user_id"`
	UserNickname string           `json:"user_nickname"`
	Avatar       string           `json:"avatar,omitempty"`
	Message      []MessageSegment `json:"message"`
	Reply        *ReplyMessage    `json:"reply,omitempty"`
}

type ReplyMessage struct {
	UserNickname string           `json:"user_nickname"`
	Message      []MessageSegment `json:"message"`
	Reply        *ReplyMessage    `json:"reply,omitempty"`
}

type Payload []Message

type Client struct {
	baseURL string
	client  *http.Client
}

func NewClient(baseURL string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

func (c *Client) Generate(ctx context.Context, payload Payload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal quote payload: %w", err)
	}
	image, gifErr := c.generate(ctx, data, "/gif/base64/")
	if gifErr == nil {
		return image, nil
	}
	image, pngErr := c.generate(ctx, data, "/png/base64/")
	if pngErr != nil {
		return "", errors.Join(fmt.Errorf("generate GIF quote: %w", gifErr), fmt.Errorf("generate PNG fallback: %w", pngErr))
	}
	return image, nil
}

func (c *Client) generate(ctx context.Context, payload []byte, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create quote request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request quote image: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read quote response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("quote server returned %s: %s", resp.Status, string(body))
	}
	return string(body), nil
}
