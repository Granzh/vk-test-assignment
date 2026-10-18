package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"BOT_TOKEN":      "token",
		"DATABASE_URL":   "postgres://u:p@localhost/db",
		"ADMIN_IDS":      "1, 2",
		"CAMPAIGN_START": "2026-09-01",
		"CAMPAIGN_END":   "2026-09-10",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxChances != 10 {
		t.Errorf("MaxChances = %d, want 10", cfg.MaxChances)
	}
	if cfg.Location.String() != "Europe/Moscow" {
		t.Errorf("Location = %s", cfg.Location)
	}
	if len(cfg.AdminIDs) != 2 {
		t.Errorf("AdminIDs = %v", cfg.AdminIDs)
	}
	want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if !cfg.CampaignStart.Equal(want) {
		t.Errorf("CampaignStart = %v, want %v", cfg.CampaignStart, want)
	}
}

func TestLoadNewDefaults(t *testing.T) {
	cfg, err := load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DayBoundary != 0 || cfg.WinnerResponseTimeout != 48*time.Hour || cfg.UpdatesMode != UpdatesPolling {
		t.Errorf("unexpected defaults: %v %v %q", cfg.DayBoundary, cfg.WinnerResponseTimeout, cfg.UpdatesMode)
	}
}

func TestLoadOverrides(t *testing.T) {
	m := validEnv()
	m["DAY_BOUNDARY"] = "04:30"
	m["WINNER_RESPONSE_TIMEOUT"] = "24h"
	m["UPDATES_MODE"] = "webhook"
	m["WEBHOOK_URL"] = "https://example.com/hook"
	m["WEBHOOK_SECRET"] = "s3cret"
	cfg, err := load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DayBoundary != 4*time.Hour+30*time.Minute {
		t.Errorf("DayBoundary = %v", cfg.DayBoundary)
	}
	if cfg.WinnerResponseTimeout != 24*time.Hour {
		t.Errorf("WinnerResponseTimeout = %v", cfg.WinnerResponseTimeout)
	}
	if cfg.WebhookListen != ":8080" || cfg.WebhookURL != "https://example.com/hook" {
		t.Errorf("webhook: %q %q", cfg.WebhookListen, cfg.WebhookURL)
	}
}

func TestLoadNewErrors(t *testing.T) {
	m := validEnv()
	m["DAY_BOUNDARY"] = "25:00"
	m["WINNER_RESPONSE_TIMEOUT"] = "soon"
	m["UPDATES_MODE"] = "webhook"
	_, err := load(env(m))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, s := range []string{"DAY_BOUNDARY", "WINNER_RESPONSE_TIMEOUT", "WEBHOOK_URL"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error does not mention %s: %v", s, err)
		}
	}
	m["UPDATES_MODE"] = "carrier-pigeon"
	if _, err := load(env(m)); err == nil || !strings.Contains(err.Error(), "UPDATES_MODE") {
		t.Errorf("want UPDATES_MODE error, got %v", err)
	}
}

func TestLoadErrors(t *testing.T) {
	m := validEnv()
	m["BOT_TOKEN"] = ""
	m["ADMIN_IDS"] = "x"
	m["CAMPAIGN_END"] = "2026-08-01"
	m["MAX_CHANCES"] = "0"
	m["TIMEZONE"] = "Nowhere/City"
	_, err := load(env(m))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, s := range []string{"BOT_TOKEN", "ADMIN_IDS", "CAMPAIGN_END", "MAX_CHANCES", "TIMEZONE"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error does not mention %s: %v", s, err)
		}
	}
}

func TestLoadAutoDraw(t *testing.T) {
	cfg, err := load(env(validEnv()))
	if err != nil || cfg.AutoDraw {
		t.Fatalf("default: AutoDraw = %v, err = %v; want false", cfg != nil && cfg.AutoDraw, err)
	}
	for raw, want := range map[string]bool{"true": true, "1": true, "false": false, "0": false} {
		m := validEnv()
		m["AUTO_DRAW"] = raw
		cfg, err := load(env(m))
		if err != nil || cfg.AutoDraw != want {
			t.Errorf("AUTO_DRAW=%q: got %v, err %v; want %v", raw, cfg != nil && cfg.AutoDraw, err, want)
		}
	}
	m := validEnv()
	m["AUTO_DRAW"] = "maybe"
	if _, err := load(env(m)); err == nil || !strings.Contains(err.Error(), "AUTO_DRAW") {
		t.Errorf("err = %v, want an AUTO_DRAW error", err)
	}
}

