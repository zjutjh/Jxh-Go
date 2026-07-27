package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/safego"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
	"github.com/zjutjh/napcat-sdk/message"
)

type GroupCommandRouter struct {
	ai            *ai.Service
	aiSlots       chan struct{}
	reloader      *knowledge.Syncer
	admin         *commands.AdminHandler
	quote         *quote.Client
	groupRequests *grouprequest.Service
	triggerStats  *triggerstats.Service
}

const maxQuoteMessages = 10

const botHelpText = `精小弘命令菜单：
/test - 检查精小弘是否存活！
/reload - 重新加载知识库（刷新精小弘的记忆？！）
/ai <问题> - 用大模型查找一些知识库中的答案（让精小弘更聪明？！）
/q [数量] - 生成被回复消息及其之前最多 10 条消息的引用图（表情包生成器ww）
@bot /admin - 查看管理员命令和权限说明（必须 @精小弘）
访问 https://status.fridayssheep.top/status/jxh 来确定精小弘是否正常！`

const adminHelpText = `管理员命令（当前群群主或群管理员可使用）：
以下命令必须先 @精小弘：
@bot /admin ban <时长> @用户1 @用户2 ... - 禁言不听话的小朋友（可批量）
@bot /admin restart - 重启 NapCat 框架
@bot /admin 定时任务 查看
@bot /admin 定时任务 添加 每天 <HH:MM> <当前群号> <消息>
@bot /admin 定时任务 添加 单次 <YYYY-MM-DD HH:MM> <当前群号> <消息>
@bot /admin 定时任务 移除 <任务ID>
@bot /admin 群申请 导出 [数量] - 不填数量时导出全部，本地按来源群分文件
@bot /admin 词条统计 [7d|30d|全部] - 本地导出全部群统计
精小弘不能禁言群主、群管理员或机器人自己ε=( o｀ω′)ノ`

func NewGroupCommandRouter(opts Options) *GroupCommandRouter {
	return &GroupCommandRouter{
		ai:            opts.AI,
		aiSlots:       make(chan struct{}, 2),
		reloader:      opts.Reloader,
		admin:         opts.Admin,
		quote:         opts.Quote,
		groupRequests: opts.GroupRequests,
		triggerStats:  opts.TriggerStats,
	}
}

func (r *GroupCommandRouter) Handle(ctx context.Context, msg GroupMessage, sender Sender) (bool, error) {
	text := strings.ReplaceAll(strings.TrimSpace(msg.Text), "　", " ")
	if text == "" {
		if mentionsSelf(msg) {
			return true, sender.SendGroupText(ctx, msg.GroupID, botHelpText)
		}
		return false, nil
	}
	switch {
	case text == "/test":
		return true, sender.SendGroupText(ctx, msg.GroupID, "精小弘正常\n访问：https://status.fridayssheep.top/status/jxh 以确定各项服务概况")
	case text == "/reload":
		return true, r.handleReload(ctx, msg, sender)
	case text == "/q" || strings.HasPrefix(text, "/q "):
		return true, r.handleQuote(ctx, msg, sender, text)
	case text == "/ai" || strings.HasPrefix(text, "/ai "):
		return true, r.startAI(ctx, msg, sender, text)
	case text == "/admin" || strings.HasPrefix(text, "/admin "):
		if !mentionsSelf(msg) {
			return false, nil
		}
		return true, r.handleAdmin(ctx, msg, sender, text)
	default:
		return false, nil
	}
}

func mentionsSelf(msg GroupMessage) bool {
	if msg.SelfID == 0 {
		return false
	}
	for _, user := range msg.AtUsers {
		if user == msg.SelfID {
			return true
		}
	}
	return false
}

func (r *GroupCommandRouter) handleReload(ctx context.Context, msg GroupMessage, sender Sender) error {
	authorized, err := authorizeNativeAdmin(ctx, msg, sender)
	if err != nil || !authorized {
		return err
	}
	if err := r.reloader.Sync(ctx); err != nil {
		// 底层 *url.Error 会带上完整的 WPS 分享地址（含 query 参数），不能进群。
		log.Printf("reload knowledge failed: group=%d: %v", msg.GroupID, err)
		return sender.SendGroupText(ctx, msg.GroupID, "重载失败，请联系管理员查看服务日志")
	}
	return sender.SendGroupText(ctx, msg.GroupID, "重载成功")
}

