// Command bot is the Telegram giveaway bot.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // time zones without relying on the OS (distroless images, Windows)

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/admin"
	"chat_bot/internal/config"
	"chat_bot/internal/domain"
	"chat_bot/internal/handlers"
	"chat_bot/internal/notify"
	"chat_bot/internal/obs"
	"chat_bot/internal/scheduler"
	"chat_bot/internal/secret"
	"chat_bot/internal/service"
	"chat_bot/internal/storage"
	"chat_bot/internal/subscription"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := newLogger(os.Stdout)
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// healthcheck probes the running bot (for the container HEALTHCHECK, since the
// distroless image has no shell or curl). Exit code 0 means ready.
func healthcheck() int {
	addr := os.Getenv("METRICS_LISTEN")
	if addr == "" {
		addr = ":9090"
	}
	if addr == "off" {
		fmt.Println("health server is disabled (METRICS_LISTEN=off)")
		return 0
	}
	if err := obs.Probe(addr); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	return 0
}

// registerSecrets hides every credential from logs and error messages.
func registerSecrets(cfg *config.Config) {
	secret.Add(cfg.BotToken)
	secret.Add(cfg.WebhookSecret)
	if u, err := url.Parse(cfg.DatabaseURL); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			secret.Add(pw)
		}
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	registerSecrets(cfg)
	if len(cfg.AdminIDs) == 0 {
		log.Warn("ADMIN_IDS is empty: nobody can use the admin commands")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	metrics := obs.NewMetrics()
	pool, err := storage.Connect(ctx, cfg.DatabaseURL, storage.Options{MaxConns: int32(cfg.DBMaxConns)})
	if err != nil {
		return fmt.Errorf("connect storage: %w", err)
	}
	defer pool.Close()
	metrics.RegisterPool(func() obs.PoolStat {
		s := pool.Stat()
		return obs.PoolStat{
			Acquired: s.AcquiredConns(), Idle: s.IdleConns(), Total: s.TotalConns(), Max: s.MaxConns(),
			WaitCount: s.EmptyAcquireCount(),
		}
	})

	// A press that waits in the update queue for longer than the 15 s update
	// timeout is dropped; the queue drains at ChecksPerSecond checks a second, so
	// MAX_CONCURRENT_UPDATES / TELEGRAM_CHECKS_PER_SECOND should stay well below it.
	if wait := time.Duration(cfg.MaxConcurrentUpdates/cfg.ChecksPerSecond) * time.Second; wait >= 12*time.Second {
		log.Warn("MAX_CONCURRENT_UPDATES is large for TELEGRAM_CHECKS_PER_SECOND: checks may time out under a rush",
			"max_concurrent_updates", cfg.MaxConcurrentUpdates, "checks_per_second", cfg.ChecksPerSecond)
	}

	bot, err := tele.NewBot(tele.Settings{
		Token:  cfg.BotToken,
		Poller: newPoller(cfg),
		OnError: func(err error, _ tele.Context) {
			log.Error("bot error", "err", err)
		},
	})
	if err != nil {
		return fmt.Errorf("create bot: %w", err)
	}

	store := storage.NewStore(pool)
	cal := domain.Calendar{Location: cfg.Location, Boundary: cfg.DayBoundary}
	campaign := service.NewCampaign(store, cal, cfg.CampaignStart, cfg.CampaignEnd).WithCache(5 * time.Second)
	notifier := notify.NewBot(bot)
	notifyAdmins := func(ctx context.Context, text string) {
		for _, id := range cfg.AdminIDs {
			if err := notifier.Send(ctx, id, text); err != nil {
				log.Error("notify admin", "admin_id", id, "err", err)
			}
		}
	}

	// Telegram calls for subscription checks go through one rate limiter. Users
	// get cached answers and merged requests; the draws always ask Telegram.
	checker := subscription.NewChecker(bot, subscription.Options{Rate: cfg.ChecksPerSecond, Observer: metrics})
	cachedChecker := subscription.NewCached(checker, cfg.SubscriptionCacheTTL, metrics)

	draw := service.NewDailyDraw(store, checker, notifier, campaign, log)
	final := service.NewFinalDraw(store, notifier, campaign, service.FinalDrawConfig{
		Winners: cfg.FinalWinners, Reserves: cfg.FinalReserves, PrizeTickets: cfg.FinalPrizeTickets,
		Prize: cfg.FinalPrize, ResponseTimeout: cfg.WinnerResponseTimeout, MaxChances: cfg.MaxChances,
	}, log)
	prize := service.NewFinalPrize(store, notifier, campaign, service.PrizeConfig{
		Prize: cfg.FinalPrize, ResponseTimeout: cfg.WinnerResponseTimeout,
	}, log)
	prize.NotifyAdmins = notifyAdmins
	broadcast := service.NewBroadcast(store, notifier, notifier, campaign, service.BroadcastConfig{RatePerSecond: cfg.BroadcastRate}, log)
	broadcast.OnResult = metrics.BroadcastResult
	stats := service.NewStats(store, cfg.Location, cfg.DayBoundary, cfg.MaxChances)

	router := metrics.Instrument(bot)
	handlers.Register(router, handlers.Deps{
		Log:                  log,
		Users:                store,
		Campaign:             campaign,
		Promo:                service.NewPromo(store, cfg.MaxChances),
		Entry:                service.NewEntry(store, cachedChecker, cfg.MaxChances),
		Chances:              service.NewChances(store, cfg.MaxChances),
		Prize:                prize,
		Metrics:              metrics,
		MaxConcurrentUpdates: cfg.MaxConcurrentUpdates,
		Location:             cfg.Location,
	})
	admin.Register(router, admin.Deps{
		Log:    log,
		Admins: cfg.AdminIDs,
		Backend: admin.NewService(store, cfg.CampaignStart, cfg.CampaignEnd, cfg.Location).
			WithDrawer(draw).WithFinalDrawer(final).WithContactPurger(prize).WithBroadcaster(broadcast).WithStats(stats),
		Location: cfg.Location,
	})

	bg := newJobs(log)
	// Response deadlines of the final winners are always watched. The broadcast
	// queue lives in the database, so unfinished broadcasts resume after a restart.
	bg.Go(ctx, "prize-deadlines", (&scheduler.PrizeDeadlines{Log: log, Prize: prize}).Run)
	bg.Go(ctx, "broadcast-worker", broadcast.Run)
	if cfg.AutoDraw {
		auto := &scheduler.AutoDraw{
			Log:     log,
			Days:    store,
			Draw:    draw,
			Today:   campaign.Today,
			Notify:  notifyAdmins,
			Preview: func(r service.DrawResult) string { return admin.DrawPreview(r, cfg.Location) },
		}
		bg.Go(ctx, "auto-draw", auto.Run)
		log.Info("automatic daily draw enabled")
	}
	if cfg.MetricsListen != "" {
		mux := obs.Mux(metrics.Reg, func(ctx context.Context) error { return pool.Ping(ctx) })
		bg.Go(ctx, "metrics-server", func(ctx context.Context) {
			if err := obs.Serve(ctx, cfg.MetricsListen, mux, log); err != nil {
				log.Error("metrics server stopped", "err", err)
			}
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		bot.Start()
	}()

	log.Info("bot started", "mode", cfg.UpdatesMode, "timezone", cfg.Location.String(),
		"db_max_conns", cfg.DBMaxConns, "max_concurrent_updates", cfg.MaxConcurrentUpdates,
		"checks_per_second", cfg.ChecksPerSecond)
	<-ctx.Done()
	log.Info("shutting down")

	// Order: stop taking updates, let the running handlers finish, stop the
	// background jobs, and only then close the database pool.
	bot.Stop()
	<-done
	waitIdle(metrics, 10*time.Second)
	bg.Wait()
	return nil
}

// waitIdle waits until no update is being handled, but not longer than max.
func waitIdle(m *obs.Metrics, max time.Duration) {
	deadline := time.Now().Add(max)
	for m.InFlight() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
}

func newPoller(cfg *config.Config) tele.Poller {
	if cfg.UpdatesMode == config.UpdatesWebhook {
		return &tele.Webhook{
			Listen:      cfg.WebhookListen,
			SecretToken: cfg.WebhookSecret,
			Endpoint:    &tele.WebhookEndpoint{PublicURL: cfg.WebhookURL},
		}
	}
	return &tele.LongPoller{Timeout: 10 * time.Second}
}
