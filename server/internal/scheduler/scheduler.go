// Package scheduler 定时任务：每日签到 + token 预刷新。
// 签到成功后重新查积分，积分 > 0 的冷却账号自动解冻。
package scheduler

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"time"

	"traework2api/internal/pool"
	"traework2api/internal/upstream"
)

// Config 调度器依赖。
type Config struct {
	Pool         *pool.Pool
	Upstream     *upstream.Client
	CheckinHour  int           // 每日签到小时（旧配置，随机窗口未启用时按整点触发，默认 9）
	CheckinStart int           // 签到窗口起始小时（含），默认 0
	CheckinEnd   int           // 签到窗口结束小时（不含），默认 9 → 窗口 [0,9) 即 00:00~09:00
	CheckinMinute    int           // 每个整点后几分钟内签到，默认 10（如 00:00~00:10 签一个账号）
	RefreshHours     []int         // token 预刷新小时，默认 [3]
	RefreshSkew      time.Duration // 预刷新窗口，默认 24h
}

// checkinWindow 报告签到窗口端点（以小时计）。
// 窗口以 [start, end) 表示，end 可小/等于 start 表示跨日（如 [22,2) = 22~次日02点）。
// 未配置（start==0 且 end==0）时回退到 CheckinHour 整点触发（旧行为）。
func (cfg Config) checkinWindow() (start, end int, ok bool) {
	start, end = cfg.CheckinStart, cfg.CheckinEnd
	if cfg.CheckinStart == 0 && cfg.CheckinEnd == 0 {
		return cfg.CheckinHour, cfg.CheckinHour + 1, false
	}
	// 非法：start/end 越界或形成 24h 全窗口
	if start < 0 || start > 23 || end < 1 || end > 24 {
		return start, end, false
	}
	if start == end {
		return start, end, false
	}
	return start, end, true
}

// Scheduler 调度器。
type Scheduler struct {
	cfg Config
}

// New 构建。
func New(cfg Config) *Scheduler {
	if cfg.CheckinHour < 0 {
		cfg.CheckinHour = 9
	}
	if cfg.CheckinMinute <= 0 {
		cfg.CheckinMinute = 10
	}
	if len(cfg.RefreshHours) == 0 {
		cfg.RefreshHours = []int{3}
	}
	if cfg.RefreshSkew <= 0 {
		cfg.RefreshSkew = 24 * time.Hour
	}
	return &Scheduler{cfg: cfg}
}

// nextFire 返回 now 之后最近的一个整点触发时间；hours 为本地小时（0-23）。
func nextFire(now time.Time, hours []int) time.Time {
	var earliest time.Time
	for _, h := range hours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// checkinSchedule 为每个账号生成当天的签到触发时间。
// 新机制：窗口 [start, end) 内，每个整点后 CheckinMinute 分钟内签一个账号。
// 账号按序轮转：账号 i 在 (start+i):00 后 CheckinMinute 分钟内随机触发。
// 若账号数超过窗口小时数，多余账号顺延到窗口结束后的整点（仍错开）。
// 返回 UID → 触发时间 的映射。
func (s *Scheduler) checkinSchedule(now time.Time) map[string]time.Time {
	start, end, ok := s.cfg.checkinWindow()
	if !ok {
		return nil
	}
	uids := s.cfg.Pool.List()
	if len(uids) == 0 {
		return nil
	}
	// 窗口起点（今日，若已过则明日）
	base := time.Date(now.Year(), now.Month(), now.Day(), start, 0, 0, 0, now.Location())
	if !base.After(now) {
		base = base.Add(24 * time.Hour)
	}
	// 窗口跨度（小时）
	spanHour := end - start
	if spanHour <= 0 {
		spanHour += 24
	}
	// 每个整点后 CheckinMinute 分钟窗口（秒）
	winSec := s.cfg.CheckinMinute * 60

	out := make(map[string]time.Time, len(uids))
	for i, st := range uids {
		// 账号 i 分配到第 i 个整点（窗口内；超出窗口则顺延到窗口外整点）
		slot := i
		hourStart := base.Add(time.Duration(slot) * time.Hour)
		// 整点后 [0, winSec) 秒内随机
		offset := rand.Intn(winSec)
		out[st.UID] = hourStart.Add(time.Duration(offset) * time.Second)
	}
	return out
}

// Run 主循环，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	all := append(append([]int{}, s.cfg.RefreshHours...), s.cfg.CheckinHour)
	// 当前签到计划（UID→触发时间），每天刷新
	var plan map[string]time.Time
	var planDay int // plan 对应的日期（年月日整数值，用于判断是否刷新）
	for {
		now := time.Now()
		today := now.YearDay() + now.Year()*1000
		// 窗口模式：每天刷新计划
		if _, _, ok := s.cfg.checkinWindow(); ok {
			if plan == nil || today != planDay {
				plan = s.checkinSchedule(now)
				planDay = today
			}
		} else {
			plan = nil
		}
		// 计算最近一次触发
		var next time.Time
		if plan != nil && len(plan) > 0 {
			// 找计划中最早的、还没过的触发时间
			for _, t := range plan {
				if t.After(now) || t.Equal(now) {
					if next.IsZero() || t.Before(next) {
						next = t
					}
				}
			}
			// 如果所有计划都过了（不应发生），重置计划
			if next.IsZero() {
				plan = nil
				continue
			}
		} else {
			next = nextFire(now, all)
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			now = time.Now()
			h := now.Hour()
			if contains(s.cfg.RefreshHours, h) {
				s.RunRefreshNow()
			}
			if plan != nil {
				// 找所有计划中当前时刻应签的账号
				for uid, t := range plan {
					// 允许 ±30 秒误差
					if t.Sub(now) < 30*time.Second && t.Sub(now) > -30*time.Second {
						// 只签该账号
						s.runCheckinOne(uid)
						// 删除已执行计划
						delete(plan, uid)
					}
				}
				// 顺便检查是否还有未签的账号（时钟漂移保护）
				if len(plan) > 0 {
					// 检查是否有计划时间已过 2 分钟还没签的（因时钟漂移）
					for uid, t := range plan {
						if now.Sub(t) > 2*time.Minute {
							s.runCheckinOne(uid)
							delete(plan, uid)
						}
					}
				}
			} else if s.cfg.CheckinHour == h {
				// 整点模式：对每个账号签到（也换 deviceId）
				for _, st := range s.cfg.Pool.List() {
					if !st.Disabled {
						s.runCheckinOne(st.UID)
					}
				}
			}
		}
	}
}

