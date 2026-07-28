package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/flashfile"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/safego"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

type Server struct {
	WSURL          string
	Token          string
	RequestTimeout time.Duration
	ReconnectDelay time.Duration
	Handler        *bot.Pipeline
	FlashFiles     *flashfile.Stager
}

func (s Server) Serve(ctx context.Context) error {
	if strings.TrimSpace(s.WSURL) == "" {
		return fmt.Errorf("napcat websocket URL is required")
	}
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

// maxConcurrentEvents bounds how many events are handled in parallel so a burst
// of group messages/notices cannot spawn unbounded goroutines. Handling stays
// off the read loop so a slow path (e.g. /reload) never blocks event intake.
const maxConcurrentEvents = 32

const (
	groupRequestSyncCount    = 100
	groupRequestSyncInterval = 10 * time.Second
)

func (s Server) consume(ctx context.Context, client *napcatsdk.Client) {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sender := SDKSender{client: client, flashFiles: s.FlashFiles}
	if s.Handler == nil {
		return
	}
	s.Handler.SetSender(sender)
	defer s.Handler.SetSender(nil)
	go s.syncGroupJoinRequests(sessionCtx, sender)
	slots := make(chan struct{}, maxConcurrentEvents)
	events := client.Events()
	for {
		select {
		case <-sessionCtx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			// Bounded concurrency: acquire a slot before dispatching. If all slots
			// are busy this blocks briefly, applying backpressure instead of
			// spawning unbounded goroutines.
			select {
			case slots <- struct{}{}:
			case <-sessionCtx.Done():
				return
			}
			go func(evt event.Event) {
				defer func() { <-slots }()
				// 事件处理链全程处理外部可控输入，未恢复的 panic 会终止整个进程。
				defer safego.Recover("napcat event")
				if err := s.handleEvent(sessionCtx, client, evt); err != nil {
					log.Printf("handle napcat event failed: %v", err)
				}
			}(ev)
		}
	}
}

func (s Server) syncGroupJoinRequests(ctx context.Context, sender SDKSender) {
	syncOnce := func() {
		// 恢复边界放在每轮工作上，一轮 panic 不会让整个同步循环静默退出。
		defer safego.Recover("group request sync")
		records, err := sender.FetchGroupJoinRequests(ctx, groupRequestSyncCount)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("fetch group join requests for automatic sync failed: %v", err)
			}
			if len(records) == 0 {
				return
			}
		}
		if err := s.Handler.ReconcileGroupJoinRequests(ctx, records); err != nil && ctx.Err() == nil {
			log.Printf("reconcile group join requests failed: %v", err)
		}
	}
	syncOnce()
	ticker := time.NewTicker(groupRequestSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncOnce()
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

func (s Server) handleEvent(ctx context.Context, client *napcatsdk.Client, ev event.Event) error {
	if s.Handler == nil {
		return nil
	}
	switch e := ev.(type) {
	case *event.GroupMessage:
		if err := markGroupMessageRead(ctx, client, e); err != nil {
			log.Printf("mark group message as read failed: %v", err)
		}
		return s.Handler.HandleGroupMessage(ctx, toGroupMessage(e))
	case *event.UnknownEvent:
		if record, ok, err := grouprequest.RecordFromEvent(e.Raw()); err != nil {
			return err
		} else if ok {
			return s.Handler.HandleGroupJoinRequest(ctx, record)
		}
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

func toGroupMessage(e *event.GroupMessage) bot.GroupMessage {
	return bot.GroupMessage{
		GroupID:    e.GroupID,
		UserID:     e.UserID,
		SelfID:     e.SelfID(),
		Text:       e.Message.Text(),
		RawMessage: e.RawMessage,
		MessageID:  e.MessageID,
		Reply:      extractReplyRef(e.Message),
		IsSelf:     e.UserID == e.SelfID(),
		AtUsers:    extractAtUsers(e.Message),
		Segments:   e.Message,
	}
}

func markGroupMessageRead(ctx context.Context, client *napcatsdk.Client, e *event.GroupMessage) error {
	groupID := strconv.FormatInt(e.GroupID, 10)
	_, err := client.API().MarkGroupMsgAsRead(ctx, api.MarkGroupMsgAsReadRequest{
		GroupID: &groupID,
	})
	return err
}

func extractReplyRef(chain message.Chain) bot.ReplyRef {
	for _, segment := range chain {
		if segment.Type != "reply" {
			continue
		}
		id, _ := strconv.ParseInt(strings.TrimSpace(segment.String("id")), 10, 64)
		seq, _ := strconv.ParseInt(strings.TrimSpace(segment.String("seq")), 10, 64)
		return bot.ReplyRef{ID: id, Seq: seq}
	}
	return bot.ReplyRef{}
}

type SDKSender struct {
	client     *napcatsdk.Client
	flashFiles *flashfile.Stager
}

func (s SDKSender) SendGroupText(ctx context.Context, groupID int64, text string) error {
	return s.SendGroupMessage(ctx, groupID, message.ChainOf(message.Text(text)))
}

func (s SDKSender) SendGroupMessage(ctx context.Context, groupID int64, msg message.Chain) error {
	encoded, err := api.NewOB11Message(msg)
	if err != nil {
		return fmt.Errorf("encode group message: %w", err)
	}
	groupIDText := strconv.FormatInt(groupID, 10)
	_, err = s.client.API().SendGroupMsg(ctx, api.SendGroupMsgRequest{
		GroupID: &groupIDText,
		Message: encoded,
	})
	return err
}

func (s SDKSender) SendGroupFlashFile(ctx context.Context, groupID int64, source, name string) error {
	if groupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	filePath := source
	if strings.HasPrefix(strings.ToLower(source), "http://") || strings.HasPrefix(strings.ToLower(source), "https://") {
		if s.flashFiles == nil {
			return fmt.Errorf("flash file stager is not initialized")
		}
		staged, err := s.flashFiles.Stage(ctx, source, name)
		if err != nil {
			return fmt.Errorf("stage remote flash file: %w", err)
		}
		filePath = staged
	} else if path.Clean(source) != source || !strings.HasPrefix(source, "/app/jxh-media/") || path.Base(source) != name {
		return fmt.Errorf("invalid local flash file source")
	}

	files, err := json.Marshal(filePath)
	if err != nil {
		return fmt.Errorf("encode flash file path: %w", err)
	}
	createResp, err := s.client.API().CreateFlashTask(ctx, api.CreateFlashTaskRequest{
		Files: api.CreateFlashTaskRequestFilesUnion{Raw: files},
		Name:  &name,
	})
	if err != nil {
		return fmt.Errorf("create flash task: %w", err)
	}
	fileSetID, err := decodeCreateFlashResponse(createResp)
	if err != nil {
		return err
	}
	groupIDText := strconv.FormatInt(groupID, 10)
	sendResp, err := s.client.API().SendFlashMsg(ctx, api.SendFlashMsgRequest{
		FilesetID: fileSetID,
		GroupID:   &groupIDText,
	})
	if err != nil {
		return fmt.Errorf("send flash message: %w", err)
	}
	if err := validateSendFlashResponse(sendResp); err != nil {
		return err
	}
	return nil
}

func decodeCreateFlashResponse(value any) (string, error) {
	var response struct {
		Result                    *oneBotInt64 `json:"result"`
		ErrMsg                    string       `json:"errMsg"`
		CreateFlashTransferResult struct {
			FileSetID string `json:"fileSetId"`
		} `json:"createFlashTransferResult"`
	}
	if err := decodeDynamicValue(value, &response); err != nil {
		return "", fmt.Errorf("decode create flash task response: %w", err)
	}
	if response.Result == nil {
		return "", fmt.Errorf("create flash task response is missing result")
	}
	if *response.Result != 0 {
		return "", fmt.Errorf("create flash task failed with result %d: %s", *response.Result, response.ErrMsg)
	}
	fileSetID := strings.TrimSpace(response.CreateFlashTransferResult.FileSetID)
	if fileSetID == "" {
		return "", fmt.Errorf("create flash task response is missing fileSetId")
	}
	return fileSetID, nil
}

func validateSendFlashResponse(value any) error {
	var response struct {
		ErrCode *oneBotInt64 `json:"errCode"`
		ErrMsg  string       `json:"errMsg"`
		Rsp     *struct {
			SendStatus []struct {
				Result *oneBotInt64 `json:"result"`
				Msg    string       `json:"msg"`
			} `json:"sendStatus"`
		} `json:"rsp"`
	}
	if err := decodeDynamicValue(value, &response); err != nil {
		return fmt.Errorf("decode send flash message response: %w", err)
	}
	if response.ErrCode == nil {
		return fmt.Errorf("send flash message response is missing errCode")
	}
	if *response.ErrCode != 0 {
		return fmt.Errorf("send flash message failed with errCode %d: %s", *response.ErrCode, response.ErrMsg)
	}
	if response.Rsp == nil || len(response.Rsp.SendStatus) == 0 {
		return fmt.Errorf("send flash message response is missing sendStatus")
	}
	for i, status := range response.Rsp.SendStatus {
		if status.Result == nil {
			return fmt.Errorf("send flash message status %d is missing result", i+1)
		}
		if *status.Result != 0 {
			return fmt.Errorf("send flash message status %d failed with result %d: %s", i+1, *status.Result, status.Msg)
		}
	}
	return nil
}

func decodeDynamicValue(value, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

type oneBotInt64 int64

func (v *oneBotInt64) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		data = data[1 : len(data)-1]
	}
	parsed, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return fmt.Errorf("decode OneBot integer %q: %w", data, err)
	}
	*v = oneBotInt64(parsed)
	return nil
}

type quoteSender struct {
	UserID   oneBotInt64 `json:"user_id"`
	Card     string      `json:"card"`
	Nickname string      `json:"nickname"`
}

type oneBotMessage struct {
	Chain message.Chain
}

func (m *oneBotMessage) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || (len(data) > 0 && data[0] == '"') {
		return nil
	}
	if len(data) == 0 || data[0] != '[' {
		return fmt.Errorf("decode OneBot message: expected segment array or string")
	}
	return json.Unmarshal(data, &m.Chain)
}

