// Package config loads and validates application settings from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	dateLayout = "2006-01-02"
	timeLayout = "15:04"

	// UpdatesPolling and UpdatesWebhook are the valid UPDATES_MODE values.
	UpdatesPolling = "polling"
	UpdatesWebhook = "webhook"
)

// Config holds all runtime settings.
type Config struct {
	BotToken    string
	DatabaseURL string
	AdminIDs    []int64
	Location    *time.Location
	// CampaignStart and CampaignEnd are the first and last campaign days.
	// They are dates, represented as midnight UTC (see domain.Calendar.DayOf).
	CampaignStart time.Time
	CampaignEnd   time.Time
	MaxChances    int
	// DayBoundary is the offset from midnight (in Location) at which a
	// campaign day starts, e.g. 0 for 00:00.
	DayBoundary time.Duration
	// WinnerResponseTimeout is how long a winner has to respond before the
	// prize passes to a reserve winner.
	WinnerResponseTimeout time.Duration
	// AutoDraw makes the scheduler hold the draw of each finished day by itself
	// (confirmation stays manual).
	AutoDraw bool

	// Final draw (spec 5.2): FinalWinners users win, each gets FinalPrizeTickets
	// prize tickets; FinalReserves users form the reserve queue.
	FinalWinners      int
	FinalReserves     int
	FinalPrizeTickets int
	FinalPrize        string // what the winners are told they won

	// BroadcastRate is the most messages per second a broadcast sends (Telegram allows about 30).
	BroadcastRate int

	// Capacity settings.
	DBMaxConns           int           // pgx pool size
	MaxConcurrentUpdates int           // updates handled at once; the rest wait
	ChecksPerSecond      int           // getChatMember calls per second, all users together
	SubscriptionCacheTTL time.Duration // how long a positive subscription check is reused
	MetricsListen        string        // address of the /metrics, /healthz and /readyz server; METRICS_LISTEN=off disables it

	UpdatesMode   string // UpdatesPolling or UpdatesWebhook
	WebhookURL    string // public HTTPS URL, webhook mode only
	WebhookListen string // local listen address, webhook mode only
	WebhookSecret string // optional secret token, webhook mode only
}

// Load reads the configuration from environment variables and validates it.
// All problems are reported together.
func Load() (*Config, error) {
	return load(os.Getenv)
}

