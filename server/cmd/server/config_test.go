package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Listen != ":7864" {
		t.Errorf("listen=%s", c.Listen)
	}
	if c.DefaultModel != "glm-5.2" {
		t.Errorf("default_model=%s", c.DefaultModel)
	}
	if err := c.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.PlanCreditDur.Hours() != 12 {
		t.Errorf("plan_credit=%v", c.PlanCreditDur)
	}
	if c.SoftRateDur.Seconds() != 60 {
		t.Errorf("soft_rate=%v", c.SoftRateDur)
	}
	// 默认随机签到窗口 [0,9)
	if c.Schedule.CheckinStart != 0 || c.Schedule.CheckinEnd != 9 {
		t.Errorf("checkin window=(%d,%d) want (0,9)", c.Schedule.CheckinStart, c.Schedule.CheckinEnd)
	}
	if c.Schedule.CheckinMin != 10 {
		t.Errorf("checkin_minute=%d want 10", c.Schedule.CheckinMin)
	}
}

func TestCheckinWindowConfig(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	// 显式配置窗口 [22,2)
	os.WriteFile(fp, []byte(`{"schedule":{"checkin_window_start":22,"checkin_window_end":2}}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.CheckinStart != 22 || c.Schedule.CheckinEnd != 2 {
		t.Errorf("window=(%d,%d) want (22,2)", c.Schedule.CheckinStart, c.Schedule.CheckinEnd)
	}
	// 非法窗口（end<=start）→ 归一化回退整点
	os.WriteFile(fp, []byte(`{"schedule":{"checkin_window_start":5,"checkin_window_end":5}}`), 0o600)
	c, err = Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.CheckinStart != 0 || c.Schedule.CheckinEnd != 0 {
		t.Errorf("invalid window should reset to (0,0), got (%d,%d)", c.Schedule.CheckinStart, c.Schedule.CheckinEnd)
	}
}

func TestCheckinWindowEnvOverride(t *testing.T) {
	t.Setenv("TW2A_CHECKIN_WINDOW_START", "23")
	t.Setenv("TW2A_CHECKIN_WINDOW_END", "1")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.CheckinStart != 23 || c.Schedule.CheckinEnd != 1 {
		t.Errorf("window=(%d,%d) want (23,1)", c.Schedule.CheckinStart, c.Schedule.CheckinEnd)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"listen":":9999","default_model":"kimi-k2.7-code","schedule":{"checkin_hour":7}}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9999" || c.DefaultModel != "kimi-k2.7-code" || c.Schedule.CheckinHour != 7 {
		t.Errorf("c=%+v", c)
	}
	// APIKey 不读 json
	if c.APIKey != "" {
		t.Errorf("api_key should not come from json, got %q", c.APIKey)
	}
}

func TestLoadMissingFileFallsBackToDefaults(t *testing.T) {
	c, err := Load("/nonexistent/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":7864" {
		t.Errorf("listen=%s", c.Listen)
	}
}

func TestAPIKeyOnlyFromEnv(t *testing.T) {
	t.Setenv("TW2A_API_KEY", "test-key")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "test-key" {
		t.Errorf("api_key=%q", c.APIKey)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("TW2A_LISTEN", ":7777")
	t.Setenv("TW2A_DEFAULT_MODEL", "qwen-3.7-plus")
	t.Setenv("TW2A_SOFT_RATE", "30s")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":7777" || c.DefaultModel != "qwen-3.7-plus" {
		t.Errorf("c=%+v", c)
	}
	if c.SoftRateDur.Seconds() != 30 {
		t.Errorf("soft_rate=%v", c.SoftRateDur)
	}
}

func TestBadDuration(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"cooldown":{"plan_credit":"not-a-duration"}}`), 0o600)
	if _, err := Load(fp); err == nil {
		t.Fatal("want error for bad duration")
	}
}