type quoteMessage struct {
	MessageID  oneBotInt64   `json:"message_id"`
	MessageSeq oneBotInt64   `json:"message_seq"`
	GroupID    oneBotInt64   `json:"group_id"`
	UserID     oneBotInt64   `json:"user_id"`
	RawMessage string        `json:"raw_message"`
	Sender     quoteSender   `json:"sender"`
	Message    oneBotMessage `json:"message"`
}

func (s SDKSender) GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error) {
	resp, err := s.client.API().GetGroupMemberInfo(ctx, api.GetGroupMemberInfoRequest{
		GroupID: strconv.FormatInt(groupID, 10),
		UserID:  strconv.FormatInt(userID, 10),
		NoCache: &api.GetGroupMemberInfoRequestNoCacheUnion{Raw: []byte("true")},
	})
	if err != nil {
		return "", err
	}
	if resp.Role == nil {
		return "", fmt.Errorf("NapCat 群成员信息缺少 role")
	}
	return *resp.Role, nil
}

const (
	maxQuoteReplyDepth = 3
	maxQuoteReplyNodes = 30
)

type quoteResolver struct {
	sender  SDKSender
	groupID int64
	byID    map[int64]*bot.QuotedMessage
	bySeq   map[int64]*bot.QuotedMessage
	nodes   int
}