func TestLoadFinalDrawSettings(t *testing.T) {
	cfg, err := load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FinalWinners != 5 || cfg.FinalReserves != 5 || cfg.FinalPrizeTickets != 2 || cfg.FinalPrize == "" {
		t.Errorf("defaults: %+v", cfg)
	}
	m := validEnv()
	m["FINAL_WINNERS"], m["FINAL_RESERVES"], m["FINAL_PRIZE_TICKETS"], m["FINAL_PRIZE"] = "3", "0", "1", "билет"
	cfg, err = load(env(m))
	if err != nil || cfg.FinalWinners != 3 || cfg.FinalReserves != 0 || cfg.FinalPrizeTickets != 1 || cfg.FinalPrize != "билет" {
		t.Errorf("overrides: %+v, err %v", cfg, err)
	}
	for _, bad := range []map[string]string{
		{"FINAL_WINNERS": "0"}, {"FINAL_WINNERS": "x"}, {"FINAL_RESERVES": "-1"}, {"FINAL_PRIZE_TICKETS": "0"},
	} {
		m := validEnv()
		for k, v := range bad {
			m[k] = v
		}
		if _, err := load(env(m)); err == nil {
			t.Errorf("%v: expected an error", bad)
		}
	}
}

func TestLoadBroadcastRate(t *testing.T) {
	cfg, err := load(env(validEnv()))
	if err != nil || cfg.BroadcastRate != 25 {
		t.Fatalf("default: %v, %v", cfg, err)
	}
	for raw, ok := range map[string]bool{"1": true, "30": true, "0": false, "31": false, "fast": false} {
		m := validEnv()
		m["BROADCAST_RATE"] = raw
		if _, err := load(env(m)); (err == nil) != ok {
			t.Errorf("BROADCAST_RATE=%q: err = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestLoadCapacitySettings(t *testing.T) {
	cfg, err := load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 20 || cfg.MaxConcurrentUpdates != 200 || cfg.ChecksPerSecond != 25 ||
		cfg.SubscriptionCacheTTL != 30*time.Second || cfg.MetricsListen != ":9090" {
		t.Errorf("defaults: %+v", cfg)
	}

	m := validEnv()
	m["DB_MAX_CONNS"], m["MAX_CONCURRENT_UPDATES"], m["TELEGRAM_CHECKS_PER_SECOND"] = "50", "500", "10"
	m["SUBSCRIPTION_CACHE_TTL"], m["METRICS_LISTEN"] = "0s", "127.0.0.1:9100"
	cfg, err = load(env(m))
	if err != nil || cfg.DBMaxConns != 50 || cfg.MaxConcurrentUpdates != 500 || cfg.ChecksPerSecond != 10 ||
		cfg.SubscriptionCacheTTL != 0 || cfg.MetricsListen != "127.0.0.1:9100" {
		t.Errorf("overrides: %+v, %v", cfg, err)
	}

	m["METRICS_LISTEN"] = "off"
	if cfg, err := load(env(m)); err != nil || cfg.MetricsListen != "" {
		t.Errorf("off: %q, %v", cfg.MetricsListen, err)
	}

	for _, bad := range []map[string]string{
		{"DB_MAX_CONNS": "0"}, {"MAX_CONCURRENT_UPDATES": "x"}, {"TELEGRAM_CHECKS_PER_SECOND": "-1"},
		{"SUBSCRIPTION_CACHE_TTL": "-5s"}, {"SUBSCRIPTION_CACHE_TTL": "soon"},
	} {
		m := validEnv()
		for k, v := range bad {
			m[k] = v
		}
		if _, err := load(env(m)); err == nil {
			t.Errorf("%v: expected an error", bad)
		}
	}
}

func TestWebhookModeRequiresASecret(t *testing.T) {
	m := validEnv()
	m["UPDATES_MODE"], m["WEBHOOK_URL"] = "webhook", "https://bot.example.com/"
	if _, err := load(env(m)); err == nil || !strings.Contains(err.Error(), "WEBHOOK_SECRET") {
		t.Errorf("err = %v, want a WEBHOOK_SECRET error", err)
	}
	m["WEBHOOK_SECRET"] = "s3cret"
	if _, err := load(env(m)); err != nil {
		t.Errorf("with a secret: %v", err)
	}
}
