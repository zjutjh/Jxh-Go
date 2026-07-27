package scheduler

import (
	"context"
	"time"
)

const (
	JobTypeDaily = "每天"
	JobTypeOnce  = "单次"
)

type SendFunc func(context.Context, int64, string) error

type Job struct {
	ID        uint64
	Type      string
	GroupID   int64
	Message   string
	TimeHHMM  string
	RunDate   *time.Time
	Enabled   bool
	LastRunAt *time.Time
}

func IsDue(job Job, now time.Time) bool {
	if !job.Enabled {
		return false
	}
	switch job.Type {
	case JobTypeOnce:
		if job.RunDate == nil || job.TimeHHMM == "" {
			return false
		}
		// Compare in now's location: RunDate may carry the DB DSN loc after a
		// round-trip, which need not equal the scheduler timezone.
		runDateOnly := job.RunDate.In(now.Location()).Format("2006-01-02")
		nowDateOnly := now.Format("2006-01-02")
		if runDateOnly != nowDateOnly {
			return false
		}
		if alreadyRanToday(job, now) {
			return false
		}
		return TimeReached(now, job.TimeHHMM)
	case JobTypeDaily:
		if job.TimeHHMM == "" || alreadyRanToday(job, now) {
			return false
		}
		return TimeReached(now, job.TimeHHMM)
	default:
		return false
	}
}

// NormalizeHHMM 解析 HH:MM 并返回补零后的规范写法。time.Parse 对小时位是非定长
// 解析，"9:30" 会被接受，所以必须把解析结果重新格式化后再存库，否则后续所有按
// HH:MM 处理的地方都会拿到非规范值。
func NormalizeHHMM(value string) (string, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return "", err
	}
	return parsed.Format("15:04"), nil
}

// TimeReached 判断 now 的时刻是否已到达 hhmm，按当天分钟数比较。
// 不能用字符串比较：未补零的 "9:30" 会得到错误结果（"23:59" < "9:30"，任务永不
// 触发），"1:00" 则要到 "20:00" 才满足。无法解析时返回 false，方向上偏保守。
func TimeReached(now time.Time, hhmm string) bool {
	parsed, err := time.Parse("15:04", hhmm)
	if err != nil {
		return false
	}
	return now.Hour()*60+now.Minute() >= parsed.Hour()*60+parsed.Minute()
}

func alreadyRanToday(job Job, now time.Time) bool {
	return job.LastRunAt != nil && job.LastRunAt.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02")
}
