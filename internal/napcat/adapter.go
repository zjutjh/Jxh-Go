package napcat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/zjutjh/jxh-go/internal/bot"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

type Handler interface {
	HandleGroupMessage(ctx context.Context, msg bot.GroupMessage) error
	HandleGroupIncrease(ctx context.Context, groupID int64, userID int64) error
}

type Dedupe interface {
	SeenOrMark(key string) bool
}

type Server struct {
	Addr           string
	WSURL          string
	Token          string
	RequestTimeout time.Duration
	ReconnectDelay time.Duration
	Handler        Handler
	Dedupe         Dedupe
}

func (s Server) Serve(ctx context.Context) error {
	if s.WSURL != "" {
		return s.serveForwardWebSocket(ctx)
	}
	return napcatsdk.ServeReverseWebSocket(ctx, s.Addr, func(client *napcatsdk.Client) {
		s.consume(ctx, client)
	}, napcatsdk.WithToken(s.Token), napcatsdk.WithRequestTimeout(s.RequestTimeout))
}

func (s Server) serveForwardWebSocket(ctx context.Context) error {
	delay := s.ReconnectDelay
	if delay <= 0 {
		delay = 5 * time.Second
	}
	for {
		client, err := napcatsdk.DialWebSocket(ctx, s.WSURL, napcatsdk.WithToken(s.Token), napcatsdk.WithRequestTimeout(s.RequestTimeout))
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("connect napcat websocket failed: %v", err)
			if !sleepContext(ctx, delay) {
				return nil
			}
			continue
		}
		log.Printf("connected to napcat websocket: %s", s.WSURL)
		s.consume(ctx, client)
		_ = client.Close()
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("napcat websocket disconnected, reconnecting in %s", delay)
		if !sleepContext(ctx, delay) {
			return nil
		}
	}
}

func (s Server) consume(ctx context.Context, client *napcatsdk.Client) {
	sender := SDKSender{client: client}
	if setter, ok := s.Handler.(interface{ SetSender(bot.Sender) }); ok {
		setter.SetSender(sender)
	}
	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if s.Dedupe != nil && s.Dedupe.SeenOrMark(eventKey(ev)) {
				continue
			}
			_ = s.handleEvent(ctx, ev)
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s Server) handleEvent(ctx context.Context, ev event.Event) error {
	if s.Handler == nil {
		return nil
	}
	switch e := ev.(type) {
	case *event.GroupMessage:
		return s.Handler.HandleGroupMessage(ctx, bot.GroupMessage{
			GroupID:        e.GroupID,
			UserID:         e.UserID,
			Text:           e.Message.Text(),
			RawMessage:     e.RawMessage,
			MessageID:      e.MessageID,
			ReplyMessageID: extractReplyID(e.Message),
			IsSelf:         e.UserID == e.SelfID(),
			AtUsers:        extractAtUsers(e.Message),
		})
	case *event.UnknownEvent:
		var notice struct {
			PostType   string `json:"post_type"`
			NoticeType string `json:"notice_type"`
			GroupID    int64  `json:"group_id"`
			UserID     int64  `json:"user_id"`
		}
		if err := json.Unmarshal(e.Raw(), &notice); err != nil {
			return nil
		}
		if notice.PostType == "notice" && notice.NoticeType == "group_increase" {
			return s.Handler.HandleGroupIncrease(ctx, notice.GroupID, notice.UserID)
		}
	}
	return nil
}

func eventKey(ev event.Event) string {
	switch e := ev.(type) {
	case *event.GroupMessage:
		return fmt.Sprintf("group-message:%d:%d:%d", e.GroupID, e.MessageID, e.Time())
	case *event.PrivateMessage:
		return fmt.Sprintf("private-message:%d:%d:%d", e.UserID, e.MessageID, e.Time())
	default:
		return fmt.Sprintf("%s:%d:%d", ev.PostType(), ev.SelfID(), ev.Time())
	}
}

func extractReplyID(chain message.Chain) int64 {
	for _, seg := range chain.OfType("reply") {
		raw := seg.String("id")
		id, err := strconv.ParseInt(raw, 10, 64)
		if err == nil {
			return id
		}
	}
	return 0
}

type SDKSender struct {
	client *napcatsdk.Client
}

func NewSDKSender(client *napcatsdk.Client) SDKSender {
	return SDKSender{client: client}
}

func (s SDKSender) SendGroupText(ctx context.Context, groupID int64, text string) error {
	return s.SendGroupMessage(ctx, groupID, message.Text(text))
}

func (s SDKSender) SendGroupMessage(ctx context.Context, groupID int64, msg any) error {
	_, err := s.client.API().SendGroupMsg(ctx, api.SendGroupMsgRequest{
		GroupID: strconv.FormatInt(groupID, 10),
		Message: msg,
	})
	return err
}

func (s SDKSender) GetMsg(ctx context.Context, messageID int64) (*api.GetMsgResponse, error) {
	return s.client.API().GetMsg(ctx, api.GetMsgRequest{MessageID: messageID})
}

func (s SDKSender) SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error {
	_, err := s.client.API().SetGroupBan(ctx, api.SetGroupBanRequest{
		GroupID:  strconv.FormatInt(groupID, 10),
		UserID:   strconv.FormatInt(userID, 10),
		Duration: int64(duration.Seconds()),
	})
	return err
}

func (s SDKSender) SetRestart(ctx context.Context) error {
	_, err := s.client.API().SetRestart(ctx, api.SetRestartRequest{})
	return err
}

func extractAtUsers(chain message.Chain) []int64 {
	var out []int64
	for _, seg := range chain.OfType("at") {
		raw := seg.String("qq")
		if raw == "all" || raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err == nil {
			out = append(out, id)
		}
	}
	return out
}