func (r *GroupCommandRouter) handleQuote(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	count, err := parseQuoteCount(text)
	if err != nil {
		return sender.SendGroupText(ctx, msg.GroupID, "用法：回复一条消息后发送 /q [1-10]")
	}
	if msg.ReplyMessageID == 0 {
		return sender.SendGroupText(ctx, msg.GroupID, "请回复一条消息后使用 /q")
	}
	quoted, err := sender.GetQuoteMessages(ctx, msg.GroupID, msg.ReplyMessageID, count)
	if err != nil {
		log.Printf("get quote messages failed: group=%d message=%d: %v", msg.GroupID, msg.ReplyMessageID, err)
		return sender.SendGroupText(ctx, msg.GroupID, "获取被引用消息失败，请稍后再试")
	}
	inputs := make([]quote.MessageInput, 0, len(quoted))
	for _, message := range quoted {
		inputs = append(inputs, quote.MessageInput{
			UserID: message.UserID, Nickname: message.Nickname,
			RawMessage: message.RawMessage, Message: message.Message,
		})
	}
	payload := quote.BuildPayload(ctx, inputs, sender.ResolveImage)
	if len(payload) == 0 {
		return sender.SendGroupText(ctx, msg.GroupID, "被引用消息内容为空")
	}
	image, err := r.quote.Generate(ctx, payload)
	if err != nil {
		// quote 客户端会把服务端响应正文拼进错误，不能直接发进群。
		log.Printf("generate quote image failed: group=%d: %v", msg.GroupID, err)
		return sender.SendGroupText(ctx, msg.GroupID, "引用图生成失败，请稍后再试")
	}
	return sender.SendGroupMessage(ctx, msg.GroupID, message.ChainOf(message.Image("base64://"+image)))
}

func parseQuoteCount(text string) (int, error) {
	args := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, "/q")))
	if len(args) == 0 {
		return 1, nil
	}
	if len(args) != 1 {
		return 0, fmt.Errorf("invalid quote arguments")
	}
	count, err := strconv.Atoi(args[0])
	if err != nil || count < 1 || count > maxQuoteMessages {
		return 0, fmt.Errorf("quote count must be between 1 and %d", maxQuoteMessages)
	}
	return count, nil
}

func (r *GroupCommandRouter) startAI(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	if r.ai == nil {
		return sender.SendGroupText(ctx, msg.GroupID, ai.DisabledAnswer)
	}
	select {
	case r.aiSlots <- struct{}{}:
		go func() {
			defer func() { <-r.aiSlots }()
			// 问题文本与模型输出都不可信，未恢复的 panic 会终止整个进程。
			defer safego.Recover("ai command")
			if err := r.handleAI(ctx, msg, sender, text); err != nil {
				log.Printf("handle ai command failed: %v", err)
				if sendErr := sender.SendGroupText(ctx, msg.GroupID, "AI问答失败，请稍后再试"); sendErr != nil {
					log.Printf("send ai failure message failed: %v", sendErr)
				}
			}
		}()
		return nil
	default:
		return sender.SendGroupText(ctx, msg.GroupID, "AI问答繁忙，请稍后再试")
	}
}

func (r *GroupCommandRouter) handleAI(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	question := strings.TrimSpace(strings.TrimPrefix(text, "/ai"))
	answer, sourceKeys, err := r.ai.AnswerWithSources(ctx, question)
	if err != nil {
		return err
	}
	// 空文本会被 QQ 渲染成不可查看的消息，这里兜底避免任何上游路径下发空串。
	if strings.TrimSpace(answer) == "" {
		log.Printf("ai answer was empty for group %d, sending fallback", msg.GroupID)
		answer = ai.EmptyKnowledgeAnswer
	}
	if err := sender.SendGroupText(ctx, msg.GroupID, answer); err != nil {
		return err
	}
	// Only record retrieval stats after the answer was actually delivered.
	if r.triggerStats != nil {
		if err := r.triggerStats.RecordAIRetrievals(ctx, sourceKeys, msg.GroupID); err != nil {
			// 统计失败不影响 /ai 的正常回答，避免新增表异常扩大成问答故障。
			log.Printf("record ai retrieval trigger failed: %v", err)
		}
	}
	return nil
}

func (r *GroupCommandRouter) handleAdmin(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	adminText := strings.TrimSpace(strings.TrimPrefix(text, "/admin"))
	authorized, err := authorizeNativeAdmin(ctx, msg, sender)
	if err != nil || !authorized {
		return err
	}
	if adminText == "" {
		return sender.SendGroupText(ctx, msg.GroupID, adminHelpText)
	}
	if strings.HasPrefix(adminText, "群申请 ") {
		return r.handleGroupRequestAdmin(ctx, msg, sender, strings.TrimSpace(strings.TrimPrefix(adminText, "群申请 ")))
	}
	if strings.HasPrefix(adminText, "词条统计") {
		return r.handleTriggerStats(ctx, msg, sender, strings.TrimSpace(strings.TrimPrefix(adminText, "词条统计")))
	}
	if adminText == "restart" {
		if err := sender.SetRestart(ctx); err != nil {
			return err
		}
		return sender.SendGroupText(ctx, msg.GroupID, "已请求重启 NapCat")
	}
	if strings.HasPrefix(adminText, "ban ") {
		atUsers := targetAtUsers(msg)
		if len(atUsers) == 0 {
			return sender.SendGroupText(ctx, msg.GroupID, "请 @ 要禁言的用户")
		}
		duration, err := parseBanDuration(strings.TrimSpace(strings.TrimPrefix(adminText, "ban ")))
		if err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, "禁言时间格式不正确")
		}
		failed := 0
		for _, userID := range atUsers {
			if err := sender.SetGroupBan(ctx, msg.GroupID, userID, duration); err != nil {
				log.Printf("ban group user failed: group=%d user=%d: %v", msg.GroupID, userID, err)
				failed++
			}
		}
		if failed > 0 {
			return sender.SendGroupText(ctx, msg.GroupID, fmt.Sprintf("已禁言 %d 人，%d 人失败\n提示：精小弘不能禁言群主、群管理员或机器人自己！", len(atUsers)-failed, failed))
		}
		return sender.SendGroupText(ctx, msg.GroupID, fmt.Sprintf("已禁言 %d 人", len(atUsers)))
	}
	resp, err := r.admin.Execute(ctx, msg.GroupID, adminText)
	if err != nil {
		return err
	}
	return sender.SendGroupText(ctx, msg.GroupID, resp)
}