func (s SDKSender) GetQuoteMessages(ctx context.Context, groupID int64, ref bot.ReplyRef, count int) ([]bot.QuotedMessage, error) {
	resolver := quoteResolver{
		sender: s, groupID: groupID,
		byID: make(map[int64]*bot.QuotedMessage), bySeq: make(map[int64]*bot.QuotedMessage),
	}
	messageSeq := ref.Seq
	var history []quoteMessage
	var err error
	if messageSeq != 0 {
		history, err = resolver.history(ctx, messageSeq, count)
	}
	if messageSeq == 0 || err != nil || quoteMessageIndex(history, messageSeq) < 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if ref.ID == 0 {
			if err != nil {
				return nil, fmt.Errorf("按回复序号获取群历史失败: %w", err)
			}
			return nil, fmt.Errorf("NapCat 返回的群历史中未找到回复序号 %d", messageSeq)
		}
		target, idErr := resolver.messageByID(ctx, ref.ID)
		if idErr != nil {
			return nil, errors.Join(err, fmt.Errorf("按回复消息 ID 获取消息失败: %w", idErr))
		}
		messageSeq = target.MessageSeq
		if messageSeq == 0 {
			return nil, fmt.Errorf("NapCat 消息 %d 缺少 message_seq", ref.ID)
		}
		history, err = resolver.history(ctx, messageSeq, count)
		if err != nil {
			return nil, fmt.Errorf("按回复消息序号获取群历史失败: %w", err)
		}
	}
	targetIndex := quoteMessageIndex(history, messageSeq)
	if targetIndex < 0 {
		return nil, fmt.Errorf("NapCat 返回的群历史中未找到回复序号 %d", messageSeq)
	}
	start := max(0, targetIndex-count+1)
	messages := make([]bot.QuotedMessage, 0, targetIndex-start+1)
	for _, wire := range history[start : targetIndex+1] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		base := resolver.remember(wire)
		quoted := *base
		quoted.Reply = nil
		resolver.expand(ctx, &quoted, base, 1, map[*bot.QuotedMessage]struct{}{base: {}})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		messages = append(messages, quoted)
	}
	s.enrichQuoteAtNames(ctx, groupID, messages)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return messages, nil
}

