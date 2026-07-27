package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/safego"
)

type RuntimeStore interface {
	ListActiveSchedulerJobs(ctx context.Context) ([]Job, error)
	MarkScheduledJobRan(ctx context.Context, id uint64, at time.Time, disable bool) error
}

type RuntimeOptions struct {
	Store    RuntimeStore
	Send     SendFunc
	Interval time.Duration
	Location *time.Location
	Logf     func(string, ...any)
}

type Runtime struct {
	store    RuntimeStore
	send     SendFunc
	interval time.Duration
	location *time.Location
	logf     func(string, ...any)

	// sent 记录每个任务已成功发送的自然日（location 时区）。消息发出去之后
	// last_run_at 写库失败时，数据库里没有任何已执行痕迹，下一轮仍会判定到期，
	// 于是同一条消息每个 tick 重发一次。这个标记把重复挡在进程内。
	mu   sync.Mutex
	sent map[uint64]string
}

func NewRuntime(opts RuntimeOptions) *Runtime {
	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	location := opts.Location
	if location == nil {
		location = time.Local
	}
	return &Runtime{
		store:    opts.Store,
		send:     opts.Send,
		interval: interval,
		location: location,
		logf:     opts.Logf,
		sent:     make(map[uint64]string),
	}
}

func (r *Runtime) Run(ctx context.Context) {
	r.runAndLog(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runAndLog(ctx)
		}
	}
}

func (r *Runtime) RunOnce(ctx context.Context, now time.Time) error {
	if r == nil || r.store == nil {
		return nil
	}
	if r.location != nil {
		now = now.In(r.location)
	}
	jobs, err := r.store.ListActiveSchedulerJobs(ctx)
	if err != nil {
		return err
	}
	today := now.Format("2006-01-02")
	for _, job := range jobs {
		if !IsDue(job, now) {
			continue
		}
		// 消息本轮已经发出去、只是标记没落库，这里只补写数据库，不重复发送。
		if r.alreadySent(job.ID, today) {
			r.markRan(ctx, job, now)
			continue
		}
		if r.send == nil {
			r.log("send scheduled job %d failed: sender is not initialized", job.ID)
			continue
		}
		if err := r.send(ctx, job.GroupID, job.Message); err != nil {
			r.log("send scheduled job %d failed: %v", job.ID, err)
			continue
		}
		r.rememberSent(job.ID, today)
		r.markRan(ctx, job, now)
	}
	return nil
}

// markRan 写入 last_run_at 并按需禁用单次任务。失败时保留 sent 标记，下一轮 tick
// 会走 alreadySent 分支重试；UPDATE 是幂等的，重试不会有副作用。
func (r *Runtime) markRan(ctx context.Context, job Job, now time.Time) {
	if err := r.store.MarkScheduledJobRan(ctx, job.ID, now, job.Type == JobTypeOnce); err != nil {
		r.log("mark scheduled job %d failed: %v", job.ID, err)
		return
	}
	// last_run_at 已落库，IsDue 会返回 false，标记不再需要。
	r.forgetSent(job.ID)
}

// alreadySent 按自然日判定，跨天的陈旧条目自然失效；条目数以任务数为上界，
// 不需要额外清理。
func (r *Runtime) alreadySent(id uint64, day string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent[id] == day
}

func (r *Runtime) rememberSent(id uint64, day string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent[id] = day
}

func (r *Runtime) forgetSent(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sent, id)
}

func (r *Runtime) runAndLog(ctx context.Context) {
	// 恢复边界放在每轮 tick 上，一轮 panic 不会让整个调度循环静默退出。
	defer safego.Recover("scheduler tick")
	if err := r.RunOnce(ctx, time.Now()); err != nil {
		r.log("run scheduled jobs failed: %v", err)
	}
}

func (r *Runtime) log(format string, args ...any) {
	if r != nil && r.logf != nil {
		r.logf(format, args...)
	}
}