func load(get func(string) string) (*Config, error) {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	cfg := &Config{
		BotToken:    strings.TrimSpace(get("BOT_TOKEN")),
		DatabaseURL: strings.TrimSpace(get("DATABASE_URL")),
	}
	if cfg.BotToken == "" {
		fail("BOT_TOKEN is required")
	}
	if cfg.DatabaseURL == "" {
		fail("DATABASE_URL is required")
	}

	if raw := strings.TrimSpace(get("ADMIN_IDS")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || id <= 0 {
				fail("ADMIN_IDS: invalid user id %q", part)
				continue
			}
			cfg.AdminIDs = append(cfg.AdminIDs, id)
		}
	}

	tz := strings.TrimSpace(get("TIMEZONE"))
	if tz == "" {
		tz = "Europe/Moscow"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		fail("TIMEZONE: %w", err)
		loc = time.UTC
	}
	cfg.Location = loc

	cfg.CampaignStart = parseDate(get("CAMPAIGN_START"), "CAMPAIGN_START", fail)
	cfg.CampaignEnd = parseDate(get("CAMPAIGN_END"), "CAMPAIGN_END", fail)
	if !cfg.CampaignStart.IsZero() && !cfg.CampaignEnd.IsZero() && cfg.CampaignEnd.Before(cfg.CampaignStart) {
		fail("CAMPAIGN_END must not be before CAMPAIGN_START")
	}

	cfg.MaxChances = 10
	if raw := strings.TrimSpace(get("MAX_CHANCES")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			fail("MAX_CHANCES: must be a positive integer, got %q", raw)
		} else {
			cfg.MaxChances = n
		}
	}

	cfg.DayBoundary = parseBoundary(get("DAY_BOUNDARY"), fail)

	cfg.WinnerResponseTimeout = 48 * time.Hour
	if raw := strings.TrimSpace(get("WINNER_RESPONSE_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			fail("WINNER_RESPONSE_TIMEOUT: must be a positive duration like 48h, got %q", raw)
		} else {
			cfg.WinnerResponseTimeout = d
		}
	}

	if raw := strings.TrimSpace(get("AUTO_DRAW")); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			fail("AUTO_DRAW: must be true or false, got %q", raw)
		}
		cfg.AutoDraw = b
	}

	cfg.FinalWinners = intEnv(get("FINAL_WINNERS"), "FINAL_WINNERS", 5, 1, fail)
	cfg.FinalReserves = intEnv(get("FINAL_RESERVES"), "FINAL_RESERVES", 5, 0, fail)
	cfg.FinalPrizeTickets = intEnv(get("FINAL_PRIZE_TICKETS"), "FINAL_PRIZE_TICKETS", 2, 1, fail)
	if cfg.FinalPrize = strings.TrimSpace(get("FINAL_PRIZE")); cfg.FinalPrize == "" {
		cfg.FinalPrize = "главный приз акции"
	}

	cfg.BroadcastRate = intEnv(get("BROADCAST_RATE"), "BROADCAST_RATE", 25, 1, fail)
	if cfg.BroadcastRate > 30 {
		fail("BROADCAST_RATE: must not exceed 30 messages per second (Telegram limit), got %d", cfg.BroadcastRate)
	}

	cfg.DBMaxConns = intEnv(get("DB_MAX_CONNS"), "DB_MAX_CONNS", 20, 1, fail)
	cfg.MaxConcurrentUpdates = intEnv(get("MAX_CONCURRENT_UPDATES"), "MAX_CONCURRENT_UPDATES", 200, 1, fail)
	cfg.ChecksPerSecond = intEnv(get("TELEGRAM_CHECKS_PER_SECOND"), "TELEGRAM_CHECKS_PER_SECOND", 25, 1, fail)
	cfg.SubscriptionCacheTTL = 30 * time.Second
	if raw := strings.TrimSpace(get("SUBSCRIPTION_CACHE_TTL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			fail("SUBSCRIPTION_CACHE_TTL: must be a non-negative duration like 30s (0 disables the cache), got %q", raw)
		} else {
			cfg.SubscriptionCacheTTL = d
		}
	}
	cfg.MetricsListen = ":9090"
	if raw := strings.TrimSpace(get("METRICS_LISTEN")); strings.EqualFold(raw, "off") {
		cfg.MetricsListen = "" // explicitly disabled
	} else if raw != "" {
		cfg.MetricsListen = raw
	}

	cfg.UpdatesMode = strings.ToLower(strings.TrimSpace(get("UPDATES_MODE")))
	if cfg.UpdatesMode == "" {
		cfg.UpdatesMode = UpdatesPolling
	}
	switch cfg.UpdatesMode {
	case UpdatesPolling:
	case UpdatesWebhook:
		cfg.WebhookURL = strings.TrimSpace(get("WEBHOOK_URL"))
		if !strings.HasPrefix(cfg.WebhookURL, "https://") {
			fail("WEBHOOK_URL: an https:// URL is required when UPDATES_MODE=webhook")
		}
		cfg.WebhookListen = strings.TrimSpace(get("WEBHOOK_LISTEN"))
		if cfg.WebhookListen == "" {
			cfg.WebhookListen = ":8080"
		}
		cfg.WebhookSecret = strings.TrimSpace(get("WEBHOOK_SECRET"))
		if cfg.WebhookSecret == "" {
			fail("WEBHOOK_SECRET: required when UPDATES_MODE=webhook, otherwise anyone can post fake updates to the bot")
		}
	default:
		fail("UPDATES_MODE: must be %q or %q, got %q", UpdatesPolling, UpdatesWebhook, cfg.UpdatesMode)
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// intEnv parses an integer setting with a default and a lower bound.
func intEnv(raw, name string, def, min int, fail func(string, ...any)) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min {
		fail("%s: must be an integer >= %d, got %q", name, min, raw)
		return def
	}
	return n
}

func parseDate(raw, name string, fail func(string, ...any)) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		fail("%s is required (format YYYY-MM-DD)", name)
		return time.Time{}
	}
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		fail("%s: %w", name, err)
		return time.Time{}
	}
	return t
}

// parseBoundary parses DAY_BOUNDARY (HH:MM, default 00:00) into an offset from midnight.
func parseBoundary(raw string, fail func(string, ...any)) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	t, err := time.Parse(timeLayout, raw)
	if err != nil {
		fail("DAY_BOUNDARY: %w", err)
		return 0
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}