func quoteMessageIndex(messages []quoteMessage, seq int64) int {
	return slices.IndexFunc(messages, func(message quoteMessage) bool {
		return int64(message.MessageSeq) == seq
	})
}

func (r *quoteResolver) history(ctx context.Context, seq int64, count int) ([]quoteMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if seq == 0 {
		return nil, fmt.Errorf("message_seq is required")
	}
	var history struct {
		Messages []quoteMessage `json:"messages"`
	}
	messageSeq := strconv.FormatInt(seq, 10)
	err := r.sender.client.API().Call(ctx, string(api.ActionGetGroupMsgHistory), api.GetGroupMsgHistoryRequest{
		GroupID:      strconv.FormatInt(r.groupID, 10),
		MessageSeq:   &messageSeq,
		Count:        float64(count),
		ReverseOrder: true,
	}, &history)
	if err != nil {
		return nil, err
	}
	for _, message := range history.Messages {
		r.remember(message)
	}
	return history.Messages, nil
}

func (r *quoteResolver) remember(message quoteMessage) *bot.QuotedMessage {
	id, seq := int64(message.MessageID), int64(message.MessageSeq)
	cached := r.bySeq[seq]
	if cached == nil {
		cached = r.byID[id]
	}
	quoted := message.quoted()
	if cached == nil {
		cached = &quoted
	} else {
		*cached = quoted
	}
	if id != 0 {
		r.byID[id] = cached
	}
	if seq != 0 {
		r.bySeq[seq] = cached
	}
	return cached
}

