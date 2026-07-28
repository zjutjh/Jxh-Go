package quote

import (
	"context"
	"maps"
	"strings"

	"github.com/zjutjh/napcat-sdk/message"
)

type MessageInput struct {
	UserID     int64
	Nickname   string
	RawMessage string
	Message    message.Chain
	Reply      *MessageInput
}

func BuildPayload(ctx context.Context, inputs []MessageInput, resolveImage func(context.Context, string) (string, error)) Payload {
	payload := make(Payload, 0, len(inputs))
	for _, input := range inputs {
		content := buildMessageContent(ctx, input, resolveImage)
		reply := buildReply(ctx, input.Reply, resolveImage)
		if isEmptyContent(content) && reply == nil {
			continue
		}
		payload = append(payload, Message{UserID: input.UserID, UserNickname: quoteNickname(input.Nickname), Message: content, Reply: reply})
	}
	return payload
}

func buildReply(ctx context.Context, input *MessageInput, resolveImage func(context.Context, string) (string, error)) *ReplyMessage {
	if input == nil {
		return nil
	}
	return &ReplyMessage{
		UserNickname: quoteNickname(input.Nickname),
		Message:      buildMessageContent(ctx, *input, resolveImage),
		Reply:        buildReply(ctx, input.Reply, resolveImage),
	}
}

func buildMessageContent(ctx context.Context, input MessageInput, resolveImage func(context.Context, string) (string, error)) []MessageSegment {
	chain := input.Message
	if chain != nil {
		chain = enrichMessageImages(ctx, chain, resolveImage)
	}
	return contentFromMessage(input.RawMessage, chain)
}

func quoteNickname(nickname string) string {
	if nickname = strings.TrimSpace(nickname); nickname != "" {
		return nickname
	}
	return "匿名"
}

func enrichMessageImages(ctx context.Context, chain message.Chain, resolveImage func(context.Context, string) (string, error)) message.Chain {
	out := message.ChainOf(chain...)
	for i, segment := range out {
		switch segment.Type {
		case "image", "mface", "marketface", "emoji":
			data, ok := segment.Data.(map[string]any)
			if ok {
				out[i].Data = enrichImageData(ctx, segment, data, resolveImage)
			}
		}
	}
	return out
}

func enrichImageData(ctx context.Context, segment message.Segment, data map[string]any, resolveImage func(context.Context, string) (string, error)) map[string]any {
	out := maps.Clone(data)
	for _, source := range []string{segment.String("url"), segment.String("file")} {
		if ctx.Err() != nil {
			return out
		}
		if source == "" {
			continue
		}
		if isUsableImageSource(source) {
			out["url"] = source
			return out
		}
		if resolveImage != nil {
			resolved, err := resolveImage(ctx, source)
			if err == nil && isUsableImageSource(resolved) {
				out["url"] = resolved
				return out
			}
		}
	}
	return out
}
