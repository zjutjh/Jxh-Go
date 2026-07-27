package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/scheduler"
)

const (
	GroupRoleOwner  = "owner"
	GroupRoleAdmin  = "admin"
	GroupRoleMember = "member"

	schedFmtHelp = "格式：@bot /admin 定时任务 添加 <每天|单次> <时间> <当前群ID> <消息内容>\n每天任务时间：HH:MM\n单次任务时间：YYYY-MM-DD HH:MM"
)

type SchedulerStore interface {
	ListScheduledJobs(ctx context.Context, groupID int64) ([]ScheduledJobView, error)
	AddScheduledJob(ctx context.Context, job ScheduledJobInput) (uint64, error)
	RemoveScheduledJob(ctx context.Context, groupID int64, id uint64) (bool, error)
}

type ScheduledJobInput struct {
	Type      string
	TimeHHMM  string
	RunDate   *time.Time
	GroupID   int64
	Message   string
	CreatedAt time.Time
}

type ScheduledJobView struct {
	ID       uint64
	Type     string
	TimeHHMM string
	RunDate  *time.Time
	GroupID  int64
	Message  string
}

type AdminHandler struct {
	store    SchedulerStore
	location *time.Location
}

func NewAdminHandler(store SchedulerStore, location *time.Location) *AdminHandler {
	if location == nil {
		location = time.Local
	}
	return &AdminHandler{store: store, location: location}
}

func (h *AdminHandler) Execute(ctx context.Context, groupID int64, input string) (string, error) {
	if h == nil || h.store == nil {
		return "定时任务存储未初始化", nil
	}
	if groupID <= 0 {
		return "当前群聊ID无效", nil
	}
	// Normalize full-width spaces to half-width for user convenience
	text := strings.ReplaceAll(strings.TrimSpace(input), "　", " ")
	switch {
	case text == "定时任务 查看":
		jobs, err := h.store.ListScheduledJobs(ctx, groupID)
		if err != nil {
			return "", err
		}
		if len(jobs) == 0 {
			return "~当前没有定时任务~", nil
		}
		lines := []string{"当前定时任务列表:"}
		for _, job := range jobs {
			scheduledAt := job.TimeHHMM
			if job.RunDate != nil {
				scheduledAt = job.RunDate.Format("2006-01-02") + " " + scheduledAt
			}
			lines = append(lines, fmt.Sprintf("%d. %s %s 群:%d %s", job.ID, job.Type, scheduledAt, job.GroupID, job.Message))
		}
		return strings.Join(lines, "\n"), nil
	case strings.HasPrefix(text, "定时任务 添加 "):
		rest := strings.TrimPrefix(text, "定时任务 添加 ")
		// First split: get job type
		typeAndRest := strings.SplitN(rest, " ", 2)
		if len(typeAndRest) < 2 {
			return schedFmtHelp, nil
		}
		jobType := typeAndRest[0]
		if jobType != scheduler.JobTypeDaily && jobType != scheduler.JobTypeOnce {
			return "任务类型只能是每天或单次", nil
		}
		var runDate *time.Time
		var timeHHMM string
		var afterTime string
		now := time.Now().In(h.location)
		if jobType == scheduler.JobTypeOnce {
			dateTimeSplit := strings.SplitN(typeAndRest[1], " ", 3)
			if len(dateTimeSplit) < 3 {
				return "单次任务格式：@bot /admin 定时任务 添加 单次 YYYY-MM-DD HH:MM <当前群ID> <消息内容>", nil
			}
			parsedDate, err := time.ParseInLocation("2006-01-02", dateTimeSplit[0], h.location)
			if err != nil {
				return "日期时间格式不正确，请使用 YYYY-MM-DD HH:MM", nil
			}
			parsedTime, err := time.ParseInLocation("15:04", dateTimeSplit[1], h.location)
			if err != nil {
				return "日期时间格式不正确，请使用 YYYY-MM-DD HH:MM", nil
			}
			runAt := time.Date(parsedDate.Year(), parsedDate.Month(), parsedDate.Day(), parsedTime.Hour(), parsedTime.Minute(), 0, 0, h.location)
			if runAt.Before(now.Truncate(time.Minute)) {
				return "单次任务时间不能早于当前时间", nil
			}
			runDate = &parsedDate
			// 存归一化后的写法：time.Parse 接受 "9:30" 这类未补零的小时。
			timeHHMM = parsedTime.Format("15:04")
			afterTime = dateTimeSplit[2]
		} else {
			timeAndRest := strings.SplitN(typeAndRest[1], " ", 2)
			if len(timeAndRest) < 2 {
				return schedFmtHelp, nil
			}
			normalized, err := scheduler.NormalizeHHMM(timeAndRest[0])
			if err != nil {
				return "时间格式不正确，请使用 HH:MM", nil
			}
			timeHHMM = normalized
			afterTime = timeAndRest[1]
		}
		groupAndMsg := strings.SplitN(afterTime, " ", 2)
		if len(groupAndMsg) < 2 {
			return schedFmtHelp, nil
		}
		targetGroupID, err := strconv.ParseInt(groupAndMsg[0], 10, 64)
		if err != nil || targetGroupID <= 0 {
			return "群聊ID格式不正确", nil
		}
		if targetGroupID != groupID {
			return "只能为当前群添加定时任务", nil
		}
		messageText := strings.TrimSpace(groupAndMsg[1])
		if messageText == "" {
			return "消息内容不能为空", nil
		}
		id, err := h.store.AddScheduledJob(ctx, ScheduledJobInput{
			Type:      jobType,
			TimeHHMM:  timeHHMM,
			RunDate:   runDate,
			GroupID:   targetGroupID,
			Message:   messageText,
			CreatedAt: now,
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已添加定时任务 %d", id), nil
	case strings.HasPrefix(text, "定时任务 移除 "):
		id, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(text, "定时任务 移除 ")), 10, 64)
		if err != nil {
			return "任务编号格式不正确", nil
		}
		removed, err := h.store.RemoveScheduledJob(ctx, groupID, id)
		if err != nil {
			return "", err
		}
		if !removed {
			return "未找到该定时任务", nil
		}
		return "已移除定时任务", nil
	default:
		return "未知管理命令", nil
	}
}

func NormalizeGroupRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case GroupRoleOwner:
		return GroupRoleOwner, true
	case GroupRoleAdmin:
		return GroupRoleAdmin, true
	case GroupRoleMember:
		return GroupRoleMember, true
	default:
		return "", false
	}
}

func IsNativeGroupAdmin(role string) bool {
	return role == GroupRoleOwner || role == GroupRoleAdmin
}
