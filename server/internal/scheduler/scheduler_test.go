package scheduler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"traework2api/internal/auth"
	"traework2api/internal/pool"
	"traework2api/internal/upstream"
)

func TestNextFire(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, loc)
	next := nextFire(now, []int{9, 21})
	if next.Hour() != 21 || next.Day() != 27 {
		t.Errorf("next=%v want 21:00 same day", next)
	}
	now = time.Date(2026, 7, 27, 22, 0, 0, 0, loc)
	next = nextFire(now, []int{9, 21})
	if next.Hour() != 9 || next.Day() != 28 {
		t.Errorf("next=%v want 09:00 next day", next)
	}
	now = time.Date(2026, 7, 27, 9, 0, 0, 0, loc)
	next = nextFire(now, []int{9})
	if next.Day() != 28 {
		t.Errorf("exact match should roll to next day: %v", next)
	}
}

func TestNextFireMergesSchedules(t *testing.T) {
	now := time.Date(2026, 7, 27, 20, 0, 0, 0, time.Local)
	next := nextFire(now, []int{9, 21, 22})
	if next.Hour() != 21 {
		t.Errorf("next=%v want 21 (earliest of 21/22)", next)
	}
}

func TestCheckinWindowDetection(t *testing.T) {
	cases := []struct {
		cfg        Config
		wantStart  int
		wantEnd    int
		wantWindow bool
	}{
		{Config{CheckinStart: 0, CheckinEnd: 2}, 0, 2, true},
		{Config{CheckinStart: 22, CheckinEnd: 2}, 22, 2, true}, // 跨日窗口
		{Config{CheckinStart: 0, CheckinEnd: 0, CheckinHour: 9}, 9, 10, false}, // 未配置 → 整点回退
		{Config{CheckinStart: 3, CheckinEnd: 3}, 3, 3, false},  // 窗口为空 → 回退
	}
	for _, c := range cases {
		start, end, ok := c.cfg.checkinWindow()
		if ok != c.wantWindow || start != c.wantStart || end != c.wantEnd {
			t.Errorf("checkinWindow(%+v)=(%d,%d,%v) want (%d,%d,%v)",
				c.cfg, start, end, ok, c.wantStart, c.wantEnd, c.wantWindow)
		}
	}
}

func TestCheckinScheduleWithinWindow(t *testing.T) {
	// 窗口 [0,9)，4 个账号：每个账号分配到独立整点，整点后 CheckinMinute 分钟内触发。
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.Local)
	p := pool.New("")
	for _, uid := range []string{"u1", "u2", "u3", "u4"} {
		p.Add(&auth.Auth{UID: uid, AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	}
	s := New(Config{Pool: p, CheckinStart: 0, CheckinEnd: 9, CheckinMinute: 10})
	for i := 0; i < 20; i++ {
		plan := s.checkinSchedule(now)
		if len(plan) != 4 {
			t.Fatalf("plan len=%d want 4", len(plan))
		}
		// 每个账号在独立整点后 10 分钟内（次日 00:00~04:10）
		times := make([]time.Time, 0, len(plan))
		for _, ts := range plan {
			if ts.Day() != 28 {
				t.Fatalf("day=%d want 28: %v", ts.Day(), ts)
			}
			if ts.Hour() < 0 || ts.Hour() >= 9 {
				t.Fatalf("hour=%d out of window [0,9): %v", ts.Hour(), ts)
			}
			if ts.Minute() >= 10 {
				t.Fatalf("minute=%d >= 10 (should be within 10min after hour): %v", ts.Minute(), ts)
			}
			times = append(times, ts)
		}
		// 每个账号在独立整点（00,01,02,03 各一个），相邻至少错开 ~50 分钟
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		for i := 1; i < len(times); i++ {
			gap := times[i].Sub(times[i-1])
			if gap < 50*time.Minute {
				t.Fatalf("gap=%v < 50m between %v and %v", gap, times[i-1], times[i])
			}
		}
	}
}

func TestCheckinScheduleRollsToNextDay(t *testing.T) {
	// 今天 01:30（窗口内但已过起点），计划应滚到次日窗口。
	now := time.Date(2026, 7, 27, 1, 30, 0, 0, time.Local)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := New(Config{Pool: p, CheckinStart: 0, CheckinEnd: 9, CheckinMinute: 10})
	for i := 0; i < 20; i++ {
		plan := s.checkinSchedule(now)
		if len(plan) != 1 {
			t.Fatalf("plan len=%d want 1", len(plan))
		}
		for _, ts := range plan {
			if ts.Day() != 28 {
				t.Fatalf("day=%d want 28 (roll to next window): %v", ts.Day(), ts)
			}
			if ts.Hour() < 0 || ts.Hour() >= 9 {
				t.Fatalf("hour=%d out of window: %v", ts.Hour(), ts)
			}
			if ts.Minute() >= 10 {
				t.Fatalf("minute=%d >= 10: %v", ts.Minute(), ts)
			}
		}
	}
}

func TestInCheckinWindow(t *testing.T) {
	loc := time.Local
	s := New(Config{CheckinStart: 0, CheckinEnd: 9})
	if !s.inCheckinWindow(time.Date(2026, 7, 27, 0, 30, 0, 0, loc)) {
		t.Error("00:30 should be in window [0,9)")
	}
	if !s.inCheckinWindow(time.Date(2026, 7, 27, 8, 59, 0, 0, loc)) {
		t.Error("08:59 should be in window [0,9)")
	}
	if s.inCheckinWindow(time.Date(2026, 7, 27, 9, 0, 0, 0, loc)) {
		t.Error("09:00 should be outside window [0,9)")
	}
	if s.inCheckinWindow(time.Date(2026, 7, 27, 12, 0, 0, 0, loc)) {
		t.Error("12:00 should be outside window [0,9)")
	}
}

// fakeUpstream 同时模拟 checkin/ent_usage/refresh。
type fakeUpstream struct {
	checkinCalls   atomic.Int32
	claimCalls     atomic.Int32
	refreshCalls   atomic.Int32
	resourceRemain int64
}

func (f *fakeUpstream) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/status"):
			f.checkinCalls.Add(1)
			w.Write([]byte(`{"checked_in":false,"credits":200,"enable":true}`))
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/claim"):
			f.claimCalls.Add(1)
			w.Write([]byte(`{"code":0,"message":"success"}`))
		case strings.HasSuffix(r.URL.Path, "/ide_user_ent_usage"):
			w.Write([]byte(`{"is_credits_billing":true,"user_entitlement_pack_list":[{"entitlement_base_info":{"quota":{"credits_limit":` +
				jsonI64(f.resourceRemain) + `}}}]}`))
		case strings.HasSuffix(r.URL.Path, "/ExchangeToken"):
			f.refreshCalls.Add(1)
			w.Write([]byte(`{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786805537,"TokenExpireDuration":1209600}}`))
		default:
			http.Error(w, "not found: "+r.URL.Path, 404)
		}
	}))
}