// runCheckinOne 对单个账号执行签到 + 积分刷新 + 解冻。
func (s *Scheduler) runCheckinOne(uid string) {
	for _, st := range s.cfg.Pool.List() {
		if st.UID != uid {
			continue
		}
		if st.Disabled {
			return
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshTokenValue() == "" {
			return
		}
		// 签到前换全新 deviceId，绕过 TRAE「一个设备只能签到一次」风控。
		newDid := a.RotateDeviceID()
		if err := a.SaveAtomic(); err != nil {
			log.Printf("checkin %s: rotate deviceId save: %v", st.UID, err)
		}
		log.Printf("checkin %s: deviceId -> %s", st.UID, newDid[:8])
		checkedIn, _, enable, err := s.cfg.Upstream.CheckinStatus(a)
		if err != nil {
			log.Printf("checkin status %s: %v", st.UID, err)
		} else if !checkedIn && enable {
			if err := s.cfg.Upstream.CheckinClaim(a); err != nil {
				var ue *upstream.Error
				if errors.As(err, &ue) && ue.Kind == upstream.ErrCheckinRate {
					log.Printf("checkin %s: rate limited (retry later): %v", st.UID, err)
				} else {
					log.Printf("checkin claim %s: %v", st.UID, err)
				}
			} else {
				log.Printf("checkin %s: ok", st.UID)
			}
		} else if checkedIn {
			log.Printf("checkin %s: already checked in", st.UID)
		}
		remain, err := s.cfg.Upstream.UserEntUsage(a)
		if err != nil {
			log.Printf("ent-usage %s: %v", st.UID, err)
			return
		}
		s.cfg.Pool.ReenableIfCredits(st.UID, remain)
		return
	}
}

// inCheckinWindow 报告 t 是否落在签到窗口内。
func (s *Scheduler) inCheckinWindow(t time.Time) bool {
	start, end, ok := s.cfg.checkinWindow()
	if !ok {
		return t.Hour() == s.cfg.CheckinHour
	}
	hour := t.Hour()
	if end > start {
		return hour >= start && hour < end
	}
	// 跨日窗口：要么在 [start,24) 要么在 [0,end)
	return (hour >= start && hour < 24) || (hour >= 0 && hour < end)
}

func contains(hours []int, h int) bool {
	for _, v := range hours {
		if v == h {
			return true
		}
	}
	return false
}

// RunRefreshNow 立即对所有账号刷新 token；session 失效的自动禁用。
func (s *Scheduler) RunRefreshNow() {
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshTokenValue() == "" {
			continue
		}
		if !a.NeedsRefresh(s.cfg.RefreshSkew) {
			continue
		}
		if err := s.cfg.Upstream.RefreshToken(a); err != nil {
			log.Printf("refresh %s: %v", st.UID, err)
			var ue *upstream.Error
			if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
				s.cfg.Pool.Disable(st.UID, "session dead")
			}
			continue
		}
		if err := a.SaveAtomic(); err != nil {
			log.Printf("refresh %s save: %v", st.UID, err)
		}
	}
}