func authorizeNativeAdmin(ctx context.Context, msg GroupMessage, sender Sender) (bool, error) {
	role, err := sender.GetGroupMemberRole(ctx, msg.GroupID, msg.UserID)
	if err != nil {
		log.Printf("query admin actor role failed: group=%d user=%d: %v", msg.GroupID, msg.UserID, err)
		return false, sender.SendGroupText(ctx, msg.GroupID, "暂时无法确认群身份，请稍后重试")
	}
	normalizedRole, ok := commands.NormalizeGroupRole(role)
	if !ok {
		log.Printf("query admin actor role returned invalid role: group=%d user=%d role=%q", msg.GroupID, msg.UserID, role)
		return false, sender.SendGroupText(ctx, msg.GroupID, "暂时无法确认群身份，请稍后重试")
	}
	if !commands.IsNativeGroupAdmin(normalizedRole) {
		return false, sender.SendGroupText(ctx, msg.GroupID, "~你好像没有权限执行该项操作耶~")
	}
	return true, nil
}

func (r *GroupCommandRouter) handleGroupRequestAdmin(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	switch {
	case strings.HasPrefix(text, "导出"):
		limit, err := parseOptionalLimit(strings.TrimSpace(strings.TrimPrefix(text, "导出")))
		if err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, "格式：@bot /admin 群申请 导出 [正整数]")
		}
		result, err := r.groupRequests.Export(ctx, limit)
		if err != nil {
			return err
		}
		if result.Count == 0 {
			return sender.SendGroupText(ctx, msg.GroupID, "暂无群申请记录可导出")
		}
		return sender.SendGroupText(ctx, msg.GroupID, fmt.Sprintf("已在本地导出群申请 %d 条，按 %d 个群分别保存到：%s", result.Count, len(result.Files), result.Dir))
	default:
		return sender.SendGroupText(ctx, msg.GroupID, "格式：@bot /admin 群申请 导出 [正整数]")
	}
}

func (r *GroupCommandRouter) handleTriggerStats(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	days, err := parseStatsDays(text)
	if err != nil {
		return sender.SendGroupText(ctx, msg.GroupID, "格式：@bot /admin 词条统计 [7d|30d|全部]")
	}
	result, err := r.triggerStats.ExportForDays(ctx, days)
	if err != nil {
		log.Printf("export trigger stats failed: %v", err)
		return sender.SendGroupText(ctx, msg.GroupID, "词条统计服务暂不可用")
	}
	if result.Count == 0 {
		return sender.SendGroupText(ctx, msg.GroupID, "暂无词条统计记录可导出")
	}
	return sender.SendGroupText(ctx, msg.GroupID, fmt.Sprintf("已在本地导出全部群的词条统计 %d 项，文件保存在：%s", result.Count, result.Path))
}

func targetAtUsers(msg GroupMessage) []int64 {
	if msg.SelfID == 0 {
		return msg.AtUsers
	}
	out := make([]int64, 0, len(msg.AtUsers))
	for _, user := range msg.AtUsers {
		if user != msg.SelfID {
			out = append(out, user)
		}
	}
	return out
}

func parseBanDuration(raw string) (time.Duration, error) {
	fields := strings.Fields(raw)
	if len(fields) != 1 {
		return 0, fmt.Errorf("invalid duration")
	}
	duration, err := time.ParseDuration(fields[0])
	if err != nil {
		duration, err = time.ParseDuration(fields[0] + "s")
	}
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return duration, nil
}

func parseOptionalLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("invalid limit")
	}
	return limit, nil
}

func parseStatsDays(raw string) (int, error) {
	switch strings.TrimSpace(raw) {
	case "", "7d":
		return 7, nil
	case "30d":
		return 30, nil
	case "全部":
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid range")
	}
}