func jsonI64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newTestScheduler(f *fakeUpstream, p *pool.Pool, srv *httptest.Server) *Scheduler {
	up := &upstream.Client{
		HTTP:      srv.Client(),
		AgentHost: srv.URL,
		UgHost:    srv.URL,
		OAuthHost: srv.URL,
		ClientID:  upstream.ClientID,
	}
	return New(Config{
		Pool:         p,
		Upstream:     up,
		CheckinHour:  9,
		RefreshHours: []int{3},
		RefreshSkew:  time.Hour,
	})
}

func TestRunCheckinReenablesCoolingAccount(t *testing.T) {
	f := &fakeUpstream{resourceRemain: 500}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	p.Add(a)
	p.Cooldown("u1", pool.CoolPlan, time.Hour, "plan limit")

	s := newTestScheduler(f, p, srv)
	s.runCheckinOne("u1")
	if f.checkinCalls.Load() != 1 {
		t.Errorf("checkin status calls=%d", f.checkinCalls.Load())
	}
	if f.claimCalls.Load() != 1 {
		t.Errorf("claim calls=%d", f.claimCalls.Load())
	}
	st, _ := p.Status("u1")
	if st.Cooling {
		t.Errorf("account should be reenabled after checkin with credits: %+v", st)
	}
	if st.Credits != 500 {
		t.Errorf("credits=%d want 500", st.Credits)
	}
}

func TestRunCheckinSkipsDisabled(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Disable("u1", "session dead")

	s := newTestScheduler(f, p, srv)
	s.runCheckinOne("u1")
	if f.checkinCalls.Load() != 0 {
		t.Errorf("disabled account should be skipped, calls=%d", f.checkinCalls.Load())
	}
}

func TestRunRefreshRefreshesTokens(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 1, ApiHost: srv.URL}
	p.Add(a)

	s := newTestScheduler(f, p, srv)
	s.RunRefreshNow()
	if f.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls=%d", f.refreshCalls.Load())
	}
	if a.AccessToken != "newat" {
		t.Errorf("token not updated: %s", a.AccessToken)
	}
}

func TestRunRefreshSkipsFreshToken(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "fresh", RefreshToken: "rt", ExpiresAt: 9999999999, ApiHost: srv.URL})

	s := newTestScheduler(f, p, srv)
	s.RunRefreshNow()
	if f.refreshCalls.Load() != 0 {
		t.Errorf("fresh token should not refresh, calls=%d", f.refreshCalls.Load())
	}
}

func TestRunRefreshSessionDeadDisables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"code":20101,"msg":"login required"}`))
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 1, ApiHost: srv.URL})

	up := &upstream.Client{HTTP: srv.Client(), AgentHost: srv.URL, UgHost: srv.URL, OAuthHost: srv.URL, ClientID: upstream.ClientID}
	s := New(Config{Pool: p, Upstream: up})
	s.RunRefreshNow()
	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Errorf("should disable session-dead account: %+v", st)
	}
}