func (r *quoteResolver) resolve(ctx context.Context, ref bot.ReplyRef) (*bot.QuotedMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var seqErr error
	if ref.Seq != 0 {
		if cached := r.bySeq[ref.Seq]; cached != nil {
			return cached, nil
		}
		history, err := r.history(ctx, ref.Seq, 1)
		if err == nil {
			if index := quoteMessageIndex(history, ref.Seq); index >= 0 {
				return r.remember(history[index]), nil
			}
			err = fmt.Errorf("NapCat 返回的群历史中未找到回复序号 %d", ref.Seq)
		}
		seqErr = fmt.Errorf("按回复序号获取消息失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.ID != 0 {
		message, err := r.messageByID(ctx, ref.ID)
		if err == nil {
			return message, nil
		}
		return nil, errors.Join(seqErr, fmt.Errorf("按回复消息 ID 获取消息失败: %w", err))
	}
	if seqErr != nil {
		return nil, seqErr
	}
	return nil, fmt.Errorf("回复引用缺少 id 和 seq")
}

func (r *quoteResolver) messageByID(ctx context.Context, id int64) (*bot.QuotedMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached := r.byID[id]; cached != nil {
		return cached, nil
	}
	var message quoteMessage
	err := r.sender.client.API().Call(ctx, string(api.ActionGetMsg), api.GetMsgRequest{
		MessageID: api.GetMsgRequestMessageIDUnion{Raw: []byte(strconv.FormatInt(id, 10))},
	}, &message)
	if err != nil {
		return nil, err
	}
	if int64(message.MessageID) != id {
		return nil, fmt.Errorf("NapCat get_msg 返回了不匹配的 message_id %d", message.MessageID)
	}
	if message.GroupID != 0 && int64(message.GroupID) != r.groupID {
		return nil, fmt.Errorf("NapCat get_msg 返回了其他群的消息")
	}
	return r.remember(message), nil
}

func (r *quoteResolver) expand(ctx context.Context, target, base *bot.QuotedMessage, depth int, visiting map[*bot.QuotedMessage]struct{}) {
	if ctx.Err() != nil {
		return
	}
	ref := extractReplyRef(base.Message)
	if ref == (bot.ReplyRef{}) {
		return
	}
	if depth > maxQuoteReplyDepth || r.nodes >= maxQuoteReplyNodes {
		target.Reply = quoteFallback("[更早的回复已省略]")
		return
	}
	r.nodes++
	next, err := r.resolve(ctx, ref)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("get replied quote message failed: group=%d reply_id=%d reply_seq=%d: %v", r.groupID, ref.ID, ref.Seq, err)
			target.Reply = quoteFallback("[引用消息不可用]")
		}
		return
	}
	if _, cycle := visiting[next]; cycle {
		target.Reply = quoteFallback("[循环回复已省略]")
		return
	}
	reply := *next
	reply.Reply = nil
	target.Reply = &reply
	visiting[next] = struct{}{}
	r.expand(ctx, &reply, next, depth+1, visiting)
	delete(visiting, next)
}

func quoteFallback(text string) *bot.QuotedMessage {
	return &bot.QuotedMessage{Nickname: "匿名", RawMessage: text}
}

