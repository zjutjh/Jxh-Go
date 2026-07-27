package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/zjutjh/jxh-go/internal/cqreply"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/message"
)

const (
	imageReplyUnavailableText = "词条中的图片暂时无法发送，请联系管理员检查图片链接。"
	fileReplyUnavailableText  = "词条中的文件暂时无法发送，请联系管理员检查文件来源。"
	replyIncompleteText       = "词条内容暂时无法完整发送，请稍后再试。"
)

func sendKeywordReply(ctx context.Context, sender Sender, groupID int64, sourceKey, answer string) error {
	parsed := cqreply.Parse(answer)
	if parsed.RejectedImageCount > 0 {
		log.Printf("keyword reply ignored %d unsafe or invalid image source(s), source_key=%q", parsed.RejectedImageCount, sourceKey)
	}
	if parsed.RejectedFileCount > 0 {
		log.Printf("keyword reply ignored %d unsafe or invalid file source(s), source_key=%q", parsed.RejectedFileCount, sourceKey)
	}
	if parsed.ImageCount == 0 && parsed.FileCount == 0 && parsed.RejectedFileCount == 0 {
		fallback := parsed.PlainText
		if strings.TrimSpace(fallback) == "" {
			switch {
			case parsed.RejectedFileCount > 0:
				fallback = fileReplyUnavailableText
			case parsed.RejectedImageCount > 0:
				fallback = imageReplyUnavailableText
			}
		}
		return sender.SendGroupText(ctx, groupID, fallback)
	}

	chain := make(message.Chain, 0, len(parsed.Parts))
	sentAny := false
	chainHasImage := false
	flush := func() (bool, error) {
		if len(chain) == 0 {
			return false, nil
		}
		err := sender.SendGroupMessage(ctx, groupID, chain)
		chain = nil
		hadImage := chainHasImage
		chainHasImage = false
		if err == nil {
			sentAny = true
			return false, nil
		}
		if isAmbiguousSendTimeout(err) {
			return false, fmt.Errorf("keyword message send outcome unknown, source_key=%q: %w", sourceKey, err)
		}
		log.Printf("send keyword message failed, source_key=%q: %v", sourceKey, err)
		fallback := parsed.PlainText
		if sentAny {
			fallback = replyIncompleteText
		} else if strings.TrimSpace(fallback) == "" && hadImage {
			fallback = imageReplyUnavailableText
		}
		if fallbackErr := sender.SendGroupText(ctx, groupID, fallback); fallbackErr != nil {
			return false, fmt.Errorf("send keyword message: %v; send text fallback: %w", err, fallbackErr)
		}
		return true, nil
	}

	for _, part := range parsed.Parts {
		switch part.Type {
		case cqreply.PartText:
			chain = append(chain, message.Text(part.Value))
		case cqreply.PartImage:
			chain = append(chain, message.Image(part.Value))
			chainHasImage = true
		case cqreply.PartFile:
			stop, err := flush()
			if err != nil || stop {
				return err
			}
			if err := sender.SendGroupFlashFile(ctx, groupID, part.Value, part.Name); err != nil {
				if isAmbiguousSendTimeout(err) {
					return fmt.Errorf("keyword file send outcome unknown, source_key=%q: %w", sourceKey, err)
				}
				log.Printf("send keyword file failed, source_key=%q: %v", sourceKey, err)
				if fallbackErr := sender.SendGroupText(ctx, groupID, fileReplyUnavailableText); fallbackErr != nil {
					return fmt.Errorf("send keyword file: %v; send file fallback: %w", err, fallbackErr)
				}
				sentAny = true
				continue
			}
			sentAny = true
		case cqreply.PartRejectedFile:
			stop, err := flush()
			if err != nil || stop {
				return err
			}
			if err := sender.SendGroupText(ctx, groupID, fileReplyUnavailableText); err != nil {
				return fmt.Errorf("send rejected keyword file placeholder: %w", err)
			}
			sentAny = true
		}
	}
	_, err := flush()
	return err
}

func isAmbiguousSendTimeout(err error) bool {
	if errors.Is(err, napcatsdk.ErrTimeout) {
		return true
	}
	var apiErr *napcatsdk.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	detail := strings.ToLower(apiErr.Message + " " + apiErr.Wording)
	return strings.Contains(detail, "timeout") || strings.Contains(detail, "超时")
}
