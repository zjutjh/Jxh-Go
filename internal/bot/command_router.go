package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/quote"
)

type GroupCommandRouter struct {
	ai       *ai.Service
	reloader Reloader
	admin    *commands.AdminHandler
	quote    QuoteGenerator
}

func NewGroupCommandRouter(opts Options) *GroupCommandRouter {
	return &GroupCommandRouter{
		ai:       opts.AI,
		reloader: opts.Reloader,
		admin:    opts.Admin,
		quote:    opts.Quote,
	}
}

func (r *GroupCommandRouter) Handle(ctx context.Context, msg GroupMessage, sender Sender) (bool, error) {
	if r == nil {
		return false, nil
	}
	text := strings.TrimSpace(msg.Text)
	switch {
	case text == "/test":
		return true, sender.SendGroupText(ctx, msg.GroupID, "ddd")
	case text == "/reload":
		return true, r.handleReload(ctx, msg, sender)
	case text == "/q":
		return true, r.handleQuote(ctx, msg, sender)
	case strings.HasPrefix(text, "/ai"):
		return true, r.handleAI(ctx, msg, sender, text)
	case strings.HasPrefix(text, "/admin"):
		return true, r.handleAdmin(ctx, msg, sender, text)
	default:
		return false, nil
	}
}

func (r *GroupCommandRouter) handleReload(ctx context.Context, msg GroupMessage, sender Sender) error {
	if r.reloader != nil {
		if err := r.reloader.Reload(ctx); err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, "重载失败："+err.Error())
		}
	}
	return sender.SendGroupText(ctx, msg.GroupID, "重载成功")
}

func (r *GroupCommandRouter) handleQuote(ctx context.Context, msg GroupMessage, sender Sender) error {
	if r.quote == nil {
		return sender.SendGroupText(ctx, msg.GroupID, "引用图服务未初始化")
	}
	if msg.ReplyMessageID == 0 {
		return sender.SendGroupText(ctx, msg.GroupID, "请回复一条消息后使用 /q")
	}
	getter, ok := sender.(QuoteMessageGetter)
	if !ok {
		return sender.SendGroupText(ctx, msg.GroupID, "NapCat 消息接口未初始化")
	}
	quoted, err := getter.GetQuoteMessage(ctx, msg.ReplyMessageID)
	if err != nil {
		return sender.SendGroupText(ctx, msg.GroupID, "获取被引用消息失败："+err.Error())
	}
	content := quote.ContentFromMessage(quoted.RawMessage, quoted.Message)
	if quote.IsEmptyContent(content) {
		return sender.SendGroupText(ctx, msg.GroupID, "被引用消息内容为空")
	}
	image, err := r.quote.Generate(ctx, quote.Payload{{
		UserID:       quoted.UserID,
		UserNickname: quote.Nickname(quoted.Nickname),
		Message:      content,
	}})
	if err != nil {
		return sender.SendGroupText(ctx, msg.GroupID, "引用图生成失败："+err.Error())
	}
	return sender.SendGroupMessage(ctx, msg.GroupID, map[string]any{"type": "image", "data": map[string]any{"file": quote.ImageFile(image)}})
}

func (r *GroupCommandRouter) handleAI(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	question := strings.TrimSpace(strings.TrimPrefix(text, "/ai"))
	if r.ai == nil {
		return sender.SendGroupText(ctx, msg.GroupID, ai.EmptyKnowledgeAnswer)
	}
	answer, err := r.ai.Answer(ctx, question)
	if err != nil {
		return err
	}
	return sender.SendGroupText(ctx, msg.GroupID, answer)
}

func (r *GroupCommandRouter) handleAdmin(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	adminText := strings.TrimSpace(strings.TrimPrefix(text, "/admin"))
	if adminText == "restart" {
		moderator, ok := sender.(Moderator)
		if !ok {
			return sender.SendGroupText(ctx, msg.GroupID, "NapCat 管理接口未初始化")
		}
		if err := moderator.SetRestart(ctx); err != nil {
			return err
		}
		return sender.SendGroupText(ctx, msg.GroupID, "已请求重启 NapCat")
	}
	if strings.HasPrefix(adminText, "ban ") {
		moderator, ok := sender.(Moderator)
		if !ok {
			return sender.SendGroupText(ctx, msg.GroupID, "NapCat 管理接口未初始化")
		}
		if len(msg.AtUsers) == 0 {
			return sender.SendGroupText(ctx, msg.GroupID, "请 @ 要禁言的用户")
		}
		duration, err := parseBanDuration(strings.TrimSpace(strings.TrimPrefix(adminText, "ban ")))
		if err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, "禁言时间格式不正确")
		}
		if err := moderator.SetGroupBan(ctx, msg.GroupID, msg.AtUsers[0], duration); err != nil {
			return err
		}
		return sender.SendGroupText(ctx, msg.GroupID, "已禁言")
	}
	if r.admin == nil {
		return sender.SendGroupText(ctx, msg.GroupID, "管理命令未初始化")
	}
	resp, err := r.admin.Handle(ctx, commands.AdminInput{
		ActorID: msg.UserID,
		Text:    adminText,
		AtUsers: msg.AtUsers,
		IsOwner: msg.IsOwner,
	})
	if err != nil {
		return err
	}
	return sender.SendGroupText(ctx, msg.GroupID, resp)
}

func parseBanDuration(raw string) (time.Duration, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty duration")
	}
	if d, err := time.ParseDuration(fields[0]); err == nil {
		return d, nil
	}
	seconds, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(seconds) * time.Second, nil
}