func (s SDKSender) enrichQuoteAtNames(ctx context.Context, groupID int64, messages []bot.QuotedMessage) {
	names := map[string]string{"all": "全体成员"}
	var enrich func(*bot.QuotedMessage)
	enrich = func(quoted *bot.QuotedMessage) {
		if quoted == nil || ctx.Err() != nil {
			return
		}
		chain := message.ChainOf(quoted.Message...)
		for segmentIndex, segment := range chain {
			if ctx.Err() != nil {
				return
			}
			if segment.Type != "at" || strings.TrimSpace(segment.String("name")) != "" ||
				strings.TrimSpace(segment.String("card")) != "" || strings.TrimSpace(segment.String("nickname")) != "" {
				continue
			}
			qq := strings.TrimSpace(segment.String("qq"))
			name, known := names[qq]
			if !known {
				userID, err := strconv.ParseInt(qq, 10, 64)
				if err != nil {
					continue
				}
				resp, err := s.client.API().GetGroupMemberInfo(ctx, api.GetGroupMemberInfoRequest{
					GroupID: strconv.FormatInt(groupID, 10),
					UserID:  strconv.FormatInt(userID, 10),
					NoCache: &api.GetGroupMemberInfoRequestNoCacheUnion{Raw: []byte("true")},
				})
				if err == nil {
					card := ""
					if resp.Card != nil {
						card = strings.TrimSpace(*resp.Card)
					}
					name = senderNickname(quoteSender{Card: card, Nickname: strings.TrimSpace(resp.Nickname)})
				}
				names[qq] = name
			}
			if name == "" {
				continue
			}
			data, ok := segment.Data.(map[string]any)
			if !ok {
				continue
			}
			data = maps.Clone(data)
			data["name"] = name
			chain[segmentIndex].Data = data
		}
		quoted.Message = chain
		enrich(quoted.Reply)
	}
	for i := range messages {
		enrich(&messages[i])
	}
}

func (m quoteMessage) quoted() bot.QuotedMessage {
	userID := int64(m.UserID)
	if userID == 0 {
		userID = int64(m.Sender.UserID)
	}
	return bot.QuotedMessage{
		MessageID: int64(m.MessageID), MessageSeq: int64(m.MessageSeq), UserID: userID,
		Nickname: senderNickname(m.Sender), RawMessage: m.RawMessage, Message: m.Message.Chain,
	}
}

func (s SDKSender) ResolveImage(ctx context.Context, file string) (string, error) {
	data, err := s.client.API().GetImage(ctx, api.GetImageRequest{File: &file})
	if err != nil {
		return "", err
	}
	if data.URL != nil && *data.URL != "" {
		return *data.URL, nil
	}
	if data.File != nil {
		return *data.File, nil
	}
	return "", nil
}

func (s SDKSender) SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error {
	_, err := s.client.API().SetGroupBan(ctx, api.SetGroupBanRequest{
		GroupID:  strconv.FormatInt(groupID, 10),
		UserID:   strconv.FormatInt(userID, 10),
		Duration: api.SetGroupBanRequestDurationUnion{Raw: []byte(strconv.FormatInt(int64(duration.Seconds()), 10))},
	})
	return err
}

func (s SDKSender) SetRestart(ctx context.Context) error {
	_, err := s.client.API().SetRestart(ctx, api.SetRestartRequest{})
	return err
}

