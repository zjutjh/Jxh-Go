package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
	"github.com/zjutjh/napcat-sdk/message"
)

type Sender interface {
	SendGroupText(ctx context.Context, groupID int64, text string) error
	SendGroupMessage(ctx context.Context, groupID int64, message message.Chain) error
	SendGroupFlashFile(ctx context.Context, groupID int64, source, name string) error
	GetQuoteMessages(ctx context.Context, groupID, messageID int64, count int) ([]QuotedMessage, error)
	ResolveImage(ctx context.Context, file string) (string, error)
	SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error
	SetRestart(ctx context.Context) error
	GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error)
}

const trackedLinkReplyPrefix = "精小弘觉得这个链接十分甚至九分不对劲，帮你移除了里面的TrackID："

type QuotedMessage struct {
	MessageID  int64
	UserID     int64
	Nickname   string
	RawMessage string
	Message    message.Chain
}

type Options struct {
	Knowledge     *knowledge.IndexRef
	AI            *ai.Service
	Reloader      *knowledge.Syncer
	Admin         *commands.AdminHandler
	Quote         *quote.Client
	GroupRequests *grouprequest.Service
	TriggerStats  *triggerstats.Service
	LinkCleaner   *linkcleaner.Service
}

type Pipeline struct {
	mu            sync.RWMutex
	knowledge     *knowledge.IndexRef
	sender        Sender
	groupRequests *grouprequest.Service
	stats         *triggerstats.Service
	linkCleaner   *linkcleaner.Service
	commandRouter *GroupCommandRouter
}

func (p *Pipeline) SetSender(sender Sender) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sender = sender
}

type GroupMessage struct {
	GroupID        int64
	UserID         int64
	SelfID         int64
	Text           string
	RawMessage     string
	MessageID      int64
	ReplyMessageID int64
	IsSelf         bool
	AtUsers        []int64
	Segments       message.Chain
}

func NewPipeline(opts Options) *Pipeline {
	return &Pipeline{
		knowledge:     opts.Knowledge,
		groupRequests: opts.GroupRequests,
		stats:         opts.TriggerStats,
		linkCleaner:   opts.LinkCleaner,
		commandRouter: NewGroupCommandRouter(opts),
	}
}

func (p *Pipeline) HandleGroupMessage(ctx context.Context, msg GroupMessage) error {
	sender := p.currentSender()
	if sender == nil || msg.IsSelf {
		return nil
	}
	text := strings.TrimSpace(msg.Text)
	handled, err := p.commandRouter.Handle(ctx, msg, sender)
	if handled || err != nil {
		return err
	}
	if p.linkCleaner != nil {
		cleaned, err := p.linkCleaner.CleanMessage(ctx, msg.Text, msg.Segments)
		if err != nil {
			log.Printf("clean tracked links failed: %v", err)
		}
		if len(cleaned) > 0 {
			return sender.SendGroupText(ctx, msg.GroupID, trackedLinkReplyPrefix+"\n"+strings.Join(cleaned, "\n"))
		}
	}
	if text == "" {
		return nil
	}
	if p.knowledge != nil {
		if entry, ok := p.knowledge.Lookup(text); ok {
			if err := sendKeywordReply(ctx, sender, msg.GroupID, entry.SourceKey, entry.Answer); err != nil {
				return err
			}
			if p.stats != nil {
				if err := p.stats.RecordKeywordReply(ctx, entry.SourceKey, msg.GroupID); err != nil {
					// 统计是附加能力，失败时不能阻断原本的关键词回复。
					log.Printf("record keyword reply trigger failed: %v", err)
				}
			}
			return nil
		}
	}
	return nil
}

func (p *Pipeline) HandleGroupIncrease(ctx context.Context, groupID int64, userID int64) error {
	sender := p.currentSender()
	if sender == nil {
		return nil
	}
	return sender.SendGroupMessage(ctx, groupID, message.ChainOf(
		message.At(userID),
		message.Text("欢迎来到浙江工业大学，精弘网络欢迎各位的到来！\n输入 菜单 获取精小弘机器人的菜单哦！\n请及时修改群名片\n格式如下：专业/大类+姓名"),
	))
}

func (p *Pipeline) HandleGroupJoinRequest(ctx context.Context, record grouprequest.Record) error {
	if p.groupRequests == nil {
		return fmt.Errorf("group request service is not initialized")
	}
	return p.groupRequests.Record(ctx, record)
}

func (p *Pipeline) ReconcileGroupJoinRequests(ctx context.Context, records []grouprequest.Record) error {
	if p.groupRequests == nil {
		return fmt.Errorf("group request service is not initialized")
	}
	return p.groupRequests.Reconcile(ctx, records)
}

func (p *Pipeline) SendGroupText(ctx context.Context, groupID int64, text string) error {
	sender := p.currentSender()
	if sender == nil {
		return fmt.Errorf("napcat sender is not connected")
	}
	return sender.SendGroupText(ctx, groupID, text)
}

func (p *Pipeline) currentSender() Sender {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sender
}