func (s SDKSender) FetchGroupJoinRequests(ctx context.Context, count int) ([]grouprequest.Record, error) {
	var resp struct {
		InvitedRequests []json.RawMessage `json:"invited_requests"`
		InvitedRequest  []json.RawMessage `json:"InvitedRequest"`
		JoinRequests    []json.RawMessage `json:"join_requests"`
	}
	err := s.client.API().Call(ctx, string(api.ActionGetGroupSystemMsg), api.GetGroupSystemMsgRequest{
		Count: api.GetGroupSystemMsgRequestCountUnion{Raw: []byte(strconv.Itoa(count))},
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("fetch group system messages: %w", err)
	}
	joinRequests, err := decodeGroupSystemMessages(resp.JoinRequests, false)
	var decodeErrors []error
	if err != nil {
		decodeErrors = append(decodeErrors, fmt.Errorf("decode join requests: %w", err))
	}
	invitedRaw := resp.InvitedRequests
	if len(invitedRaw) == 0 {
		invitedRaw = resp.InvitedRequest
	}
	invitedRequests, err := decodeGroupSystemMessages(invitedRaw, true)
	if err != nil {
		decodeErrors = append(decodeErrors, fmt.Errorf("decode invited requests: %w", err))
	}
	return grouprequest.RecordsFromSystemMessages(joinRequests, invitedRequests), errors.Join(decodeErrors...)
}

type groupSystemMessageWire struct {
	RequestID    json.RawMessage `json:"request_id"`
	RequesterUin json.RawMessage `json:"requester_uin"`
	RequesterID  json.RawMessage `json:"requester_id"`
	UserID       json.RawMessage `json:"user_id"`
	Uin          json.RawMessage `json:"uin"`
	InvitorUin   json.RawMessage `json:"invitor_uin"`
	GroupID      json.RawMessage `json:"group_id"`
	Message      string          `json:"message"`
	Checked      bool            `json:"checked"`
}

func decodeGroupSystemMessages(rawMessages []json.RawMessage, invited bool) ([]grouprequest.SystemMessage, error) {
	messages := make([]grouprequest.SystemMessage, 0, len(rawMessages))
	var decodeErrors []error
	for i, raw := range rawMessages {
		message, err := decodeGroupSystemMessage(raw, invited)
		if err != nil {
			decodeErrors = append(decodeErrors, fmt.Errorf("item %d: %w", i, err))
			continue
		}
		messages = append(messages, message)
	}
	return messages, errors.Join(decodeErrors...)
}

func decodeGroupSystemMessage(raw json.RawMessage, invited bool) (grouprequest.SystemMessage, error) {
	var wire groupSystemMessageWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return grouprequest.SystemMessage{}, fmt.Errorf("decode group system message: %w", err)
	}
	requestID, err := decimalJSONValue(wire.RequestID, "request_id", true)
	if err != nil {
		return grouprequest.SystemMessage{}, err
	}
	if strings.TrimLeft(requestID, "0") == "" {
		return grouprequest.SystemMessage{}, fmt.Errorf("group system message request_id must be positive")
	}
	groupID, err := firstInt64JSONValue("group_id", wire.GroupID)
	if err != nil {
		return grouprequest.SystemMessage{}, err
	}
	var userID int64
	if invited {
		userID, err = firstInt64JSONValue("invitor_uin", wire.InvitorUin)
	} else {
		// Current NapCat versions expose the join applicant as invitor_uin.
		// Prefer explicit requester fields if a future response provides them.
		userID, err = firstInt64JSONValue("requester", wire.RequesterUin, wire.RequesterID, wire.UserID, wire.Uin, wire.InvitorUin)
	}
	if err != nil {
		return grouprequest.SystemMessage{}, err
	}
	return grouprequest.SystemMessage{
		RequestID: requestID,
		GroupID:   groupID,
		UserID:    userID,
		Message:   wire.Message,
		Checked:   wire.Checked,
		RawJSON:   string(raw),
	}, nil
}

func firstInt64JSONValue(field string, values ...json.RawMessage) (int64, error) {
	// 调用方按优先级传入多个候选字段。某个候选格式不对不代表整条记录无效，
	// 继续尝试后面的；全部失败时返回第一个错误，保留具体原因。
	var firstErr error
	for _, raw := range values {
		value, err := decimalJSONValue(raw, field, false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if value == "" || value == "0" {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("decode %s %q: %w", field, value, err)
			}
			continue
		}
		if parsed == 0 {
			continue
		}
		return parsed, nil
	}
	if firstErr != nil {
		return 0, firstErr
	}
	return 0, fmt.Errorf("group system message %s is missing or zero", field)
}

func decimalJSONValue(raw json.RawMessage, field string, required bool) (string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		if required {
			return "", fmt.Errorf("group system message %s is missing", field)
		}
		return "", nil
	}
	if value[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("decode group system message %s: %w", field, err)
		}
		value = strings.TrimSpace(value)
	}
	if value == "" {
		if required {
			return "", fmt.Errorf("group system message %s is empty", field)
		}
		return "", nil
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("group system message %s %q is not a decimal integer", field, value)
		}
	}
	return value, nil
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

func senderNickname(sender quoteSender) string {
	if sender.Card != "" {
		return sender.Card
	}
	return sender.Nickname
}
